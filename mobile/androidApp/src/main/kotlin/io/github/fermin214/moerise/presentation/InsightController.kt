package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.*
import io.ktor.http.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*
import kotlin.time.Duration.Companion.milliseconds
import kotlin.time.TimeSource

class InsightController(
    private val repository: suspend () -> NoteRepository, private val core: () -> CoreClient?,
    private val account: () -> String?, private val scope: CoroutineScope, private val language: () -> String,
    private val timezone: () -> String = { "UTC" },
    filters: FilterReads? = null,
    private val privacy: PrivacyController? = null,
    private val onSaved: (String, Note) -> Unit,
) {
    private val mutable = MutableStateFlow(InsightState())
    val state = mutable.asStateFlow()
    private var visible = false
    private var key: String? = null
    private var generation = 0L
    private var operation: Job? = null
    private var lifecycleWrite: Job? = null
    private var polling: Job? = null
    private var questionWrite: Job? = null
    private var control: Job? = null
    val picker = InsightMaterialPicker(core, account, scope, filters, privacy) { !state.value.locked }
    private var saving: Job? = null
    private val historyRead = Mutex()
    private var historyOwner: String? = null
    private var historyLoaded = 0L
    private val historyCursors = mutableSetOf<String>()
    private val recoveryStore = InsightRecoveryStore(repository)
    private var diskRevision = 0L
    private val seeds = mutableSetOf<String>()

    fun tab(value: Int) { mutable.update { it.copy(tab = value) }; if (value == 2) history() }
    fun question(value: String) {
        if (state.value.locked) return
        mutable.update { it.copy(recovery = it.recovery.copy(question = value)) }
        ++diskRevision
        scheduleQuestionWrite()
    }

    private fun scheduleQuestionWrite() {
        if (questionWrite?.isActive == true) return
        val account = key ?: return
        questionWrite = scope.launch {
            val started = TimeSource.Monotonic.markNow()
            var observed = diskRevision
            try {
                while (key == account) {
                    delay(300)
                    if (observed != diskRevision && started.elapsedNow() < 1000.milliseconds) {
                        observed = diskRevision
                        continue
                    }
                    persist(account, state.value.recovery, diskRevision)
                    if (recoveryStore.isWritten(account,diskRevision)) return@launch
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (key == account) mutable.update { it.copy(error = "LOCAL_SAVE_FAILED") }
            }
        }
    }

    fun visible(value: Boolean) {
        if (value == visible) return
        visible = value
        if (!value) { leave(); return }
        execute { active, account, stamp ->
            if (key != account) {
                val recovery = recoveryStore.read(account)
                checkActive(stamp, account)
                key = account
                mutable.update { it.copy(recovery = recovery) }
            }
            val pendingSave = repository().feature(account, "insight-save")?.takeIf { it.isNotBlank() }
                ?.let { CoreJson.decodeFromString<ChatSaveIntent>(it) }
            val cachedHistory = if (state.value.history.isEmpty()) repository().readCachedSnapshot(account, "chats")
                ?.let { runCatching { CoreJson.decodeFromString<ChatPage>(it.body).chats }.getOrNull() }.orEmpty() else state.value.history
            checkActive(stamp, account)
            mutable.update { it.copy(capabilities = ChatCapabilities(materialSelection = true, saveSelection = true),
                pendingSave = pendingSave, history = cachedHistory) }
            if (state.value.recovery.chatId != null) readChat(active, account, stamp)
            else loadHistory(active, account, stamp, append = false)
        }
    }

    private fun leave() {
        val account = key
        val recovery = state.value.recovery
        ++generation; operation?.cancel(); control?.cancel(); polling?.cancel(); questionWrite?.cancel(); saving?.cancel()
        picker.discard()
        mutable.update { it.copy(busy = false, streaming = false, stopping = false, deleting = false, starting = "", saving = false) }
        val revision = ++diskRevision
        val previous = lifecycleWrite
        if (account != null) lifecycleWrite = scope.launch {
            try { previous?.join(); persist(account, recovery, revision) }
            catch (error: Exception) {
                if (error is CancellationException) throw error
                if (account == key) mutable.update { it.copy(error = "LOCAL_SAVE_FAILED") }
            }
        }
    }
    fun clear() {
        visible = false; leave(); key = null; seeds.clear()
        historyOwner = null; historyLoaded = 0; historyCursors.clear()
        mutable.value = InsightState()
    }
    suspend fun stopForRemoval() {
        visible = false
        leave()
        operation?.join(); control?.join(); lifecycleWrite?.join(); polling?.cancelAndJoin(); questionWrite?.cancelAndJoin()
        picker.awaitRequests(); saving?.join()
        key = null
        seeds.clear(); mutable.value = InsightState()
    }

    suspend fun flush() {
        key?.let { persist(it, state.value.recovery, ++diskRevision) }
    }
    private suspend fun persist(account: String, recovery: InsightRecovery, revision: Long) =
        recoveryStore.write(account,recovery,revision)
    private suspend fun record(account: String, recovery: InsightRecovery, stamp: Long) {
        checkActive(stamp, account)
        mutable.update { it.copy(recovery = recovery) }
        persist(account, recovery, ++diskRevision)
        checkActive(stamp, account)
    }

    private fun checkActive(stamp: Long, account: String) {
        if (stamp != generation || account != this.account()) throw CancellationException("Insight page changed")
    }
    private fun execute(block: suspend (CoreClient, String, Long) -> Unit) {
        if (!visible || state.value.busy || state.value.stopping || state.value.deleting) return
        val active = core() ?: return
        val account = account() ?: return
        val previous = operation
        val stamp = ++generation
        mutable.update { it.copy(busy = true, error = null) }
        operation = scope.launch {
            try {
                previous?.join()
                lifecycleWrite?.join()
                checkActive(stamp, account)
                block(active, account, stamp)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == generation && account == this@InsightController.account()) {
                    val code = errorCode(error)
                    mutable.update { it.copy(error = code, conflicted = it.conflicted || code == "CHAT_CONFLICT") }
                    picker.fail(code, (error as? CoreException)?.detail?.changedNames.orEmpty())
                }
            } finally {
                if (stamp == generation) {
                    mutable.update { it.copy(busy = false, streaming = false, starting = "") }
                    drainSeeds()
                    schedulePoll()
                }
            }
        }
    }

    private fun schedulePoll() {
        polling?.cancel()
        if (visible && state.value.running && !state.value.stopping && !state.value.deleting)
            polling = scope.launch { delay(3000); refresh() }
    }
    fun refresh() = execute { active, account, stamp -> readChat(active, account, stamp) }

    private suspend fun fetchChat(active: CoreClient, id: String, account: String, stamp: Long): ChatSession {
        val chat = CoreJson.decodeFromString<ChatSession>(active.api(HttpMethod.Get, "/chats/${chatId(id)}"))
        checkActive(stamp, account)
        validateChat(chat, id)
        return chat
    }

    private suspend fun readChat(active: CoreClient, account: String, stamp: Long, loadMaterials: Boolean = true) {
        val id = state.value.recovery.chatId ?: return
        val chat = fetchChat(active, id, account, stamp)
        val saved = state.value.recovery
        val pending = saved.generation
        val resolved = pending?.let { input -> chat.turns.firstOrNull { it.requestId == input.input.requestId } }
        val previous = state.value.chat
        val applied = saved.mutation?.takeIf { it.method == "PATCH" }?.let { patchApplied(chat, it) } == true
        val conflict = previous?.id == chat.id && previous.version != chat.version && !applied &&
            (saved.question.isNotBlank() && pending == null || saved.mutation != null)
        val selected = if (!conflict && saved.creation == null && saved.mutation == null)
            chat.materials.map { it.name } else saved.selected
        val partial = mergePartial(saved.partial, resolved ?: saved.partial?.let { local ->
            chat.turns.firstOrNull { it.requestId == local.requestId }
        })
        mutable.update { it.copy(confirmed = chat.turns, failedGeneration = pending != null &&
            (resolved == null || resolved.assistant.status in setOf("failed", "interrupted")), chat = chat.copy(turns = chat.turns.map { turn ->
            partial?.takeIf { it.requestId == turn.requestId } ?: turn
        }), conflicted = it.conflicted || conflict, error = if (conflict) "CHAT_CONFLICT" else it.error) }
        // Durable server results replace local stream previews; reading never starts model generation.
        record(account, saved.copy(
            selected = selected, selectionInitialized = true,
            generation = if (resolved?.assistant?.status in setOf("completed", "canceled")) null else pending,
            partial = partial,
            question = if (resolved?.assistant?.status == "completed" && pending?.input?.let {
                it.kind == "question" && saved.question == it.content
            } == true) "" else saved.question,
        ), stamp)
        if (resolved?.assistant?.status in setOf("failed", "canceled", "interrupted"))
            mutable.update { it.copy(error = resolved!!.assistant.errorCode.ifBlank { "CHAT_INTERRUPTED" }) }
        if (loadMaterials && saved.candidates.map { it.reference() } != chat.materials) {
            val materials = resolve(active, account, stamp, selected, enforceBudget = false)
            val original = chat.materials.associateBy { it.name }
            record(account, state.value.recovery.copy(candidates = materials.notes.map { note ->
                note.candidate().copy(sourceHash = original[note.name]?.sourceHash ?: note.sourceHash)
            }), stamp)
        }
    }

    private fun mergePartial(local: ChatTurn?, remote: ChatTurn?): ChatTurn? {
        if (remote == null) return local
        if (local == null || local.requestId != remote.requestId) return remote
        val content = if (remote.assistant.status !in setOf("completed", "canceled") &&
            local.assistant.content.startsWith(remote.assistant.content) &&
            local.assistant.content.length > remote.assistant.content.length) local.assistant.content
        else remote.assistant.content
        val savedName = remote.assistant.savedName.ifBlank {
            local.assistant.savedName.takeIf { local.assistant.version == remote.assistant.version }.orEmpty()
        }
        return remote.copy(assistant = remote.assistant.copy(content = content, savedName = savedName))
    }

    fun history(append: Boolean = false) = execute { active, account, stamp -> loadHistory(active, account, stamp, append) }
    suspend fun prefetchHistory() {
        val active = core() ?: return
        val account = account() ?: return
        loadHistory(active, account, generation, append = false)
    }
    private suspend fun loadHistory(active: CoreClient, account: String, stamp: Long, append: Boolean) {
        historyRead.withLock {
        if (!append && historyOwner == account && epochMillis() - historyLoaded in 0..60_000) return
        val cursor = if (append) state.value.historyNext.takeIf { it.isNotBlank() } ?: return else ""
        val body = active.api(HttpMethod.Get,
            "/chats?page_size=20" + if (cursor.isNotEmpty()) "&page_token=${cursor.encodeURLQueryComponent(encodeFull = true)}" else "")
        val page = CoreJson.decodeFromString<ChatPage>(body)
        checkActive(stamp, account)
        if (!append) historyCursors.clear()
        historyCursors += cursor
        require(page.chats.all { chatId(it.id).isNotBlank() } &&
            (page.nextPageToken.isEmpty() || page.nextPageToken !in historyCursors)) { "CHAT_PROTOCOL" }
        if (!append) repository().putCachedSnapshot(account, "chats", body)
        historyOwner = account; historyLoaded = epochMillis()
        mutable.update { it.copy(history = (if (append) it.history + page.chats else page.chats).distinctBy { c -> c.id },
            historyNext = page.nextPageToken) }
        }
    }

    fun open(id: String) = execute { active, account, stamp ->
        require(state.value.pendingSave == null && !state.value.saving) { "CHAT_OUTCOME_UNKNOWN" }
        require(!state.value.running && state.value.canLeaveGeneration &&
            state.value.recovery.mutation == null && state.value.recovery.creation == null) { "CHAT_BUSY" }
        picker.discard()
        record(account, InsightRecovery(chatId = chatId(id)), stamp)
        mutable.update { it.copy(chat = null, materials = null, tab = 0, conflicted = false, confirmed = emptyList()) }
        readChat(active, account, stamp)
    }
    fun newChat() = execute { _, account, stamp ->
        require(state.value.pendingSave == null && !state.value.saving) { "CHAT_OUTCOME_UNKNOWN" }
        require(!state.value.running && state.value.canLeaveGeneration &&
            state.value.recovery.mutation == null && state.value.recovery.creation == null) { "CHAT_BUSY" }
        picker.discard()
        record(account, InsightRecovery(), stamp)
        mutable.update { it.copy(chat = null, materials = null, tab = 0, conflicted = false, confirmed = emptyList()) }
    }

    fun openPicker() {
        if (state.value.locked || picker.state.value.visible) return
        execute { active, account, stamp ->
            val recovery = state.value.recovery
            picker.open(account, serviceMonth(timezone()), recovery.dateGroup, true)
            picker.ready(if (recovery.candidates.map { it.name } == recovery.selected) recovery.candidates
                else resolve(active, account, stamp, enforceBudget = false).notes.map { it.candidate() })
        }
    }

    fun closePicker() {
        if (!state.value.busy) { picker.outcome("canceled"); picker.discard(); drainSeeds() }
    }

    fun completePicker() {
        val names = picker.selectedNames() ?: return
        val selection = picker.state.value
        if (names.isEmpty() && state.value.chat?.materials.isNullOrEmpty()) return
        execute { active, account, stamp ->
            try { persistSelection(active, account, stamp, names, selection.selected) }
            catch (failure: Exception) { picker.outcome(usageResult(failure)); throw failure }
            record(account, state.value.recovery.copy(dateGroup = selection.dateGroup, candidates = selection.selected), stamp)
            picker.outcome("success")
            picker.discard()
        }
    }

    fun removeMaterial(name: String) {
        if (state.value.locked || picker.state.value.visible || name !in state.value.recovery.selected) return
        changeSelection(state.value.recovery.selected - name)
    }
    private fun changeSelection(names: List<String>) = execute { active, account, stamp ->
        require(!picker.state.value.visible) { "CHAT_BUSY" }
        record(account, state.value.recovery.copy(selected = names, selectionInitialized = true), stamp)
        mutable.update { it.copy(materials = it.materials?.takeIf { materials -> materials.notes.map { note -> note.name } == names }) }
        persistSelection(active, account, stamp, names)
    }
    fun seed(name: String) {
        if (!validName(name)) { mutable.update { it.copy(error = "INVALID_ARGUMENT") }; return }
        if (state.value.busy || picker.state.value.visible || key != account()) { seeds += name; return }
        if (state.value.locked) return
        changeSelection((state.value.recovery.selected + name).distinct())
    }
    private fun drainSeeds() {
        if (state.value.locked || picker.state.value.visible || seeds.isEmpty()) return
        val names = seeds.toList(); seeds.clear()
        names.forEach(::seed)
    }
    private suspend fun resolve(active: CoreClient, account: String, stamp: Long,
        names: List<String> = state.value.recovery.selected, enforceBudget: Boolean = true,
        publish: Boolean = true): ChatMaterials {
        checkActive(stamp, account)
        require(names.size <= 1000) { "CHAT_RESOURCE_LIMIT" }
        require(names.distinct().size == names.size && names.all(::validName)) { "INVALID_ARGUMENT" }
        if (names.isEmpty()) {
            val empty = ChatMaterials(emptyList(), emptyList(), 0, 20_000)
            if (publish) mutable.update { it.copy(materials = empty) }
            return empty
        }
        val result = CoreJson.decodeFromString<ChatMaterials>(active.api(HttpMethod.Post, "/chat-materials/resolve",
            namesBody(names)))
        checkActive(stamp, account)
        require(result.notes.all { it.instanceId == account.split('\u0000')[1] } &&
            result.materials.map { it.name } == names && result.notes.map { it.name } == names &&
            result.characters == materialCharacters(result.notes) && result.limit > 0) { "CHAT_PROTOCOL" }
        if (publish) mutable.update { it.copy(materials = result) }
        if (enforceBudget) require(result.characters <= result.limit) { "CHAT_MATERIAL_LIMIT" }
        return result
    }
    fun resolve() = execute { active, account, stamp -> resolve(active, account, stamp) }

    fun applyMaterials() = execute { active, account, stamp ->
        require(!picker.state.value.visible) { "CHAT_BUSY" }
        val recovery = state.value.recovery
        persistSelection(active, account, stamp, if (!recovery.selectionInitialized && recovery.selected.isEmpty())
            recovery.creation?.names ?: recovery.selected else recovery.selected)
    }

    private suspend fun persistSelection(active: CoreClient, account: String, stamp: Long,
        names: List<String> = state.value.recovery.selected, selected: List<MaterialCandidate>? = null): ChatMaterials {
        require(!state.value.conflicted) { "CHAT_CONFLICT" }
        require(state.value.canLeaveGeneration && !state.value.running) { "CHAT_BUSY" }
        if (state.value.recovery.generation != null) record(account, state.value.recovery.copy(generation = null), stamp)
        if (state.value.recovery.mutation != null) {
            require(state.value.recovery.mutation!!.method == "PATCH") { "CHAT_OUTCOME_UNKNOWN" }
            reconcileMutation(active, account, stamp)
        }
        require(!state.value.conflicted) { "CHAT_CONFLICT" }
        require(!state.value.running) { "CHAT_BUSY" }
        val expected = selected?.map { it.reference() }
        val checked = if (expected != null)
            ChatMaterials(emptyList(), expected, selected.orEmpty().sumOf { it.characters }, 20_000)
        else if (state.value.recovery.creation == null)
            resolve(active, account, stamp, names, enforceBudget = false, publish = false) else null
        require(checked == null || checked.characters <= checked.limit) { "CHAT_MATERIAL_LIMIT" }
        // Creation is replayed with its original names before applying a later selection.
        var current = state.value.chat
        if (current == null && (names.isNotEmpty() || state.value.recovery.creation != null)) {
            val input = state.value.recovery.creation ?: ChatCreate(newLocalId(), if (language() == "en") "Insights" else "洞察", names,
                expected ?: checked?.materials)
            record(account, state.value.recovery.copy(creation = input), stamp)
            val response = try { active.api(HttpMethod.Post, "/chats", buildJsonObject {
                put("request_id", input.requestId); put("title", input.title)
                putSelection(input.names, input.materials)
            }.toString()) }
            catch (error: Exception) {
                clearRejectedCreation(error, account, stamp)
                throw error
            }
            val chat = CoreJson.decodeFromString<ChatSession>(response)
            checkActive(stamp, account)
            validateChat(chat)
            require(chat.materials.map { it.name } == input.names) { "CHAT_PROTOCOL" }
            mutable.update { it.copy(chat = chat, tab = 0) }
            record(account, state.value.recovery.copy(chatId = chat.id, creation = null,
                selectionInitialized = true), stamp)
            current = chat
        }
        val materials = checked ?: resolve(active, account, stamp, names, enforceBudget = false, publish = false)
        if (current != null && current.materials != materials.materials) {
            val mutation = ChatMutation("PATCH", "/chats/${chatId(current.id)}", buildJsonObject {
                put("version", current.version); put("title", current.title)
                putSelection(names, expected ?: materials.materials)
            }.toString())
            require(state.value.recovery.mutation == null) { "CHAT_OUTCOME_UNKNOWN" }
            record(account, state.value.recovery.copy(mutation = mutation), stamp)
            performMutation(active, account, stamp, mutation)
        }
        require(state.value.chat == null || state.value.chat!!.materials == materials.materials) { "SOURCE_CHANGED" }
        record(account, state.value.recovery.copy(selected = names, selectionInitialized = true,
            candidates = selected ?: materials.notes.map { it.candidate() }), stamp)
        historyLoaded = 0
        mutable.update { it.copy(materials = materials, tab = 0) }
        return materials
    }

    private suspend fun clearRejectedCreation(error: Exception, account: String, stamp: Long) {
        if (error is CancellationException) throw error
        if (error is CoreException && error.status in 400..499 &&
            !error.detail.retryable && error.detail.code != "CHAT_CONFLICT")
            record(account, state.value.recovery.copy(creation = null), stamp)
    }

    fun generate(kind: String) {
        if (state.value.locked || !visible) return
        val started = epochMillis()
        mutable.update { it.copy(starting = kind, failedGeneration = false) }
        execute { active, account, stamp ->
            require(!picker.state.value.visible) { "CHAT_BUSY" }
            require(state.value.canLeaveGeneration && !state.value.running) { "CHAT_BUSY" }
            if (state.value.recovery.generation != null) record(account, state.value.recovery.copy(generation = null), stamp)
            require(kind == "insight" || kind == "question" && state.value.recovery.question.isNotBlank()) { "INVALID_ARGUMENT" }
            require(state.value.recovery.selected.isNotEmpty()) { "CHAT_MATERIAL_REQUIRED" }
            if (state.value.chat == null) persistSelection(active, account, stamp)
            val chat = state.value.chat ?: throw IllegalArgumentException("CHAT_MATERIAL_REQUIRED")
            val input = ChatGenerate(newLocalId(), chat.version, kind, if (kind == "question") state.value.recovery.question else "", language())
            stream(active, account, stamp, ChatGeneration(chat.id, input), started)
        }
    }

    fun retryGeneration() = execute { active, account, stamp ->
        val pending = state.value.recovery.generation ?: return@execute
        readChat(active, account, stamp)
        val turn = state.value.chat?.turns?.firstOrNull { it.requestId == pending.input.requestId }
        if (!state.value.failedGeneration) return@execute
        require(!state.value.conflicted) { "CHAT_CONFLICT" }
        require(state.value.recovery.mutation == null) { "CHAT_OUTCOME_UNKNOWN" }
        val retry = if (turn == null) pending else pending.copy(input = pending.input.copy(
            requestId = newLocalId(), version = state.value.chat!!.version))
        stream(active, account, stamp, retry)
    }

    private suspend fun stream(active: CoreClient, account: String, stamp: Long, input: ChatGeneration, started: Long = epochMillis()) {
        record(account, state.value.recovery.copy(generation = input), stamp)
        mutable.update { it.copy(streaming = true, failedGeneration = false, tab = 0) }
        var partial: ChatTurn? = null
        var latest = state.value.recovery
        var lastPublish = 0L
        var lastDisk = epochMillis()
        var pendingSave: Job? = null
        var firstText = false
        try {
            active.chatStream(input.chatId, input.input) { event ->
                checkActive(stamp, account)
                if (event.type == "delta") {
                    val previous = partial ?: throw IllegalArgumentException("CHAT_PROTOCOL")
                    partial = previous.copy(assistant = previous.assistant.copy(content = previous.assistant.content + event.text))
                } else {
                    partial = event.turn
                    event.turn?.let { persisted ->
                        mutable.update { it.copy(confirmed = it.confirmed.filterNot { turn -> turn.requestId == persisted.requestId } + persisted) }
                    }
                }
                if (!firstText && partial?.assistant?.content?.isNotEmpty() == true) {
                    privacy?.record("insight", started = started)
                    firstText = true
                }
                val preview = mergePartial(latest.partial, partial)
                latest = latest.copy(partial = preview)
                val now = epochMillis()
                if (event.type != "delta" || now - lastPublish >= 60) {
                    val next = preview ?: throw IllegalArgumentException("CHAT_PROTOCOL")
                    mutable.update { it.copy(chat = it.chat?.copy(turns = it.chat.turns.filter { t -> t.id != next.id } + next),
                        starting = "", recovery = it.recovery.copy(partial = next)) }
                    lastPublish = now
                }
                if (event.type == "complete") {
                    pendingSave?.cancelAndJoin()
                    record(account, latest, stamp)
                    lastDisk = now
                } else if (now - lastDisk >= 1000 && pendingSave?.isActive != true) {
                    val snapshot = latest
                    lastDisk = now
                    pendingSave = scope.launch { runCatching { persist(account, snapshot, ++diskRevision) } }
                }
            }
            pendingSave?.cancelAndJoin()
            // Only a parser-validated complete frame reaches this point.
            latest = latest.copy(generation = null, partial = mergePartial(latest.partial, partial),
                question = if (input.input.kind == "question" && latest.question == input.input.content) "" else latest.question)
            record(account, latest, stamp)
            readChat(active, account, stamp)
            latest = state.value.recovery
        } catch (failure: Exception) {
            if (!firstText && account == this.account()) privacy?.record("insight", usageResult(failure), started, error = failure)
            if (failure !is CancellationException) {
                pendingSave?.cancelAndJoin()
                record(account, latest, stamp)
                try {
                    readChat(active, account, stamp, loadMaterials = false)
                    latest = state.value.recovery
                    partial = latest.partial
                } catch (recoveryError: Exception) {
                    if (recoveryError is CancellationException) throw recoveryError
                }
            }
            throw failure
        } finally {
            pendingSave?.cancel()
            if (partial != null) {
                val recovery = latest.copy(partial = mergePartial(latest.partial, partial))
                val revision = ++diskRevision
                withContext(NonCancellable) { persist(account, recovery, revision) }
                if (stamp == generation) mutable.update { it.copy(recovery = recovery) }
                else if (key == account && state.value.recovery.generation == input)
                    mutable.update { it.copy(recovery = it.recovery.copy(partial = recovery.partial)) }
            }
        }
    }

    fun stop() { if (state.value.canStop) stopOrDelete(deleting = false) }

    private fun stopOrDelete(deleting: Boolean) {
        val snapshot = state.value
        if (!visible || snapshot.stopping || snapshot.deleting || control?.isActive == true) return
        if (deleting && !snapshot.canDelete || !deleting && snapshot.busy && !snapshot.streaming && !snapshot.running) return
        val active = core() ?: return
        val account = account() ?: return
        val id = snapshot.chat?.id ?: snapshot.recovery.chatId ?: snapshot.recovery.generation?.chatId ?: return
        val pendingDelete = snapshot.recovery.mutation?.takeIf { it.method == "DELETE" }
        val previous = operation
        val initialStamp = generation
        polling?.cancel()
        mutable.update { it.copy(stopping = !deleting, deleting = deleting, error = null) }
        control = scope.launch {
            var stamp = initialStamp
            try {
                val expectedVersion = if (deleting) pendingDelete?.let {
                    require(it.path == "/chats/${chatId(id)}") { "CHAT_PROTOCOL" }
                    CoreJson.parseToJsonElement(it.body).jsonObject.getValue("version").jsonPrimitive.long
                } ?: snapshot.chat?.version else null
                val deadline = TimeSource.Monotonic.markNow()
                var latest = fetchChat(active, id, account, stamp)
                if (expectedVersion != null && latest.version != expectedVersion) {
                    mutable.update { it.copy(chat = latest, conflicted = true) }
                    throw IllegalArgumentException("CHAT_CONFLICT")
                }
                val running = latest.turns.firstOrNull { it.assistant.status == "running" }
                val canceled = mutableSetOf<String>()
                if (running != null) active.api(HttpMethod.Post,
                    "/chats/${chatId(id)}/messages/${running.requestId.encodeURLPathPart()}/cancel", "{}").also {
                        canceled += running.requestId
                    }
                checkActive(stamp, account)
                // Preserve the stream preview before the control operation writes its durable intent.
                stamp = ++generation
                previous?.cancelAndJoin()
                polling?.cancel()
                checkActive(stamp, account)
                mutable.update { it.copy(busy = true, streaming = false) }
                do {
                    latest = fetchChat(active, id, account, stamp)
                    if (expectedVersion != null && latest.version != expectedVersion) {
                        mutable.update { it.copy(chat = latest, conflicted = true) }
                        throw IllegalArgumentException("CHAT_CONFLICT")
                    }
                    val activeTurn = latest.turns.firstOrNull { it.assistant.status == "running" } ?: break
                    require(deadline.elapsedNow() < 10_000.milliseconds) { "REQUEST_TIMEOUT" }
                    if (canceled.add(activeTurn.requestId)) active.api(HttpMethod.Post,
                        "/chats/${chatId(id)}/messages/${activeTurn.requestId.encodeURLPathPart()}/cancel", "{}")
                    delay(150)
                } while (true)
                val local = state.value.recovery.partial
                val remote = local?.let { partial -> latest.turns.firstOrNull { it.requestId == partial.requestId } }
                val partial = mergePartial(local, remote)
                mutable.update { it.copy(chat = latest.copy(turns = latest.turns.map { turn ->
                    partial?.takeIf { it.requestId == turn.requestId } ?: turn
                })) }
                record(account, state.value.recovery.copy(generation = null, partial = partial), stamp)
                if (deleting) {
                    val input = ChatMutation("DELETE", "/chats/${chatId(id)}",
                        buildJsonObject { put("version", expectedVersion!!) }.toString())
                    record(account, state.value.recovery.copy(mutation = input), stamp)
                    performMutation(active, account, stamp, input)
                } else readChat(active, account, stamp, loadMaterials = false)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == generation && account == this@InsightController.account()) {
                    if (deleting && error is CoreException && error.detail.code == "CHAT_NOT_FOUND") {
                        resetChat(account, stamp)
                    } else mutable.update { it.copy(error = errorCode(error),
                        conflicted = it.conflicted || errorCode(error) == "CHAT_CONFLICT") }
                }
            } finally {
                if (stamp == generation && account == this@InsightController.account()) {
                    mutable.update { it.copy(busy = operation?.isActive == true, streaming = it.streaming,
                        stopping = false, deleting = false) }
                    schedulePoll()
                }
            }
        }
    }

    fun save(turn: ChatTurn) = execute { active, account, stamp ->
        val latest = state.value.chat?.turns?.firstOrNull { it.requestId == turn.requestId } ?: return@execute
        if (latest.assistant.savedName.isNotBlank() || turn.assistant.savedName.isNotBlank()) return@execute
        require(latest.assistant.status == "completed") { "CHAT_BUSY" }
        require(latest.assistant.content.encodeToByteArray().size <= 65_536) { "CONTENT_TOO_LARGE" }
        val id = state.value.chat?.id ?: return@execute
        require(state.value.recovery.mutation == null) { "CHAT_OUTCOME_UNKNOWN" }
        val input = ChatMutation("POST", "/chats/${chatId(id)}/messages/${latest.requestId.encodeURLPathPart()}/save",
            buildJsonObject { put("version", latest.assistant.version) }.toString())
        record(account, state.value.recovery.copy(mutation = input), stamp)
        performMutation(active, account, stamp, input)
    }
    fun saveSelection(messages: List<ChatMessageRef>, incomplete: Boolean) {
        val snapshot = state.value
        val chat = snapshot.chat ?: return
        if (snapshot.pendingSave != null || snapshot.saving || snapshot.deleting) return
        val choices = selectableMessages(snapshot.confirmed).filter { it.eligible && it.ref in messages }
        if (choices.isEmpty() || choices.size != messages.size || choices.any { it.status == "canceled" } && !incomplete) return
        if (selectionBody(choices, language() == "en").encodeToByteArray().size > 65_536) return
        submitSave(ChatSaveIntent(chat.id, ChatSaveSelection(newLocalId(), language(), messages, incomplete)))
    }

    fun retrySave() { state.value.pendingSave?.let(::submitSave) }

    private fun submitSave(intent: ChatSaveIntent) {
        val active = core() ?: return
        val owner = account() ?: return
        if (!visible || state.value.saving) return
        val started = epochMillis()
        val operations = buildList {
            if (intent.input.messages.size > 1) add("save_multiple")
            else if (intent.input.messages.single().role == "assistant") add("save_answer")
            if (intent.input.messages.any { it.role == "user" }) add("save_question")
        }
        mutable.update { it.copy(saving = true, saveError = null, pendingSave = intent) }
        saving = scope.launch {
            try {
                repository().putFeature(owner, "insight-save", CoreJson.encodeToString(intent))
                val body = active.api(HttpMethod.Post, "/chats/${chatId(intent.chatId)}/save-selection",
                    CoreJson.encodeToString(intent.input))
                val note = CoreJson.decodeFromString<Note>(body)
                require(validName(note.name) && note.instanceId == owner.split('\u0000')[1]) { "ACCOUNT_CHANGED" }
                repository().recordLocalNote(owner, note)
                repository().putFeature(owner, "insight-save", "")
                if (account() == owner) {
                    onSaved(owner, note)
                    operations.forEach { privacy?.record(it, started = started) }
                    mutable.update { it.copy(savedOperation = intent.input.operationKey, pendingSave = null) }
                }
            } catch (error: Exception) {
                if (account() == owner) operations.forEach {
                    privacy?.record(it, if (error is CancellationException) "unknown" else usageResult(error), started, error = error)
                }
                if (error is CancellationException) throw error
                if (account() == owner) {
                    if (error is CoreException && error.status in 400..499 && !error.detail.retryable) {
                        repository().putFeature(owner, "insight-save", "")
                        mutable.update { it.copy(pendingSave = null) }
                    }
                    mutable.update { it.copy(saveError = errorCode(error)) }
                }
            } finally {
                if (account() == owner) mutable.update { it.copy(saving = false) }
            }
        }
    }
    fun delete() = stopOrDelete(deleting = true)
    fun retryMutation() = execute { active, account, stamp ->
        val input = state.value.recovery.mutation ?: return@execute
        val id = state.value.recovery.chatId ?: return@execute
        if (input.method == "DELETE") {
            try { readChat(active, account, stamp, loadMaterials = false) }
            catch (error: CoreException) {
                if (error.detail.code == "CHAT_NOT_FOUND") {
                    resetChat(account, stamp); return@execute
                }
                throw error
            }
            throw IllegalArgumentException("CHAT_DELETE_REQUIRES_CONFIRMATION")
        }
        if (input.method == "PATCH") {
            reconcileMutation(active, account, stamp)
            val names = state.value.chat!!.materials.map { it.name }
            record(account, state.value.recovery.copy(selected = names, selectionInitialized = true), stamp)
            resolve(active, account, stamp, names, enforceBudget = false)
            return@execute
        }
        require(id == state.value.chat?.id) { "CHAT_CONFLICT" }
        performMutation(active, account, stamp, input)
    }
    fun adoptServer() = execute { active, account, stamp ->
        readChat(active, account, stamp, loadMaterials = false)
        require(!state.value.running) { "CHAT_BUSY" }
        val chat = state.value.chat ?: return@execute
        record(account, state.value.recovery.copy(mutation = null, generation = null, partial = null,
            candidates = emptyList(), dateGroup = MaterialDateGroup(), question = "",
            selected = chat.materials.map { it.name }, selectionInitialized = true), stamp)
        picker.discard()
        mutable.update { it.copy(materials = null, conflicted = false, error = null) }
        readChat(active, account, stamp)
    }
    fun confirmDeleteAgain() {
        if (state.value.recovery.mutation?.method == "DELETE") stopOrDelete(deleting = true)
    }

    private fun patchApplied(chat: ChatSession, input: ChatMutation): Boolean {
        val expected = CoreJson.parseToJsonElement(input.body).jsonObject
        return chat.version == expected.getValue("version").jsonPrimitive.long + 1 &&
            chat.title == expected.getValue("title").jsonPrimitive.content &&
            (expected["materials"]?.let { CoreJson.decodeFromJsonElement<List<ChatMaterial>>(it) == chat.materials }
                ?: (chat.materials.map { it.name } == expected.getValue("names").jsonArray.map { it.jsonPrimitive.content }))
    }

    private suspend fun reconcileMutation(active: CoreClient, account: String, stamp: Long) {
        val input = state.value.recovery.mutation ?: return
        require(input.method == "PATCH") { "CHAT_OUTCOME_UNKNOWN" }
        val id = state.value.recovery.chatId ?: throw IllegalArgumentException("CHAT_PROTOCOL")
        require(input.path == "/chats/${chatId(id)}") { "CHAT_PROTOCOL" }
        val chat = fetchChat(active, id, account, stamp)
        val version = CoreJson.parseToJsonElement(input.body).jsonObject.getValue("version").jsonPrimitive.long
        if (patchApplied(chat, input)) {
            mutable.update { it.copy(chat = chat, conflicted = false, error = null) }
            record(account, state.value.recovery.copy(mutation = null), stamp)
            return
        }
        mutable.update { it.copy(chat = chat, conflicted = chat.version != version) }
        require(chat.version == version) { "CHAT_CONFLICT" }
        require(!state.value.running) { "CHAT_BUSY" }
        performMutation(active, account, stamp, input)
    }

    private suspend fun resetChat(account: String, stamp: Long) {
        val id = state.value.recovery.chatId
        picker.discard()
        record(account, InsightRecovery(), stamp)
        historyLoaded = 0
        mutable.update { it.copy(chat = null, materials = null, conflicted = false, tab = 0, error = null, confirmed = emptyList(),
            history = it.history.filterNot { summary -> summary.id == id }) }
    }
    private suspend fun performMutation(active: CoreClient, account: String, stamp: Long, input: ChatMutation) {
        val response = try { active.api(HttpMethod.parse(input.method), input.path, input.body) }
        catch (error: Exception) {
            if (error is CancellationException) throw error
            checkActive(stamp, account)
            if (input.method == "DELETE") {
                try {
                    fetchChat(active, state.value.recovery.chatId ?: throw error, account, stamp)
                } catch (readError: CoreException) {
                    if (readError.detail.code != "CHAT_NOT_FOUND") throw error
                    resetChat(account, stamp)
                    return
                }
            }
            if (error is CoreException && error.status in 400..499 &&
                !error.detail.retryable && error.detail.code != "CHAT_CONFLICT")
                record(account, state.value.recovery.copy(mutation = null), stamp)
            throw error
        }
        checkActive(stamp, account)
        when {
            input.method == "DELETE" -> resetChat(account, stamp)
            input.method == "PATCH" -> {
                val chat = CoreJson.decodeFromString<ChatSession>(response)
                validateChat(chat, state.value.recovery.chatId)
                require(patchApplied(chat, input)) { "CHAT_PROTOCOL" }
                mutable.update { it.copy(chat = chat, tab = 0, conflicted = false) }
                record(account, state.value.recovery.copy(mutation = null), stamp)
            }
            else -> {
                val note = CoreJson.decodeFromString<Note>(response)
                require(note.instanceId == account.split('\u0000')[1] && validName(note.name)) { "ACCOUNT_CHANGED" }
                val saved = state.value.chat?.turns?.firstOrNull { turn ->
                    input.path == "/chats/${state.value.recovery.chatId}/messages/${turn.requestId.encodeURLPathPart()}/save"
                } ?: throw IllegalArgumentException("CHAT_PROTOCOL")
                val turn = saved.copy(assistant = saved.assistant.copy(savedName = note.name))
                mutable.update { it.copy(chat = it.chat?.copy(turns = it.chat.turns.map { candidate ->
                    if (candidate.requestId == turn.requestId) turn else candidate
                })) }
                record(account, state.value.recovery.copy(mutation = null,
                    partial = if (state.value.recovery.partial?.requestId == turn.requestId) turn else state.value.recovery.partial), stamp)
                onSaved(account, note)
                readChat(active, account, stamp, loadMaterials = false)
            }
        }
    }
    private fun validateChat(chat: ChatSession, id: String? = null) {
        require(chatId(chat.id).isNotBlank() && (id == null || chat.id == id) && chat.version > 0 &&
            chat.turns.map { it.id }.distinct().size == chat.turns.size &&
            chat.turns.map { it.requestId }.distinct().size == chat.turns.size &&
            chat.materials.map { it.name }.distinct().size == chat.materials.size &&
            chat.materials.all { validName(it.name) && it.sourceHash.isNotBlank() } &&
            chat.turns.all { it.id.isNotBlank() && it.requestId.isNotBlank() && it.kind in setOf("insight", "question") &&
                it.user.role == "user" && it.assistant.role == "assistant" && it.user.version > 0 && it.assistant.version > 0 &&
                it.assistant.status in setOf("running", "completed", "failed", "canceled", "interrupted") }) { "CHAT_PROTOCOL" }
    }
    private fun validName(name: String) = Regex("^memos/[A-Za-z0-9_-]+$").matches(name)
    private fun chatId(id: String): String {
        require(Regex("^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$").matches(id)) { "CHAT_PROTOCOL" }
        return id
    }
    private fun namesBody(names: List<String>) = buildJsonObject { put("names", JsonArray(names.map(::JsonPrimitive))) }.toString()
    private fun JsonObjectBuilder.putSelection(names: List<String>, materials: List<ChatMaterial>?) {
        if (materials != null) put("materials", CoreJson.encodeToJsonElement(materials))
        else put("names", JsonArray(names.map(::JsonPrimitive)))
    }
}

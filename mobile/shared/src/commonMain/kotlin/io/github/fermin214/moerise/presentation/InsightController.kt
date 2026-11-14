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

data class InsightState(
    val recovery: InsightRecovery = InsightRecovery(), val chat: ChatSession? = null,
    val materials: ChatMaterials? = null, val candidates: List<Note> = emptyList(), val pickerNext: String = "",
    val history: List<ChatSummary> = emptyList(), val historyNext: String = "", val tab: Int = 0,
    val query: String = "", val filter: String = "ALL", val busy: Boolean = false, val streaming: Boolean = false,
    val error: String? = null,
) {
    val running get() = chat?.turns?.any { it.assistant.status == "running" } == true
    val canStop get() = streaming || running || recovery.partial?.assistant?.status == "running"
    val locked get() = busy || running || recovery.generation != null || recovery.mutation != null || recovery.creation != null
}

class InsightController(
    private val repository: suspend () -> NoteRepository, private val core: () -> CoreClient?,
    private val account: () -> String?, private val scope: CoroutineScope, private val language: () -> String,
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
    private val disk = Mutex()
    private var diskRevision = 0L
    private val writtenRevisions = mutableMapOf<String, Long>()
    private val seeds = mutableSetOf<String>()

    fun tab(value: Int) { mutable.update { it.copy(tab = value) }; if (value == 2) history() }
    fun query(value: String) { if (!state.value.busy) mutable.update { it.copy(query = value, pickerNext = "", candidates = emptyList()) } }
    fun filter(value: String) { if (!state.value.busy && value in setOf("ALL", "NORMAL", "ARCHIVED")) mutable.update { it.copy(filter = value, pickerNext = "", candidates = emptyList()) } }
    fun question(value: String) {
        if (state.value.busy || state.value.running || state.value.recovery.generation != null) return
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
                    if (writtenRevisions[account] == diskRevision) return@launch
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
                val recovery = disk.withLock { repository().feature(account, "insights") }?.let {
                    CoreJson.decodeFromString<InsightRecovery>(it)
                } ?: InsightRecovery()
                checkActive(stamp, account)
                key = account
                mutable.update { it.copy(recovery = recovery) }
            }
            if (state.value.recovery.chatId != null) readChat(active, account, stamp)
            else loadHistory(active, stamp, append = false)
        }
    }

    private fun leave() {
        val account = key
        val recovery = state.value.recovery
        ++generation; operation?.cancel(); polling?.cancel(); questionWrite?.cancel()
        mutable.update { it.copy(busy = false, streaming = false) }
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
    fun clear() { visible = false; leave(); key = null; seeds.clear(); mutable.value = InsightState() }
    suspend fun stopForRemoval() {
        visible = false
        leave()
        operation?.join(); lifecycleWrite?.join(); polling?.cancelAndJoin(); questionWrite?.cancelAndJoin()
        disk.withLock { key = null }
        seeds.clear(); mutable.value = InsightState()
    }

    suspend fun flush() {
        key?.let { persist(it, state.value.recovery, ++diskRevision) }
    }
    private suspend fun persist(account: String, recovery: InsightRecovery, revision: Long) = disk.withLock {
        // A slower lifecycle write must not overwrite the request recorded immediately before a POST.
        if (revision < (writtenRevisions[account] ?: 0L)) return@withLock
        repository().putFeature(account, "insights", CoreJson.encodeToString(recovery))
        writtenRevisions[account] = revision
    }
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
        if (!visible || state.value.busy) return
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
                if (stamp == generation && account == this@InsightController.account())
                    mutable.update { it.copy(error = errorCode(error)) }
            } finally {
                if (stamp == generation) {
                    mutable.update { it.copy(busy = false, streaming = false) }
                    if (!state.value.locked && seeds.isNotEmpty()) {
                        val names = seeds.toList(); seeds.clear()
                        names.forEach(::seed)
                    }
                    schedulePoll()
                }
            }
        }
    }

    private fun schedulePoll() {
        polling?.cancel()
        if (visible && state.value.running) polling = scope.launch { delay(3000); refresh() }
    }
    fun refresh() = execute { active, account, stamp -> readChat(active, account, stamp) }

    private suspend fun readChat(active: CoreClient, account: String, stamp: Long) {
        val id = state.value.recovery.chatId ?: return
        val chat = CoreJson.decodeFromString<ChatSession>(active.api(HttpMethod.Get, "/chats/${chatId(id)}"))
        checkActive(stamp, account)
        require(chat.id == id && chat.version > 0 && chat.turns.map { it.id }.distinct().size == chat.turns.size) { "CHAT_PROTOCOL" }
        val saved = state.value.recovery
        val pending = saved.generation
        val resolved = pending?.let { input -> chat.turns.firstOrNull { it.requestId == input.input.requestId } }
        mutable.update { it.copy(chat = chat) }
        // Durable server results replace local stream previews; reading never starts model generation.
        record(account, saved.copy(
            selected = saved.selected.ifEmpty { chat.materials.map { it.name } },
            generation = if (resolved?.assistant?.status == "completed") null else pending,
            partial = if (resolved != null) resolved else saved.partial,
        ), stamp)
        if (resolved?.assistant?.status in setOf("failed", "canceled", "interrupted"))
            mutable.update { it.copy(error = resolved!!.assistant.errorCode.ifBlank { "CHAT_INTERRUPTED" }) }
    }

    fun history(append: Boolean = false) = execute { active, _, stamp -> loadHistory(active, stamp, append) }
    private suspend fun loadHistory(active: CoreClient, stamp: Long, append: Boolean) {
        val cursor = if (append) state.value.historyNext.takeIf { it.isNotBlank() } ?: return else ""
        val page = CoreJson.decodeFromString<ChatPage>(active.api(HttpMethod.Get,
            "/chats?page_size=20" + if (cursor.isNotEmpty()) "&page_token=${cursor.encodeURLQueryComponent()}" else ""))
        if (stamp != generation) return
        require(page.chats.all { chatId(it.id).isNotBlank() }) { "CHAT_PROTOCOL" }
        mutable.update { it.copy(history = (if (append) it.history + page.chats else page.chats).distinctBy { c -> c.id },
            historyNext = page.nextPageToken) }
    }

    fun open(id: String) = execute { active, account, stamp ->
        require(!state.value.running && state.value.recovery.mutation == null && state.value.recovery.creation == null) { "CHAT_BUSY" }
        record(account, InsightRecovery(chatId = chatId(id)), stamp)
        mutable.update { it.copy(chat = null, materials = null, tab = 0) }
        readChat(active, account, stamp)
    }
    fun newChat() = execute { _, account, stamp ->
        require(!state.value.running && state.value.recovery.mutation == null && state.value.recovery.creation == null) { "CHAT_BUSY" }
        record(account, InsightRecovery(), stamp)
        mutable.update { it.copy(chat = null, materials = null, tab = 1) }
    }

    fun pick(append: Boolean = false) = execute { active, account, stamp ->
        val cursor = if (append) state.value.pickerNext.takeIf { it.isNotBlank() } ?: return@execute else ""
        val response = active.api(HttpMethod.Get, "/chat-materials?state=${state.value.filter}&page_size=20" +
            "&query=${state.value.query.encodeURLQueryComponent()}" + if (cursor.isNotEmpty()) "&page_token=${cursor.encodeURLQueryComponent()}" else "")
        val page = CoreJson.decodeFromString<ChatMaterialPage>(response)
        checkActive(stamp, account)
        require(page.notes.all { it.instanceId == account.split('\u0000')[1] }) { "ACCOUNT_CHANGED" }
        mutable.update { it.copy(candidates = (if (append) it.candidates + page.notes else page.notes).distinctBy { n -> n.name },
            pickerNext = page.nextPageToken) }
    }
    fun toggle(name: String) {
        if (state.value.locked) return
        val names = state.value.recovery.selected
        mutable.update { it.copy(recovery = it.recovery.copy(selected = if (name in names) names - name else names + name), materials = null) }
        ++diskRevision
    }
    fun seed(name: String) {
        require(Regex("^memos/[A-Za-z0-9_-]+$").matches(name))
        if (state.value.busy || key != account()) { seeds += name; return }
        if (state.value.locked) return
        mutable.update { it.copy(tab = 1, recovery = it.recovery.copy(selected = (it.recovery.selected + name).distinct()), materials = null) }
        ++diskRevision
    }
    private suspend fun resolve(active: CoreClient, account: String, stamp: Long): ChatMaterials {
        val names = state.value.recovery.selected
        require(names.isNotEmpty()) { "CHAT_MATERIAL_REQUIRED" }
        val result = CoreJson.decodeFromString<ChatMaterials>(active.api(HttpMethod.Post, "/chat-materials/resolve",
            namesBody(names)))
        checkActive(stamp, account)
        require(result.notes.all { it.instanceId == account.split('\u0000')[1] } &&
            result.materials.map { it.name } == names && result.notes.map { it.name } == names &&
            result.characters >= 0 && result.limit > 0) { "CHAT_PROTOCOL" }
        mutable.update { it.copy(materials = result) }
        require(result.characters <= result.limit) { "CHAT_MATERIAL_LIMIT" }
        return result
    }
    fun resolve() = execute { active, account, stamp -> resolve(active, account, stamp) }

    fun applyMaterials() = execute { active, account, stamp ->
        require(state.value.recovery.generation == null && !state.value.running) { "CHAT_BUSY" }
        resolve(active, account, stamp)
        val current = state.value.chat
        if (current == null) {
            val input = state.value.recovery.creation ?: ChatCreate(newLocalId(), if (language() == "en") "Insights" else "洞察", state.value.recovery.selected)
            record(account, state.value.recovery.copy(creation = input), stamp)
            val chat = CoreJson.decodeFromString<ChatSession>(active.api(HttpMethod.Post, "/chats", CoreJson.encodeToString(input)))
            checkActive(stamp, account)
            require(chat.materials.map { it.name } == input.names) { "CHAT_PROTOCOL" }
            mutable.update { it.copy(chat = chat, tab = 0) }
            record(account, state.value.recovery.copy(chatId = chat.id, creation = null), stamp)
        } else {
            val mutation = ChatMutation("PATCH", "/chats/${chatId(current.id)}", buildJsonObject {
                put("version", current.version); put("title", current.title)
                put("names", JsonArray(state.value.recovery.selected.map(::JsonPrimitive)))
            }.toString())
            require(state.value.recovery.mutation == null) { "CHAT_OUTCOME_UNKNOWN" }
            record(account, state.value.recovery.copy(mutation = mutation), stamp)
            performMutation(active, account, stamp, mutation)
        }
    }

    fun generate(kind: String) = execute { active, account, stamp ->
        require(state.value.recovery.generation == null && state.value.recovery.mutation == null && !state.value.running) { "CHAT_BUSY" }
        val chat = state.value.chat ?: throw IllegalArgumentException("CHAT_MATERIAL_REQUIRED")
        val materials = resolve(active, account, stamp)
        require(chat.materials == materials.materials) { "SOURCE_CHANGED" }
        require(kind == "insight" || kind == "question" && state.value.recovery.question.isNotBlank()) { "INVALID_ARGUMENT" }
        val input = ChatGenerate(newLocalId(), chat.version, kind, if (kind == "question") state.value.recovery.question else "", language())
        stream(active, account, stamp, ChatGeneration(chat.id, input))
    }

    fun retryGeneration() = execute { active, account, stamp ->
        val pending = state.value.recovery.generation ?: return@execute
        readChat(active, account, stamp)
        val turn = state.value.chat?.turns?.firstOrNull { it.requestId == pending.input.requestId }
        if (turn?.assistant?.status in setOf("completed", "running")) return@execute
        require(state.value.recovery.mutation == null) { "CHAT_OUTCOME_UNKNOWN" }
        val retry = if (turn == null) pending else pending.copy(input = pending.input.copy(
            requestId = newLocalId(), version = state.value.chat!!.version))
        stream(active, account, stamp, retry)
    }

    private suspend fun stream(active: CoreClient, account: String, stamp: Long, input: ChatGeneration) {
        record(account, state.value.recovery.copy(generation = input), stamp)
        mutable.update { it.copy(streaming = true, tab = 0) }
        var partial: ChatTurn? = null
        var latest = state.value.recovery
        var lastPublish = 0L
        var lastDisk = epochMillis()
        try {
            active.chatStream(input.chatId, input.input) { event ->
                checkActive(stamp, account)
                if (event.type == "delta") {
                    val previous = partial ?: throw IllegalArgumentException("CHAT_PROTOCOL")
                    partial = previous.copy(assistant = previous.assistant.copy(content = previous.assistant.content + event.text))
                } else partial = event.turn
                latest = latest.copy(partial = partial)
                val now = epochMillis()
                if (event.type != "delta" || now - lastPublish >= 60) {
                    val next = partial!!
                    mutable.update { it.copy(chat = it.chat?.copy(turns = it.chat.turns.filter { t -> t.id != next.id } + next),
                        recovery = it.recovery.copy(partial = next)) }
                    lastPublish = now
                }
                if (event.type == "complete" || now - lastDisk >= 1000) {
                    record(account, latest, stamp)
                    lastDisk = now
                }
            }
            // Only a parser-validated complete frame reaches this point.
            latest = latest.copy(generation = null, partial = partial,
                question = if (input.input.kind == "question" && latest.question == input.input.content) "" else latest.question)
            record(account, latest, stamp)
            readChat(active, account, stamp)
            latest = state.value.recovery
        } finally {
            if (partial != null) {
                val recovery = latest.copy(partial = partial)
                val revision = ++diskRevision
                withContext(NonCancellable) { persist(account, recovery, revision) }
                if (stamp == generation) mutable.update { it.copy(recovery = recovery) }
            }
        }
    }

    fun stop() {
        val recovery = state.value.recovery
        val id = state.value.chat?.id ?: recovery.chatId ?: recovery.generation?.chatId ?: return
        val requestId = state.value.chat?.turns?.lastOrNull { it.assistant.status == "running" }?.requestId
            ?: recovery.generation?.input?.requestId ?: recovery.partial?.takeIf { it.assistant.status == "running" }?.requestId ?: return
        ++generation; operation?.cancel(); polling?.cancel()
        mutable.update { it.copy(busy = false, streaming = false) }
        execute { active, account, stamp ->
            try { active.api(HttpMethod.Post, "/chats/${chatId(id)}/messages/$requestId/cancel", "{}") }
            finally { readChat(active, account, stamp) }
        }
    }

    fun save(turn: ChatTurn) = execute { active, account, stamp ->
        require(turn.assistant.status == "completed" && turn.assistant.content.encodeToByteArray().size <= 65_536) { "CONTENT_TOO_LARGE" }
        val id = state.value.chat?.id ?: return@execute
        require(state.value.recovery.mutation == null) { "CHAT_OUTCOME_UNKNOWN" }
        val input = ChatMutation("POST", "/chats/${chatId(id)}/messages/${turn.requestId}/save",
            buildJsonObject { put("version", turn.assistant.version) }.toString())
        record(account, state.value.recovery.copy(mutation = input), stamp)
        performMutation(active, account, stamp, input)
    }
    fun delete() = execute { active, account, stamp ->
        val chat = state.value.chat ?: return@execute
        require(!state.value.running && state.value.recovery.mutation == null) { "CHAT_BUSY" }
        val input = ChatMutation("DELETE", "/chats/${chatId(chat.id)}", buildJsonObject { put("version", chat.version) }.toString())
        record(account, state.value.recovery.copy(mutation = input), stamp)
        performMutation(active, account, stamp, input)
    }
    fun retryMutation() = execute { active, account, stamp ->
        val input = state.value.recovery.mutation ?: return@execute
        val id = state.value.recovery.chatId ?: return@execute
        if (input.method == "DELETE") {
            try { readChat(active, account, stamp) }
            catch (error: CoreException) {
                if (error.detail.code == "CHAT_NOT_FOUND") {
                    record(account, InsightRecovery(), stamp); mutable.update { it.copy(chat = null) }; return@execute
                }
                throw error
            }
            throw IllegalArgumentException("CHAT_DELETE_REQUIRES_CONFIRMATION")
        }
        if (input.method == "PATCH") {
            readChat(active, account, stamp)
            val expected = CoreJson.parseToJsonElement(input.body).jsonObject
            val chat = state.value.chat!!
            val oldVersion = expected.getValue("version").jsonPrimitive.long
            val names = expected.getValue("names").jsonArray.map { it.jsonPrimitive.content }
            if (chat.version == oldVersion + 1 && chat.title == expected.getValue("title").jsonPrimitive.content && chat.materials.map { it.name } == names) {
                record(account, state.value.recovery.copy(mutation = null), stamp); return@execute
            }
            require(chat.version == oldVersion) { "CHAT_CONFLICT" }
        }
        require(id == state.value.chat?.id) { "CHAT_CONFLICT" }
        performMutation(active, account, stamp, input)
    }
    fun adoptServer() = execute { active, account, stamp ->
        readChat(active, account, stamp)
        require(!state.value.running) { "CHAT_BUSY" }
        val chat = state.value.chat ?: return@execute
        record(account, state.value.recovery.copy(mutation = null, generation = null,
            selected = chat.materials.map { it.name }), stamp)
        mutable.update { it.copy(materials = null) }
    }
    fun confirmDeleteAgain() = execute { active, account, stamp ->
        val mutation = state.value.recovery.mutation ?: return@execute
        require(mutation.method == "DELETE") { "INVALID_ARGUMENT" }
        readChat(active, account, stamp)
        val chat = state.value.chat ?: return@execute
        val version = CoreJson.parseToJsonElement(mutation.body).jsonObject.getValue("version").jsonPrimitive.long
        require(chat.version == version && !state.value.running) { "CHAT_CONFLICT" }
        performMutation(active, account, stamp, mutation)
    }
    private suspend fun performMutation(active: CoreClient, account: String, stamp: Long, input: ChatMutation) {
        val response = active.api(HttpMethod.parse(input.method), input.path, input.body)
        checkActive(stamp, account)
        when {
            input.method == "DELETE" -> { record(account, InsightRecovery(), stamp); mutable.update { it.copy(chat = null) } }
            input.method == "PATCH" -> {
                val chat = CoreJson.decodeFromString<ChatSession>(response)
                mutable.update { it.copy(chat = chat, tab = 0) }
                record(account, state.value.recovery.copy(mutation = null), stamp)
            }
            else -> {
                val note = CoreJson.decodeFromString<Note>(response)
                require(note.instanceId == account.split('\u0000')[1]) { "ACCOUNT_CHANGED" }
                onSaved(account, note)
                record(account, state.value.recovery.copy(mutation = null), stamp)
                readChat(active, account, stamp)
            }
        }
    }
    private fun chatId(id: String): String {
        require(Regex("^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$").matches(id)) { "CHAT_PROTOCOL" }
        return id
    }
    private fun namesBody(names: List<String>) = buildJsonObject { put("names", JsonArray(names.map(::JsonPrimitive))) }.toString()
}

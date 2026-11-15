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
    val picker: InsightPickerState = InsightPickerState(),
    val conflicted: Boolean = false, val stopping: Boolean = false, val deleting: Boolean = false,
) {
    val running get() = chat?.turns?.any { it.assistant.status == "running" } == true
    val canStop get() = streaming || running || recovery.partial?.assistant?.status == "running"
    val locked get() = busy || running || conflicted || stopping || deleting ||
        recovery.generation != null || recovery.mutation != null || recovery.creation != null
    val canDelete get() = chat != null && !stopping && !deleting &&
        (recovery.mutation == null || recovery.mutation.method == "DELETE") &&
        recovery.creation == null && (!busy || streaming || running)
}

class InsightController(
    private val repository: suspend () -> NoteRepository, private val core: () -> CoreClient?,
    private val account: () -> String?, private val scope: CoroutineScope, private val language: () -> String,
    private val timezone: () -> String = { "UTC" },
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
    private var pickerRequest: Job? = null
    private var pickerFilters: Job? = null
    private var pickerGeneration = 0L
    private var pickerFilterGeneration = 0L
    private var pickerSession = 0L
    private val disk = Mutex()
    private var diskRevision = 0L
    private val writtenRevisions = mutableMapOf<String, Long>()
    private val seeds = mutableSetOf<String>()

    fun tab(value: Int) { mutable.update { it.copy(tab = value) }; if (value == 2) history() }
    fun query(value: String) {
        if (state.value.busy) return
        if (state.value.picker.visible) {
            if (!pickerEditable()) return
            mutable.update { it.copy(picker = it.picker.copy(query = value)) }
            loadPicker()
        } else mutable.update { it.copy(query = value, pickerNext = "", candidates = emptyList()) }
    }
    fun filter(value: String) {
        if (state.value.busy || value !in setOf("ALL", "NORMAL", "ARCHIVED")) return
        if (state.value.picker.visible) {
            if (!pickerEditable()) return
            mutable.update { it.copy(picker = it.picker.copy(filter = value)) }
            loadPickerFilters(); loadPicker()
        } else mutable.update { it.copy(filter = value, pickerNext = "", candidates = emptyList()) }
    }
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
            else loadHistory(active, account, stamp, append = false)
        }
    }

    private fun leave() {
        val account = key
        val recovery = state.value.recovery
        ++generation; operation?.cancel(); control?.cancel(); polling?.cancel(); questionWrite?.cancel()
        discardPicker()
        mutable.update { it.copy(busy = false, streaming = false, stopping = false, deleting = false) }
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
        operation?.join(); control?.join(); lifecycleWrite?.join(); polling?.cancelAndJoin(); questionWrite?.cancelAndJoin()
        pickerRequest?.join(); pickerFilters?.join()
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
                if (stamp == generation && account == this@InsightController.account())
                    mutable.update { it.copy(error = errorCode(error),
                        conflicted = it.conflicted || errorCode(error) == "CHAT_CONFLICT",
                        picker = if (it.picker.visible) it.picker.copy(error = errorCode(error)) else it.picker) }
            } finally {
                if (stamp == generation) {
                    mutable.update { it.copy(busy = false, streaming = false) }
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
        val conflict = previous?.id == chat.id && previous.version != chat.version && !applied
        val selected = if (!saved.selectionInitialized && saved.selected.isEmpty() &&
            saved.creation == null && saved.mutation == null) chat.materials.map { it.name } else saved.selected
        val partial = mergePartial(saved.partial, resolved ?: saved.partial?.let { local ->
            chat.turns.firstOrNull { it.requestId == local.requestId }
        })
        mutable.update { it.copy(chat = chat.copy(turns = chat.turns.map { turn ->
            partial?.takeIf { it.requestId == turn.requestId } ?: turn
        }), conflicted = it.conflicted || conflict, error = if (conflict) "CHAT_CONFLICT" else it.error) }
        // Durable server results replace local stream previews; reading never starts model generation.
        record(account, saved.copy(
            selected = selected, selectionInitialized = true,
            generation = if (resolved?.assistant?.status == "completed") null else pending,
            partial = partial,
            question = if (resolved?.assistant?.status == "completed" && pending?.input?.let {
                it.kind == "question" && saved.question == it.content
            } == true) "" else saved.question,
        ), stamp)
        if (resolved?.assistant?.status in setOf("failed", "canceled", "interrupted"))
            mutable.update { it.copy(error = resolved!!.assistant.errorCode.ifBlank { "CHAT_INTERRUPTED" }) }
        if (loadMaterials) resolve(active, account, stamp, selected, enforceBudget = false)
    }

    private fun mergePartial(local: ChatTurn?, remote: ChatTurn?): ChatTurn? {
        if (remote == null) return local
        if (local == null || local.requestId != remote.requestId) return remote
        val content = if (remote.assistant.status != "completed" &&
            local.assistant.content.startsWith(remote.assistant.content) &&
            local.assistant.content.length > remote.assistant.content.length) local.assistant.content
        else remote.assistant.content
        val savedName = remote.assistant.savedName.ifBlank {
            local.assistant.savedName.takeIf { local.assistant.version == remote.assistant.version }.orEmpty()
        }
        return remote.copy(assistant = remote.assistant.copy(content = content, savedName = savedName))
    }

    fun history(append: Boolean = false) = execute { active, account, stamp -> loadHistory(active, account, stamp, append) }
    private suspend fun loadHistory(active: CoreClient, account: String, stamp: Long, append: Boolean) {
        val cursor = if (append) state.value.historyNext.takeIf { it.isNotBlank() } ?: return else ""
        val page = CoreJson.decodeFromString<ChatPage>(active.api(HttpMethod.Get,
            "/chats?page_size=20" + if (cursor.isNotEmpty()) "&page_token=${cursor.encodeURLQueryComponent(encodeFull = true)}" else ""))
        checkActive(stamp, account)
        require(page.chats.all { chatId(it.id).isNotBlank() } &&
            (page.nextPageToken.isEmpty() || page.nextPageToken != cursor)) { "CHAT_PROTOCOL" }
        mutable.update { it.copy(history = (if (append) it.history + page.chats else page.chats).distinctBy { c -> c.id },
            historyNext = page.nextPageToken) }
    }

    fun open(id: String) = execute { active, account, stamp ->
        require(!state.value.running && state.value.recovery.generation == null &&
            state.value.recovery.mutation == null && state.value.recovery.creation == null) { "CHAT_BUSY" }
        discardPicker()
        record(account, InsightRecovery(chatId = chatId(id)), stamp)
        mutable.update { it.copy(chat = null, materials = null, tab = 0, conflicted = false) }
        readChat(active, account, stamp)
    }
    fun newChat() = execute { _, account, stamp ->
        require(!state.value.running && state.value.recovery.generation == null &&
            state.value.recovery.mutation == null && state.value.recovery.creation == null) { "CHAT_BUSY" }
        discardPicker()
        record(account, InsightRecovery(), stamp)
        mutable.update { it.copy(chat = null, materials = null, tab = 0, conflicted = false) }
    }

    fun openPicker() {
        if (state.value.locked || state.value.picker.visible) return
        execute { active, account, stamp ->
            val month = serviceMonth(timezone())
            val year = month.take(4).toInt()
            ++pickerSession
            mutable.update { it.copy(picker = InsightPickerState(
                visible = true, month = month, years = nearbyYears(year), busy = true)) }
            try {
                val materials = resolve(active, account, stamp, enforceBudget = false)
                mutable.update { it.copy(picker = it.picker.copy(selected = materials.notes,
                    characters = materialCharacters(materials.notes), busy = false)) }
                loadPickerFilters()
            } finally {
                if (stamp == generation) mutable.update { it.copy(picker = it.picker.copy(busy = false)) }
            }
        }
    }

    private fun discardPicker() {
        ++pickerSession; ++pickerGeneration; ++pickerFilterGeneration
        pickerRequest?.cancel(); pickerFilters?.cancel()
        mutable.update { it.copy(picker = InsightPickerState()) }
    }

    fun closePicker() {
        if (!state.value.busy) { discardPicker(); drainSeeds() }
    }

    fun completePicker() {
        val picker = state.value.picker
        if (!picker.visible || picker.busy || state.value.locked) return
        val names = picker.selected.map { it.name }
        execute { active, account, stamp ->
            persistSelection(active, account, stamp, names)
            discardPicker()
        }
    }

    fun pickerTab(value: String) {
        if (!pickerEditable() || value !in setOf("date", "tag")) return
        mutable.update { it.copy(picker = it.picker.copy(tab = value)) }
        loadPicker()
    }

    fun pickerDate(from: String, through: String) {
        if (!pickerEditable()) return
        if (!validDate(from) || !validDate(through) || from.isEmpty() && through.isNotEmpty()) {
            pickerError("INVALID_ARGUMENT"); return
        }
        val start = if (through.isNotEmpty() && through < from) through else from
        val end = if (through.isNotEmpty() && through < from) from else through
        mutable.update { it.copy(picker = it.picker.copy(from = start, through = end)) }
        loadPicker()
    }

    fun pickerClearRange() = pickerDate("", "")

    fun pickerMonth(value: String) {
        if (!pickerEditable()) return
        if (!validMonth(value)) { pickerError("INVALID_ARGUMENT"); return }
        mutable.update { it.copy(picker = it.picker.copy(month = value,
            years = nearbyYears(value.take(4).toInt()), calendarDays = emptySet())) }
        loadPickerFilters()
    }

    fun pickerYear(value: Int) {
        if (!pickerEditable()) return
        if (value !in 1000..9999) { pickerError("INVALID_ARGUMENT"); return }
        pickerMonth("$value-${state.value.picker.month.takeLast(2)}")
    }

    fun pickerTag(value: String) {
        if (!pickerEditable()) return
        val chosen = state.value.picker.chosenTags
        val tags = if (value.isBlank()) emptySet() else if (value in chosen) chosen - value else chosen + value
        if (tags.size > 20) { pickerError("CHAT_RESOURCE_LIMIT"); return }
        mutable.update { it.copy(picker = it.picker.copy(chosenTags = tags)) }
        loadPicker()
    }

    fun pickerToggle(note: Note) {
        if (!pickerEditable() || state.value.picker.busy) return
        if (note.instanceId != account()?.split('\u0000')?.getOrNull(1)) { pickerError("ACCOUNT_CHANGED"); return }
        val selected = state.value.picker.selected
        updatePickerSelection(if (selected.any { it.name == note.name }) selected.filterNot { it.name == note.name } else selected + note)
    }

    fun pickerSelectAll() {
        if (!pickerEditable() || state.value.picker.busy) return
        if (state.value.picker.next.isNotBlank()) loadPicker(append = true, all = true, select = true)
        else selectPickerNotes(state.value.picker.candidates)
    }

    fun pickerLoadMore() { if (pickerEditable()) loadPicker(append = true) }

    private fun updatePickerSelection(notes: List<Note>) {
        mutable.update { it.copy(picker = it.picker.copy(selected = notes, characters = materialCharacters(notes), error = null)) }
    }
    private fun selectPickerNotes(notes: List<Note>) {
        val choices = state.value.picker.selected.associateBy { it.name }.toMutableMap()
        notes.forEach { choices[it.name] = it }
        updatePickerSelection(choices.values.toList())
    }
    private fun pickerEditable() = state.value.picker.visible && !state.value.locked
    private fun pickerError(code: String) { mutable.update { it.copy(picker = it.picker.copy(error = code)) } }
    private fun nearbyYears(year: Int) = (maxOf(1000, year - 5)..minOf(9999, year + 5)).toList()
    private fun validMonth(value: String): Boolean = Regex("^[1-9][0-9]{3}-(0[1-9]|1[0-2])$").matches(value)
    private fun validDate(value: String): Boolean {
        if (value.isEmpty()) return true
        if (!Regex("^[1-9][0-9]{3}-(0[1-9]|1[0-2])-[0-9]{2}$").matches(value)) return false
        return value.takeLast(2).toInt() in 1..monthGrid(value.take(7)).days
    }
    private fun nextDate(value: String): String {
        require(validDate(value) && value.isNotEmpty()) { "INVALID_ARGUMENT" }
        val day = value.takeLast(2).toInt()
        if (day < monthGrid(value.take(7)).days) return "${value.take(7)}-${(day + 1).toString().padStart(2, '0')}"
        require(value.take(7) != "9999-12") { "INVALID_ARGUMENT" }
        return "${shiftMonth(value.take(7), 1)}-01"
    }
    private fun pickerCurrent(session: Long, account: String) =
        visible && state.value.picker.visible && session == pickerSession && account == this.account() && account == key

    private fun loadPicker(append: Boolean = false, all: Boolean = false, select: Boolean = false) {
        val active = core() ?: return
        val account = account() ?: return
        val snapshot = state.value.picker
        if (!snapshot.visible || append && snapshot.busy) return
        val cursor = if (append) snapshot.next.takeIf { it.isNotBlank() } ?: return else ""
        val session = pickerSession
        val stamp = ++pickerGeneration
        pickerRequest?.cancel()
        val base = if (append) snapshot.candidates else emptyList()
        mutable.update { it.copy(picker = it.picker.copy(candidates = base, next = "", busy = true, error = null)) }
        if (snapshot.tab == "date" && snapshot.from.isEmpty()) {
            mutable.update { it.copy(picker = it.picker.copy(busy = false)) }; return
        }
        pickerRequest = scope.launch {
            try {
                val query = buildString {
                    append("state=${snapshot.filter}&page_size=20&query=${snapshot.query.encodeURLQueryComponent(encodeFull = true)}")
                    if (snapshot.tab == "date" && snapshot.from.isNotEmpty()) {
                        append("&from=${snapshot.from}&until=${nextDate(snapshot.through.ifEmpty { snapshot.from })}")
                    }
                    if (snapshot.tab == "tag") snapshot.chosenTags.sorted().forEach { append("&tag=${it.encodeURLQueryComponent(encodeFull = true)}") }
                }
                var next = cursor
                var notes = base
                val cursors = mutableSetOf<String>()
                do {
                    require(cursors.add(next)) { "CHAT_PROTOCOL" }
                    val page = CoreJson.decodeFromString<ChatMaterialPage>(active.api(HttpMethod.Get,
                        "/chat-materials?$query" + if (next.isNotEmpty()) "&page_token=${next.encodeURLQueryComponent(encodeFull = true)}" else ""))
                    if (!pickerCurrent(session, account) || stamp != pickerGeneration) return@launch
                    require(page.notes.all { it.instanceId == account.split('\u0000')[1] }) { "ACCOUNT_CHANGED" }
                    notes = (notes + page.notes).distinctBy { it.name }
                    require(notes.size <= 10_000) { "CHAT_RESOURCE_LIMIT" }
                    next = page.nextPageToken
                    require(next.isEmpty() || next !in cursors) { "CHAT_PROTOCOL" }
                } while (all && next.isNotEmpty())
                mutable.update { it.copy(picker = it.picker.copy(candidates = notes, next = next, busy = false)) }
                if (select) selectPickerNotes(notes)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (pickerCurrent(session, account) && stamp == pickerGeneration)
                    mutable.update { it.copy(picker = it.picker.copy(busy = false, next = cursor, error = errorCode(error))) }
            }
        }
    }

    private fun loadPickerFilters() {
        val active = core() ?: return
        val account = account() ?: return
        val picker = state.value.picker
        val session = pickerSession
        val stamp = ++pickerFilterGeneration
        pickerFilters?.cancel()
        pickerFilters = scope.launch {
            try {
                val states = if (picker.filter == "ALL") listOf("NORMAL", "ARCHIVED") else listOf(picker.filter)
                val pages = coroutineScope {
                    val tags = states.map { source -> async {
                        CoreJson.decodeFromString<TagPage>(active.api(HttpMethod.Get, "/tags?state=$source"))
                    } }
                    val calendars = states.map { source -> async {
                        CoreJson.decodeFromString<CalendarPage>(active.api(HttpMethod.Get, "/calendar?state=$source&month=${picker.month}"))
                    } }
                    tags.awaitAll() to calendars.awaitAll()
                }
                if (!pickerCurrent(session, account) || stamp != pickerFilterGeneration) return@launch
                require(pages.second.all { it.month == picker.month }) { "CHAT_PROTOCOL" }
                val tags = pages.first.flatMap { it.tags }.groupBy { it.tag }.map { (tag, counts) ->
                    TagCount(tag, counts.sumOf { it.count })
                }.sortedBy { it.tag }
                val days = pages.second.flatMap { it.days }.filter { it.count > 0 }.map { it.date }.toSet()
                mutable.update { it.copy(picker = it.picker.copy(tags = tags, calendarDays = days)) }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (pickerCurrent(session, account) && stamp == pickerFilterGeneration) pickerError(errorCode(error))
            }
        }
    }

    fun pick(append: Boolean = false) = execute { active, account, stamp ->
        val cursor = if (append) state.value.pickerNext.takeIf { it.isNotBlank() } ?: return@execute else ""
        val response = active.api(HttpMethod.Get, "/chat-materials?state=${state.value.filter}&page_size=20" +
            "&query=${state.value.query.encodeURLQueryComponent(encodeFull = true)}" + if (cursor.isNotEmpty()) "&page_token=${cursor.encodeURLQueryComponent(encodeFull = true)}" else "")
        val page = CoreJson.decodeFromString<ChatMaterialPage>(response)
        checkActive(stamp, account)
        require(page.notes.all { it.instanceId == account.split('\u0000')[1] }) { "ACCOUNT_CHANGED" }
        mutable.update { it.copy(candidates = (if (append) it.candidates + page.notes else page.notes).distinctBy { n -> n.name },
            pickerNext = page.nextPageToken) }
    }
    fun toggle(note: Note) {
        if (state.value.picker.visible || state.value.locked) return
        if (note.instanceId != account()?.split('\u0000')?.getOrNull(1)) {
            mutable.update { it.copy(error = "ACCOUNT_CHANGED") }; return
        }
        toggle(note.name)
    }
    fun toggle(name: String) {
        if (state.value.locked || state.value.picker.visible) return
        if (!validName(name)) { mutable.update { it.copy(error = "INVALID_ARGUMENT") }; return }
        val names = state.value.recovery.selected
        changeSelection(if (name in names) names - name else names + name)
    }
    fun removeMaterial(name: String) {
        if (state.value.locked || state.value.picker.visible || name !in state.value.recovery.selected) return
        changeSelection(state.value.recovery.selected - name)
    }
    private fun changeSelection(names: List<String>) = execute { active, account, stamp ->
        require(!state.value.picker.visible) { "CHAT_BUSY" }
        record(account, state.value.recovery.copy(selected = names, selectionInitialized = true), stamp)
        mutable.update { it.copy(materials = it.materials?.takeIf { materials -> materials.notes.map { note -> note.name } == names }) }
        persistSelection(active, account, stamp, names)
    }
    fun seed(name: String) {
        if (!validName(name)) { mutable.update { it.copy(error = "INVALID_ARGUMENT") }; return }
        if (state.value.busy || state.value.picker.visible || key != account()) { seeds += name; return }
        if (state.value.locked) return
        changeSelection((state.value.recovery.selected + name).distinct())
    }
    private fun drainSeeds() {
        if (state.value.locked || state.value.picker.visible || seeds.isEmpty()) return
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
        require(!state.value.picker.visible) { "CHAT_BUSY" }
        val recovery = state.value.recovery
        persistSelection(active, account, stamp, if (!recovery.selectionInitialized && recovery.selected.isEmpty())
            recovery.creation?.names ?: recovery.selected else recovery.selected)
    }

    private suspend fun persistSelection(active: CoreClient, account: String, stamp: Long,
        names: List<String> = state.value.recovery.selected): ChatMaterials {
        require(!state.value.conflicted) { "CHAT_CONFLICT" }
        require(state.value.recovery.generation == null && !state.value.running) { "CHAT_BUSY" }
        if (state.value.recovery.mutation != null) {
            require(state.value.recovery.mutation!!.method == "PATCH") { "CHAT_OUTCOME_UNKNOWN" }
            reconcileMutation(active, account, stamp)
        }
        require(!state.value.conflicted) { "CHAT_CONFLICT" }
        require(!state.value.running) { "CHAT_BUSY" }
        val checked = if (state.value.recovery.creation == null)
            resolve(active, account, stamp, names, enforceBudget = false, publish = false) else null
        // Creation is replayed with its original names before applying a later selection.
        var current = state.value.chat
        if (current == null && (names.isNotEmpty() || state.value.recovery.creation != null)) {
            val input = state.value.recovery.creation ?: ChatCreate(newLocalId(), if (language() == "en") "Insights" else "洞察", names)
            record(account, state.value.recovery.copy(creation = input), stamp)
            val response = try { active.api(HttpMethod.Post, "/chats", CoreJson.encodeToString(input)) }
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
                put("names", JsonArray(names.map(::JsonPrimitive)))
            }.toString())
            require(state.value.recovery.mutation == null) { "CHAT_OUTCOME_UNKNOWN" }
            record(account, state.value.recovery.copy(mutation = mutation), stamp)
            performMutation(active, account, stamp, mutation)
        }
        require(state.value.chat == null || state.value.chat!!.materials == materials.materials) { "SOURCE_CHANGED" }
        record(account, state.value.recovery.copy(selected = names, selectionInitialized = true), stamp)
        mutable.update { it.copy(materials = materials, tab = 0) }
        return materials
    }

    private suspend fun clearRejectedCreation(error: Exception, account: String, stamp: Long) {
        if (error is CancellationException) throw error
        if (error is CoreException && error.status in 400..499 &&
            !error.detail.retryable && error.detail.code != "CHAT_CONFLICT")
            record(account, state.value.recovery.copy(creation = null), stamp)
    }

    fun generate(kind: String) = execute { active, account, stamp ->
        require(!state.value.picker.visible) { "CHAT_BUSY" }
        require(state.value.recovery.generation == null && !state.value.running) { "CHAT_BUSY" }
        require(kind == "insight" || kind == "question" && state.value.recovery.question.isNotBlank()) { "INVALID_ARGUMENT" }
        require(state.value.recovery.selected.isNotEmpty()) { "CHAT_MATERIAL_REQUIRED" }
        val materials = persistSelection(active, account, stamp)
        require(materials.characters <= materials.limit) { "CHAT_MATERIAL_LIMIT" }
        val chat = state.value.chat ?: throw IllegalArgumentException("CHAT_MATERIAL_REQUIRED")
        val input = ChatGenerate(newLocalId(), chat.version, kind, if (kind == "question") state.value.recovery.question else "", language())
        stream(active, account, stamp, ChatGeneration(chat.id, input))
    }

    fun retryGeneration() = execute { active, account, stamp ->
        val pending = state.value.recovery.generation ?: return@execute
        readChat(active, account, stamp)
        val turn = state.value.chat?.turns?.firstOrNull { it.requestId == pending.input.requestId }
        if (turn?.assistant?.status in setOf("completed", "running")) return@execute
        require(!state.value.conflicted) { "CHAT_CONFLICT" }
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
                val preview = mergePartial(latest.partial, partial)
                latest = latest.copy(partial = preview)
                val now = epochMillis()
                if (event.type != "delta" || now - lastPublish >= 60) {
                    val next = preview ?: throw IllegalArgumentException("CHAT_PROTOCOL")
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
            latest = latest.copy(generation = null, partial = mergePartial(latest.partial, partial),
                question = if (input.input.kind == "question" && latest.question == input.input.content) "" else latest.question)
            record(account, latest, stamp)
            readChat(active, account, stamp)
            latest = state.value.recovery
        } finally {
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
                val partial = mergePartial(local, remote)?.let { turn ->
                    if (remote == null && turn.assistant.status == "running")
                        turn.copy(assistant = turn.assistant.copy(status = "canceled", errorCode = "CHAT_CANCELED"))
                    else turn
                }
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
        record(account, state.value.recovery.copy(mutation = null, generation = null,
            selected = chat.materials.map { it.name }, selectionInitialized = true), stamp)
        discardPicker()
        mutable.update { it.copy(materials = null, conflicted = false, error = null) }
        resolve(active, account, stamp, enforceBudget = false)
    }
    fun confirmDeleteAgain() {
        if (state.value.recovery.mutation?.method == "DELETE") stopOrDelete(deleting = true)
    }

    private fun patchApplied(chat: ChatSession, input: ChatMutation): Boolean {
        val expected = CoreJson.parseToJsonElement(input.body).jsonObject
        return chat.version == expected.getValue("version").jsonPrimitive.long + 1 &&
            chat.title == expected.getValue("title").jsonPrimitive.content &&
            chat.materials.map { it.name } == expected.getValue("names").jsonArray.map { it.jsonPrimitive.content }
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
        discardPicker()
        record(account, InsightRecovery(), stamp)
        mutable.update { it.copy(chat = null, materials = null, conflicted = false, tab = 0, error = null,
            history = it.history.filterNot { summary -> summary.id == id }, candidates = emptyList(), pickerNext = "") }
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
}

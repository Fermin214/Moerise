package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.*
import io.ktor.http.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*

class InsightMaterialPicker internal constructor(
    private val core: () -> CoreClient?, private val account: () -> String?,
    private val scope: CoroutineScope, private val filters: FilterReads?, private val privacy: PrivacyController?,
    private val editable: () -> Boolean,
) {
    private val mutable = MutableStateFlow(InsightPickerState())
    val state = mutable.asStateFlow()
    private var request: Job? = null
    private var filterRequest: Job? = null
    private var changedRead: Job? = null
    private var generation = 0L
    private var owner: String? = null
    private val visited = mutableSetOf<String>()
    private val manual = linkedMapOf<String, MaterialCandidate>()
    private val automatic = linkedMapOf<String, MaterialCandidate>()
    private val provisional = linkedMapOf<String, MaterialCandidate>()
    private val skipped = mutableSetOf<String>()
    private var excluded = mutableSetOf<String>()
    private var mode = "material_manual"
    private var started = 0L
    private var exceeding = false

    internal fun outcome(result: String) { privacy?.record(mode, result, started) }

    internal fun open(key: String, month: String, group: MaterialDateGroup, compact: Boolean) {
        discard()
        owner = key
        mode = "material_manual"; started = epochMillis(); exceeding = false
        excluded = group.excluded.toMutableSet()
        mutable.value = InsightPickerState(visible = true, month = month, dateGroup = group, compact = compact,
            from = group.from, through = group.through, busy = true)
    }

    internal fun ready(notes: List<MaterialCandidate>) {
        if (!state.value.visible) return
        notes.forEach { if (it.name in state.value.dateGroup.automatic) automatic[it.name] = it else manual[it.name] = it }
        publish()
        loadFilters()
        restart()
    }

    internal fun fail(code: String, names: List<String> = emptyList()) {
        if (!state.value.visible) return
        if (names.isNotEmpty()) {
            names.forEach { manual.remove(it); automatic.remove(it); provisional.remove(it); excluded += it }
            publish()
            restart()
            mutable.update { it.copy(changed = names.distinct()) }
        } else mutable.update { it.copy(error = code, busy = false) }
    }

    internal fun discard() {
        ++generation
        owner = null
        request?.cancel(); filterRequest?.cancel(); changedRead?.cancel()
        manual.clear(); automatic.clear(); provisional.clear(); skipped.clear(); visited.clear()
        mutable.value = InsightPickerState()
    }

    internal suspend fun awaitRequests() { request?.join(); filterRequest?.join(); changedRead?.join() }

    internal fun selectedNames(): List<String>? = state.value.let {
        if (canEdit() && it.batch.isEmpty() && it.characters <= it.limit && it.selected.size <= 1000)
            it.selected.map { item -> item.name } else null
    }

    private fun canEdit() = state.value.visible && owner == account() && editable()

    fun tab(tab: String) {
        if (!canEdit() || tab == state.value.tab || tab !in setOf("date", "tag")) return
        stopBatch()
        mutable.update { it.copy(tab = tab) }
        restart()
    }

    fun date(start: String, end: String) {
        if (!canEdit()) return
        val other = end.ifEmpty { start }
        val from = minOf(start, other)
        val through = maxOf(start, other)
        stopBatch()
        mode = if (start.isEmpty() || !state.value.compact) "material_manual" else "material_date"
        if (from != state.value.dateGroup.from || through != state.value.dateGroup.through) excluded.clear()
        automatic.clear()
        mutable.update { it.copy(from = from, through = through, dateGroup = MaterialDateGroup(from, through, excluded = excluded.toSet())) }
        publish()
        restart(if (from.isNotBlank() && state.value.compact) "date" else "")
    }

    fun month(value: String) {
        if (!canEdit() || value == state.value.month) return
        mutable.update { it.copy(month = value, calendarDays = null) }
        loadFilters()
    }

    fun tag(value: String) {
        if (!canEdit()) return
        stopBatch()
        val chosen = state.value.chosenTags
        val tags = when {
            value.isEmpty() -> emptySet()
            value in chosen -> chosen - value
            !state.value.compact -> setOf(value)
            else -> chosen + value
        }
        if (tags.size > 20) { fail("CHAT_RESOURCE_LIMIT"); return }
        mode = if (tags.isEmpty()) "material_manual" else "material_tags"
        mutable.update { it.copy(chosenTags = tags) }
        restart()
    }

    fun toggle(note: MaterialCandidate) {
        if (!canEdit()) return
        if (note.name in state.value.changed) { reselect(note.name); return }
        if (state.value.selected.any { it.name == note.name }) {
            manual.remove(note.name); automatic.remove(note.name); provisional.remove(note.name)
            if (state.value.batch == "remove") skipped.remove(note.name) else skipped += note.name
            if (state.value.dateGroup.from.isNotEmpty()) excluded += note.name
        } else {
            manual[note.name] = note
            if (state.value.batch == "remove") skipped += note.name else skipped.remove(note.name)
            excluded.remove(note.name)
        }
        mutable.update { it.copy(changed = it.changed - note.name) }
        publish()
    }

    fun reselect(name: String) {
        val active = core() ?: return
        val key = owner ?: return
        if (!canEdit() || name !in state.value.changed || changedRead?.isActive == true ||
            !Regex("memos/[A-Za-z0-9_-]+").matches(name)) return
        val stamp = generation
        changedRead = scope.launch {
            try {
                val note = CoreJson.decodeFromString<Note>(active.api(HttpMethod.Get, "/notes/${name.removePrefix("memos/")}"))
                require(note.name == name && note.instanceId == key.split('\u0000')[1] && note.sourceHash.isNotEmpty()) { "CHAT_PROTOCOL" }
                if (generation != stamp || owner != key || key != account() || name !in state.value.changed) return@launch
                val item = note.candidate()
                manual[name] = item
                excluded.remove(name)
                mutable.update { it.copy(changed = it.changed - name, candidates = it.candidates.map { old ->
                    if (old.name == name) item else old
                }, error = null) }
                publish()
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (generation == stamp && key == owner) fail(errorCode(error))
            }
        }
    }

    fun bulk(select: Boolean) {
        if (!canEdit() || !state.value.compact) return
        stopBatch()
        skipped.clear()
        val action = if (select) "select" else "remove"
        mutable.update { it.copy(batch = action, manualOnly = false, error = null) }
        accumulate(state.value.candidates)
        if (state.value.exhausted) finishBatch() else if (!state.value.busy) fetch()
    }

    fun stopBatch() {
        if (state.value.batch.isBlank()) return
        ++generation
        request?.cancel()
        provisional.clear(); skipped.clear()
        mode = "material_manual"
        mutable.update { it.copy(batch = "", busy = false, manualOnly = true, error = null) }
        publish()
    }

    private fun publish() {
        val removed = if (state.value.batch == "remove") removed() else emptySet()
        val selection = (automatic + manual + provisional).values.filterNot { it.name in removed }
        mutable.update { it.copy(selected = selection, characters = selection.sumOf { note -> note.characters },
            dateGroup = it.dateGroup.copy(automatic = automatic.keys.toSet(), excluded = excluded.toSet())) }
        val over = selection.size > 1000 || state.value.characters > state.value.limit
        if (over && !exceeding) outcome("over_limit")
        exceeding = over
    }

    private fun removed() = state.value.candidates.asSequence().map { it.name }.filterNot { it in skipped }.toSet()

    private fun accumulate(notes: List<MaterialCandidate>) {
        val batch = state.value.batch
        if (batch == "date" || batch == "select") notes.forEach { note ->
            if (note.name !in manual && note.name !in skipped && (batch != "date" || note.name !in excluded))
                provisional[note.name] = note
        }
        publish()
    }

    private fun finishBatch() {
        when (state.value.batch) {
            "date" -> automatic.putAll(provisional)
            "select" -> {
                manual.putAll(provisional)
                provisional.keys.forEach { automatic.remove(it); excluded.remove(it) }
            }
            "remove" -> removed().forEach { manual.remove(it); automatic.remove(it); excluded += it }
        }
        provisional.clear(); skipped.clear()
        mutable.update { it.copy(batch = "") }
        publish()
    }

    fun loadMore() {
        if (canEdit() && !state.value.busy && !state.value.exhausted && state.value.error != "CHAT_PROTOCOL") fetch()
    }

    private fun restart(batch: String = "") {
        ++generation
        request?.cancel()
        visited.clear(); skipped.clear(); provisional.clear()
        mutable.update { it.copy(previousCandidates = it.candidates.ifEmpty { it.previousCandidates },
            candidates = emptyList(), next = "", exhausted = false, batch = batch,
            busy = false, error = null, manualOnly = !it.compact) }
        fetch()
    }

    private fun query(snapshot: InsightPickerState): String = buildString {
        append("state=ALL&page_size=40")
        if (snapshot.compact) append("&format=compact&tag_match=any")
        if (snapshot.tab == "date" && snapshot.from.isNotEmpty()) {
            val end = snapshot.through.ifEmpty { snapshot.from }
            val month = end.take(7)
            val day = end.takeLast(2).toInt()
            val until = if (day < monthGrid(month).days) "$month-${(day + 1).toString().padStart(2, '0')}"
                else "${shiftMonth(month, 1)}-01"
            append("&from=${snapshot.from}&until=$until")
        }
        if (snapshot.tab == "tag") snapshot.chosenTags.sorted().forEach { append("&tag=${it.encodeURLQueryComponent(encodeFull = true)}") }
    }

    private fun fetch() {
        val active = core() ?: return
        val owner = owner ?: return
        val stamp = generation
        val snapshot = state.value
        mutable.update { it.copy(busy = true, error = null) }
        request = scope.launch {
            try {
                val query = query(snapshot)
                do {
                    val cursor = state.value.next
                    val body = active.api(HttpMethod.Get, "/chat-materials?$query" +
                        if (cursor.isEmpty()) "" else "&page_token=${cursor.encodeURLQueryComponent(encodeFull = true)}")
                    if (generation != stamp || account() != owner) return@launch
                    val page = if (snapshot.compact) CoreJson.decodeFromString<CompactMaterialPage>(body)
                    else CoreJson.decodeFromString<ChatMaterialPage>(body).let { full ->
                        require(full.notes.all { it.instanceId == owner.split('\u0000')[1] }) { "ACCOUNT_CHANGED" }
                        CompactMaterialPage(full.notes.map { it.candidate() }, full.nextPageToken, 20_000, 1000)
                    }
                    require(page.characterLimit in 1..20_000 && page.sourceLimit in 1..1000 && page.candidates.all {
                        it.name.matches(Regex("memos/[A-Za-z0-9_-]+")) && it.sourceHash.isNotBlank() && it.characters >= 0
                    }) { "CHAT_PROTOCOL" }
                    val notes = (state.value.candidates + page.candidates).distinctBy { it.name }
                    if (state.value.batch.isNotEmpty() && notes.size > page.sourceLimit) {
                        mutable.update { it.copy(candidates = notes.take(page.sourceLimit), exhausted = true, next = "") }
                        outcome("over_limit")
                        throw IllegalArgumentException("CHAT_RESOURCE_LIMIT")
                    }
                    visited += cursor
                    require(page.nextPageToken.isEmpty() || page.nextPageToken !in visited) { "CHAT_PROTOCOL" }
                    mutable.update { it.copy(candidates = notes, next = page.nextPageToken,
                        previousCandidates = if (notes.isNotEmpty() || page.nextPageToken.isEmpty()) emptyList() else it.previousCandidates,
                        limit = page.characterLimit, exhausted = page.nextPageToken.isEmpty()) }
                    accumulate(page.candidates)
                } while (!state.value.exhausted && (state.value.batch.isNotEmpty() || state.value.candidates.isEmpty()))
                if (state.value.exhausted && state.value.batch.isNotEmpty()) finishBatch()
                mutable.update { it.copy(busy = false) }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (generation == stamp && account() == owner) fail(errorCode(error))
            }
        }
    }

    private fun loadFilters() {
        val filters = filters ?: return
        val owner = owner ?: return
        val month = state.value.month
        filterRequest?.cancel()
        filterRequest = scope.launch {
            try {
                val states = listOf("NORMAL", "ARCHIVED")
                val tags = states.flatMap { filters.tags(it).tags }.groupBy { it.tag }
                    .map { (tag, rows) -> TagCount(tag, rows.sumOf { it.count }) }.sortedBy { it.tag }
                val calendar = states.map { filters.calendar(it, month) }
                require(calendar.all { it.month == month }) { "CHAT_PROTOCOL" }
                if (account() == owner && state.value.month == month && state.value.visible) {
                    val days = calendar.flatMap { it.days }.groupBy { it.date }.mapValues { (_, rows) -> rows.sumOf { it.count } }
                    mutable.update { it.copy(tags = tags, calendarDays = days) }
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (account() == owner && state.value.visible) mutable.update { it.copy(error = errorCode(error)) }
            }
        }
    }
}

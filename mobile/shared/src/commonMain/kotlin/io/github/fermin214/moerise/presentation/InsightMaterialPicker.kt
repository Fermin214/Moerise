package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.*
import io.ktor.http.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*

class InsightMaterialPicker internal constructor(
    private val core: () -> CoreClient?, private val account: () -> String?,
    private val scope: CoroutineScope, private val editable: () -> Boolean,
) {
    private val mutable = MutableStateFlow(InsightPickerState())
    val state = mutable.asStateFlow()
    private var pickerRequest: Job? = null
    private var pickerFilters: Job? = null
    private var pickerGeneration = 0L
    private var pickerFilterGeneration = 0L
    private var pickerSession = 0L
    private var owner: String? = null

    internal fun open(key: String, month: String) {
        ++pickerSession
        owner = key
        mutable.value = InsightPickerState(visible = true, month = month, years = nearbyYears(month.take(4).toInt()), busy = true)
    }

    internal fun ready(notes: List<Note>) {
        updateSelection(notes)
        mutable.update { it.copy(busy = false) }
        loadFilters()
    }

    internal fun fail(code: String) {
        if (state.value.visible) mutable.update { it.copy(busy = false, error = code) }
    }

    internal fun discard() {
        ++pickerSession
        owner = null
        pickerRequest?.cancel(); pickerFilters?.cancel()
        mutable.value = InsightPickerState()
    }

    internal suspend fun awaitRequests() {
        pickerRequest?.join(); pickerFilters?.join()
    }

    internal fun selectedNames(): List<String>? =
        if (canEdit() && !state.value.busy) state.value.selected.map { it.name } else null

    fun tab(value: String) {
        if (!canEdit() || value !in setOf("date", "tag")) return
        mutable.update { it.copy(tab = value) }
        load()
    }

    fun date(from: String, through: String) {
        if (!canEdit()) return
        if (!validDate(from) || !validDate(through) || from.isEmpty() && through.isNotEmpty()) {
            showError("INVALID_ARGUMENT"); return
        }
        val start = if (through.isNotEmpty() && through < from) through else from
        val end = if (through.isNotEmpty() && through < from) from else through
        mutable.update { it.copy(from = start, through = end) }
        load()
    }

    fun clearRange() = date("", "")

    fun month(value: String) {
        if (!canEdit()) return
        if (!validMonth(value)) { showError("INVALID_ARGUMENT"); return }
        mutable.update { it.copy(month = value,
            years = nearbyYears(value.take(4).toInt()), calendarDays = emptySet()) }
        loadFilters()
    }

    fun year(value: Int) {
        if (!canEdit()) return
        if (value !in 1000..9999) { showError("INVALID_ARGUMENT"); return }
        month("$value-${state.value.month.takeLast(2)}")
    }

    fun tag(value: String) {
        if (!canEdit()) return
        val chosen = state.value.chosenTags
        val tags = if (value.isBlank()) emptySet() else if (value in chosen) chosen - value else chosen + value
        if (tags.size > 20) { showError("CHAT_RESOURCE_LIMIT"); return }
        mutable.update { it.copy(chosenTags = tags) }
        load()
    }

    fun toggle(note: Note) {
        if (!canEdit() || state.value.busy) return
        if (note.instanceId != account()?.split('\u0000')?.getOrNull(1)) { showError("ACCOUNT_CHANGED"); return }
        val selected = state.value.selected
        updateSelection(if (selected.any { it.name == note.name }) selected.filterNot { it.name == note.name } else selected + note)
    }

    fun selectAll() {
        if (!canEdit() || state.value.busy) return
        if (state.value.next.isNotBlank()) load(append = true, all = true, select = true)
        else selectNotes(state.value.candidates)
    }

    fun loadMore() { if (canEdit()) load(append = true) }

    private fun updateSelection(notes: List<Note>) {
        mutable.update { it.copy(selected = notes, characters = materialCharacters(notes), error = null) }
    }
    private fun selectNotes(notes: List<Note>) {
        val choices = state.value.selected.associateBy { it.name }.toMutableMap()
        notes.forEach { choices[it.name] = it }
        updateSelection(choices.values.toList())
    }
    private fun canEdit() = state.value.visible && owner == account() && editable()
    private fun showError(code: String) { mutable.update { it.copy(error = code) } }
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
    private fun isCurrent(session: Long, account: String) =
        state.value.visible && session == pickerSession && account == owner && account == this.account()

    private fun load(append: Boolean = false, all: Boolean = false, select: Boolean = false) {
        val active = core() ?: return
        val account = account() ?: return
        val snapshot = state.value
        if (!snapshot.visible || append && snapshot.busy) return
        val cursor = if (append) snapshot.next.takeIf { it.isNotBlank() } ?: return else ""
        val session = pickerSession
        val stamp = ++pickerGeneration
        pickerRequest?.cancel()
        val base = if (append) snapshot.candidates else emptyList()
        mutable.update { it.copy(candidates = base, next = "", busy = true, error = null) }
        if (snapshot.tab == "date" && snapshot.from.isEmpty()) {
            mutable.update { it.copy(busy = false) }; return
        }
        pickerRequest = scope.launch {
            try {
                val query = buildString {
                    append("state=ALL&page_size=20")
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
                    if (!isCurrent(session, account) || stamp != pickerGeneration) return@launch
                    require(page.notes.all { it.instanceId == account.split('\u0000')[1] }) { "ACCOUNT_CHANGED" }
                    notes = (notes + page.notes).distinctBy { it.name }
                    require(notes.size <= 10_000) { "CHAT_RESOURCE_LIMIT" }
                    next = page.nextPageToken
                    require(next.isEmpty() || next !in cursors) { "CHAT_PROTOCOL" }
                } while (all && next.isNotEmpty())
                mutable.update { it.copy(candidates = notes, next = next, busy = false) }
                if (select) selectNotes(notes)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (isCurrent(session, account) && stamp == pickerGeneration)
                    mutable.update { it.copy(busy = false, next = cursor, error = errorCode(error)) }
            }
        }
    }

    private fun loadFilters() {
        val active = core() ?: return
        val account = account() ?: return
        val picker = state.value
        val session = pickerSession
        val stamp = ++pickerFilterGeneration
        pickerFilters?.cancel()
        pickerFilters = scope.launch {
            try {
                val states = listOf("NORMAL", "ARCHIVED")
                val pages = coroutineScope {
                    val tags = states.map { source -> async {
                        CoreJson.decodeFromString<TagPage>(active.api(HttpMethod.Get, "/tags?state=$source"))
                    } }
                    val calendars = states.map { source -> async {
                        CoreJson.decodeFromString<CalendarPage>(active.api(HttpMethod.Get, "/calendar?state=$source&month=${picker.month}"))
                    } }
                    tags.awaitAll() to calendars.awaitAll()
                }
                if (!isCurrent(session, account) || stamp != pickerFilterGeneration) return@launch
                require(pages.second.all { it.month == picker.month }) { "CHAT_PROTOCOL" }
                val tags = pages.first.flatMap { it.tags }.groupBy { it.tag }.map { (tag, counts) ->
                    TagCount(tag, counts.sumOf { it.count })
                }.sortedBy { it.tag }
                val days = pages.second.flatMap { it.days }.filter { it.count > 0 }.map { it.date }.toSet()
                mutable.update { it.copy(tags = tags, calendarDays = days) }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (isCurrent(session, account) && stamp == pickerFilterGeneration) showError(errorCode(error))
            }
        }
    }

}

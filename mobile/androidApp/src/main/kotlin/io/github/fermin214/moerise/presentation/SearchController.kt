package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.HttpMethod
import io.ktor.http.encodeURLQueryComponent
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.serialization.Serializable
import kotlinx.serialization.SerialName
import kotlinx.serialization.json.*

@Serializable data class TagCount(val tag: String, val count: Int)
@Serializable data class TagPage(val tags: List<TagCount>)
@Serializable data class DayCount(val date: String, val count: Int)
@Serializable data class CalendarPage(val month: String, val days: List<DayCount>)
@Serializable data class SearchSemantic(val used: Boolean = false, val reason: String = "")
@Serializable data class SearchPage(
    val notes: List<Note>, @SerialName("next_page_token") val nextPageToken: String = "",
    val mode: String, val semantic: SearchSemantic,
)

data class SearchQuery(
    val text: String = "", val mode: String = "keyword", val tags: Set<String> = emptySet(),
    val state: String = "NORMAL", val date: String = "",
) {
    internal val filtered: Boolean get() = text.isNotBlank() || tags.isNotEmpty() || date.isNotEmpty()

    internal fun matchesScope(note: Note, timezone: String): Boolean =
        (state == "ALL" || state == note.state) && tags.all { it in note.tags } &&
            (date.isEmpty() || displayServiceTime(note.createTime, timezone).take(10) == date)

    internal fun matchesKeyword(note: Note): Boolean = note.content.contains(text.trim(), ignoreCase = true)
}

data class SearchState(
    val query: SearchQuery = SearchQuery(), val notes: List<Note> = emptyList(), val next: String = "",
    val tags: List<TagCount> = emptyList(), val calendar: CalendarPage? = null,
    val loading: Boolean = false, val filtersLoading: Boolean = false, val error: String? = null,
    val filtersError: String? = null, val responseMode: String? = null, val semantic: SearchSemantic? = null,
    val tab: Int = 0, val month: String? = null,
)

class SearchController(
    private val core: () -> CoreClient?, private val account: () -> String?, private val scope: CoroutineScope,
    private val reads: FilterReads? = null,
    private val privacy: PrivacyController? = null,
    private val repository: (suspend () -> NoteRepository)? = null,
    private val timezone: () -> String = { "UTC" },
) {
    private val mutable = MutableStateFlow(SearchState())
    val state = mutable.asStateFlow()
    private var generation = 0L
    private var filterGeneration = 0L
    private var request: Job? = null
    private var refreshPending = false
    private var filters: Job? = null
    private val cursors = mutableSetOf<String>()

    fun clear() {
        cursors.clear()
        refreshPending = false
        ++generation; ++filterGeneration; request?.cancel(); filters?.cancel()
        mutable.value = SearchState()
    }

    fun change(query: SearchQuery) {
        if (query == state.value.query) return
        refreshPending = false
        ++generation; request?.cancel()
        val stateChanged = query.state != state.value.query.state
        if (stateChanged) {
            ++filterGeneration; filters?.cancel()
            mutable.update { it.copy(tags = emptyList(), calendar = null, filtersLoading = false, filtersError = null) }
        }
        val keepLocalList = !stateChanged && query.text.isBlank() && state.value.query.text.isBlank()
        mutable.update {
            it.copy(
                query = query,
                notes = if (keepLocalList) it.notes else emptyList(),
                next = "",
                loading = false,
                error = null,
                responseMode = if (keepLocalList) it.responseMode else null,
                semantic = if (keepLocalList) it.semantic else null,
            )
        }
    }

    fun tab(value: Int) { mutable.update { it.copy(tab = value) } }
    fun month(value: String) { mutable.update { it.copy(month = value) } }

    internal fun replaceNote(note: Note, refresh: Boolean) {
        val zone = timezone()
        ++generation
        request?.cancel()
        request = null
        refreshPending = state.value.query.filtered
        mutable.update { current ->
            val matches = current.query.matchesScope(note, zone) &&
                (current.query.mode != "keyword" || current.query.matchesKeyword(note))
            current.copy(notes = current.notes.mapNotNull { previous ->
                if (previous.name != note.name) previous else note.takeIf { matches }
            }, next = "", loading = false)
        }
        if (refresh) refreshIfNeeded()
    }

    internal fun refreshIfNeeded() {
        if (refreshPending && request?.isActive != true) search()
    }

    fun search(append: Boolean = false) {
        val key = account() ?: return
        if (state.value.loading) return
        val query = state.value.query
        val token = if (append) state.value.next.takeIf { it.isNotEmpty() } ?: return else ""
        val stamp = ++generation
        request?.cancel()
        val effectiveMode = if (query.text.isBlank()) "keyword" else query.mode
        val localCandidate = effectiveMode == "keyword" && repository != null
        val operation = "search_${if (query.text.isBlank()) "keyword" else query.mode}"
        val started = epochMillis()
        if (!localCandidate) mutable.update { it.copy(loading = true, error = null) }
        request = scope.launch {
            try {
                require(query.mode in setOf("keyword", "semantic", "hybrid")) { "SEARCH_MODE_INVALID" }
                if (localCandidate) {
                    val repo = repository!!.invoke()
                    if (repo.isNotesSynced(key)) {
                        val offset = if (append && token.startsWith("local:")) token.removePrefix("local:").toIntOrNull() ?: 0 else 0
                        val (rows, hasMore) = repo.localSearch(key, query, timezone(), 20, offset)
                        if (stamp != generation || key != account()) return@launch
                        val next = if (hasMore) "local:${offset + rows.size}" else ""
                        if (!append) refreshPending = false
                        mutable.update {
                            it.copy(
                                notes = (if (append) it.notes + rows else rows).distinctBy { note -> note.name },
                                next = next,
                                responseMode = "keyword",
                                semantic = SearchSemantic(),
                                loading = false,
                                error = null,
                            )
                        }
                        if (!append) privacy?.record(operation, started = started)
                        return@launch
                    }
                    if (stamp != generation || key != account()) return@launch
                    if (core() == null) return@launch
                    mutable.update { it.copy(loading = true, error = null) }
                }
                val active = core() ?: return@launch
                val page = if (effectiveMode == "keyword") {
                    val parameters = buildList {
                        add("state" to query.state); add("order" to "created_desc"); add("page_size" to "20")
                        if (query.text.isNotEmpty()) add("query" to query.text)
                        query.tags.sorted().forEach { add("tag" to it) }
                        if (query.date.isNotEmpty()) add("date" to query.date)
                        if (token.isNotEmpty()) add("page_token" to token)
                    }.joinToString("&") { (name, value) -> "$name=${value.encodeURLQueryComponent(encodeFull = true)}" }
                    val response = active.api(HttpMethod.Get, "/notes?$parameters")
                    val notes = withContext(Dispatchers.Default) { CoreJson.decodeFromString<io.github.fermin214.moerise.domain.NotePage>(response) }
                    SearchPage(notes.notes, notes.nextPageToken, "keyword", SearchSemantic())
                } else {
                    val body = buildJsonObject {
                        put("query", query.text); put("mode", effectiveMode); put("state", query.state)
                        put("tags", JsonArray(query.tags.sorted().map(::JsonPrimitive)))
                        put("date", query.date); put("page_size", 20); put("page_token", token)
                    }.toString()
                    val response = active.api(HttpMethod.Post, "/search", body)
                    withContext(Dispatchers.Default) { CoreJson.decodeFromString<SearchPage>(response) }
                }
                require(page.mode in setOf("keyword", "semantic", "hybrid")) { "INCOMPATIBLE_CORE" }
                require(page.notes.all { it.instanceId == key.split('\u0000')[1] &&
                    Regex("^memos/[A-Za-z0-9_-]+$").matches(it.name) && it.sourceHash.isNotBlank() &&
                    it.state in setOf("NORMAL", "ARCHIVED") && (query.state == "ALL" || it.state == query.state)
                }) { "ACCOUNT_CHANGED" }
                if (stamp != generation || key != account()) return@launch
                if (!append) {
                    cursors.clear()
                    refreshPending = false
                }
                val next = page.nextPageToken.takeIf { it.isNotBlank() && cursors.add(it) }.orEmpty()
                mutable.update { it.copy(notes = (if (append) it.notes + page.notes else page.notes).distinctBy { note -> note.name },
                    next = next, responseMode = page.mode, semantic = page.semantic, loading = false) }
                if (!append) privacy?.record(operation, started = started)
            } catch (error: Exception) {
                if (!append && key == account()) privacy?.record(operation, usageResult(error), started, error = error)
                if (error is CancellationException) throw error
                if (stamp == generation && key == account()) mutable.update { it.copy(loading = false, error = errorCode(error)) }
            }
        }
    }

    fun filters(month: String) {
        val active = core()
        if (reads == null && active == null) return
        val key = account() ?: return
        val sourceState = state.value.query.state
        val stamp = ++filterGeneration
        filters?.cancel()
        reads?.cached(sourceState, month)?.let { page ->
            mutable.update { it.copy(tags = page.tags.tags, calendar = page.calendar, filtersLoading = false, filtersError = null) }
            return
        }
        mutable.update {
            it.copy(
                tags = if (reads == null) it.tags else emptyList(),
                calendar = it.calendar?.takeIf { page -> reads == null && page.month == month },
                filtersLoading = true,
                filtersError = null,
            )
        }
        filters = scope.launch {
            try {
                val page = reads?.filters(sourceState, month) ?: FilterPage(
                    CoreJson.decodeFromString(requireNotNull(active).api(HttpMethod.Get, "/tags?state=$sourceState")),
                    CoreJson.decodeFromString(requireNotNull(active).api(HttpMethod.Get,
                        "/calendar?state=$sourceState&month=${month.encodeURLQueryComponent(encodeFull = true)}")),
                )
                require(page.calendar.month == month)
                if (stamp == filterGeneration && key == account() && sourceState == state.value.query.state) mutable.update { it.copy(
                    tags = page.tags.tags, calendar = page.calendar, filtersLoading = false) }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == filterGeneration) mutable.update { it.copy(filtersLoading = false, filtersError = errorCode(error)) }
            }
        }
    }

    fun leave() {
        ++generation; ++filterGeneration; request?.cancel(); filters?.cancel()
        request = null
        mutable.update { it.copy(loading = false, filtersLoading = false) }
    }
}

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
)

data class SearchState(
    val query: SearchQuery = SearchQuery(), val notes: List<Note> = emptyList(), val next: String = "",
    val tags: List<TagCount> = emptyList(), val calendar: CalendarPage? = null,
    val loading: Boolean = false, val filtersLoading: Boolean = false, val error: String? = null,
    val filtersError: String? = null, val responseMode: String? = null, val semantic: SearchSemantic? = null,
    val tab: Int = 0, val month: String? = null,
)

class SearchController(
    private val core: () -> CoreClient?, private val account: () -> String?, private val scope: CoroutineScope,
) {
    private val mutable = MutableStateFlow(SearchState())
    val state = mutable.asStateFlow()
    private var generation = 0L
    private var filterGeneration = 0L
    private var request: Job? = null
    private var filters: Job? = null

    fun clear() {
        ++generation; ++filterGeneration; request?.cancel(); filters?.cancel()
        mutable.value = SearchState()
    }

    fun change(query: SearchQuery) {
        if (query == state.value.query) return
        ++generation; request?.cancel()
        if (query.state != state.value.query.state) {
            ++filterGeneration; filters?.cancel()
            mutable.update { it.copy(tags = emptyList(), calendar = null, filtersLoading = false, filtersError = null) }
        }
        mutable.update { it.copy(query = query, notes = emptyList(), next = "", loading = false, error = null,
            responseMode = null, semantic = null) }
    }

    fun tab(value: Int) { mutable.update { it.copy(tab = value) } }
    fun month(value: String) { mutable.update { it.copy(month = value) } }

    fun search(append: Boolean = false) {
        val active = core() ?: return
        val key = account() ?: return
        if (state.value.loading) return
        val query = state.value.query
        val token = if (append) state.value.next.takeIf { it.isNotEmpty() } ?: return else ""
        val stamp = ++generation
        request?.cancel()
        mutable.update { it.copy(loading = true, error = null) }
        request = scope.launch {
            try {
                require(query.mode == "keyword" || query.text.isNotBlank()) { "SEARCH_QUERY_REQUIRED" }
                val body = buildJsonObject {
                    put("query", query.text); put("mode", query.mode); put("state", query.state)
                    put("tags", JsonArray(query.tags.sorted().map(::JsonPrimitive)))
                    put("date", query.date); put("page_size", 20); put("page_token", token)
                }.toString()
                val response = active.api(HttpMethod.Post, "/search", body)
                val page = withContext(Dispatchers.Default) { CoreJson.decodeFromString<SearchPage>(response) }
                require(page.mode in setOf("keyword", "semantic", "hybrid")) { "INCOMPATIBLE_CORE" }
                require(page.notes.all { it.instanceId == key.split('\u0000')[1] }) { "ACCOUNT_CHANGED" }
                if (stamp != generation || key != account()) return@launch
                mutable.update { it.copy(notes = (if (append) it.notes + page.notes else page.notes).distinctBy { note -> note.name },
                    next = page.nextPageToken, responseMode = page.mode, semantic = page.semantic, loading = false) }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == generation) mutable.update { it.copy(loading = false, error = errorCode(error)) }
            }
        }
    }

    fun filters(month: String) {
        val active = core() ?: return
        val key = account() ?: return
        val sourceState = state.value.query.state
        val stamp = ++filterGeneration
        filters?.cancel()
        mutable.update { it.copy(filtersLoading = true, filtersError = null, tags = emptyList(), calendar = null) }
        filters = scope.launch {
            try {
                coroutineScope {
                    val tags = async { CoreJson.decodeFromString<TagPage>(active.api(HttpMethod.Get, "/tags?state=$sourceState")) }
                    val calendar = async { CoreJson.decodeFromString<CalendarPage>(active.api(HttpMethod.Get,
                        "/calendar?state=$sourceState&month=${month.encodeURLQueryComponent()}")) }
                    val tagPage = tags.await()
                    val calendarPage = calendar.await()
                    require(calendarPage.month == month)
                    if (stamp == filterGeneration && key == account() && sourceState == state.value.query.state) mutable.update { it.copy(
                        tags = tagPage.tags, calendar = calendarPage, filtersLoading = false) }
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == filterGeneration) mutable.update { it.copy(filtersLoading = false, filtersError = errorCode(error)) }
            }
        }
    }

    fun leave() { ++generation; ++filterGeneration; request?.cancel(); filters?.cancel(); mutable.update { it.copy(loading = false, filtersLoading = false) } }
}

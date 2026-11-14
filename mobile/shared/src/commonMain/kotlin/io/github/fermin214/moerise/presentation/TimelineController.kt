package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.NotePage
import io.ktor.http.HttpMethod
import io.ktor.http.encodeURLQueryComponent
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*

data class TimelineUiState(
    val notes: List<Note> = emptyList(),
    val nextPageToken: String = "",
    val loading: Boolean = false,
    val fresh: Boolean = false,
    val error: String? = null,
)

class TimelineController(
    private val repository: suspend () -> NoteRepository,
    private val core: () -> CoreClient?,
    private val scope: CoroutineScope,
    private val filter: String = "NORMAL",
) {
    private val mutable = MutableStateFlow(TimelineUiState())
    val state = mutable.asStateFlow()
    private var key: String? = null
    private var generation = 0L
    private var accountGeneration = 0L
    private var request: Job? = null

    fun reset(accountKey: String?) {
        ++generation
        ++accountGeneration
        request?.cancel()
        key = accountKey
        mutable.value = TimelineUiState()
    }

    suspend fun readCache() {
        if (filter != "NORMAL") return
        val account = key ?: return
        val stamp = accountGeneration
        val cached = repository().snapshot(account) ?: return
        if (stamp == accountGeneration && !state.value.fresh) mutable.update { it.copy(notes = cached.notes) }
    }

    fun hideAccountData() {
        val account = key ?: return
        reset(account)
        scope.launch { repository().clearCache(account) }
    }

    fun refresh() = load(false)
    fun more() = load(true)

    private fun load(append: Boolean) {
        val account = key ?: return
        val active = core() ?: return
        val token = if (append) state.value.nextPageToken.takeIf { it.isNotBlank() } ?: return else ""
        if (append && state.value.loading) return
        val stamp = ++generation
        request?.cancel()
        mutable.update { it.copy(loading = true, error = null) }
        request = scope.launch {
            try {
                val page = CoreJson.decodeFromString<NotePage>(active.api(HttpMethod.Get,
                    "/notes?state=$filter&order=created_desc&page_size=20" +
                        if (token.isEmpty()) "" else "&page_token=${token.encodeURLQueryComponent()}"))
                require(page.notes.all { it.instanceId == account.split('\u0000')[1] }) { "ACCOUNT_CHANGED" }
                if (stamp != generation) return@launch
                val notes = (if (append) state.value.notes + page.notes else page.notes).distinctBy { it.name }
                if (filter == "NORMAL") repository().cache(account, NotePage(notes, page.nextPageToken))
                if (stamp == generation) mutable.value = TimelineUiState(notes, page.nextPageToken, fresh = true)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == generation) mutable.update { it.copy(loading = false, error = errorCode(error)) }
            }
        }
    }

    fun onSaved(accountKey: String, note: Note) {
        if (key != accountKey || key?.split('\u0000')?.getOrNull(1) != note.instanceId) return
        mutable.update { it.copy(notes = orderedNotes((listOf(note) + it.notes.filter { old -> old.name != note.name }).filter { it.state == "NORMAL" })) }
        refresh()
    }
}

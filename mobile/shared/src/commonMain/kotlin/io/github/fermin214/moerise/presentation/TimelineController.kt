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
    val snapshotAt: Long = 0,
    val refreshing: Boolean = false,
    val loadingMore: Boolean = false,
    val displayKeys: Map<String, String> = emptyMap(),
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
    private var pageCount = 1
    private val cursors = mutableSetOf<String>()

    fun reset(accountKey: String?) {
        ++generation
        ++accountGeneration
        request?.cancel()
        key = accountKey
        pageCount = 1
        cursors.clear()
        mutable.value = TimelineUiState()
    }

    suspend fun stopForRemoval() {
        ++generation; ++accountGeneration
        request?.cancelAndJoin()
        key = null
        mutable.value = TimelineUiState()
    }

    suspend fun readCache() {
        if (filter != "NORMAL") return
        val account = key ?: return
        val stamp = accountGeneration
        val cached = repository().datedSnapshot(account) ?: return
        if (stamp == accountGeneration && !state.value.fresh) mutable.update { it.copy(notes = cached.first.notes, snapshotAt = cached.second) }
    }

    fun hideAccountData() {
        val account = key ?: return
        reset(account)
        scope.launch { repository().clearCache(account) }
    }

    fun refresh(userInitiated: Boolean = false) = load(false, userInitiated)
    fun more() = load(true, false)

    private fun load(append: Boolean, userInitiated: Boolean) {
        val account = key ?: return
        val active = core() ?: return
        val token = if (append) state.value.nextPageToken.takeIf { it.isNotBlank() } ?: return else ""
        if (state.value.loading && (!userInitiated || !state.value.loadingMore)) return
        val stamp = ++generation
        request?.cancel()
        mutable.update { it.copy(loading = true, error = null, loadingMore = append,
            refreshing = !append && (userInitiated || it.refreshing)) }
        request = scope.launch {
            try {
                val repo = repository()
                if (filter == "NORMAL" && repo.hasPendingCreation(account)) return@launch
                val rows = mutableListOf<Note>()
                val seen = if (append) cursors.toMutableSet() else mutableSetOf()
                var next = token
                var loaded = 0
                val tail = state.value.notes.lastOrNull()?.name.takeIf { !append && pageCount > 1 }
                val oldNames = state.value.notes.mapTo(mutableSetOf()) { it.name }
                var added = 0
                do {
                    val body = active.api(HttpMethod.Get,
                        "/notes?state=$filter&order=created_desc&page_size=20" +
                            if (next.isEmpty()) "" else "&page_token=${next.encodeURLQueryComponent(encodeFull = true)}")
                    val page = withContext(Dispatchers.Default) { CoreJson.decodeFromString<NotePage>(body) }
                    require(page.notes.all { it.instanceId == account.split('\u0000')[1] && it.state == filter }) { "ACCOUNT_CHANGED" }
                    rows.addAll(page.notes)
                    added += page.notes.count { it.name !in oldNames }
                    next = page.nextPageToken.takeIf { it.isNotBlank() && seen.add(it) }.orEmpty()
                    loaded++
                } while (!append && next.isNotBlank() && (loaded < pageCount ||
                        tail != null && rows.none { it.name == tail } && rows.size < state.value.notes.size + added && loaded < pageCount + 2))
                if (filter == "NORMAL" && repo.hasPendingCreation(account) || stamp != generation) return@launch
                val previous = state.value
                val byName = previous.notes.associateBy { it.name }
                val notes = (if (append) previous.notes + rows else rows).distinctBy { it.name }
                    .map { note -> byName[note.name]?.takeIf { it == note } ?: note }
                val unchanged = notes == previous.notes && next == previous.nextPageToken
                if (filter == "NORMAL" && (!unchanged || !previous.fresh))
                    repo.cache(account, NotePage(notes, next))
                if (stamp == generation) {
                    pageCount = if (append) pageCount + 1 else loaded
                    cursors.clear(); cursors.addAll(seen)
                    mutable.value = previous.copy(notes = if (unchanged) previous.notes else notes, nextPageToken = next, fresh = true,
                        snapshotAt = if (unchanged && previous.snapshotAt != 0L) previous.snapshotAt else epochMillis())
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == generation) mutable.update { it.copy(fresh = false, error = errorCode(error)) }
            } finally {
                if (stamp == generation) mutable.update { it.copy(loading = false, refreshing = false, loadingMore = false) }
            }
        }
    }

    fun remove(name: String) {
        ++generation
        request?.cancel()
        mutable.update { it.copy(notes = it.notes.filterNot { note -> note.name == name }, displayKeys = it.displayKeys - name,
            loading = false, refreshing = false, loadingMore = false) }
    }

    fun onSaved(accountKey: String, note: Note, draftId: String? = null) {
        if (key != accountKey || key?.split('\u0000')?.getOrNull(1) != note.instanceId) return
        ++generation
        request?.cancel()
        mutable.update { it.copy(notes = orderedNotes((listOf(note) + it.notes.filter { old -> old.name != note.name }).filter { it.state == filter }),
            loading = false, refreshing = false, loadingMore = false,
            displayKeys = if (draftId == null) it.displayKeys else it.displayKeys + (note.name to "queued:$draftId")) }
    }
}

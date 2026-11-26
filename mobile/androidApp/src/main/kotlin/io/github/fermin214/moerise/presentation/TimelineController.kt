package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.NotePage
import io.ktor.http.HttpMethod
import io.ktor.http.encodeURLQueryComponent
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

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
    private val visible: (String) -> Boolean = { true },
    private val localPage: suspend (NoteRepository, String, String, Int) -> Pair<List<Note>, Boolean> = { repo, account, state, size ->
        repo.localNotesPage(account, state, size, 0)
    },
) {
    private val mutable = MutableStateFlow(TimelineUiState())
    val state = mutable.asStateFlow()
    private var key: String? = null
    private var generation = 0L
    private var accountGeneration = 0L
    private var request: Job? = null
    private var pageCount = 1
    private val cursors = mutableSetOf<String>()
    private val projectionRead = Mutex()
    private var deliveredProjection: Pair<String, Long>? = null
    private var hidden = false

    private fun canRead(account: String) = !hidden && key == account && visible(account)

    fun reset(accountKey: String?) {
        ++generation
        ++accountGeneration
        request?.cancel()
        key = accountKey
        hidden = false
        deliveredProjection = null
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
        val account = key?.takeIf(::canRead) ?: return
        val stamp = accountGeneration
        val repo = repository()
        if (repo.isNotesSynced(account)) {
            reloadLocal()
            return
        }
        if (filter != "NORMAL") return
        val cached = repo.datedSnapshot(account) ?: return
        if (stamp == accountGeneration && canRead(account) && !state.value.fresh) mutable.update { it.copy(notes = cached.first.notes, snapshotAt = cached.second) }
    }

    suspend fun refreshProjection(legacyFallback: Boolean = true) {
        val account = key?.takeIf(::canRead) ?: return
        val stamp = generation
        val repo = repository()
        if (repo.isNotesSynced(account)) {
            if (deliveredProjection != (account to repo.projection.value)) readProjection(account, stamp)
        }
        else if (legacyFallback && stamp == generation && canRead(account)) refresh()
    }

    suspend fun reloadLocal() {
        val account = key?.takeIf(::canRead) ?: return
        readProjection(account, generation)
    }

    private suspend fun readProjection(account: String, stamp: Long, append: Boolean = false) = projectionRead.withLock {
        if (!canRead(account) || stamp != generation) return@withLock
        val repo = repository()
        if (!canRead(account) || stamp != generation || !repo.isNotesSynced(account)) return@withLock
        var force = append
        val count = pageCount + if (append) 1 else 0
        while (canRead(account) && stamp == generation) {
            val readProjection = account to repo.projection.value
            if (!force && deliveredProjection == readProjection) break
            val (local, hasMore) = localPage(repo, account, filter, 20 * count)
            if (!canRead(account) || stamp != generation) break
            pageCount = count
            applyNotes(local, if (hasMore) "local:${local.size}" else "", stamp)
            deliveredProjection = readProjection
            force = false
            if (repo.projection.value == readProjection.second) break
        }
    }

    private fun applyNotes(loaded: List<Note>, next: String, expectedGen: Long) {
        if (expectedGen != generation) return
        val previous = state.value
        val byName = previous.notes.associateBy { it.name }
        val notes = loaded.map { note -> byName[note.name]?.takeIf { it == note } ?: note }
        val unchanged = notes == previous.notes && next == previous.nextPageToken
        mutable.value = previous.copy(
            notes = if (unchanged) previous.notes else notes,
            nextPageToken = next,
            fresh = true,
            error = null,
            snapshotAt = if (unchanged && previous.snapshotAt != 0L) previous.snapshotAt else epochMillis(),
        )
    }

    fun hideAccountData() {
        val account = key ?: return
        reset(account)
        hidden = true
        scope.launch { repository().clearCache(account) }
    }

    fun revealAccountData() { hidden = false }

    fun refresh(userInitiated: Boolean = false) = load(false, userInitiated)
    fun more() = load(true, false)

    private fun load(append: Boolean, userInitiated: Boolean) {
        val account = key?.takeIf(::canRead) ?: return
        val token = if (append) state.value.nextPageToken.takeIf { it.isNotBlank() } ?: return else ""
        val active = core()
        if (active == null && !(append && token.startsWith("local:"))) return
        if (state.value.loading && (!userInitiated || !state.value.loadingMore)) return
        val stamp = ++generation
        request?.cancel()
        if (append && token.startsWith("local:")) {
            mutable.update { it.copy(loading = true, error = null, loadingMore = true) }
            request = scope.launch {
                try {
                    readProjection(account, stamp, append = true)
                } catch (error: Exception) {
                    if (error is CancellationException) throw error
                    if (stamp == generation) mutable.update { it.copy(error = errorCode(error)) }
                } finally {
                    if (stamp == generation) mutable.update { it.copy(loading = false, loadingMore = false) }
                }
            }
            return
        }
        mutable.update { it.copy(loading = true, error = null, loadingMore = append,
            refreshing = !append && (userInitiated || it.refreshing)) }
        request = scope.launch {
            try {
                val repo = repository()
                if (!canRead(account)) return@launch
                if (repo.isNotesSynced(account)) {
                    if (userInitiated && active != null) repo.syncNotes(account, active)
                    readProjection(account, stamp, append)
                    return@launch
                }
                val client = active ?: return@launch
                if (filter == "NORMAL" && repo.hasPendingCreation(account)) return@launch
                val rows = mutableListOf<Note>()
                val seen = if (append) cursors.toMutableSet() else mutableSetOf()
                var next = token
                var loaded = 0
                val tail = state.value.notes.lastOrNull()?.name.takeIf { !append && pageCount > 1 }
                val oldNames = state.value.notes.mapTo(mutableSetOf()) { it.name }
                var added = 0
                do {
                    if (!canRead(account)) return@launch
                    val body = client.api(HttpMethod.Get,
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
                if (filter == "NORMAL" && repo.hasPendingCreation(account) || stamp != generation || !canRead(account)) return@launch
                if (repo.isNotesSynced(account)) {
                    readProjection(account, stamp, append)
                    return@launch
                }
                val previous = state.value
                val byName = previous.notes.associateBy { it.name }
                val notes = (if (append) previous.notes + rows else rows).distinctBy { it.name }
                    .map { note -> byName[note.name]?.takeIf { it == note } ?: note }
                val unchanged = notes == previous.notes && next == previous.nextPageToken
                if (filter == "NORMAL" && (!unchanged || !previous.fresh))
                    repo.cache(account, NotePage(notes, next))
                if (stamp == generation && canRead(account) && deliveredProjection == null) {
                    deliveredProjection = null
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
        deliveredProjection = null
        ++generation
        request?.cancel()
        mutable.update { it.copy(notes = it.notes.filterNot { note -> note.name == name }, displayKeys = it.displayKeys - name,
            loading = false, refreshing = false, loadingMore = false) }
    }

    fun onSaved(accountKey: String, note: Note, displayKey: String? = null) {
        if (!canRead(accountKey) || accountKey.split('\u0000').getOrNull(1) != note.instanceId) return
        ++generation
        request?.cancel()
        mutable.update { it.copy(notes = orderedNotes((listOf(note) + it.notes.filter { old -> old.name != note.name }).filter { it.state == filter }),
            loading = false, refreshing = false, loadingMore = false,
            displayKeys = if (displayKey == null) it.displayKeys else it.displayKeys + (note.name to displayKey)) }
    }
}

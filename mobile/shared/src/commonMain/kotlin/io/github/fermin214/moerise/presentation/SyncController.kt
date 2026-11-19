package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.DraftEntity
import io.github.fermin214.moerise.data.NoteRepository
import io.github.fermin214.moerise.data.epochMillis
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

enum class SyncStatus { SYNCING, CONNECTED, OFFLINE, PENDING, ATTENTION, SIGN_IN }

data class SyncUiState(
    val status: SyncStatus = SyncStatus.SYNCING,
    val notes: List<DraftEntity> = emptyList(),
)

class SyncController internal constructor(
    private val session: SessionController,
    private val repository: suspend () -> NoteRepository,
    private val writes: NoteWriteQueue,
    private val scope: CoroutineScope,
    private val onRefreshed: () -> Unit,
    initialForeground: Boolean = true,
    private val privacy: PrivacyController? = null,
) {
    private val mutable = MutableStateFlow(SyncUiState())
    val state = mutable.asStateFlow()
    private val triggers = Channel<Unit>(Channel.CONFLATED)
    private var foreground = initialForeground
    private var account: String? = null
    private var worker: Job? = null
    private var visibilityRevision = 0L
    private val cycleLock = Mutex()
    private var activeCycle: Job? = null
    private var requested = false

    init {
        scope.launch {
            session.state.map { it.profile?.accountKey }.distinctUntilChanged().collectLatest { key ->
                worker?.cancelAndJoin()
                activeCycle?.cancelAndJoin()
                writes.reset()
                account = key
                mutable.value = SyncUiState()
                if (key != null && foreground) start(key)
            }
        }
    }

    fun foreground(value: Boolean) {
        foreground = value
        val revision = ++visibilityRevision
        val previous = worker
        if (!value) previous?.cancel()
        scope.launch {
            if (!value) { previous?.join(); if (worker === previous) worker = null }
            else {
                if (previous?.isCancelled == true) previous.join()
                if (revision == visibilityRevision && foreground) account?.let {
                    if (worker?.isActive != true) start(it)
                }
            }
        }
    }

    fun request() { triggers.trySend(Unit) }
    fun userRequest() { requested = true; request() }

    suspend fun changed() {
        val key = account ?: return
        val notes = repository().queuedNotes(key)
        val pending = repository().automaticWritesPending(key)
        if (key == account) mutable.update {
            it.copy(notes = notes, status = if (notes.any { note -> note.conflictJson != null }) SyncStatus.ATTENTION
                else if (pending && session.state.value.connected) SyncStatus.PENDING else it.status)
        }
        request()
    }

    suspend fun stop() {
        worker?.cancelAndJoin()
        activeCycle?.cancelAndJoin()
        writes.reset()
        worker = null
        mutable.value = SyncUiState()
    }

    suspend fun synchronizeOnce(): Boolean = coroutineScope {
        cycleLock.withLock {
            activeCycle = coroutineContext[Job]
            val source = if (requested) "user" else "background"
            val started = epochMillis()
            try {
                val key = session.state.value.profile?.accountKey ?: return@withLock !session.state.value.loading
                if (session.state.value.busy || account != key) return@withLock false
                requested = false
                val queued = repository().queuedNotes(key)
                mutable.value = SyncUiState(SyncStatus.SYNCING, queued)
                if (!session.checkConnection()) {
                    privacy?.record("sync", "failure", started, source)
                    mutable.update { it.copy(status = if (session.state.value.needsLogin) SyncStatus.SIGN_IN else SyncStatus.OFFLINE) }
                    return@withLock false
                }
                val active = session.core ?: return@withLock false
                val drained = writes.drain(key, active)
                if (key != account) return@withLock false
                val notes = repository().queuedNotes(key)
                val pending = repository().automaticWritesPending(key)
                mutable.value = SyncUiState(
                    when {
                        notes.any { it.conflictJson != null } -> SyncStatus.ATTENTION
                        !drained || pending -> SyncStatus.PENDING
                        else -> SyncStatus.CONNECTED
                    }, notes)
                if (foreground) onRefreshed()
                privacy?.record("sync", when {
                    notes.any { it.conflictJson != null } -> "conflict"
                    drained && !pending -> "success"
                    else -> "unknown"
                }, started, source)
                drained && !pending
            } catch (error: Exception) {
                privacy?.record("sync", usageResult(error), started, source, error)
                if (error is CancellationException) throw error
                account?.let { session.connectionFailed(it) }
                mutable.update { it.copy(status = if (session.state.value.needsLogin) SyncStatus.SIGN_IN else SyncStatus.OFFLINE) }
                false
            } finally { activeCycle = null }
        }
    }

    private fun start(key: String) {
        worker = scope.launch {
            while (isActive && foreground && key == account) {
                val drained = synchronizeOnce()
                val retryDelay = if (drained) 30_000L else if (state.value.status == SyncStatus.PENDING) 500L else 5_000L
                withTimeoutOrNull(retryDelay) { triggers.receive() }
            }
        }
    }
}

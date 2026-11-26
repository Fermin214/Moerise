package io.github.fermin214.moerise.presentation

import android.os.SystemClock
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.data.DraftEntity
import io.github.fermin214.moerise.data.NoteRepository
import io.github.fermin214.moerise.data.SyncNotesOutcome
import io.github.fermin214.moerise.data.epochMillis
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

enum class SyncStatus { SYNCING, CONNECTED, OFFLINE, PENDING, ATTENTION, SIGN_IN }

internal enum class SyncTrigger { MANUAL, ENQUEUED, NETWORK_RECOVERED, FOREGROUND_RESUME, PERIODIC_FALLBACK, BACKGROUND_JOB }

data class SyncUiState(
    val status: SyncStatus = SyncStatus.SYNCING,
    val notes: List<DraftEntity> = emptyList(),
    val lastSyncAt: Long? = null,
) {
    internal fun notesFor(account: String?): List<DraftEntity> =
        if (account == null) emptyList() else notes.filter { it.accountKey == account }
}

class SyncController internal constructor(
    private val session: SessionController,
    private val repository: suspend () -> NoteRepository,
    private val writes: NoteWriteQueue,
    private val scope: CoroutineScope,
    private val onRefreshed: () -> Unit,
    initialForeground: Boolean = true,
    private val privacy: PrivacyController? = null,
    private val clockMillis: () -> Long = SystemClock::elapsedRealtime,
    private val draftLock: Mutex = Mutex(),
    private val checkConnection: suspend () -> Boolean = session::checkConnection,
    private val core: () -> CoreClient? = { session.core },
) {
    private val mutable = MutableStateFlow(SyncUiState())
    val state = mutable.asStateFlow()
    private val mutableManualSync = MutableStateFlow(false)
    val manualSync = mutableManualSync.asStateFlow()
    private val triggers = Channel<Unit>(Channel.CONFLATED)
    private val pendingTriggers = mutableSetOf<SyncTrigger>()
    private var foreground = initialForeground
    private var account: String? = null
    private var worker: Job? = null
    private var visibilityRevision = 0L
    private val cycleLock = Mutex()
    private var activeCycle: Job? = null
    private var lastSuccessMonotonicMs: Long? = null
    private var retryAt: Long? = null
    private var networkLossRevision = 0L

    init {
        scope.launch {
            session.state.map { it.profile?.accountKey }.distinctUntilChanged().collectLatest { key ->
                account = null
                lastSuccessMonotonicMs = null
                retryAt = null
                pendingTriggers.clear()
                triggers.tryReceive()
                mutableManualSync.value = false
                mutable.value = SyncUiState()
                worker?.cancelAndJoin()
                activeCycle?.cancelAndJoin()
                writes.reset()
                account = key
                if (key != null) {
                    try {
                        val lastSyncAt = repository().lastNoteSync(key)
                        if (currentAccount(key)) mutable.update { it.copy(lastSyncAt = lastSyncAt) }
                    } catch (error: Exception) { if (error is CancellationException) throw error }
                    if (foreground && worker?.isActive != true) start(key)
                }
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

    internal fun request(reason: SyncTrigger) {
        pendingTriggers += reason
        triggers.trySend(Unit)
    }

    fun userRequest() {
        mutableManualSync.value = true
        request(SyncTrigger.MANUAL)
    }

    internal fun networkUnavailable() {
        val key = account?.takeIf(::currentAccount) ?: return
        ++networkLossRevision
        val deadline = clockMillis() + 5_000L
        retryAt = minOf(retryAt ?: deadline, deadline)
        session.connectionFailed(key)
        if (!session.state.value.needsLogin) mutable.update { it.copy(status = SyncStatus.OFFLINE) }
        triggers.trySend(Unit)
    }

    private fun currentAccount(key: String) = key == account && key == session.state.value.profile?.accountKey

    internal fun publishEnqueued(draft: DraftEntity) {
        if (draft.accountKey != session.state.value.profile?.accountKey || session.state.value.needsLogin) return
        mutable.update { it.copy(notes = listOf(draft) + it.notes.filterNot { note -> note.id == draft.id },
            status = if (session.state.value.connected) SyncStatus.PENDING else it.status) }
    }

    internal fun confirmEnqueued(id: String, remaining: DraftEntity?) {
        mutable.update { state -> state.copy(notes = listOfNotNull(remaining) + state.notes.filterNot { it.id == id }) }
    }

    suspend fun changed() {
        val key = account ?: return
        request(SyncTrigger.ENQUEUED)
        draftLock.withLock {
            val notes = repository().queuedNotes(key)
            val pending = repository().automaticWritesPending(key)
            if (currentAccount(key)) mutable.update {
                it.copy(notes = notes, status = if (notes.any { note -> note.conflictJson != null }) SyncStatus.ATTENTION
                    else if (pending && session.state.value.connected) SyncStatus.PENDING else it.status)
            }
        }
    }

    suspend fun stop() {
        worker?.cancelAndJoin()
        activeCycle?.cancelAndJoin()
        writes.reset()
        worker = null
        mutableManualSync.value = false
        mutable.update { SyncUiState(lastSyncAt = it.lastSyncAt) }
    }

    internal suspend fun synchronizeOnce(
        trigger: SyncTrigger = SyncTrigger.ENQUEUED,
        foregroundResume: Boolean = false,
        shouldRun: () -> Boolean = { true },
    ): Boolean = coroutineScope {
        cycleLock.withLock cycle@{
            if (!shouldRun()) return@cycle true
            val key = session.state.value.profile?.accountKey ?: return@cycle !session.state.value.loading
            if (session.state.value.busy || !currentAccount(key)) return@cycle false
            activeCycle = coroutineContext[Job]
            val lossAtStart = networkLossRevision
            val source = if (trigger == SyncTrigger.MANUAL) "user" else "background"
            val started = epochMillis()
            try {
                val repo = repository()
                when (trigger) {
                    SyncTrigger.BACKGROUND_JOB -> if (!repo.automaticWritesPending(key)) return@cycle true
                    SyncTrigger.ENQUEUED -> if (!repo.automaticWritesPending(key) &&
                        (!foregroundResume || !resumeDue())) return@cycle true
                    SyncTrigger.FOREGROUND_RESUME -> if (!resumeDue() && !repo.automaticWritesPending(key)) return@cycle true
                    else -> Unit
                }
                if (!shouldRun() || !currentAccount(key)) return@cycle true
                draftLock.withLock {
                    val queued = repo.queuedNotes(key)
                    if (!shouldRun() || !currentAccount(key)) return@cycle true
                    mutable.update { it.copy(status = SyncStatus.SYNCING, notes = queued) }
                }
                if (!checkConnection()) {
                    privacy?.record("sync", "failure", started, source)
                    if (currentAccount(key)) mutable.update {
                        it.copy(status = if (session.state.value.needsLogin) SyncStatus.SIGN_IN else SyncStatus.OFFLINE)
                    }
                    return@cycle false
                }
                if (!currentAccount(key)) return@cycle false
                val active = core() ?: return@cycle false
                val drain = writes.drain(key, active)
                if (!currentAccount(key)) return@cycle false
                val outcome = if (drain.drained) repo.syncNotes(key, active) else null
                if (!currentAccount(key)) return@cycle false
                val pending = draftLock.withLock {
                    val notes = repo.queuedNotes(key)
                    val pending = repo.automaticWritesPending(key)
                    val lastSyncAt = repo.lastNoteSync(key)
                    if (!currentAccount(key)) return@cycle false
                    mutable.value = SyncUiState(
                        when {
                            lossAtStart != networkLossRevision -> SyncStatus.OFFLINE
                            !drain.drained || pending -> SyncStatus.PENDING
                            notes.any { it.conflictJson != null } -> SyncStatus.ATTENTION
                            else -> SyncStatus.CONNECTED
                        }, notes, lastSyncAt)
                    pending
                }
                val success = drain.drained && !pending
                if (success) {
                    val now = clockMillis()
                    lastSuccessMonotonicMs = now
                    if (lossAtStart == networkLossRevision) retryAt = now + 180_000L
                }
                if (foreground) onRefreshed()
                syncTelemetryResult(trigger, drain, outcome, success)?.let { privacy?.record("sync", it, started, source) }
                success
            } catch (error: Exception) {
                privacy?.record("sync", usageResult(error), started, source, error)
                if (error is CancellationException) throw error
                if (currentAccount(key)) {
                    session.connectionFailed(key)
                    mutable.update { it.copy(status = if (session.state.value.needsLogin) SyncStatus.SIGN_IN else SyncStatus.OFFLINE) }
                }
                false
            } finally {
                activeCycle = null
                if (trigger == SyncTrigger.MANUAL && SyncTrigger.MANUAL !in pendingTriggers) mutableManualSync.value = false
            }
        }
    }

    private fun start(key: String) {
        worker = scope.launch {
            var resume = true
            while (isActive && foreground && key == account) {
                val immediate = when {
                    SyncTrigger.MANUAL in pendingTriggers -> SyncTrigger.MANUAL
                    SyncTrigger.NETWORK_RECOVERED in pendingTriggers -> SyncTrigger.NETWORK_RECOVERED
                    SyncTrigger.ENQUEUED in pendingTriggers -> SyncTrigger.ENQUEUED
                    else -> null
                }
                val captured = pendingTriggers.toSet()
                val trigger = immediate ?: if (resume) SyncTrigger.FOREGROUND_RESUME else null
                val justResumed = resume
                resume = false
                if (trigger != null || retryAt?.let { it <= clockMillis() } == true) {
                    pendingTriggers.removeAll(captured)
                    val drained = synchronizeOnce(trigger ?: SyncTrigger.PERIODIC_FALLBACK, foregroundResume = justResumed) {
                        trigger != null || retryAt?.let { it <= clockMillis() } == true
                    }
                    if (!drained && currentAccount(key)) {
                        retryAt = clockMillis() + (if (state.value.status == SyncStatus.PENDING) 500L else 5_000L)
                    }
                    try { repository().collectFiles() }
                    catch (error: Exception) { if (error is CancellationException) throw error }
                } else {
                    val due = retryAt ?: (clockMillis() + 180_000L).also { retryAt = it }
                    withTimeoutOrNull((due - clockMillis()).coerceAtLeast(1L)) { triggers.receive() }
                }
            }
        }
    }

    private fun resumeDue(): Boolean =
        lastSuccessMonotonicMs?.let { clockMillis() - it >= 10_000L } ?: true
}

internal fun syncTelemetryResult(trigger: SyncTrigger, drain: DrainResult, outcome: SyncNotesOutcome?, success: Boolean): String? = when {
    drain.hadNewConflict -> "conflict"
    !success -> "unknown"
    trigger == SyncTrigger.MANUAL || drain.committedCount > 0 || outcome == SyncNotesOutcome.UPDATED -> "success"
    else -> null
}

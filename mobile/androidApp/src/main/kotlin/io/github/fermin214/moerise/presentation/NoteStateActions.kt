package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.NoteRepository
import io.github.fermin214.moerise.data.StateAction
import io.github.fermin214.moerise.data.StateRequestNotSent
import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

internal data class SearchHit(val query: SearchQuery, val index: Int)

internal data class NoteStateProgress(val action: StateAction, val unknown: Boolean = false, val hit: SearchHit? = null)

internal class ArchiveUndo(private val undo: () -> Unit) : () -> Unit {
    private val failure = CompletableDeferred<Unit>()
    val failed get() = failure.isCompleted
    suspend fun awaitFailure() = failure.await()
    fun reject() { failure.complete(Unit) }
    override fun invoke() { if (!failed) undo() }
}

internal class NoteStateActions(
    private val repository: suspend () -> NoteRepository,
    private val account: () -> String?,
    private val core: () -> CoreClient?,
    private val scope: CoroutineScope,
    private val onChanged: (String, Note?, Note?) -> Unit,
    private val onNotice: (String, (() -> Unit)?) -> Unit,
    private val onRefresh: () -> Unit,
) {
    private val boundary = Mutex()
    private val jobs = mutableMapOf<String, Job>()
    private val undoNotices = mutableMapOf<String, Undo>()
    private val mutable = MutableStateFlow<Map<String, NoteStateProgress>>(emptyMap())
    val state = mutable.asStateFlow()

    private inner class Undo(val original: StateAction, val hit: SearchHit?) {
        var receipt: Note? = null
        val notice = ArchiveUndo { undo(this) }
    }

    fun owns(name: String): Boolean = name in jobs || name in state.value

    fun clear() {
        jobs.values.toList().forEach { it.cancel() }
        jobs.clear()
        undoNotices.values.forEach { it.notice.reject() }
        undoNotices.clear()
        mutable.value = emptyMap()
    }

    suspend fun stop() {
        val running = jobs.values.toList()
        clear()
        running.joinAll()
        boundary.withLock { }
    }

    suspend fun recover(key: String) {
        boundary.withLock {
            if (key != account()) return
            for (action in repository().stateActions(key)) {
                if (key != account()) return
                if (action.name in jobs) continue
                show(action, unknown = core() == null || action.write.status != "prepared")
                if (core() != null) launchAction(action.name) { send(action) }
            }
        }
    }

    fun change(note: Note, hit: SearchHit? = null) {
        val key = account() ?: return
        launchAction(note.name) {
            try {
                val saved = boundary.withLock {
                    if (key != account()) return@launchAction
                    val target = if (note.state == "ARCHIVED") "NORMAL" else "ARCHIVED"
                    val action = repository().prepareStateAction(key, note, target)
                    if (key != account()) return@launchAction
                    show(action, hit = hit)
                    if (target == "ARCHIVED" && action.fresh &&
                        action.target == target && action.intent?.undo != true && core() != null) {
                        val token = Undo(action, hit)
                        undoNotices[action.write.id] = token
                        onNotice("NOTE_ARCHIVED", token.notice)
                    }
                    action
                }
                send(saved)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (key == account()) onNotice(if (error is IllegalArgumentException) errorCode(error) else "LOCAL_WRITE_FAILED", null)
            }
        }
    }

    private fun undo(token: Undo) {
        val key = token.original.write.accountKey
        scope.launch {
            try {
                val pending = boundary.withLock {
                    if (key != account()) return@launch
                    val repo = repository()
                    val marked = repo.requestStateUndo(token.original)
                    if (key != account()) return@launch
                    if (marked != null) {
                        val unknown = state.value[marked.name]?.unknown == true
                        show(marked, unknown)
                        marked.takeIf { unknown && core() != null }
                    } else {
                        val receipt = token.receipt ?: return@launch
                        val restore = repo.prepareStateAction(key, receipt, "NORMAL", undo = true)
                        if (key != account()) return@launch
                        show(restore, hit = token.hit)
                        restore
                    }
                }
                if (pending != null) {
                    jobs[pending.name]?.join()
                    if (key == account() && state.value[pending.name]?.action?.write?.id == pending.write.id) {
                        launchAction(pending.name) { send(pending) }
                    }
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (key == account()) onNotice("NOTE_UNDO_FAILED", null)
            }
        }
    }

    private fun launchAction(name: String, block: suspend () -> Unit) {
        if (jobs.containsKey(name)) return
        val job = scope.launch(start = CoroutineStart.LAZY) {
            try { block() }
            finally { if (jobs[name] === coroutineContext[Job]) jobs.remove(name) }
        }
        jobs[name] = job
        job.start()
    }

    private fun show(action: StateAction, unknown: Boolean = false, hit: SearchHit? = null) {
        mutable.update { it + (action.name to NoteStateProgress(action, unknown, it[action.name]?.hit ?: hit)) }
    }

    private suspend fun send(initial: StateAction) {
        val key = initial.write.accountKey
        val repo = repository()
        var next: StateAction? = initial
        while (next != null && key == account()) {
            val action = next
            val write = action.write
            val active = core()
            val fresh = action.fresh && write.status == "prepared"
            try {
                if (active == null) throw StateRequestNotSent()
                boundary.withLock {
                    if (key != account()) return
                    show(state.value[action.name]?.action ?: action)
                    repo.status(write, "sending")
                }
                if (key != account() || core() !== active) throw StateRequestNotSent()
                val remote = CoreJson.decodeFromString<Note>(
                    active.patchNoteState(write.path, write.body, write.idempotencyKey) { key == account() && core() === active }
                )
                require(remote.name == action.name && remote.instanceId == key.split('\u0000')[1] &&
                    remote.state in setOf("NORMAL", "ARCHIVED") && remote.sourceHash.isNotBlank())
                next = boundary.withLock {
                    val previous = repo.localNote(key, action.name) ?: action.intent?.note
                    val remainder = repo.settleStateAction(action, remote)
                    val undo = undoNotices.remove(write.id)
                    if (key == account()) {
                        onChanged(action.name, previous, remote)
                        if (remainder == null) {
                            if (undo != null && remote.state == "ARCHIVED") undo.receipt = remote
                            mutable.update { it - action.name }
                        } else show(remainder)
                    }
                    remainder
                }
            } catch (error: Exception) {
                val cancelled = error is CancellationException && !currentCoroutineContext().isActive
                val notSent = generateSequence<Throwable>(error) { it.cause }.filterIsInstance<StateRequestNotSent>().firstOrNull()
                val rejected = error is CoreException && error.status in 400..499 &&
                    error.detail.code != "WRITE_OUTCOME_UNKNOWN" &&
                    (write.status == "prepared" || error.status !in setOf(401, 403, 408, 429))
                if (rejected || fresh && notSent != null) {
                    boundary.withLock {
                        repo.settleStateAction(action)
                        undoNotices.remove(write.id)?.notice?.reject()
                        if (key == account()) {
                            mutable.update { it - action.name }
                            val code = errorCode(notSent?.cause as? Exception ?: error)
                            onNotice(when {
                                code == "SOURCE_CHANGED" && action.intent?.undo == true -> "NOTE_UNDO_CONFLICT"
                                code == "SOURCE_CHANGED" -> "NOTE_STATE_CONFLICT"
                                code in setOf("SESSION_UNAUTHORIZED", "RATE_LIMITED") || notSent != null -> code
                                else -> "NOTE_STATE_REJECTED"
                            }, null)
                        }
                    }
                    if (rejected && active != null) refreshRejected(repo, action, active)
                    else if (key == account()) onRefresh()
                } else {
                    withContext(NonCancellable) {
                        boundary.withLock {
                            if (active != null && notSent == null) runCatching { repo.status(write, "unknown") }
                            else if (active != null) runCatching { repo.status(write, write.status) }
                            if (key == account() && !cancelled)
                                show(state.value[action.name]?.action ?: action, unknown = true)
                        }
                    }
                }
                if (cancelled) throw error
                return
            }
        }
    }

    private suspend fun refreshRejected(repo: NoteRepository, action: StateAction, active: CoreClient) {
        val key = action.write.accountKey
        if (key != account()) return
        try {
            val previous = repo.localNote(key, action.name) ?: action.intent?.note
            val remote = try {
                CoreJson.decodeFromString<Note>(active.api(HttpMethod.Get, action.write.path)).also {
                    require(it.name == action.name && it.instanceId == key.split('\u0000')[1] &&
                        it.state in setOf("NORMAL", "ARCHIVED") && it.sourceHash.isNotBlank())
                }
            } catch (error: CoreException) {
                if (error.status == 404 && error.detail.code in setOf("SOURCE_NOT_FOUND", "NOT_FOUND")) null else throw error
            }
            if (key == account()) {
                repo.finishAction(action.write, remote, action.name)
                if (key == account()) onChanged(action.name, previous, remote)
            }
        } catch (error: Exception) {
            if (error is CancellationException && !currentCoroutineContext().isActive) throw error
        } finally {
            if (key == account()) onRefresh()
        }
    }
}

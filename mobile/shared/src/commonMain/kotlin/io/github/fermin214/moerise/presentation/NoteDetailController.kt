package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.serialization.json.*

data class NoteDetailState(
    val name: String? = null, val note: Note? = null, val loading: Boolean = false,
    val busy: Boolean = false, val error: String? = null, val deleteUnknown: Boolean = false, val pendingAction: Boolean = false,
    val cached: Boolean = false, val snapshotAt: Long = 0,
)

class NoteDetailController(
    private val repository: suspend () -> NoteRepository, private val account: () -> String?,
    private val core: () -> CoreClient?, private val scope: CoroutineScope,
    private val onChanged: (Note?) -> Unit,
) {
    private val mutable = MutableStateFlow(NoteDetailState())
    val state = mutable.asStateFlow()
    private var generation = 0L
    private var operation: Job? = null
    private var pending: PendingWriteEntity? = null

    fun clear() { ++generation; operation?.cancel(); pending = null; mutable.value = NoteDetailState() }
    suspend fun stopForRemoval() {
        ++generation; operation?.cancelAndJoin(); pending = null; mutable.value = NoteDetailState()
    }
    fun pauseRead() { ++generation; operation?.cancel(); mutable.update { it.copy(loading = false) } }

    fun open(name: String, cached: Note? = null, snapshotAt: Long = 0, online: Boolean = true) {
        require(Regex("^memos/[A-Za-z0-9_-]+$").matches(name))
        val active = core()
        val key = account() ?: return
        val fallback = cached?.takeIf { it.name == name && it.instanceId == key.split('\u0000')[1] }
        val stamp = ++generation
        val previous = operation
        previous?.cancel()
        pending = null
        mutable.value = NoteDetailState(name = name, loading = true)
        operation = scope.launch {
            try {
                previous?.join()
                val restored = repository().action(key, name)
                if (stamp != generation) return@launch
                pending = restored
                mutable.update { it.copy(note = fallback, cached = fallback != null, snapshotAt = snapshotAt,
                    deleteUnknown = restored?.method == "DELETE", pendingAction = restored != null, loading = online && active != null) }
                if (!online || active == null) return@launch
                val note = CoreJson.decodeFromString<Note>(active.api(HttpMethod.Get, path(name)))
                require(note.name == name && note.instanceId == key.split('\u0000')[1])
                if (stamp == generation) mutable.value = NoteDetailState(name = name, note = note,
                    deleteUnknown = restored?.method == "DELETE", pendingAction = restored != null)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == generation) {
                    if (pending?.method == "DELETE" && missing(error)) {
                        repository().finishAction(pending!!, null, name)
                        mutable.value = NoteDetailState(); onChanged(null)
                    } else mutable.update { it.copy(loading = false, error = errorCode(error)) }
                }
            }
        }
    }

    private fun path(name: String) = "/notes/${name.removePrefix("memos/")}"

    fun changeState() {
        val note = state.value.note ?: return
        val active = core() ?: return
        if (state.value.busy || state.value.deleteUnknown) return
        execute(note, active, "PATCH", buildJsonObject {
            put("source_hash", note.sourceHash)
            put("state", if (note.state == "ARCHIVED") "NORMAL" else "ARCHIVED")
        }.toString())
    }

    fun delete() {
        val note = state.value.note ?: return
        val active = core() ?: return
        if (state.value.busy || state.value.deleteUnknown || pending != null) return
        execute(note, active, "DELETE", buildJsonObject { put("source_hash", note.sourceHash) }.toString())
    }

    private fun execute(note: Note, active: CoreClient, method: String, body: String) {
        val key = account() ?: return
        val stamp = generation
        mutable.update { it.copy(busy = true, error = null) }
        operation = scope.launch {
            var write: PendingWriteEntity? = null
            try {
                write = repository().prepareAction(key, note.name, method, body)
                if (stamp != generation) throw CancellationException("Page changed")
                pending = write
                if (stamp == generation) mutable.update { it.copy(pendingAction = true) }
                repository().status(write, "sending")
                val result = active.api(HttpMethod.parse(write.method), write.path, write.body,
                    write.idempotencyKey.takeIf { method != "DELETE" })
                val updated = if (method == "DELETE") null else CoreJson.decodeFromString<Note>(result)
                if (updated != null) require(updated.name == note.name && updated.instanceId == key.split('\u0000')[1])
                repository().finishAction(write, updated, note.name)
                if (stamp == generation) {
                    pending = null
                    mutable.value = if (updated == null) NoteDetailState() else NoteDetailState(name = updated.name, note = updated)
                    onChanged(updated)
                }
            } catch (error: Exception) {
                val conflict = error is CoreException && error.detail.code == "SOURCE_CHANGED"
                withContext(NonCancellable) {
                    write?.let { frozen -> runCatching { repository().status(frozen, if (conflict) "conflict" else "unknown") } }
                }
                if (error is CancellationException) throw error
                if (stamp == generation) {
                    if (conflict) pending = null
                    mutable.update { it.copy(busy = false, deleteUnknown = method == "DELETE" && write != null && !conflict,
                        pendingAction = write != null && !conflict, error = errorCode(error)) }
                }
            }
        }
    }

    fun checkDeletion() {
        val name = state.value.name ?: return
        val active = core() ?: return
        if (state.value.busy) return
        val stamp = generation
        mutable.update { it.copy(busy = true, error = null) }
        operation = scope.launch {
            try {
                val note = CoreJson.decodeFromString<Note>(active.api(HttpMethod.Get, path(name)))
                if (stamp == generation) {
                    pending?.let { repository().finishAction(it, note, name) }
                    pending = null
                    mutable.update { it.copy(note = note, busy = false, deleteUnknown = false, pendingAction = false) }
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == generation) {
                    if (missing(error)) {
                        pending?.let { repository().finishAction(it, null, name) }
                        pending = null
                        mutable.value = NoteDetailState(); onChanged(null)
                    } else mutable.update { it.copy(busy = false, error = errorCode(error)) }
                }
            }
        }
    }

    private fun missing(error: Exception) = error is CoreException && error.status == 404 &&
        error.detail.code in setOf("SOURCE_NOT_FOUND", "NOT_FOUND")

    fun acceptSaved(key: String, note: Note) {
        if (key == account() && state.value.name == note.name) mutable.update { it.copy(note = note) }
    }
}

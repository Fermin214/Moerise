package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*

class EditorController(
    private val repository: suspend () -> NoteRepository,
    private val core: () -> CoreClient?,
    private val scope: CoroutineScope,
    private val onConfirmed: (Note) -> Unit = {},
) {
    private val mutable = MutableStateFlow(EditorUiState())
    val state = mutable.asStateFlow()
    private val localLock = Mutex()
    private var editJob: Job? = null
    private var sendJob: Job? = null
    private var generation = 0L

    suspend fun open(key: String?) {
        val stamp = ++generation
        editJob?.cancel()
        sendJob?.cancel()
        // Flush the previous editor before changing the account.
        if (!flushWithFeedback()) return
        mutable.value = EditorUiState(accountKey = key)
        if (key == null) return
        try {
            val restored = localLock.withLock { repository().restore(key) }
            if (generation == stamp) mutable.value = EditorUiState(key, restored.draft, restored.pending, savedRevision = restored.draft.revision)
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            if (generation == stamp) mutable.update { it.copy(error = "LOCAL_WRITE_FAILED") }
        }
    }

    fun change(content: String) {
        val before = mutable.value
        val draft = before.draft ?: return
        mutable.value = before.copy(draft = draft.copy(content = content, revision = draft.revision + 1), error = null, serverSaved = false)
        editJob?.cancel()
        editJob = scope.launch {
            delay(300)
            flushWithFeedback()
        }
    }

    suspend fun flush() = localLock.withLock {
        val before = mutable.value
        val draft = before.draft ?: return@withLock
        val confirmation = before.confirmation
        if (confirmation != null) repository().confirm(confirmation.write, draft, confirmation.remote, confirmation.conflict)
        else repository().persist(draft)
        mutable.update { latest ->
            if (latest.draft?.id != draft.id || latest.accountKey != before.accountKey) latest
            else latest.copy(savedRevision = draft.revision,
                serverSaved = latest.serverSaved || confirmation != null && !confirmation.conflict,
                confirmation = if (latest.confirmation == confirmation) null else latest.confirmation)
        }
        if (confirmation != null && !confirmation.conflict) confirmation.remote?.let(onConfirmed)
    }

    private suspend fun flushWithFeedback(): Boolean = try {
        flush()
        true
    } catch (error: Exception) {
        if (error is CancellationException) throw error
        mutable.update { it.copy(error = "LOCAL_WRITE_FAILED") }
        false
    }

    suspend fun flushForLifecycle(): Boolean = flushWithFeedback()

    fun save() {
        if (state.value.busy || state.value.draft == null) return
        val active = core() ?: return
        val stamp = generation
        mutable.update { it.copy(busy = true, error = null) }
        sendJob = scope.launch {
            var attempt: PendingWriteEntity? = null
            var networkStarted = false
            try {
                editJob?.cancel()
                flush()
                val write = localLock.withLock {
                    val draft = requireNotNull(state.value.draft)
                    repository().prepare(draft).also { pending ->
                        mutable.update { it.copy(pending = pending) }
                    }
                }
                attempt = write
                repository().status(write, "sending")
                networkStarted = true
                val remote = CoreJson.decodeFromString<Note>(active.api(HttpMethod.parse(write.method), write.path, write.body, write.idempotencyKey))
                if (stamp != generation) throw CancellationException("Account changed")
                val before = state.value
                val next = completedDraft(requireNotNull(before.draft), write, remote)
                mutable.value = before.copy(draft = next, pending = null,
                    confirmation = ConfirmedChange(write, remote, next.conflictJson != null))
                attempt = null
                flush()
            } catch (error: Exception) {
                val write = attempt
                if (write != null) {
                    if (error is CoreException && error.detail.code in setOf("SOURCE_CHANGED", "IDEMPOTENCY_CONFLICT") && stamp == generation) {
                        val remote = if (write.method == "PATCH") runCatching {
                            CoreJson.decodeFromString<Note>(active.api(HttpMethod.Get, write.path))
                        }.getOrNull() else null
                        val before = state.value
                        val draft = before.draft
                        if (draft != null && stamp == generation) {
                            mutable.value = before.copy(draft = draft.copy(conflictJson = conflictBody(write, remote)),
                                pending = null, confirmation = ConfirmedChange(write, remote, true), error = error.detail.code)
                            flushWithFeedback()
                        }
                    } else {
                        withContext(NonCancellable) { runCatching { repository().status(write, "unknown") } }
                        if (stamp == generation) mutable.update { it.copy(pending = write.copy(status = "unknown")) }
                    }
                }
                if (error is CancellationException) throw error
                if (stamp == generation) mutable.update { it.copy(error =
                    if (it.confirmation != null || !networkStarted && error !is IllegalArgumentException) "LOCAL_WRITE_FAILED" else errorCode(error)) }
            } finally {
                if (stamp == generation) mutable.update { it.copy(busy = false) }
            }
        }
    }

    fun merge(content: String, remote: Note) {
        val before = state.value
        val draft = before.draft ?: return
        require(draft.conflictJson != null && draft.targetName == remote.name)
        mutable.value = before.copy(draft = draft.copy(content = content, revision = draft.revision + 1,
            sourceHash = remote.sourceHash, baselineJson = CoreJson.encodeToString(remote), conflictJson = null), error = null)
        editJob?.cancel()
        editJob = scope.launch { flushWithFeedback() }
    }

    fun refreshConflict() {
        val draft = state.value.draft ?: return
        val target = draft.targetName ?: return
        val active = core() ?: return
        val stamp = generation
        scope.launch {
            try {
                val remote = CoreJson.decodeFromString<Note>(active.api(HttpMethod.Get, "/notes/${target.removePrefix("memos/")}"))
                if (stamp != generation || state.value.draft?.id != draft.id) return@launch
                require(remote.name == target && remote.instanceId == draft.accountKey.split('\u0000')[1])
                val body = JsonObject(CoreJson.parseToJsonElement(requireNotNull(state.value.draft?.conflictJson)).jsonObject +
                    ("remote" to CoreJson.encodeToJsonElement(remote)))
                mutable.update { it.copy(draft = it.draft?.copy(conflictJson = body.toString())) }
                flushWithFeedback()
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == generation) mutable.update { it.copy(error = errorCode(error)) }
            }
        }
    }
}

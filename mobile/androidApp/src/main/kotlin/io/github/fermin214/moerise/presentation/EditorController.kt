package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*
import kotlin.time.TimeSource

class EditorController(
    private val repository: suspend () -> NoteRepository,
    private val core: () -> CoreClient?,
    private val scope: CoroutineScope,
    private val localLock: Mutex = Mutex(),
) {
    private val mutable = MutableStateFlow(EditorUiState())
    val state = mutable.asStateFlow()
    private var editJob: Job? = null
    private var generation = 0L
    private var lastEdit = TimeSource.Monotonic.markNow()
    private val importJob = SupervisorJob(scope.coroutineContext[Job])
    val attachmentScope = CoroutineScope(scope.coroutineContext + importJob)

    private suspend fun stopImports() {
        val children = importJob.children.toList()
        children.forEach { it.cancel() }
        children.joinAll()
    }

    suspend fun open(key: String?, note: Note? = null, onRestored: () -> Unit = {}) =
        openDraft(key, onRestored) { it.restore(requireNotNull(key), note) }
    suspend fun openStored(key: String, id: String, onRestored: () -> Unit = {}) =
        openDraft(key, onRestored) { it.storedDraft(key, id) }

    private suspend fun openDraft(
        key: String?,
        onRestored: () -> Unit,
        restore: suspend (NoteRepository) -> RestoredDraft,
    ) {
        val stamp = ++generation
        stopImports()
        editJob?.cancelAndJoin()
        try {
            localLock.withLock {
                if (generation != stamp) return@withLock
                flushLatestLocked()
                if (generation != stamp) return@withLock
                mutable.value = EditorUiState(accountKey = key)
                if (key == null) return@withLock
                val restored = restore(repository())
                if (generation == stamp) {
                    mutable.value = EditorUiState(
                        key, restored.draft, restored.pending, savedRevision = restored.draft.revision)
                    onRestored()
                }
            }
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            if (generation == stamp) mutable.update { it.copy(error = "LOCAL_WRITE_FAILED") }
        }
    }

    suspend fun stopForRemoval() {
        check(close()) { "LOCAL_WRITE_FAILED" }
    }

    suspend fun close(persist: Boolean = true): Boolean {
        stopImports()
        editJob?.cancelAndJoin()
        return try {
            localLock.withLock {
                if (persist) flushLatestLocked()
                ++generation
                mutable.value = EditorUiState()
            }
            true
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            mutable.update { it.copy(error = "LOCAL_WRITE_FAILED") }
            false
        }
    }

    suspend fun adoptDraft(restored: RestoredDraft, preservePrevious: Boolean = false): Boolean {
        val stamp = ++generation
        stopImports()
        editJob?.cancelAndJoin()
        return try {
            localLock.withLock {
                if (generation != stamp) return@withLock false
                val before = state.value
                val previous = before.draft
                if (previous != null && previous.id != restored.draft.id) {
                    if (preservePrevious) {
                        val keep = previous.content.isNotEmpty() || previous.attachmentEdits() != AttachmentEdits() ||
                            previous.conflictJson != null || previous.queuedRevisionJson != null ||
                            before.pending != null
                        if (keep) {
                            mutable.update { current ->
                                if (current.draft?.id != previous.id) current else
                                    current.copy(draft = current.draft.copy(slot = "saved:${previous.id}"))
                            }
                            flushLatestLocked()
                        }
                    } else flushLatestLocked()
                } else if (previous?.id == restored.draft.id) {
                    flushLatestLocked()
                    return@withLock true
                }
                if (generation != stamp) return@withLock false
                mutable.value = EditorUiState(restored.draft.accountKey, restored.draft, restored.pending,
                    savedRevision = restored.draft.revision)
                true
            }
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            if (generation == stamp) mutable.update { it.copy(error = "LOCAL_WRITE_FAILED") }
            false
        }
    }

    suspend fun forkConflict(content: String, attachments: AttachmentEdits): Boolean {
        if (state.value.busy) return false
        editJob?.cancelAndJoin()
        if (!flushWithFeedback()) return false
        return try {
            localLock.withLock {
                val draft = state.value.draft ?: return@withLock false
                val restored = repository().forkConflict(draft, content, attachments)
                mutable.value = EditorUiState(draft.accountKey, restored.draft, savedRevision = restored.draft.revision)
                true
            }
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            mutable.update { it.copy(error = "LOCAL_WRITE_FAILED") }; false
        }
    }

    fun change(content: String) {
        val id = state.value.draft?.id ?: return
        mutable.update { before ->
            val draft = before.draft
            if (draft?.id != id) before else before.copy(
                draft = draft.copy(content = content, revision = draft.revision + 1), error = null)
        }
        scheduleFlush()
    }

    private fun scheduleFlush(immediate: Boolean = false) {
        lastEdit = TimeSource.Monotonic.markNow()
        if (editJob?.isActive == true && !immediate) return
        editJob?.cancel()
        editJob = scope.launch {
            var batch = TimeSource.Monotonic.markNow()
            var first = immediate
            while (isActive) {
                val wait = if (first) 0 else minOf(300 - lastEdit.elapsedNow().inWholeMilliseconds,
                    1000 - batch.elapsedNow().inWholeMilliseconds)
                if (wait > 0) { delay(wait); continue }
                first = false
                if (!flushWithFeedback()) return@launch
                val latest = state.value
                val draft = latest.draft ?: return@launch
                if (draft.revision == latest.savedRevision) return@launch
                batch = TimeSource.Monotonic.markNow()
            }
        }
    }

    suspend fun flush() = localLock.withLock { flushLocked() }

    private suspend fun flushLocked() {
        val before = mutable.value
        val draft = before.draft ?: return
        repository().persist(draft)
        mutable.update { latest ->
            if (latest.draft?.id != draft.id || latest.accountKey != before.accountKey) latest
            else latest.copy(savedRevision = draft.revision)
        }
    }

    private suspend fun flushLatestLocked() {
        do {
            flushLocked()
            val latest = state.value
            val draft = latest.draft ?: return
        } while (draft.revision != latest.savedRevision)
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

    suspend fun enqueue(returnHome: Boolean = true, onEnqueued: (DraftEntity) -> Unit = {}): Boolean {
        val before = state.value
        if (before.busy || before.importing) return false
        val submitted = before.draft?.copy(queuedRevisionJson = null) ?: return false
        val stamp = generation
        mutable.update { it.copy(busy = true, error = null) }
        return try {
            editJob?.cancelAndJoin()
            localLock.withLock {
                if (stamp != generation || state.value.accountKey != submitted.accountKey ||
                    state.value.draft?.id != submitted.id) return@withLock false
                flushLatestLocked()
                if (stamp != generation) return@withLock false
                val repo = repository()
                if (stamp != generation) return@withLock false
                val current = requireNotNull(state.value.draft)
                if (current.conflictJson != null) throw IllegalArgumentException("SOURCE_CHANGED")
                val previousWrite = before.pending
                val acknowledged = previousWrite?.takeIf { current.baselineJson != submitted.baselineJson }?.let {
                    current.baselineJson?.let { body -> CoreJson.decodeFromString<Note>(body) }
                }
                if (acknowledged != null && requireNotNull(previousWrite).draftRevision >= submitted.revision) {
                    if (!returnHome) mutable.value = EditorUiState(accountKey = submitted.accountKey)
                    return@withLock true
                }
                val frozen = if (acknowledged != null)
                    completedDraft(submitted, requireNotNull(previousWrite), acknowledged)
                        .copy(queuedRevisionJson = null)
                    else submitted
                val write = repo.prepare(frozen, current)
                val next = repo.enqueue(frozen, write)
                onEnqueued(frozen)
                val queued = repo.storedDraft(submitted.accountKey, submitted.id).draft
                mutable.update { latest ->
                    val draft = latest.draft
                    if (latest.accountKey != submitted.accountKey || draft?.id != submitted.id) latest
                    else latest.copy(draft = draft.copy(slot = queued.slot, createdAt = queued.createdAt,
                        queuedRevisionJson = queued.queuedRevisionJson), pending = write,
                        savedRevision = if (draft == queued) queued.revision else -1)
                }
                flushLatestLocked()
                if (stamp != generation) return@withLock false
                mutable.update { latest ->
                    when {
                        !returnHome -> EditorUiState(accountKey = submitted.accountKey)
                        latest.draft?.revision != submitted.revision -> latest
                        else -> EditorUiState(submitted.accountKey, next.draft, next.pending,
                            savedRevision = next.draft.revision)
                    }
                }
                true
            }
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            if (stamp == generation) mutable.update { it.copy(error = errorCode(error)) }
            false
        } finally {
            if (stamp == generation) mutable.update { it.copy(busy = false) }
        }
    }

    internal fun acceptConfirmation(
        original: DraftEntity, persisted: DraftEntity, error: String?, rebase: (DraftEntity) -> DraftEntity,
    ) {
        mutable.update { current ->
            val draft = current.draft
            if (draft == null || draft.id != original.id || draft.accountKey != original.accountKey) current
            else if (draft.revision == original.revision) current.copy(
                draft = persisted, pending = null, savedRevision = persisted.revision, error = error)
            else current.copy(draft = rebase(draft), pending = null, savedRevision = -1, error = error)
        }
        if (state.value.draft?.revision != state.value.savedRevision) scheduleFlush(immediate = true)
    }

    fun merge(content: String, remote: Note, attachments: AttachmentEdits = AttachmentEdits()) {
        val before = state.value
        val draft = before.draft ?: return
        require(draft.conflictJson != null && draft.targetName == remote.name)
        mutable.value = before.copy(draft = draft.copy(content = content, revision = draft.revision + 1,
            sourceHash = remote.sourceHash, baselineJson = CoreJson.encodeToString(remote), conflictJson = null,
            attachmentsJson = CoreJson.encodeToString(attachments)), error = null)
        scheduleFlush(immediate = true)
    }

    fun attachmentError(code: String, draftId: String? = null) {
        mutable.update { if (draftId == null || it.draft?.id == draftId) it.copy(error = code) else it }
    }

    fun importing(draftId: String, value: Boolean) {
        mutable.update { if (it.draft?.id == draftId) it.copy(importing = value) else it }
    }

    fun attach(accountKey: String, draftId: String, files: List<StagedAttachment>): Boolean {
        return updateAttachments(accountKey, draftId) { it.copy(staged = it.staged + files) }
    }

    fun removeStaged(id: String) {
        val draft = state.value.draft ?: return
        updateAttachments(draft.accountKey, draft.id) { edits ->
            edits.copy(staged = edits.staged.filter { it.id != id })
        }
    }

    fun toggleRemoval(name: String) {
        val draft = state.value.draft ?: return
        val baseline = draft.baselineJson?.let { CoreJson.decodeFromString<Note>(it) } ?: return
        if (baseline.attachments.none { it["name"]?.jsonPrimitive?.content == name }) return
        updateAttachments(draft.accountKey, draft.id) { edits ->
            edits.copy(removed = if (name in edits.removed) edits.removed - name else edits.removed + name)
        }
    }

    private fun updateAttachments(
        accountKey: String,
        draftId: String,
        change: (AttachmentEdits) -> AttachmentEdits,
    ): Boolean {
        var updated = false
        mutable.update { before ->
            updated = false
            val draft = before.draft
            if (before.busy || draft == null || draft.conflictJson != null ||
                draft.accountKey != accountKey || draft.id != draftId) before
            else {
                val edits = change(draft.attachmentEdits())
                if (edits.staged.size > 10 || edits.staged.sumOf { it.size } > 8 * 1024 * 1024)
                    before.copy(error = "ATTACHMENTS_TOO_LARGE")
                else {
                    updated = true
                    before.copy(draft = draft.copy(attachmentsJson = CoreJson.encodeToString(edits),
                        revision = draft.revision + 1), error = null)
                }
            }
        }
        if (updated) scheduleFlush()
        return updated
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

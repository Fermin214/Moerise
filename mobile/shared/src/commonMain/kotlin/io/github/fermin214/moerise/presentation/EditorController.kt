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
import kotlin.time.TimeSource

class EditorController(
    private val repository: suspend () -> NoteRepository,
    private val core: () -> CoreClient?,
    private val scope: CoroutineScope,
    private val localLock: Mutex = Mutex(),
    private val onConfirmed: (String, Note) -> Unit = { _, _ -> },
    private val onPersisted: suspend (DraftEntity) -> Unit = {},
) {
    private val mutable = MutableStateFlow(EditorUiState())
    val state = mutable.asStateFlow()
    private val syncLock = Mutex()
    private var editJob: Job? = null
    private var sendJob: Job? = null
    private var generation = 0L
    private var accountGeneration = 0L
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
        if (key != state.value.accountKey) ++accountGeneration
        val stamp = ++generation
        stopImports()
        editJob?.cancelAndJoin()
        sendJob?.cancelAndJoin()
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
        editJob?.cancelAndJoin(); sendJob?.cancelAndJoin()
        return try {
            localLock.withLock {
                if (persist) flushLatestLocked()
                ++generation
                ++accountGeneration
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
        editJob?.cancelAndJoin(); sendJob?.cancelAndJoin()
        return try {
            localLock.withLock {
                if (generation != stamp) return@withLock false
                val before = state.value
                val previous = before.draft
                if (previous != null && previous.id != restored.draft.id) {
                    if (preservePrevious) {
                        val keep = previous.content.isNotEmpty() || previous.attachmentEdits() != AttachmentEdits() ||
                            previous.conflictJson != null || previous.queuedRevisionJson != null ||
                            before.pending != null || before.confirmation != null
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
                if (state.value.accountKey != restored.draft.accountKey) ++accountGeneration
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
                draft = draft.copy(content = content, revision = draft.revision + 1), error = null, serverSaved = false)
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

    private suspend fun flushLocked(notify: Boolean = true) {
        val before = mutable.value
        val draft = before.draft ?: return
        val confirmation = before.confirmation
        if (confirmation != null) repository().confirm(confirmation.write, draft, confirmation.remote, confirmation.conflict)
        else repository().persist(draft)
        onPersisted(draft)
        mutable.update { latest ->
            if (latest.draft?.id != draft.id || latest.accountKey != before.accountKey) latest
            else latest.copy(savedRevision = draft.revision,
                serverSaved = latest.serverSaved || confirmation != null && !confirmation.conflict &&
                    latest.draft.revision == draft.revision,
                confirmation = if (latest.confirmation == confirmation) null else latest.confirmation)
        }
        if (notify && confirmation != null && !confirmation.conflict) confirmation.remote?.let { onConfirmed(draft.accountKey, it) }
    }

    private suspend fun flushLatestLocked() {
        do {
            flushLocked()
            val latest = state.value
            val draft = latest.draft ?: return
        } while (draft.revision != latest.savedRevision || latest.confirmation != null)
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

    suspend fun enqueue(returnHome: Boolean = true): Boolean {
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
                val previousWrite = before.pending ?: before.confirmation?.write
                val acknowledged = previousWrite?.takeIf { current.baselineJson != submitted.baselineJson }?.let {
                    current.baselineJson?.let { body -> CoreJson.decodeFromString<Note>(body) }
                }
                if (acknowledged != null && requireNotNull(previousWrite).draftRevision >= submitted.revision) {
                    if (!returnHome) mutable.value = EditorUiState(accountKey = submitted.accountKey)
                    return@withLock true
                }
                val frozen = if (acknowledged != null)
                    completedDraft(submitted, requireNotNull(previousWrite), acknowledged, true, "")
                        .copy(queuedRevisionJson = null)
                    else submitted
                val write = repo.prepare(frozen, current)
                val next = repo.enqueue(frozen, write)
                val queued = repo.storedDraft(submitted.accountKey, submitted.id).draft
                // 保存只冻结点按时的修订；挂起期间的输入仍属于同一份本机草稿。
                mutable.update { latest ->
                    val draft = latest.draft
                    if (latest.accountKey != submitted.accountKey || draft?.id != submitted.id) latest
                    else latest.copy(draft = draft.copy(slot = queued.slot, createdAt = queued.createdAt,
                        queuedRevisionJson = queued.queuedRevisionJson), pending = write,
                        savedRevision = if (draft == queued) queued.revision else -1, serverSaved = false)
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

    private fun visibleHolder(
        account: String,
        draftId: String,
        otherEditors: () -> List<EditorController>,
    ): EditorController? = (listOf(this) + otherEditors()).firstOrNull {
        it.state.value.accountKey == account && it.state.value.draft?.id == draftId
    }

    suspend fun synchronize(
        account: String,
        active: CoreClient,
        otherEditors: () -> List<EditorController> = { emptyList() },
    ): Boolean = syncLock.withLock {
        val stamp = accountGeneration
        fun ensureAccount() {
            if (stamp != accountGeneration || state.value.accountKey != account)
                throw CancellationException("Account changed")
        }
        var processed = 0
        while (processed++ < 100 && account == state.value.accountKey && stamp == accountGeneration) {
            val repo = repository()
            // A committed continuation survives a process exit between confirmation and preparation.
            localLock.withLock {
                ensureAccount()
                for (holder in (listOf(this) + otherEditors()).distinct()) {
                    val confirmation = holder.state.value.confirmation ?: continue
                    if (holder.state.value.accountKey != account || confirmation.write.accountKey != account) continue
                    holder.flushLocked(notify = false)
                    ensureAccount()
                    if (!confirmation.conflict) confirmation.remote?.let { onConfirmed(account, it) }
                }
                for (draft in repo.queuedNotes(account)) {
                    ensureAccount()
                    if (draft.conflictJson != null || draft.queuedRevisionJson == null ||
                        repo.storedDraft(account, draft.id).pending != null) continue
                    ensureAccount()
                    repo.prepare(CoreJson.decodeFromString<DraftEntity>(draft.queuedRevisionJson), draft)
                }
            }
            ensureAccount()
            val writes = repo.queuedWrites(account)
            ensureAccount()
            val write = writes.firstOrNull() ?: return@withLock true
            var networkStarted = false
            var receivedResult = false
            try {
                val saved = write.submittedDraftJson?.let { CoreJson.decodeFromString<DraftEntity>(it) }
                    ?: repo.storedDraft(account, write.draftId).draft
                ensureAccount()
                repo.status(write, "sending")
                ensureAccount()
                networkStarted = true
                val result = active.api(HttpMethod.parse(write.method), write.path, write.body, write.idempotencyKey)
                receivedResult = true
                val remote = CoreJson.decodeFromString<Note>(result)
                require(remote.instanceId == account.split('\u0000')[1]) { "ACCOUNT_CHANGED" }
                val expected = expectedSubmittedResult(saved, write, remote, active)
                val material = conflictBody(write, remote)
                localLock.withLock {
                    ensureAccount()
                    val stored = repo.storedDraft(account, write.draftId).draft
                    ensureAccount()
                    val holder = visibleHolder(account, write.draftId, otherEditors)
                    val before = holder?.state?.value
                    val latest = before?.draft ?: stored
                    val queued = latest.slot.startsWith("queued:")
                    val requested = stored.queuedRevisionJson?.let { CoreJson.decodeFromString<DraftEntity>(it) }
                    val continuation = if (expected && requested != null && requested.revision > write.draftRevision)
                        completedDraft(requested, write, remote, true, material).copy(queuedRevisionJson = null)
                        else null
                    fun completed(draft: DraftEntity): DraftEntity {
                        val result = if (expected) completedDraft(draft, write, remote, true, material)
                            else draft.copy(conflictJson = material)
                        return result.copy(
                            slot = if (queued && result.conflictJson == null && continuation == null) "saved:${result.id}" else result.slot,
                            queuedRevisionJson = continuation?.let { CoreJson.encodeToString(it) },
                        )
                    }
                    if (holder != null && before != null) {
                        // 先切换内存基线，再提交本机确认；挂起期间的输入沿用新基线和同一份记录。
                        holder.mutable.update { current ->
                            val draft = current.draft
                            if (current.accountKey != account || draft?.id != write.draftId) current
                            else {
                                val next = completed(draft)
                                current.copy(draft = next, pending = null,
                                    confirmation = ConfirmedChange(write, remote, next.conflictJson != null),
                                    savedRevision = -1, serverSaved = false, error = null)
                            }
                        }
                        holder.flushLocked(notify = false)
                        if (holder.state.value.draft?.revision != holder.state.value.savedRevision) holder.scheduleFlush()
                    } else {
                        val next = completed(latest)
                        if (queued && next.conflictJson == null && continuation == null &&
                            latest.revision == write.draftRevision) repo.finishQueued(write, remote)
                        else repo.confirm(write, next, remote, next.conflictJson != null)
                    }
                }
                onConfirmed(account, remote)
            } catch (error: Exception) {
                if (!receivedResult && error is CoreException && (error.detail.code in setOf(
                        "SOURCE_CHANGED", "SOURCE_NOT_FOUND", "NOT_FOUND", "IDEMPOTENCY_CONFLICT",
                        "ATTACHMENT_REFERENCED", "ATTACHMENT_DELETE_REJECTED", "WRITE_CONFLICT",
                    ) || error.status in 400..499 && error.status !in setOf(401, 429) && !error.detail.retryable)
                    && stamp == accountGeneration && state.value.accountKey == account) {
                    val remote = if (write.method == "PATCH") runCatching {
                        CoreJson.decodeFromString<Note>(active.api(HttpMethod.Get, write.path))
                    }.getOrNull() else null
                    localLock.withLock {
                        ensureAccount()
                        val stored = repo.storedDraft(account, write.draftId).draft
                        ensureAccount()
                        val holder = visibleHolder(account, write.draftId, otherEditors)
                        val before = holder?.state?.value
                        val draft = before?.draft ?: stored
                        val next = draft.copy(conflictJson = conflictBody(write, remote))
                        if (holder == null || before == null) repo.confirm(write, next, remote, true)
                        else {
                            holder.mutable.update { current ->
                                if (current.accountKey != account || current.draft?.id != write.draftId) current
                                else current.copy(draft = current.draft.copy(conflictJson = next.conflictJson),
                                    pending = null, confirmation = ConfirmedChange(write, remote, true),
                                    savedRevision = -1, serverSaved = false, error = error.detail.code)
                            }
                            holder.flushLocked(notify = false)
                            if (holder.state.value.draft?.revision != holder.state.value.savedRevision) holder.scheduleFlush()
                        }
                    }
                    continue
                }
                withContext(NonCancellable) {
                    runCatching { repo.status(write, if (networkStarted) "unknown" else "prepared") }
                }
                if (error is CancellationException) throw error
                throw error
            }
        }
        false
    }

    fun save() {
        if (state.value.busy || state.value.importing || state.value.draft == null) return
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
                val response = active.api(HttpMethod.parse(write.method), write.path, write.body, write.idempotencyKey)
                val remote = withContext(Dispatchers.Default) { CoreJson.decodeFromString<Note>(response) }
                val draftBeforeCheck = write.submittedDraftJson?.let { CoreJson.decodeFromString<DraftEntity>(it) }
                    ?: requireNotNull(state.value.draft)
                val expected = withContext(Dispatchers.Default) {
                    try { expectedSubmittedResult(draftBeforeCheck, write, remote, active) }
                    catch (failure: Exception) { if (failure is CancellationException) throw failure; false }
                }
                val conflictMaterial = withContext(Dispatchers.Default) { conflictBody(write, remote) }
                if (stamp != generation) throw CancellationException("Account changed")
                val before = state.value
                val next = completedDraft(requireNotNull(before.draft), write, remote, expected, conflictMaterial)
                mutable.value = before.copy(draft = next, pending = null,
                    confirmation = ConfirmedChange(write, remote, next.conflictJson != null))
                attempt = null
                flush()
            } catch (error: Exception) {
                val write = attempt
                if (write != null) {
                    if (error is CoreException && error.detail.code in setOf("SOURCE_CHANGED", "SOURCE_NOT_FOUND", "NOT_FOUND", "IDEMPOTENCY_CONFLICT", "ATTACHMENT_REFERENCED", "ATTACHMENT_DELETE_REJECTED", "WRITE_CONFLICT") && stamp == generation) {
                        val remote = if (write.method == "PATCH") runCatching {
                            CoreJson.decodeFromString<Note>(active.api(HttpMethod.Get, write.path))
                        }.getOrNull() else null
                        val material = withContext(Dispatchers.Default) { conflictBody(write, remote) }
                        val before = state.value
                        val draft = before.draft
                        if (draft != null && stamp == generation) {
                            mutable.value = before.copy(draft = draft.copy(conflictJson = material),
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
                        revision = draft.revision + 1), error = null, serverSaved = false)
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

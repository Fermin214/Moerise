package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

internal data class DrainResult(val drained: Boolean, val committedCount: Int, val hadNewConflict: Boolean)

internal class NoteWriteQueue(
    private val repository: suspend () -> NoteRepository,
    private val draftLock: Mutex,
    private val editors: () -> Collection<EditorController>,
    private val account: () -> String?,
    private val privacy: PrivacyController? = null,
    private val onProjected: (String, Note?, Note) -> Unit = { _, _, _ -> },
    private val onSaved: (String, Note, String?) -> Unit = { _, _, _ -> },
    private val onCommitted: (String, DraftEntity?, PendingWriteEntity) -> Unit = { _, _, _ -> },
) {
    private val sendLock = Mutex()
    private var received: WriteResult? = null

    suspend fun reset() = sendLock.withLock { received = null }

    suspend fun drain(key: String, client: CoreClient): DrainResult = sendLock.withLock {
        val repo = repository()
        var committed = 0
        var newConflict = false
        repeat(100) {
            if (key != account()) return@withLock DrainResult(false, committed, newConflict)
            draftLock.withLock {
                for (draft in repo.queuedNotes(key)) {
                    val frozen = draft.queuedRevisionJson ?: continue
                    if (draft.conflictJson == null && repo.storedDraft(key, draft.id).pending == null) {
                        checkAccount(key)
                        repo.prepare(CoreJson.decodeFromString<DraftEntity>(frozen), draft)
                    }
                }
            }
            val write = repo.queuedWrites(key).firstOrNull() ?: return@withLock DrainResult(!repo.automaticWritesPending(key), committed, newConflict)
            checkAccount(key)
            try {
                val result = received?.takeIf { it.write.id == write.id } ?: send(repo, client, write).also { received = it }
                draftLock.withLock {
                    val previous = result.remote?.let { repo.localNote(key, it.name) }
                    currentCoroutineContext().ensureActive()
                    withContext(NonCancellable) {
                        val next = commit(repo, result)
                        if (key == account()) {
                            result.remote?.let { onProjected(key, previous, it) }
                            if (result.error == null) result.remote?.let { onSaved(key, it, write.draftId.takeIf { write.method == "POST" }) }
                            onCommitted(key, next, write)
                        }
                        received = null
                    }
                }
                if (result.matches) committed++
                if (!result.matches) newConflict = true
                privacy?.endWrite("${write.draftId}:${write.draftRevision}", when {
                    result.matches -> "success"
                    result.error == null || result.error in setOf("WRITE_CONFLICT", "SOURCE_CHANGED", "IDEMPOTENCY_CONFLICT") -> "conflict"
                    else -> "failure"
                })
            } catch (failure: Exception) {
                privacy?.endWrite("${write.draftId}:${write.draftRevision}", if (failure is CancellationException) "unknown" else usageResult(failure), failure)
                withContext(NonCancellable) { runCatching { repo.status(write, "unknown") } }
                throw failure
            }
        }
        DrainResult(!repo.automaticWritesPending(key), committed, newConflict)
    }

    private fun checkAccount(key: String) {
        if (account() != key) throw CancellationException("Account changed")
    }

    private suspend fun send(repo: NoteRepository, client: CoreClient, write: PendingWriteEntity): WriteResult {
        val saved = write.submittedDraftJson?.let { CoreJson.decodeFromString<DraftEntity>(it) }
            ?: repo.storedDraft(write.accountKey, write.draftId).draft
        repo.status(write, "sending")
        checkAccount(write.accountKey)
        privacy?.sendingWrite("${write.draftId}:${write.draftRevision}")
        val response = try {
            client.api(HttpMethod.parse(write.method), write.path, write.body, write.idempotencyKey)
        } catch (error: CoreException) {
            if (!error.rejectedNoteWrite()) throw error
            val remote = if (write.method == "PATCH") try {
                CoreJson.decodeFromString<Note>(client.api(HttpMethod.Get, write.path))
            } catch (failure: Exception) {
                if (failure is CancellationException) throw failure
                null
            } else null
            if (remote != null) validateRemote(write, remote)
            return WriteResult(write, remote, false, error.detail.code)
        }
        val remote = CoreJson.decodeFromString<Note>(response)
        validateRemote(write, remote)
        val matches = expectedSubmittedResult(saved, write, remote, client)
        return WriteResult(write, remote, matches)
    }

    private fun validateRemote(write: PendingWriteEntity, remote: Note) {
        require(remote.instanceId == write.accountKey.split('\u0000')[1]) { "ACCOUNT_CHANGED" }
        require(Regex("^memos/[A-Za-z0-9_-]+$").matches(remote.name) && remote.sourceHash.isNotBlank())
        require(write.method != "PATCH" || write.path == "/notes/${remote.name.removePrefix("memos/")}")
    }

    private suspend fun commit(repo: NoteRepository, result: WriteResult): DraftEntity? {
        val write = result.write
        checkAccount(write.accountKey)
        val stored = repo.storedDraft(write.accountKey, write.draftId).draft
        checkAccount(write.accountKey)
        val holder = editors().firstOrNull {
            it.state.value.accountKey == write.accountKey && it.state.value.draft?.id == write.draftId
        }
        val latest = holder?.state?.value?.draft ?: stored
        val continuation = stored.queuedRevisionJson?.let { CoreJson.decodeFromString<DraftEntity>(it) }
            ?.takeIf { result.matches && it.revision > write.draftRevision }
            ?.let { result.rebase(it).copy(queuedRevisionJson = null) }

        fun confirmed(draft: DraftEntity): DraftEntity {
            val next = result.rebase(draft)
            val finished = next.slot.startsWith("queued:") && next.conflictJson == null && continuation == null
            return next.copy(slot = if (finished) "saved:${next.id}" else next.slot,
                queuedRevisionJson = continuation?.let { CoreJson.encodeToString(it) })
        }

        if (holder == null && result.matches && continuation == null &&
            latest.slot.startsWith("queued:") && latest.revision == write.draftRevision) {
            repo.finishQueued(write, requireNotNull(result.remote))
            return null
        } else {
            val next = confirmed(latest)
            repo.confirm(write, next, result.remote, next.conflictJson != null)
            holder?.acceptConfirmation(latest, next, result.error, ::confirmed)
            return next.takeIf { it.conflictJson != null || it.queuedRevisionJson != null ||
                it.slot.startsWith("saved:") && (it.content != result.remote?.content || it.attachmentEdits() != AttachmentEdits()) }
        }
    }
}

private data class WriteResult(
    val write: PendingWriteEntity,
    val remote: Note?,
    val matches: Boolean,
    val error: String? = null,
) {
    fun rebase(draft: DraftEntity): DraftEntity =
        if (matches) completedDraft(draft, write, requireNotNull(remote))
        else draft.copy(targetName = remote?.name ?: draft.targetName, conflictJson = conflictBody(write, remote))
}

private fun CoreException.rejectedNoteWrite(): Boolean =
    detail.code in setOf("SOURCE_CHANGED", "SOURCE_NOT_FOUND", "NOT_FOUND", "IDEMPOTENCY_CONFLICT",
        "ATTACHMENT_REFERENCED", "ATTACHMENT_DELETE_REJECTED", "WRITE_CONFLICT") ||
        status in 400..499 && status !in setOf(401, 429) && !detail.retryable

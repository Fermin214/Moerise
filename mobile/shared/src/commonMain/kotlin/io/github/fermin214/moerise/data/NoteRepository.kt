package io.github.fermin214.moerise.data

import androidx.room.immediateTransaction
import androidx.room.useWriterConnection
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.NotePage
import kotlinx.serialization.json.*
import kotlin.time.Instant
import kotlin.time.ExperimentalTime

expect fun newLocalId(): String

data class RestoredDraft(val draft: DraftEntity, val pending: PendingWriteEntity?)

@OptIn(ExperimentalTime::class)
fun orderedNotes(notes: List<Note>): List<Note> = notes.sortedByDescending { Instant.parse(it.createTime) }

class NoteRepository(private val db: LocalDatabase) {
    private suspend fun <T> transaction(block: suspend () -> T): T =
        db.useWriterConnection { it.immediateTransaction { block() } }

    suspend fun restore(key: String): RestoredDraft = transaction {
        db.dao().recoverSending(key)
        val draft = db.dao().homeDraft(key) ?: DraftEntity(newLocalId(), key, "", 0).also { db.dao().putDraft(it) }
        RestoredDraft(draft, db.dao().pending(draft.id))
    }

    suspend fun persist(draft: DraftEntity) { db.dao().putDraft(draft) }

    suspend fun prepare(draft: DraftEntity): PendingWriteEntity = transaction {
        db.dao().pending(draft.id)?.let { return@transaction it }
        require(draft.conflictJson == null) { "SOURCE_CHANGED" }
        require(draft.content.isNotBlank()) { "CONTENT_EMPTY" }
        require(draft.content.encodeToByteArray().size <= 65_536) { "CONTENT_TOO_LARGE" }
        val target = draft.targetName
        if (target != null) require(Regex("^memos/[A-Za-z0-9_-]+$").matches(target) && !draft.sourceHash.isNullOrBlank())
        val body = buildJsonObject {
            put("content", draft.content)
            if (target != null) put("source_hash", draft.sourceHash)
        }.toString()
        db.dao().putDraft(draft)
        PendingWriteEntity(newLocalId(), draft.id, draft.accountKey, newLocalId(),
            if (target == null) "POST" else "PATCH", if (target == null) "/notes" else "/notes/${target.removePrefix("memos/")}",
            body, draft.revision,
        ).also { db.dao().putWrite(it) }
    }

    suspend fun status(write: PendingWriteEntity, status: String) { db.dao().putWrite(write.copy(status = status)) }

    suspend fun confirm(write: PendingWriteEntity, next: DraftEntity, remote: Note?, conflict: Boolean) = transaction {
        require(next.accountKey == write.accountKey)
        if (write.draftId != next.id) db.dao().deleteDraft(write.draftId)
        db.dao().putDraft(next)
        if (conflict) db.dao().putWrite(write.copy(status = "conflict"))
        else db.dao().deleteWrite(write.id)
        if (remote != null) updateSnapshot(write.accountKey, remote)
    }

    private suspend fun updateSnapshot(key: String, remote: Note) {
        val previous = snapshot(key)?.notes ?: emptyList()
        val notes = orderedNotes((previous.filter { it.name != remote.name } + remote).filter { it.state == "NORMAL" }).take(100)
        db.dao().putSnapshot(SnapshotEntity(key, "home", CoreJson.encodeToString(NotePage(notes)), epochMillis()))
    }

    suspend fun snapshot(key: String): NotePage? =
        db.dao().snapshot(key, "home")?.let { CoreJson.decodeFromString(it.body) }

    suspend fun cache(key: String, page: NotePage) {
        db.dao().putSnapshot(SnapshotEntity(key, "home", CoreJson.encodeToString(page.copy(notes = page.notes.take(100))), epochMillis()))
    }

    suspend fun clearCache(key: String) { db.dao().deleteSnapshots(key) }
}

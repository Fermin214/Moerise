package io.github.fermin214.moerise.data

import androidx.room.immediateTransaction
import androidx.room.useWriterConnection
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.NotePage
import kotlinx.serialization.json.*
import kotlin.time.Instant
import kotlin.time.ExperimentalTime
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

expect fun newLocalId(): String

data class RestoredDraft(val draft: DraftEntity, val pending: PendingWriteEntity?)

@OptIn(ExperimentalTime::class)
fun orderedNotes(notes: List<Note>): List<Note> = notes.sortedByDescending { Instant.parse(it.createTime) }

class NoteRepository(private val db: LocalDatabase, private val files: AttachmentFiles? = null) {
    private suspend fun <T> transaction(block: suspend () -> T): T =
        db.useWriterConnection { it.immediateTransaction { block() } }

    suspend fun restore(key: String, note: Note? = null): RestoredDraft = transaction {
        db.dao().recoverSending(key)
        val slot = note?.name ?: "home"
        val existing = db.dao().draftForSlot(key, slot)
        val pending = existing?.let { db.dao().pending(it.id) }
        val draft = when {
            existing == null -> if (note == null) DraftEntity(newLocalId(), key, "", 0) else
                DraftEntity(newLocalId(), key, note.content, 0, note.name, note.sourceHash,
                    baselineJson = CoreJson.encodeToString(note), slot = slot)
            note == null || pending != null || existing.conflictJson != null || existing.sourceHash == note.sourceHash -> existing
            else -> {
                val baseline = existing.baselineJson?.let { CoreJson.decodeFromString<Note>(it) }
                if (existing.content == baseline?.content && existing.attachmentEdits() == AttachmentEdits()) {
                    existing.copy(content = note.content, sourceHash = note.sourceHash, baselineJson = CoreJson.encodeToString(note))
                } else existing.copy(conflictJson = buildJsonObject {
                    put("submitted", buildJsonObject { put("content", baseline?.content ?: existing.content) })
                    put("remote", CoreJson.encodeToJsonElement(note))
                }.toString())
            }
        }
        db.dao().putDraft(draft)
        RestoredDraft(draft, pending)
    }

    suspend fun persist(draft: DraftEntity) { db.dao().putDraft(draft) }

    suspend fun storedDraft(key: String, id: String): RestoredDraft = transaction {
        db.dao().recoverSending(key)
        val draft = requireNotNull(db.dao().draft(id)) { "DRAFT_NOT_FOUND" }
        require(draft.accountKey == key) { "ACCOUNT_CHANGED" }
        RestoredDraft(draft, db.dao().pending(id))
    }

    suspend fun drafts(key: String): List<DraftEntity> {
        val pending = db.dao().pendingDraftIds(key).toSet()
        return db.dao().drafts(key).filter { draft ->
            val baseline = draft.baselineJson?.let { runCatching { CoreJson.decodeFromString<Note>(it) }.getOrNull() }
            val changed = draft.content != (baseline?.content ?: "")
            draft.conflictJson != null || draft.id in pending || changed || draft.attachmentEdits() != AttachmentEdits()
        }
    }
    suspend fun pendingDraftIds(key: String): List<String> = db.dao().pendingDraftIds(key)

    suspend fun forkConflict(draft: DraftEntity, content: String, attachments: AttachmentEdits): RestoredDraft = transaction {
        require(draft.conflictJson != null)
        val stored = requireNotNull(db.dao().draft(draft.id))
        require(stored.accountKey == draft.accountKey && stored.revision == draft.revision)
        val home = db.dao().homeDraft(draft.accountKey)
        if (home != null && home.id != draft.id) db.dao().putDraft(home.copy(slot = "saved:${home.id}"))
        db.dao().putDraft(stored.copy(slot = "conflict:${draft.id}"))
        val next = DraftEntity(newLocalId(), draft.accountKey, content, 0,
            attachmentsJson = CoreJson.encodeToString(attachments.copy(removed = emptyList())), slot = "home")
        db.dao().putDraft(next)
        RestoredDraft(next, null)
    }

    suspend fun clearAccount(key: String) {
        val owned = db.dao().drafts(key).flatMap { it.attachmentEdits().staged }
        transaction {
            db.dao().deleteWrites(key); db.dao().deleteDrafts(key); db.dao().deleteSnapshots(key)
            db.dao().deleteFeatures(key); db.dao().deleteProfile(key)
        }
        val remaining = db.dao().attachmentReferences().flatMap {
            if (it == "[]") emptyList() else CoreJson.decodeFromString<AttachmentEdits>(it).staged.map { file -> file.id }
        }.toSet()
        owned.distinctBy { it.id }.filter { it.id !in remaining }.forEach { files?.remove(it) }
    }

    suspend fun prepare(draft: DraftEntity): PendingWriteEntity = withContext(Dispatchers.Default) {
        db.dao().pending(draft.id)?.let { return@withContext it }
        require(draft.conflictJson == null) { "SOURCE_CHANGED" }
        val edits = draft.attachmentEdits()
        val baseline = draft.baselineJson?.let { CoreJson.decodeFromString<Note>(it) }
        require(draft.content.isNotBlank() || edits.staged.isNotEmpty() ||
            baseline?.attachments?.any { it["name"]?.jsonPrimitive?.content !in edits.removed } == true) { "CONTENT_EMPTY" }
        require(draft.content.encodeToByteArray().size <= 65_536) { "CONTENT_TOO_LARGE" }
        require(edits.staged.size <= 10 && edits.staged.sumOf { it.size } <= 8 * 1024 * 1024) { "ATTACHMENTS_TOO_LARGE" }
        val target = draft.targetName
        if (target != null) require(Regex("^memos/[A-Za-z0-9_-]+$").matches(target) && !draft.sourceHash.isNullOrBlank())
        val encodedFiles = edits.staged.map { file ->
            val content = requireNotNull(files).encoded(file)
            buildJsonObject {
                put("filename", file.filename); put("type", file.type)
                put("content", content)
            }
        }
        val body = buildJsonObject {
            put("content", draft.content)
            if (target != null) put("source_hash", draft.sourceHash)
            if (encodedFiles.isNotEmpty()) put("attachments", JsonArray(encodedFiles))
            if (edits.removed.isNotEmpty()) {
                require(target != null)
                put("remove_attachments", JsonArray(edits.removed.map(::JsonPrimitive)))
            }
        }.toString()
        require(body.encodeToByteArray().size <= 12 * 1024 * 1024) { "ATTACHMENTS_TOO_LARGE" }
        transaction {
            db.dao().pending(draft.id)?.let { return@transaction it }
            db.dao().putDraft(draft)
            PendingWriteEntity(newLocalId(), draft.id, draft.accountKey, newLocalId(),
                if (target == null) "POST" else "PATCH", if (target == null) "/notes" else "/notes/${target.removePrefix("memos/")}",
                body, draft.revision,
            ).also { db.dao().putWrite(it) }
        }
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
    suspend fun datedSnapshot(key: String): Pair<NotePage, Long>? =
        db.dao().snapshot(key, "home")?.let { CoreJson.decodeFromString<NotePage>(it.body) to it.fetchedAt }

    suspend fun cache(key: String, page: NotePage) {
        db.dao().putSnapshot(SnapshotEntity(key, "home", CoreJson.encodeToString(page.copy(notes = page.notes.take(100))), epochMillis()))
    }

    suspend fun clearCache(key: String) { db.dao().deleteSnapshots(key) }
    suspend fun feature(key: String, feature: String): String? = db.dao().feature(key, feature)
    suspend fun putFeature(key: String, feature: String, body: String) { db.dao().putFeature(FeatureStateEntity(key, feature, body)) }

    suspend fun collectFiles() = withContext(Dispatchers.Default) {
        files?.collect(db.dao().attachmentReferences().flatMap { body ->
            if (body == "[]") emptyList() else CoreJson.decodeFromString<AttachmentEdits>(body).staged.map { it.id }
        }.toSet())
        Unit
    }

    private fun actionSlot(key: String, name: String) = "$key\u0000action:$name"

    suspend fun action(key: String, name: String): PendingWriteEntity? = transaction {
        db.dao().recoverSending(key)
        db.dao().pending(actionSlot(key, name))
    }

    suspend fun prepareAction(key: String, name: String, method: String, body: String): PendingWriteEntity = transaction {
        require(method in setOf("PATCH", "DELETE") && Regex("^memos/[A-Za-z0-9_-]+$").matches(name))
        val slot = actionSlot(key, name)
        db.dao().pending(slot)?.let {
            require(it.method == method) { "WRITE_OUTCOME_UNKNOWN" }
            return@transaction it
        }
        PendingWriteEntity(newLocalId(), slot, key, newLocalId(), method,
            "/notes/${name.removePrefix("memos/")}", body, 0).also { db.dao().putWrite(it) }
    }

    suspend fun finishAction(write: PendingWriteEntity, remote: Note?, name: String) = transaction {
        db.dao().deleteWrite(write.id)
        if (remote != null) updateSnapshot(write.accountKey, remote)
        else {
            val notes = snapshot(write.accountKey)?.notes?.filter { it.name != name } ?: emptyList()
            db.dao().putSnapshot(SnapshotEntity(write.accountKey, "home", CoreJson.encodeToString(NotePage(notes)), epochMillis()))
        }
    }
}

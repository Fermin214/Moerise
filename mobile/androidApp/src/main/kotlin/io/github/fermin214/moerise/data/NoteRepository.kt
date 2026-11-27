package io.github.fermin214.moerise.data

import androidx.room.immediateTransaction
import androidx.room.useWriterConnection
import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.NotePage
import io.github.fermin214.moerise.presentation.CalendarPage
import io.github.fermin214.moerise.presentation.DayCount
import io.github.fermin214.moerise.presentation.SearchQuery
import io.github.fermin214.moerise.presentation.TagCount
import io.github.fermin214.moerise.presentation.TagPage
import io.github.fermin214.moerise.presentation.displayServiceTime
import java.time.YearMonth
import java.time.ZoneId
import kotlinx.serialization.json.*
import kotlinx.serialization.Serializable
import kotlin.time.Instant
import kotlin.time.ExperimentalTime
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext


data class RestoredDraft(val draft: DraftEntity, val pending: PendingWriteEntity?)

internal data class QueuedDraft(val frozen: DraftEntity, val home: RestoredDraft)

internal const val ACTIVE_NOTE_EDITOR = "active-note-editor"

@Serializable
internal data class StateActionIntent(val note: Note, val undo: Boolean = false)

internal data class StateAction(val write: PendingWriteEntity, val intent: StateActionIntent?, val fresh: Boolean = false) {
    val name: String get() = "memos/${write.path.substringAfterLast('/')}"
    val target: String get() = CoreJson.parseToJsonElement(write.body).jsonObject.getValue("state").jsonPrimitive.content
    val visibleState: String get() = if (intent?.undo == true) "NORMAL" else target
}

enum class SyncNotesOutcome { UPDATED, NO_CHANGES, UNSUPPORTED_LEGACY }

data class LocalRemovalCounts(val queued: Int, val conflicts: Int, val attachments: Int) {
    val requiresConfirmation: Boolean get() = queued > 0 || conflicts > 0 || attachments > 0
}

private fun Note.toEntity(accountKey: String): NoteEntity = NoteEntity(
    accountKey = accountKey,
    name = name,
    instanceId = instanceId,
    content = content,
    state = state,
    sourceHash = sourceHash,
    createTime = sortableTime(createTime),
    updateTime = sortableTime(updateTime),
    noteJson = CoreJson.encodeToString(this),
)

private fun Note.tagEntities(accountKey: String): List<NoteTagEntity> =
    tags.asSequence().filter { it.isNotBlank() }.distinct().map { NoteTagEntity(accountKey, name, it) }.toList()

private fun NoteEntity.toNote(): Note = CoreJson.decodeFromString(noteJson)

class NoteRepository(private val db: LocalDatabase, private val files: AttachmentFiles? = null) {
    private val recoveredAccounts = mutableSetOf<String>()
    private val syncLock = Mutex()
    private val removalLock = Mutex()
    private val pendingRemoval = "pending-account-attachment-removal"
    private val pendingLogout = "pending-account-logout"
    private val mutableProjection = MutableStateFlow(0L)
    internal val projection = mutableProjection.asStateFlow()

    private suspend fun <T> transaction(publishesNotes: Boolean = false, block: suspend () -> T): T {
        val caller = currentCoroutineContext()
        return db.useWriterConnection { connection ->
            if (!publishesNotes) connection.immediateTransaction { block() }
            else {
                caller.ensureActive()
                val result = withContext(NonCancellable) {
                    val committed = connection.immediateTransaction { block() }
                    mutableProjection.update { it + 1 }
                    committed
                }
                caller.ensureActive()
                result
            }
        }
    }

    suspend fun restore(key: String, note: Note? = null): RestoredDraft = transaction {
        recoverOnce(key)
        val slot = note?.name ?: "home"
        val existing = db.dao().draftForSlot(key, slot)
        val pending = existing?.let { db.dao().pending(it.id) }
        val draft = when {
            existing == null -> if (note == null) DraftEntity(newLocalId(), key, "", 0, createdAt = epochMillis()) else
                DraftEntity(newLocalId(), key, note.content, 0, note.name, note.sourceHash,
                    baselineJson = CoreJson.encodeToString(note), slot = slot, createdAt = epochMillis())
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

    suspend fun storedDraft(key: String, id: String): RestoredDraft =
        requireNotNull(storedDraftOrNull(key, id)) { "DRAFT_NOT_FOUND" }

    internal suspend fun storedDraftOrNull(key: String, id: String): RestoredDraft? = transaction {
        recoverOnce(key)
        val draft = db.dao().draft(id) ?: return@transaction null
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
    suspend fun hasPendingCreation(key: String): Boolean = db.dao().hasPendingCreation(key)

    suspend fun queuedNotes(key: String): List<DraftEntity> {
        val pending = db.dao().pendingDraftIds(key).toSet()
        return db.dao().drafts(key).filter {
            it.slot.startsWith("queued:") || it.slot.startsWith("conflict:") ||
                it.id in pending || it.slot.startsWith("saved:") &&
                (it.content != it.baselineJson?.let { body -> CoreJson.decodeFromString<Note>(body).content }.orEmpty() ||
                    it.attachmentEdits() != AttachmentEdits())
        }.sortedWith(compareByDescending<DraftEntity> { it.createdAt }.thenBy { it.id })
    }

    suspend fun queuedWrites(key: String): List<PendingWriteEntity> = db.dao().queuedWrites(key)
    suspend fun automaticWritesPending(key: String): Boolean {
        val pending = db.dao().pendingDraftIds(key).toSet()
        return queuedNotes(key).any { it.conflictJson == null && (it.id in pending || it.queuedRevisionJson != null) }
    }

    internal suspend fun enqueue(draft: DraftEntity, write: PendingWriteEntity): QueuedDraft = transaction {
        enqueueLocked(draft, write)
    }

    internal suspend fun submit(draft: DraftEntity): QueuedDraft {
        val prepared = db.dao().pending(draft.id) ?: prepareWrite(draft)
        currentCoroutineContext().ensureActive()
        return withContext(NonCancellable) {
            transaction {
                val write = db.dao().pending(draft.id) ?: prepared.also { db.dao().putWrite(it) }
                enqueueLocked(draft, write).also { clearActiveEditor(draft.accountKey, draft.id) }
            }
        }
    }

    internal suspend fun clearActiveEditor(account: String, id: String) {
        val mark = feature(account, ACTIVE_NOTE_EDITOR)?.let {
            runCatching { CoreJson.parseToJsonElement(it).jsonObject["draft_id"]?.jsonPrimitive?.content }.getOrNull()
        }
        if (mark == id) putFeature(account, ACTIVE_NOTE_EDITOR, "{}")
    }

    private suspend fun enqueueLocked(draft: DraftEntity, write: PendingWriteEntity): QueuedDraft {
        require(write.draftId == draft.id && write.accountKey == draft.accountKey)
        require(db.dao().pending(draft.id)?.id == write.id)
        val queued = draft.copy(slot = "queued:${draft.id}",
            createdAt = if (draft.slot.startsWith("queued:") && draft.createdAt > 0) draft.createdAt else epochMillis(),
            queuedRevisionJson = null)
        val frozen = queued.copy(queuedRevisionJson = CoreJson.encodeToString(queued))
        db.dao().putDraft(frozen)
        val home = db.dao().homeDraft(draft.accountKey) ?: DraftEntity(newLocalId(), draft.accountKey, "", 0, createdAt = epochMillis())
        db.dao().putDraft(home)
        return QueuedDraft(frozen, RestoredDraft(home, db.dao().pending(home.id)))
    }

    suspend fun finishQueued(write: PendingWriteEntity, remote: Note) = transaction(publishesNotes = true) {
        require(remote.instanceId == write.accountKey.split('\u0000')[1])
        db.dao().deleteWrite(write.id)
        db.dao().deleteDraft(write.draftId)
        upsertLocalNoteLocked(write.accountKey, remote)
        updateSnapshot(write.accountKey, remote)
    }

    suspend fun forkConflict(draft: DraftEntity, content: String, attachments: AttachmentEdits): RestoredDraft = transaction {
        require(draft.conflictJson != null)
        val stored = requireNotNull(db.dao().draft(draft.id))
        require(stored.accountKey == draft.accountKey && stored.revision == draft.revision)
        val home = db.dao().homeDraft(draft.accountKey)
        if (home != null && home.id != draft.id) db.dao().putDraft(home.copy(slot = "saved:${home.id}"))
        db.dao().putDraft(stored.copy(slot = "conflict:${draft.id}"))
        val next = DraftEntity(newLocalId(), draft.accountKey, content, 0,
            attachmentsJson = CoreJson.encodeToString(attachments.copy(removed = emptyList())), slot = "home", createdAt = epochMillis())
        db.dao().putDraft(next)
        putFeature(draft.accountKey, ACTIVE_NOTE_EDITOR, buildJsonObject {
            put("kind", "new")
            put("draft_id", next.id)
        }.toString())
        RestoredDraft(next, null)
    }

    private fun accountAttachments(drafts: List<DraftEntity>, writes: List<PendingWriteEntity>): List<StagedAttachment> =
        (drafts + (drafts.mapNotNull { it.queuedRevisionJson } + writes.mapNotNull { it.submittedDraftJson })
            .map { CoreJson.decodeFromString<DraftEntity>(it) })
            .flatMap { it.attachmentEdits().staged }.distinctBy { it.id }

    suspend fun localRemovalCounts(key: String): LocalRemovalCounts = transaction {
        val drafts = db.dao().drafts(key)
        val writes = db.dao().accountWrites(key)
        val draftIds = drafts.map { it.id }.toSet()
        val draftWrites = writes.filter { it.draftId in draftIds }
        val conflicts = (drafts.filter { it.conflictJson != null }.map { it.id } +
            draftWrites.filter { it.status == "conflict" }.map { it.draftId }).toSet()
        val queued = (drafts.filter { it.queuedRevisionJson != null }.map { it.id } +
            draftWrites.filter { it.status != "conflict" }.map { it.draftId }).toSet() - conflicts
        LocalRemovalCounts(queued.size, conflicts.size, accountAttachments(drafts, writes).size)
    }

    suspend fun clearAccount(key: String) = removalLock.withLock {
        clearAccountData(key, signOut = false)
        finishAttachmentRemoval(key)
    }

    internal suspend fun clearAccountForLogout(key: String) = removalLock.withLock {
        clearAccountData(key, signOut = true)
    }

    private suspend fun clearAccountData(key: String, signOut: Boolean) {
        transaction(publishesNotes = true) {
            val attachments = pendingAttachmentRemoval(key) +
                accountAttachments(db.dao().drafts(key), db.dao().accountWrites(key)).map { it.id }
            val logout = signOut || hasPendingLogout(key)
            db.dao().deleteWrites(key); db.dao().deleteDrafts(key); db.dao().deleteSnapshots(key)
            db.dao().deleteFeatures(key); db.dao().deleteProfile(key)
            db.dao().deleteAllNoteTags(key); db.dao().deleteAllNotes(key)
            db.dao().deleteSyncState(key); db.dao().deleteCachedSnapshots(key)
            if (attachments.isNotEmpty() && files != null) putFeature(key, pendingRemoval, CoreJson.encodeToString(attachments.distinct()))
            if (logout) putFeature(key, pendingLogout, "")
        }
    }

    internal suspend fun hasPendingLogout(key: String) = feature(key, pendingLogout) != null

    internal suspend fun completePendingLogout(key: String) { db.dao().deleteFeature(key, pendingLogout) }

    internal suspend fun finishAccountAttachmentRemoval(key: String) = removalLock.withLock { finishAttachmentRemoval(key) }

    private suspend fun pendingAttachmentRemoval(key: String): List<String> =
        feature(key, pendingRemoval)?.let { CoreJson.decodeFromString<List<String>>(it) }.orEmpty()

    private suspend fun finishAttachmentRemoval(key: String) {
        val storage = files ?: return
        val pending = pendingAttachmentRemoval(key)
        if (pending.isEmpty()) return
        val referenced = attachmentIds()
        for ((index, id) in pending.withIndex()) {
            if (id !in referenced) storage.remove(id)
            val remaining = pending.drop(index + 1)
            if (remaining.isEmpty()) db.dao().deleteFeature(key, pendingRemoval)
            else putFeature(key, pendingRemoval, CoreJson.encodeToString(remaining))
        }
    }

    suspend fun resumeAttachmentRemovals() = removalLock.withLock {
        for (key in db.dao().featureAccounts(pendingRemoval)) finishAttachmentRemoval(key)
    }

    suspend fun prepare(draft: DraftEntity, stored: DraftEntity = draft): PendingWriteEntity {
        db.dao().pending(draft.id)?.let { return it }
        val write = prepareWrite(draft)
        return transaction {
            db.dao().pending(draft.id)?.let { return@transaction it }
            require(stored.id == draft.id && stored.accountKey == draft.accountKey)
            db.dao().putDraft(stored)
            db.dao().putWrite(write)
            write
        }
    }

    private suspend fun prepareWrite(draft: DraftEntity): PendingWriteEntity = withContext(Dispatchers.Default) {
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
        PendingWriteEntity(newLocalId(), draft.id, draft.accountKey, newLocalId(),
                if (target == null) "POST" else "PATCH", if (target == null) "/notes" else "/notes/${target.removePrefix("memos/")}",
                body, draft.revision,
                submittedDraftJson = CoreJson.encodeToString(draft.copy(queuedRevisionJson = null)),
        )
    }

    suspend fun status(write: PendingWriteEntity, status: String) { db.dao().writeStatus(write.id, write.accountKey, status) }

    suspend fun confirm(write: PendingWriteEntity, next: DraftEntity, remote: Note?, conflict: Boolean) =
        transaction(publishesNotes = remote != null) {
            require(next.accountKey == write.accountKey)
            if (write.draftId != next.id) db.dao().deleteDraft(write.draftId)
            db.dao().putDraft(next)
            if (conflict) db.dao().putWrite(write.copy(status = "conflict"))
            else db.dao().deleteWrite(write.id)
            if (remote != null) {
                upsertLocalNoteLocked(write.accountKey, remote)
                updateSnapshot(write.accountKey, remote)
            }
        }

    private suspend fun upsertLocalNoteLocked(key: String, note: Note) {
        if (!isNotesSynced(key)) db.dao().deleteFilterSnapshots(key)
        db.dao().deleteNoteTagsByName(key, listOf(note.name))
        db.dao().putNote(note.toEntity(key))
        val tags = note.tagEntities(key)
        if (tags.isNotEmpty()) db.dao().putNoteTags(tags)
    }

    private suspend fun deleteLocalNoteLocked(key: String, name: String) {
        if (!isNotesSynced(key)) db.dao().deleteFilterSnapshots(key)
        db.dao().deleteNoteTagsByName(key, listOf(name))
        db.dao().deleteNotesByName(key, listOf(name))
    }

    suspend fun recordLocalNote(key: String, note: Note) = transaction(publishesNotes = true) {
        upsertLocalNoteLocked(key, note)
    }

    suspend fun deleteLocalNote(key: String, name: String) = transaction(publishesNotes = true) {
        deleteLocalNoteLocked(key, name)
    }

    suspend fun isNotesSynced(key: String): Boolean = db.dao().syncState(key)?.initialized == true
    suspend fun lastNoteSync(key: String): Long? = db.dao().syncState(key)?.updatedAt

    suspend fun syncNotes(key: String, core: CoreClient): SyncNotesOutcome = syncLock.withLock {
        val instance = key.split('\u0000').getOrNull(1).orEmpty()
        val initial = db.dao().syncState(key)
        var cursor = initial?.cursor.orEmpty()
        val changes = NoteSyncChanges(instance, initial?.initialized == true, initial?.syncGeneration.orEmpty())
        try {
            while (true) {
                val page = core.syncNotes(cursor, limit = 100)
                changes.accept(page)
                if (!page.hasMore) {
                    cursor = page.nextCursor
                    break
                }
                cursor = page.nextCursor
            }
        } catch (error: CoreException) {
            if (error.status == 404 && initial == null) return@withLock SyncNotesOutcome.UNSUPPORTED_LEGACY
            throw error
        }
        val reset = changes.reset
        val upserts = changes.upserts
        val deletions = changes.deletions
        val generation = changes.generation
        val now = epochMillis()
        val projectionChanged = reset || upserts.isNotEmpty() || deletions.isNotEmpty()
        transaction(publishesNotes = projectionChanged) {
            if (reset) {
                db.dao().deleteAllNoteTags(key)
                db.dao().deleteAllNotes(key)
            } else {
                val clearedNames = (deletions + upserts.keys).distinct()
                clearedNames.chunked(200).forEach { chunk -> db.dao().deleteNoteTagsByName(key, chunk) }
                deletions.toList().chunked(200).forEach { chunk -> db.dao().deleteNotesByName(key, chunk) }
            }
            if (upserts.isNotEmpty()) {
                val entities = upserts.values.map { it.toEntity(key) }
                entities.chunked(200).forEach { chunk -> db.dao().putNotes(chunk) }
                val tags = upserts.values.flatMap { it.tagEntities(key) }
                tags.chunked(200).forEach { chunk -> db.dao().putNoteTags(chunk) }
            }
            db.dao().putSyncState(SyncStateEntity(key, cursor, generation, initialized = true, updatedAt = now))
        }
        if (projectionChanged) SyncNotesOutcome.UPDATED else SyncNotesOutcome.NO_CHANGES
    }

    suspend fun localWalkNote(key: String, visited: List<String>): Note? = db.dao().walkNote(key, visited)?.toNote()

    suspend fun localNote(key: String, name: String): Note? = db.dao().note(key, name)?.toNote()

    suspend fun localNotesPage(key: String, state: String, limit: Int, offset: Int): Pair<List<Note>, Boolean> {
        val rows = db.dao().notesPage(key, state, limit + 1, offset)
        val hasMore = rows.size > limit
        return rows.take(limit).map { it.toNote() } to hasMore
    }

    suspend fun localTagPage(key: String, state: String): TagPage =
        TagPage(db.dao().tagCounts(key, state).map { TagCount(it.tag, it.count) })

    suspend fun localCalendarPage(key: String, state: String, month: String, timezone: String): CalendarPage {
        val yearMonth = YearMonth.parse(month)
        val zone = ZoneId.of(timezone)
        val start = yearMonth.atDay(1).atStartOfDay(zone).toInstant()
        val end = yearMonth.plusMonths(1).atDay(1).atStartOfDay(zone).toInstant()
        val times = if (start.epochSecond < 0) db.dao().historicNoteTimes(key, state, start.epochSecond, end.epochSecond)
        else {
            val lower = sortableTime(start.toString())
            val upper = sortableTime(end.toString())
            if (state == "ALL") db.dao().noteTimes(key, lower, upper) else db.dao().noteTimes(key, state, lower, upper)
        }
        val counts = times.groupingBy { time ->
            val seconds = time.substringBefore('.').trimStart('0').ifEmpty { "0" }.toLong()
            val nanos = time.substringAfter('.').toLong()
            java.time.Instant.ofEpochSecond(seconds, nanos).atZone(zone).toLocalDate().toString()
        }.eachCount()
        return CalendarPage(month, counts.toSortedMap().map { (date, count) -> DayCount(date, count) })
    }

    suspend fun localSearch(key: String, query: SearchQuery, timezone: String, limit: Int, offset: Int): Pair<List<Note>, Boolean> {
        val filtered = db.dao().notesByState(key, query.state).asSequence()
            .map { it.toNote() }
            .filter { note -> query.matchesScope(note, timezone) && query.matchesKeyword(note) }
            .drop(offset)
            .take(limit + 1)
            .toList()
        val hasMore = filtered.size > limit
        return filtered.take(limit) to hasMore
    }

    suspend fun readCachedSnapshot(key: String, kind: String): CachedSnapshotEntity? = db.dao().cachedSnapshot(key, kind)
    suspend fun putCachedSnapshot(key: String, kind: String, body: String, fetchedAt: Long = epochMillis()) =
        db.dao().putCachedSnapshot(CachedSnapshotEntity(key, kind, body, fetchedAt))

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
    suspend fun readSnapshot(key: String, filter: String) = db.dao().snapshot(key, filter)
    suspend fun putSnapshot(snapshot: SnapshotEntity) = db.dao().putSnapshot(snapshot)
    internal suspend fun putFilterSnapshot(snapshot: SnapshotEntity, projectionVersion: Long) = transaction {
        if (projection.value != projectionVersion) throw CancellationException()
        db.dao().putSnapshot(snapshot)
    }
    suspend fun feature(key: String, feature: String): String? = db.dao().feature(key, feature)
    suspend fun putFeature(key: String, feature: String, body: String) { db.dao().putFeature(FeatureStateEntity(key, feature, body)) }

    suspend fun collectFiles() = withContext(Dispatchers.Default) {
        files?.collect(attachmentIds())
        Unit
    }

    private suspend fun attachmentIds(): Set<String> {
        val visible = db.dao().attachmentReferences().flatMap { body ->
            if (body == "[]") emptyList() else CoreJson.decodeFromString<AttachmentEdits>(body).staged.map { it.id }
        }
        val frozen = (db.dao().queuedRevisions() + db.dao().submittedRevisions()).flatMap { body ->
            CoreJson.decodeFromString<DraftEntity>(body).attachmentEdits().staged.map { it.id }
        }
        return (visible + frozen).toSet()
    }

    private fun actionSlot(key: String, name: String) = "$key\u0000action:$name"
    private fun intentSlot(name: String) = "note-state-action:$name"

    private suspend fun stateAction(write: PendingWriteEntity): StateAction {
        val name = "memos/${write.path.substringAfterLast('/')}"
        val intent = feature(write.accountKey, intentSlot(name))?.let { CoreJson.decodeFromString<StateActionIntent>(it) }
        return StateAction(write, intent)
    }

    internal suspend fun stateActions(key: String): List<StateAction> = transaction {
        recoverOnce(key)
        db.dao().accountWrites(key).filter {
            it.method == "PATCH" && it.draftId.startsWith("$key\u0000action:") && it.status != "conflict"
        }.map { stateAction(it) }
    }

    private fun stateWrite(key: String, note: Note, target: String) =
        PendingWriteEntity(newLocalId(), actionSlot(key, note.name), key, newLocalId(), "PATCH",
            "/notes/${note.name.removePrefix("memos/")}", buildJsonObject {
                put("source_hash", note.sourceHash)
                put("state", target)
            }.toString(), 0)

    internal suspend fun prepareStateAction(key: String, note: Note, target: String, undo: Boolean = false): StateAction = transaction {
        recoverOnce(key)
        require(note.instanceId == key.split('\u0000')[1] && Regex("^memos/[A-Za-z0-9_-]+$").matches(note.name))
        val existing = db.dao().pending(actionSlot(key, note.name))
        if (existing != null) {
            require(!undo && existing.method == "PATCH") { "WRITE_OUTCOME_UNKNOWN" }
            return@transaction stateAction(existing)
        }
        val write = stateWrite(key, note, target)
        val intent = StateActionIntent(note, undo)
        db.dao().putWrite(write)
        putFeature(key, intentSlot(note.name), CoreJson.encodeToString(intent))
        StateAction(write, intent, fresh = true)
    }

    internal suspend fun requestStateUndo(action: StateAction): StateAction? = transaction {
        val write = db.dao().pending(action.write.draftId)?.takeIf { it.id == action.write.id } ?: return@transaction null
        val state = stateAction(write)
        val intent = (state.intent ?: action.intent ?: return@transaction null).copy(undo = true)
        putFeature(write.accountKey, intentSlot(action.name), CoreJson.encodeToString(intent))
        state.copy(intent = intent)
    }

    internal suspend fun settleStateAction(action: StateAction, remote: Note? = null): StateAction? =
        transaction(publishesNotes = remote != null) {
            val write = db.dao().pending(action.write.draftId)?.takeIf { it.id == action.write.id } ?: return@transaction null
            val key = write.accountKey
            val intent = stateAction(write).intent
            db.dao().deleteWrite(write.id)
            db.dao().deleteFeature(key, intentSlot(action.name))
            if (remote == null) return@transaction null
            upsertLocalNoteLocked(key, remote)
            updateSnapshot(key, remote)
            if (action.target != "ARCHIVED" || remote.state != "ARCHIVED" || intent?.undo != true) return@transaction null
            val restore = stateWrite(key, remote, "NORMAL")
            val restoreIntent = StateActionIntent(remote, undo = true)
            db.dao().putWrite(restore)
            putFeature(key, intentSlot(action.name), CoreJson.encodeToString(restoreIntent))
            StateAction(restore, restoreIntent)
        }

    suspend fun action(key: String, name: String): PendingWriteEntity? = transaction {
        recoverOnce(key)
        db.dao().pending(actionSlot(key, name))
    }

    suspend fun prepareDeletion(key: String, name: String, body: String): PendingWriteEntity = transaction {
        require(Regex("^memos/[A-Za-z0-9_-]+$").matches(name))
        val slot = actionSlot(key, name)
        db.dao().pending(slot)?.let {
            require(it.method == "DELETE") { "WRITE_OUTCOME_UNKNOWN" }
            return@transaction it
        }
        PendingWriteEntity(newLocalId(), slot, key, newLocalId(), "DELETE",
            "/notes/${name.removePrefix("memos/")}", body, 0).also { db.dao().putWrite(it) }
    }

    suspend fun finishAction(write: PendingWriteEntity, remote: Note?, name: String) = transaction(publishesNotes = true) {
        db.dao().deleteWrite(write.id)
        if (remote != null) {
            upsertLocalNoteLocked(write.accountKey, remote)
            updateSnapshot(write.accountKey, remote)
        } else {
            deleteLocalNoteLocked(write.accountKey, name)
            val notes = snapshot(write.accountKey)?.notes?.filter { it.name != name } ?: emptyList()
            db.dao().putSnapshot(SnapshotEntity(write.accountKey, "home", CoreJson.encodeToString(NotePage(notes)), epochMillis()))
        }
    }

    private suspend fun recoverOnce(key: String) {
        if (key !in recoveredAccounts) {
            db.dao().recoverSending(key)
            recoveredAccounts.add(key)
        }
    }
}

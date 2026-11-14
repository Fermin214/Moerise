package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import kotlinx.serialization.json.*

data class ConfirmedChange(val write: PendingWriteEntity, val remote: Note?, val conflict: Boolean)

data class EditorUiState(
    val accountKey: String? = null,
    val draft: DraftEntity? = null,
    val pending: PendingWriteEntity? = null,
    val confirmation: ConfirmedChange? = null,
    val savedRevision: Long = -1,
    val busy: Boolean = false,
    val error: String? = null,
    val serverSaved: Boolean = false,
)

fun completedDraft(draft: DraftEntity, write: PendingWriteEntity, remote: Note, expected: Boolean, conflictMaterial: String): DraftEntity {
    require(draft.id == write.draftId && draft.accountKey == write.accountKey)
    require(remote.instanceId == draft.accountKey.split('\u0000')[1])
    require(Regex("^memos/[A-Za-z0-9_-]+$").matches(remote.name) && remote.sourceHash.isNotBlank())
    require(write.method != "PATCH" || remote.name == draft.targetName)
    return when {
        draft.revision == write.draftRevision -> if (draft.slot == "home") DraftEntity(newLocalId(), draft.accountKey, "", 0)
            else DraftEntity(newLocalId(), draft.accountKey, remote.content, 0, remote.name, remote.sourceHash,
                baselineJson = CoreJson.encodeToString(remote), slot = draft.slot)
        expected -> draft.copy(targetName = remote.name, sourceHash = remote.sourceHash,
            attachmentsJson = "[]", baselineJson = CoreJson.encodeToString(remote))
        else -> draft.copy(targetName = remote.name, conflictJson = conflictMaterial)
    }
}

// Attachments are identified by resource name and bytes, never by filename alone.
suspend fun expectedSubmittedResult(draft: DraftEntity, write: PendingWriteEntity, remote: Note, core: CoreClient): Boolean {
    val submitted = CoreJson.parseToJsonElement(write.body).jsonObject
    val baseline = draft.baselineJson?.let { CoreJson.decodeFromString<Note>(it) }
    if (remote.content != submitted.getValue("content").jsonPrimitive.content ||
        remote.state != (submitted["state"]?.jsonPrimitive?.content ?: baseline?.state ?: "NORMAL")) return false
    val edits = draft.attachmentEdits()
    val kept = (baseline?.attachments ?: emptyList()).filter { it["name"]?.jsonPrimitive?.content !in edits.removed }
    val keptNames = kept.map { it["name"]?.jsonPrimitive?.content }.toSet()
    if (remote.attachments.filter { it["name"]?.jsonPrimitive?.content in keptNames } != kept) return false
    val added = remote.attachments.filter { it["name"]?.jsonPrimitive?.content !in keptNames }.toMutableList()
    if (added.size != edits.staged.size) return false
    for (file in edits.staged) {
        var match: JsonObject? = null
        for (item in added) {
            if (item["filename"]?.jsonPrimitive?.content != file.filename || item["type"]?.jsonPrimitive?.content != file.type ||
                item["size"]?.jsonPrimitive?.longOrNull != file.size) continue
            val bytes = core.attachmentBytes(remote, item, 8 * 1024 * 1024)
            if (sha256(bytes) == file.sha256) { match = item; break }
        }
        if (match == null) return false
        added.remove(match)
    }
    return added.isEmpty()
}

fun conflictBody(write: PendingWriteEntity, remote: Note?): String = buildJsonObject {
    put("submitted", JsonObject(CoreJson.parseToJsonElement(write.body).jsonObject.filterKeys { it != "attachments" }))
    remote?.let { put("remote", CoreJson.encodeToJsonElement(it)) }
}.toString()

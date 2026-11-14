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

fun completedDraft(draft: DraftEntity, write: PendingWriteEntity, remote: Note): DraftEntity {
    require(draft.id == write.draftId && draft.accountKey == write.accountKey)
    require(remote.instanceId == draft.accountKey.split('\u0000')[1])
    require(Regex("^memos/[A-Za-z0-9_-]+$").matches(remote.name) && remote.sourceHash.isNotBlank())
    require(write.method != "PATCH" || remote.name == draft.targetName)
    val submitted = CoreJson.parseToJsonElement(write.body).jsonObject
    val baseline = draft.baselineJson?.let { CoreJson.decodeFromString<Note>(it) }
    val expected = remote.content == submitted.getValue("content").jsonPrimitive.content &&
        remote.state == (submitted["state"]?.jsonPrimitive?.content ?: baseline?.state ?: "NORMAL") &&
        remote.attachments == (baseline?.attachments ?: emptyList<JsonObject>())
    return when {
        draft.revision == write.draftRevision -> DraftEntity(newLocalId(), draft.accountKey, "", 0)
        expected -> draft.copy(targetName = remote.name, sourceHash = remote.sourceHash,
            baselineJson = CoreJson.encodeToString(remote))
        else -> draft.copy(targetName = remote.name, conflictJson = conflictBody(write, remote))
    }
}

fun conflictBody(write: PendingWriteEntity, remote: Note?): String = buildJsonObject {
    put("submitted", CoreJson.parseToJsonElement(write.body))
    remote?.let { put("remote", CoreJson.encodeToJsonElement(it)) }
}.toString()

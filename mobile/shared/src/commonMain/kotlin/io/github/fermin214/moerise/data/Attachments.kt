package io.github.fermin214.moerise.data

import kotlinx.serialization.Serializable

@Serializable
data class StagedAttachment(val id: String, val filename: String, val type: String, val size: Long, val sha256: String)

@Serializable
data class AttachmentEdits(val staged: List<StagedAttachment> = emptyList(), val removed: List<String> = emptyList())

fun DraftEntity.attachmentEdits(): AttachmentEdits =
    if (attachmentsJson == "[]") AttachmentEdits() else CoreJson.decodeFromString(attachmentsJson)

fun validFilename(name: String): Boolean =
    name.isNotBlank() && name == name.trim() && name != "." && name != ".." &&
        name.encodeToByteArray().size <= 255 && name.none { it.code < 32 || it.code in 127..159 || it in "/\\" }

expect class AttachmentFiles(root: String) {
    suspend fun stage(filename: String, type: String, content: ByteArray): StagedAttachment
    suspend fun encoded(file: StagedAttachment): String
    suspend fun remove(file: StagedAttachment)
    suspend fun collect(referenced: Set<String>)
}

expect fun sha256(bytes: ByteArray): String

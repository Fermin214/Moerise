package io.github.fermin214.moerise.data

import java.io.File
import java.security.MessageDigest
import java.util.Base64
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

actual class AttachmentFiles actual constructor(private val root: String) {
    private fun file(id: String): File {
        require(Regex("^[a-f0-9-]{36}$").matches(id))
        return File(root, id)
    }
    actual suspend fun stage(filename: String, type: String, content: ByteArray): StagedAttachment = withContext(Dispatchers.IO) {
        require(validFilename(filename)) { "INVALID_FILENAME" }
        require(Regex("^[a-z0-9!#&^_.+-]+/[a-z0-9!#&^_.+-]+$").matches(type)) { "INVALID_FILE_TYPE" }
        require(content.isNotEmpty() && content.size <= 8 * 1024 * 1024) { "ATTACHMENTS_TOO_LARGE" }
        File(root).mkdirs()
        val item = StagedAttachment(newLocalId(), filename, type, content.size.toLong(), sha256(content))
        val temporary = File(root, item.id + ".part")
        try {
            temporary.outputStream().use { it.write(content); it.fd.sync() }
            check(temporary.renameTo(file(item.id))) { "LOCAL_WRITE_FAILED" }
        } finally { temporary.delete() }
        item
    }
    actual suspend fun encoded(file: StagedAttachment): String = withContext(Dispatchers.IO) {
        val source = file(file.id)
        require(source.length() == file.size && file.size in 1..8L * 1024 * 1024) { "ATTACHMENT_MISSING" }
        val bytes = source.readBytes()
        require(sha256(bytes) == file.sha256) { "ATTACHMENT_CHANGED" }
        Base64.getEncoder().encodeToString(bytes)
    }
    actual suspend fun remove(file: StagedAttachment) = withContext(Dispatchers.IO) { file(file.id).delete(); Unit }
    actual suspend fun collect(referenced: Set<String>, minimumAgeMillis: Long) = withContext(Dispatchers.IO) {
        val cutoff = System.currentTimeMillis() - minimumAgeMillis
        File(root).listFiles()?.filter { it.name !in referenced && it.lastModified() <= cutoff }?.forEach { it.delete() }
        Unit
    }
}

actual fun sha256(bytes: ByteArray): String =
    MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }

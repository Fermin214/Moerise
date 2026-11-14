package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.presentation.expectedSubmittedResult
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.nio.file.Files
import kotlin.test.*
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*

class AttachmentRecoveryTest {
    @Test
    fun privateFilesAndFrozenBodySurviveReopenAndConfirmAttachmentBytes() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-attachments-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        var db = jvmDatabase(path)
        val key = "https://notes.example\u0000one\u0000users/owner"
        val files = AttachmentFiles(folder.resolve("files").absolutePath)
        val bytes = "file bytes".encodeToByteArray()
        try {
            val staged = files.stage("file.txt", "text/plain", bytes)
            val draft = DraftEntity("draft", key, "A", 1, attachmentsJson = CoreJson.encodeToString(AttachmentEdits(listOf(staged))))
            val frozen = NoteRepository(db, files).prepare(draft)
            db.close()
            db = jvmDatabase(path)
            val restored = NoteRepository(db, AttachmentFiles(folder.resolve("files").absolutePath)).restore(key)
            assertEquals(frozen.body, restored.pending?.body)
            assertEquals(frozen.idempotencyKey, restored.pending?.idempotencyKey)
            assertEquals(staged, restored.draft.attachmentEdits().staged.single())
            val request = CoreJson.parseToJsonElement(frozen.body).jsonObject
            assertEquals("ZmlsZSBieXRlcw==", request.getValue("attachments").jsonArray[0].jsonObject.getValue("content").jsonPrimitive.content)
            val item = buildJsonObject {
                put("name", "attachments/file"); put("filename", "file.txt"); put("type", "text/plain")
                put("size", bytes.size)
                put("content_url", "/api/app/v1/notes/one/attachments/file/content")
            }
            val remote = Note("one", "memos/one", "A", "NORMAL", "hash", attachments = listOf(item))
            var downloadCount = 0
            var serverBytes = bytes
            val engine = MockEngine { request ->
                if (request.url.encodedPath.endsWith("/session")) respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""")
                else {
                    assertEquals("/api/app/v1/notes/one/attachments/file/content", request.url.encodedPath)
                    downloadCount++
                    respond(serverBytes, headers = headersOf(HttpHeaders.ContentLength, serverBytes.size.toString()))
                }
            }
            CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine).use { core ->
                assertTrue(expectedSubmittedResult(draft, frozen, remote, core))
                serverBytes = "other byte".encodeToByteArray()
                assertFalse(expectedSubmittedResult(draft, frozen, remote, core))
                assertEquals(2, downloadCount)
                assertFailsWith<IllegalArgumentException> { core.attachmentBytes(remote, item, 2) }
            }
            assertFailsWith<IllegalArgumentException> { files.stage("../bad", "text/plain", bytes) }
            assertFailsWith<IllegalArgumentException> { files.stage("file.txt", "text/plain; charset=utf-8", bytes) }
            assertFailsWith<IllegalArgumentException> { files.stage("empty", "text/plain", byteArrayOf()) }
        } finally { db.close(); folder.deleteRecursively() }
    }
}

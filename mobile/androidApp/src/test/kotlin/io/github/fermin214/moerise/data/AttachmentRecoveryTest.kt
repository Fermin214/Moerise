package io.github.fermin214.moerise.data

import android.content.Context
import android.graphics.Bitmap
import androidx.test.core.app.ApplicationProvider
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

import io.github.fermin214.moerise.domain.AttachmentReceipt
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.presentation.expectedSubmittedResult
import io.github.fermin214.moerise.presentation.ProtectedImageCache
import io.github.fermin214.moerise.presentation.ProtectedImageResult
import io.github.fermin214.moerise.presentation.clearAttachmentCache
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.io.ByteArrayOutputStream
import java.nio.file.Files
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.serialization.json.*

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class AttachmentRecoveryTest {
    @Test
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    fun imageCacheSharesRequestsAndClearDropsAnOlderDownload() = runBlocking<Unit> {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val origin = "https://images.example"
        val key = "$origin\u0000instance\u0000users/owner"
        fun attachment(id: String) = buildJsonObject {
            put("name", "attachments/$id"); put("filename", "$id.png"); put("type", "image/png")
            put("content_url", "/api/app/v1/notes/one/attachments/$id/content")
        }
        val blocked = attachment("blocked")
        val ready = attachment("ready")
        val malformed = attachment("malformed")
        val large = JsonObject(attachment("large") + ("size" to JsonPrimitive(8 * 1024 * 1024 + 1)))
        val note = Note("instance", "memos/one", "image", "NORMAL", "hash", attachments = listOf(blocked, ready, malformed, large))
        val png = ByteArrayOutputStream().use { stream ->
            val bitmap = Bitmap.createBitmap(20, 20, Bitmap.Config.ARGB_8888)
            check(bitmap.compress(Bitmap.CompressFormat.PNG, 100, stream))
            bitmap.recycle()
            stream.toByteArray()
        }
        val started = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        val requests = AtomicInteger()
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            when {
                request.url.encodedPath.endsWith("/session") ->
                    respond("""{"instance_id":"instance","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2099-01-01T00:00:00Z","csrf_token":"token"}""")
                request.url.encodedPath.endsWith("/blocked/content") -> {
                    started.complete(Unit)
                    withContext(NonCancellable) { release.await() }
                    respond(png)
                }
                request.url.encodedPath.endsWith("/malformed/content") -> respond("invalid image")
                else -> { requests.incrementAndGet(); respond(png) }
            }
        }).also { it.bindAccount(key) }
        val root = context.cacheDir.resolve("image_cache")
        try {
            clearAttachmentCache(context)
            val stamp = ProtectedImageCache.epoch.value
            val pending = async { ProtectedImageCache.load(context, core, note, blocked, stamp) }
            withTimeout(10_000) { started.await() }
            clearAttachmentCache(context)
            release.complete(Unit)
            assertFailsWith<CancellationException> { withTimeout(10_000) { pending.await() } }
            assertFalse(root.exists())
            assertNull(ProtectedImageCache.peek(core, blocked, ProtectedImageCache.epoch.value))
            val active = ProtectedImageCache.epoch.value
            val first = async { ProtectedImageCache.load(context, core, note, ready, active) }
            val second = async { ProtectedImageCache.load(context, core, note, ready, active) }
            val loaded = assertIs<ProtectedImageResult.Loaded>(first.await())
            assertSame(loaded.bitmap, assertIs<ProtectedImageResult.Loaded>(second.await()).bitmap)
            assertEquals(1, requests.get())
            assertEquals(1, root.walkTopDown().count { it.isFile })
            assertEquals(ProtectedImageResult.Failed(false), ProtectedImageCache.load(context, core, note, large, active))
            assertEquals(ProtectedImageResult.Failed(false), ProtectedImageCache.load(context, core, note, malformed, active))
            assertEquals(1, requests.get())
        } finally {
            release.complete(Unit)
            clearAttachmentCache(context)
            core.close()
        }
    }

    @Test
    fun failedAttachmentConfirmationReplaysQueueAndPreservesUnfinishedInput() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-attachment-queue-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        val key = "https://notes.example\u0000one\u0000users/owner"
        val files = AttachmentFiles(folder.resolve("files").absolutePath)
        val bytes = ByteArray(2048) { (it % 251).toByte() }
        var db = testDatabase(path)
        var repo = NoteRepository(db, files)
        var scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        var failDownload = true
        val requests = mutableListOf<Pair<String?, String>>()
        val item = buildJsonObject {
            put("name", "attachments/video"); put("filename", "video.mp4")
            put("type", "video/mp4"); put("size", bytes.size)
        }
        val remote = Note("one", "memos/video", "Saved video", "NORMAL", "hash", attachments = listOf(item))
        val engine = MockEngine { request ->
            when {
                request.url.encodedPath.endsWith("/session") -> respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""")
                request.method == HttpMethod.Post -> {
                    requests += request.headers["Idempotency-Key"] to request.body.toByteArray().decodeToString()
                    respond(CoreJson.encodeToString(remote))
                }
                else -> {
                    if (failDownload) throw java.io.IOException("interrupted download")
                    respond(bytes, headers = headersOf(HttpHeaders.ContentLength, bytes.size.toString()))
                }
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            var fixture = DraftQueueFixture({ repo }, core, scope)
            var editor = fixture.composer
            editor.open(key)
            editor.change("Saved video")
            val id = requireNotNull(editor.state.value.draft).id
            assertTrue(editor.attach(key, id, listOf(files.stage("video.mp4", "video/mp4", bytes))))
            assertTrue(editor.enqueue())
            editor.change("Unfinished input"); editor.flush()
            assertFailsWith<java.io.IOException> { fixture.queue.drain(key, core) }
            assertEquals(1, repo.queuedNotes(key).size)
            scope.coroutineContext[Job]!!.cancelAndJoin()
            db.close(); db = testDatabase(path); repo = NoteRepository(db, files)
            scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
            fixture = DraftQueueFixture({ repo }, core, scope)
            editor = fixture.composer
            editor.open(key)
            failDownload = false
            assertEquals(1, fixture.queue.drain(key, core).committedCount)
            assertEquals(2, requests.size)
            assertEquals(requests[0], requests[1])
            assertTrue(repo.queuedNotes(key).isEmpty())
            assertEquals("Unfinished input", editor.state.value.draft?.content)
            assertEquals(listOf(remote.name), repo.snapshot(key)?.notes?.map { it.name })
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close(); db.close(); folder.deleteRecursively()
        }
    }

    @Test
    fun privateFilesAndFrozenBodySurviveReopenAndConfirmAttachmentBytes() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-attachments-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        var db = testDatabase(path)
        val key = "https://notes.example\u0000one\u0000users/owner"
        val files = AttachmentFiles(folder.resolve("files").absolutePath)
        val bytes = "file bytes".encodeToByteArray()
        try {
            val staged = files.stage("file.txt", "text/plain", bytes)
            val draft = DraftEntity("draft", key, "A", 1, attachmentsJson = CoreJson.encodeToString(AttachmentEdits(listOf(staged))))
            val frozen = NoteRepository(db, files).prepare(draft)
            db.close()
            db = testDatabase(path)
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

    @Test
    fun expectedSubmittedResultVerifiesReceiptsWithoutDownloadingAttachment() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-receipt-verify-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val key = "https://notes.example\u0000one\u0000users/owner"
        val files = AttachmentFiles(folder.resolve("files").absolutePath)
        val bytes = "receipt bytes".encodeToByteArray()
        var downloadCount = 0
        val engine = MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) {
                respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""")
            } else {
                downloadCount++
                respond(bytes, headers = headersOf(HttpHeaders.ContentLength, bytes.size.toString()))
            }
        }
        try {
            val staged = files.stage("doc.txt", "text/plain", bytes)
            val draft = DraftEntity("draft-r", key, "A", 1, attachmentsJson = CoreJson.encodeToString(AttachmentEdits(listOf(staged))))
            val frozen = NoteRepository(db, files).prepare(draft)
            val item = buildJsonObject {
                put("name", "attachments/r1")
                put("filename", "doc.txt")
                put("type", "text/plain")
                put("size", bytes.size)
                put("content_url", "/api/app/v1/notes/one/attachments/r1/content")
            }
            val payload = "[${JsonPrimitive(frozen.idempotencyKey)}, 0, ${JsonPrimitive("attachments/r1")}, ${JsonPrimitive(staged.sha256)}]"
            val validDigest = sha256(payload.encodeToByteArray())
            val remote = Note(
                "one", "memos/r1", "A", "NORMAL", "hash",
                attachments = listOf(item),
                attachmentReceipts = listOf(AttachmentReceipt("attachments/r1", 0, staged.sha256, validDigest)),
            )
            CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine).use { core ->
                assertTrue(expectedSubmittedResult(draft, frozen, remote, core))
                assertEquals(0, downloadCount)
                val tampered = remote.copy(
                    attachmentReceipts = listOf(AttachmentReceipt("attachments/r1", 0, staged.sha256, "bad-digest")),
                )
                assertFalse(expectedSubmittedResult(draft, frozen, tampered, core))
                assertEquals(0, downloadCount)
            }
        } finally {
            db.close()
            folder.deleteRecursively()
        }
    }
}

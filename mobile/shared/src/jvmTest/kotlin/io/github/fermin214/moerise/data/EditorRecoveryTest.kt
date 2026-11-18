package io.github.fermin214.moerise.data

import androidx.room.Room
import androidx.room.immediateTransaction
import androidx.room.useWriterConnection
import androidx.sqlite.SQLiteConnection
import androidx.sqlite.SQLiteDriver
import androidx.sqlite.SQLiteStatement
import androidx.sqlite.driver.bundled.BundledSQLiteDriver
import io.github.fermin214.moerise.domain.Note
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.io.IOException
import java.nio.file.Files
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.serialization.json.*

class EditorRecoveryTest {
    private val account = "https://notes.example\u0000one\u0000users/owner"
    private fun note(content: String, hash: String) = Note(
        "one", "memos/one", content, "NORMAL", hash, "2026-11-14T00:00:00Z",
    )
    private fun session() = """{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}"""

    @Test
    fun typingDuringConfirmationTransactionKeepsNewContentAndRemoteBinding() = runBlocking {
        val folder = Files.createTempDirectory("moerise-editor-").toFile()
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val path = folder.resolve("data.sqlite3").absolutePath
        val arrived = CompletableDeferred<Unit>()
        val respondNow = CompletableDeferred<Unit>()
        val confirming = CompletableDeferred<Unit>()
        val resumeConfirmation = CountDownLatch(1)
        val bundled = BundledSQLiteDriver()
        val driver = object : SQLiteDriver by bundled {
            override fun open(fileName: String): SQLiteConnection {
                val connection = bundled.open(fileName)
                return object : SQLiteConnection by connection {
                    override fun prepare(sql: String): SQLiteStatement {
                        if (sql.startsWith("DELETE FROM pending_writes WHERE id") && confirming.complete(Unit)) {
                            check(resumeConfirmation.await(10, TimeUnit.SECONDS))
                        }
                        return connection.prepare(sql)
                    }
                }
            }
        }
        var db = Room.databaseBuilder<LocalDatabase>(name = path).setDriver(driver)
            .setQueryCoroutineContext(Dispatchers.IO).build()
        val requests = mutableListOf<Pair<HttpMethod, String>>()
        val engine = MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(session())
            else {
                val body = request.body.toByteArray().decodeToString()
                requests.add(request.method to body)
                if (requests.size == 1) {
                    arrived.complete(Unit)
                    respondNow.await()
                    respond(CoreJson.encodeToString(note("A", "hash-a")))
                } else respond(CoreJson.encodeToString(note("ABC", "hash-abc")))
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            val fixture = DraftQueueFixture({ NoteRepository(db) }, core, scope)
            val editor = fixture.composer
            editor.open(account); editor.change("A"); assertTrue(editor.enqueue())
            val id = db.dao().queuedWrites(account).single().draftId
            editor.openStored(account, id)
            val sending = async(dispatcher) { fixture.queue.drain(account, core) }
            withTimeout(10_000) { arrived.await() }
            withContext(dispatcher) { editor.change("AB") }
            respondNow.complete(Unit)
            withTimeout(10_000) { confirming.await() }
            withContext(dispatcher) { editor.change("ABC") }
            resumeConfirmation.countDown()
            assertTrue(withTimeout(10_000) { sending.await() })
            withTimeout(10_000) {
                editor.state.first {
                    val draft = it.draft
                    draft != null && draft.content == "ABC" && draft.sourceHash == "hash-a" && draft.revision == it.savedRevision
                }
            }
            db.close()
            db = jvmDatabase(path)
            val persisted = db.dao().draft(id)!!
            assertEquals("ABC", persisted.content)
            assertEquals("memos/one", persisted.targetName)
            assertEquals("hash-a", persisted.sourceHash)
            assertNull(db.dao().pending(persisted.id))
            val restored = DraftQueueFixture({ NoteRepository(db) }, core, scope)
            restored.composer.openStored(account, id)
            assertTrue(restored.composer.enqueue())
            assertTrue(restored.queue.drain(account, core))
            assertEquals(HttpMethod.Patch, requests[1].first)
            val patch = CoreJson.parseToJsonElement(requests[1].second).jsonObject
            assertEquals("ABC", patch.getValue("content").jsonPrimitive.content)
            assertEquals("hash-a", patch.getValue("source_hash").jsonPrimitive.content)
            assertEquals("", restored.composer.state.value.draft?.content)
        } finally {
            resumeConfirmation.countDown()
            scope.cancel()
            core.close()
            dispatcher.close()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun lostCreateAndPatchResponsesReopenWithSameBodyAndDetectExternalEdit() = runBlocking {
        for (editing in listOf(false, true)) {
            val folder = Files.createTempDirectory("moerise-replay-").toFile()
            val path = folder.resolve("data.sqlite3").absolutePath
            val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
            val scope = CoroutineScope(SupervisorJob() + dispatcher)
            var db = jvmDatabase(path)
            val arrived = CompletableDeferred<Unit>()
            val loseResponse = CompletableDeferred<Unit>()
            val requests = mutableListOf<Triple<HttpMethod, String, String?>>()
            val engine = MockEngine { request ->
                if (request.url.encodedPath.endsWith("/session")) respond(session())
                else {
                    requests.add(Triple(request.method, request.body.toByteArray().decodeToString(), request.headers["Idempotency-Key"]))
                    if (requests.size == 1) {
                        arrived.complete(Unit)
                        loseResponse.await()
                        throw IOException("Response lost after remote commit")
                    }
                    respond(CoreJson.encodeToString(note("AC", "hash-ac")))
                }
            }
            val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
            try {
                if (editing) db.dao().putDraft(DraftEntity("edit", account, "", 0, "memos/one", "hash-original",
                    baselineJson = CoreJson.encodeToString(note("", "hash-original"))))
                val initial = DraftQueueFixture({ NoteRepository(db) }, core, scope)
                val first = initial.composer
                first.open(account); first.change("A"); assertTrue(first.enqueue())
                val id = db.dao().queuedWrites(account).single().draftId
                first.openStored(account, id)
                val sending = async(dispatcher) { assertFailsWith<IOException> { initial.queue.drain(account, core) } }
                withTimeout(10_000) { arrived.await() }
                withContext(dispatcher) { first.change("AB"); first.flush() }
                loseResponse.complete(Unit)
                withTimeout(10_000) { sending.await() }
                val frozen = db.dao().pending(first.state.value.draft!!.id)!!
                assertEquals("unknown", frozen.status)
                db.close()
                db = jvmDatabase(path)
                val restored = DraftQueueFixture({ NoteRepository(db) }, core, scope)
                val reopened = restored.composer
                reopened.openStored(account, id)
                assertTrue(restored.queue.drain(account, core))
                assertEquals(requests[0], requests[1])
                val result = db.dao().draft(id)!!
                assertEquals("AB", result.content)
                assertNotNull(result.conflictJson)
                assertNotEquals("hash-ac", result.sourceHash)
                assertNull(db.dao().pending(result.id))
                withContext(dispatcher) { reopened.merge("ABC", note("AC", "hash-ac")); reopened.flush() }
                val merged = db.dao().draft(id)!!
                assertNull(merged.conflictJson)
                assertEquals("hash-ac", merged.sourceHash)
                assertEquals("ABC", merged.content)
            } finally {
                loseResponse.complete(Unit)
                scope.cancel()
                core.close()
                dispatcher.close()
                db.close()
                folder.deleteRecursively()
            }
        }
    }

    @Test
    fun failedConfirmationKeepsDraftAndPreventsAnotherNetworkWrite() = runBlocking {
        val folder = Files.createTempDirectory("moerise-confirm-failure-").toFile()
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val db = jvmDatabase(folder.resolve("data.sqlite3").absolutePath)
        val arrived = CompletableDeferred<Unit>()
        val respondNow = CompletableDeferred<Unit>()
        var writes = 0
        val engine = MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(session())
            else {
                writes++
                arrived.complete(Unit)
                respondNow.await()
                respond(CoreJson.encodeToString(note("A", "hash-a")))
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            val fixture = DraftQueueFixture({ NoteRepository(db) }, core, scope)
            val editor = fixture.composer
            editor.open(account); editor.change("A"); assertTrue(editor.enqueue())
            val id = db.dao().queuedWrites(account).single().draftId
            editor.openStored(account, id)
            val sending = async(dispatcher) { assertFails { fixture.queue.drain(account, core) } }
            withTimeout(10_000) { arrived.await() }
            withContext(dispatcher) { editor.change("AB"); editor.flush() }
            db.useWriterConnection { it.immediateTransaction {
                it.usePrepared("CREATE TRIGGER fail_confirmation BEFORE DELETE ON pending_writes BEGIN SELECT RAISE(FAIL, 'write failed'); END") { statement -> statement.step() }
            } }
            respondNow.complete(Unit)
            withTimeout(10_000) { sending.await() }
            assertNotNull(db.dao().pending(id))
            assertNotNull(editor.state.value.pending)
            assertFails { fixture.queue.drain(account, core) }
            assertEquals(1, writes)
            assertEquals("AB", db.dao().draft(id)?.content)
            db.useWriterConnection { it.immediateTransaction { it.usePrepared("DROP TRIGGER fail_confirmation") { statement -> statement.step() } } }
            assertTrue(fixture.queue.drain(account, core))
            assertEquals(1, writes)
            assertNull(editor.state.value.pending)
            val saved = db.dao().draft(id)!!
            assertEquals("AB", saved.content)
            assertEquals("hash-a", saved.sourceHash)
            assertNull(db.dao().pending(saved.id))
        } finally {
            respondNow.complete(Unit)
            scope.cancel()
            core.close()
            dispatcher.close()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun cancellationFinishesWriteJournalBeforeQueueCanRetry() = runBlocking {
        val folder = Files.createTempDirectory("moerise-retired-write-").toFile()
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val db = jvmDatabase(folder.resolve("data.sqlite3").absolutePath)
        val arrived = CompletableDeferred<Unit>()
        val writerHeld = CompletableDeferred<Unit>()
        val releaseWriter = CompletableDeferred<Unit>()
        val writes = mutableListOf<Pair<String?, String>>()
        val engine = MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(session())
            else {
                writes += request.headers["Idempotency-Key"] to request.body.toByteArray().decodeToString()
                if (writes.size == 1) { arrived.complete(Unit); awaitCancellation() }
                respond(CoreJson.encodeToString(note("A", "hash-a")))
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            val fixture = DraftQueueFixture({ NoteRepository(db) }, core, scope)
            val editor = fixture.composer
            editor.open(account); editor.change("A"); assertTrue(editor.enqueue())
            val id = db.dao().queuedWrites(account).single().draftId
            editor.openStored(account, id)
            val sending = async(dispatcher) { fixture.queue.drain(account, core) }
            withTimeout(10_000) { arrived.await() }
            val blocker = launch(Dispatchers.IO) {
                db.useWriterConnection { it.immediateTransaction { writerHeld.complete(Unit); releaseWriter.await() } }
            }
            withTimeout(10_000) { writerHeld.await() }
            sending.cancel()
            val retry = async(dispatcher) { fixture.queue.drain(account, core) }
            delay(30)
            assertFalse(retry.isCompleted)
            releaseWriter.complete(Unit)
            blocker.join()
            assertTrue(withTimeout(10_000) { retry.await() })
            assertEquals(writes[0], writes[1])
            assertNull(db.dao().pending(id))
            assertEquals("memos/one", editor.state.value.draft?.targetName)
            assertEquals("", db.dao().homeDraft(account)?.content)
        } finally {
            releaseWriter.complete(Unit)
            scope.cancel(); core.close(); dispatcher.close(); db.close(); folder.deleteRecursively()
        }
    }
}

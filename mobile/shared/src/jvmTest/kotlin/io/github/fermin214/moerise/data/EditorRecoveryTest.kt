package io.github.fermin214.moerise.data

import androidx.room.immediateTransaction
import androidx.room.useWriterConnection
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.presentation.EditorController
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.io.IOException
import java.nio.file.Files
import java.util.concurrent.Executors
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
        val db = jvmDatabase(folder.resolve("data.sqlite3").absolutePath)
        val arrived = CompletableDeferred<Unit>()
        val respondNow = CompletableDeferred<Unit>()
        val writerHeld = CompletableDeferred<Unit>()
        val releaseWriter = CompletableDeferred<Unit>()
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
            val editor = EditorController({ NoteRepository(db) }, { core }, scope)
            withContext(dispatcher) { editor.open(account); editor.change("A"); editor.save() }
            withTimeout(10_000) { arrived.await() }
            withContext(dispatcher) { editor.change("AB") }
            val blocker = launch(Dispatchers.IO) {
                db.useWriterConnection { it.immediateTransaction {
                    writerHeld.complete(Unit)
                    releaseWriter.await()
                } }
            }
            withTimeout(10_000) { writerHeld.await() }
            respondNow.complete(Unit)
            withTimeout(10_000) { editor.state.first { it.confirmation != null } }
            withContext(dispatcher) { editor.change("ABC") }
            releaseWriter.complete(Unit)
            blocker.join()
            withTimeout(10_000) { editor.state.first { !it.busy } }
            withContext(dispatcher) { editor.flush() }
            val persisted = db.dao().homeDraft(account)!!
            assertEquals("ABC", persisted.content)
            assertEquals("memos/one", persisted.targetName)
            assertEquals("hash-a", persisted.sourceHash)
            assertNull(db.dao().pending(persisted.id))
            withContext(dispatcher) { editor.save() }
            withTimeout(10_000) { editor.state.first { !it.busy } }
            assertEquals(HttpMethod.Patch, requests[1].first)
            val patch = CoreJson.parseToJsonElement(requests[1].second).jsonObject
            assertEquals("ABC", patch.getValue("content").jsonPrimitive.content)
            assertEquals("hash-a", patch.getValue("source_hash").jsonPrimitive.content)
            assertEquals("", editor.state.value.draft?.content)
        } finally {
            releaseWriter.complete(Unit)
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
                val first = EditorController({ NoteRepository(db) }, { core }, scope)
                withContext(dispatcher) { first.open(account); first.change("A"); first.save() }
                withTimeout(10_000) { arrived.await() }
                withContext(dispatcher) { first.change("AB"); first.flush() }
                loseResponse.complete(Unit)
                withTimeout(10_000) { first.state.first { !it.busy } }
                val frozen = db.dao().pending(first.state.value.draft!!.id)!!
                assertEquals("unknown", frozen.status)
                db.close()
                db = jvmDatabase(path)
                val reopened = EditorController({ NoteRepository(db) }, { core }, scope)
                withContext(dispatcher) { reopened.open(account); reopened.save() }
                withTimeout(10_000) { reopened.state.first { !it.busy } }
                assertEquals(requests[0], requests[1])
                val result = db.dao().homeDraft(account)!!
                assertEquals("AB", result.content)
                assertNotNull(result.conflictJson)
                assertNotEquals("hash-ac", result.sourceHash)
                assertNull(db.dao().pending(result.id))
                withContext(dispatcher) { reopened.merge("ABC", note("AC", "hash-ac")); reopened.flush() }
                val merged = db.dao().homeDraft(account)!!
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
            val editor = EditorController({ NoteRepository(db) }, { core }, scope)
            withContext(dispatcher) { editor.open(account); editor.change("A"); editor.save() }
            withTimeout(10_000) { arrived.await() }
            withContext(dispatcher) { editor.change("AB"); editor.flush() }
            db.useWriterConnection { it.immediateTransaction {
                it.usePrepared("CREATE TRIGGER fail_confirmation BEFORE DELETE ON pending_writes BEGIN SELECT RAISE(FAIL, 'write failed'); END") { statement -> statement.step() }
            } }
            respondNow.complete(Unit)
            withTimeout(10_000) { editor.state.first { !it.busy } }
            assertEquals("LOCAL_WRITE_FAILED", editor.state.value.error)
            assertNotNull(editor.state.value.confirmation)
            assertFalse(editor.state.value.serverSaved)
            withContext(dispatcher) { editor.save() }
            withTimeout(10_000) { editor.state.first { !it.busy } }
            assertEquals(1, writes)
            assertEquals("AB", db.dao().homeDraft(account)?.content)
            db.useWriterConnection { it.immediateTransaction { it.usePrepared("DROP TRIGGER fail_confirmation") { statement -> statement.step() } } }
            withContext(dispatcher) { editor.flush() }
            assertNull(editor.state.value.confirmation)
            assertTrue(editor.state.value.serverSaved)
            val saved = db.dao().homeDraft(account)!!
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
    fun reopeningWaitsForCanceledWriteJournalBeforeAllowingRetry() = runBlocking {
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
            val editor = EditorController({ NoteRepository(db) }, { core }, scope)
            withContext(dispatcher) { editor.open(account); editor.change("A"); editor.save() }
            withTimeout(10_000) { arrived.await() }
            val blocker = launch(Dispatchers.IO) {
                db.useWriterConnection { it.immediateTransaction { writerHeld.complete(Unit); releaseWriter.await() } }
            }
            withTimeout(10_000) { writerHeld.await() }
            val reopening = scope.async { editor.open(account) }
            delay(30)
            assertFalse(reopening.isCompleted)
            releaseWriter.complete(Unit)
            blocker.join()
            withTimeout(10_000) { reopening.await() }
            assertEquals("unknown", editor.state.value.pending?.status)
            withContext(dispatcher) { editor.save() }
            withTimeout(10_000) { editor.state.first { !it.busy } }
            assertEquals(writes[0], writes[1])
            assertNull(db.dao().pending(editor.state.value.draft!!.id))
            assertEquals("", db.dao().homeDraft(account)?.content)
        } finally {
            releaseWriter.complete(Unit)
            scope.cancel(); core.close(); dispatcher.close(); db.close(); folder.deleteRecursively()
        }
    }
}

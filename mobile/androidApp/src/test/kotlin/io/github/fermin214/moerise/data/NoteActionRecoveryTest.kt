package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.presentation.NoteDetailController
import io.github.fermin214.moerise.presentation.NoteStateActions
import io.github.fermin214.moerise.presentation.SearchHit
import io.github.fermin214.moerise.presentation.SearchQuery
import io.github.fermin214.moerise.presentation.applyStateActions
import io.github.fermin214.moerise.domain.Note
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.io.IOException
import java.nio.file.Files
import java.util.concurrent.Executors
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.serialization.json.*
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class NoteActionRecoveryTest {
    private val identity = """{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2099-01-01T00:00:00Z","csrf_token":"token"}"""

    @Test
    fun unknownStateChangeReplaysSameBodyAndDeletionIsCheckedAfterReopen() = runBlocking {
        val folder = Files.createTempDirectory("moerise-actions-").toFile()
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val path = folder.resolve("data.sqlite3").absolutePath
        var db = testDatabase(path)
        var repo = NoteRepository(db)
        val key = "https://notes.example\u0000one\u0000users/owner"
        var state = "NORMAL"
        var deleted = false
        var deleteCount = 0
        val patches = mutableListOf<Pair<String?, String>>()
        val engine = MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""")
            else when (request.method) {
                HttpMethod.Patch -> {
                    patches += request.headers["Idempotency-Key"] to request.body.toByteArray().decodeToString()
                    state = CoreJson.parseToJsonElement(patches.last().second).jsonObject.getValue("state").jsonPrimitive.content
                    if (patches.size in setOf(1, 3)) throw IOException("Response lost")
                    respond("""{"instance_id":"one","name":"memos/one","content":"A","state":"$state","source_hash":"hash","create_time":"2026-11-14T00:00:00Z"}""")
                }
                HttpMethod.Delete -> { deleteCount++; deleted = true; throw IOException("Delete response lost") }
                else -> if (deleted) respond("""{"error":{"code":"SOURCE_NOT_FOUND"}}""", HttpStatusCode.NotFound)
                    else respond("""{"instance_id":"one","name":"memos/one","content":"A","state":"$state","source_hash":"hash","create_time":"2026-11-14T00:00:00Z"}""")
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            fun actions() = NoteStateActions({ repo }, { key }, { core }, scope, { _, _, _ -> }, { _, _ -> }, {})
            val note = Note("one", "memos/one", "A", "NORMAL", "hash", createTime = "2026-11-14T00:00:00Z")
            repo.recordLocalNote(key, note)
            var changes = actions()
            for ((index, target) in listOf("ARCHIVED", "NORMAL").withIndex()) {
                withContext(dispatcher) { changes.change(requireNotNull(repo.localNote(key, note.name))) }
                withTimeout(10_000) { changes.state.first { it[note.name]?.unknown == true } }
                val pending = assertNotNull(repo.action(key, note.name))
                assertEquals("unknown", pending.status)
                assertEquals(patches[index * 2], pending.idempotencyKey to pending.body)
                assertNotEquals(target, repo.localNote(key, note.name)?.state)
                withContext(dispatcher) { changes.stop() }
                db.close(); db = testDatabase(path); repo = NoteRepository(db)
                changes = actions()
                withContext(dispatcher) { changes.recover(key) }
                withTimeout(10_000) { changes.state.first { it.isEmpty() } }
                assertEquals(target, repo.localNote(key, note.name)?.state)
                assertEquals(patches[index * 2], patches[index * 2 + 1])
                assertNull(repo.action(key, note.name))
            }
            fun controller() = NoteDetailController({ repo }, { key }, { core }, scope) { _, _, _ -> }
            val reopened = controller()
            withContext(dispatcher) { reopened.open(note.name) }
            withTimeout(10_000) { reopened.state.first { !it.loading } }
            withContext(dispatcher) { reopened.delete() }
            withTimeout(10_000) { reopened.state.first { !it.busy } }
            assertTrue(reopened.state.value.deleteUnknown)
            db.close(); db = testDatabase(path); repo = NoteRepository(db)
            val afterDelete = controller()
            withContext(dispatcher) { afterDelete.open("memos/one") }
            withTimeout(10_000) { afterDelete.state.first { !it.loading } }
            assertNull(afterDelete.state.value.name)
            assertEquals(1, deleteCount)
            assertNull(NoteRepository(db).action(key, "memos/one"))
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close(); dispatcher.close(); db.close(); folder.deleteRecursively()
        }
    }

    @Test fun earlyUndoSurvivesRestartBeforeArchiveResponse() = earlyUndo(disconnectAfterReceipt = false)

    @Test fun earlyUndoSurvivesRestartAfterConversionBeforeRestoreRequest() = earlyUndo(disconnectAfterReceipt = true)

    @Test
    fun undoAfterConfirmedArchiveRollsBackWhenItCannotSendAndDoesNotRetryOnReconnect() = runBlocking<Unit> {
        val directory = Files.createTempDirectory("moerise-offline-undo").toFile()
        val db = testDatabase(directory.resolve("notes.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val origin = "https://notes.example"
        val key = "$origin\u0000one\u0000users/owner"
        val note = Note("one", "memos/one", "original", "NORMAL", "original")
        val archived = note.copy(state = "ARCHIVED", sourceHash = "archived")
        val changed = CompletableDeferred<Note>()
        val undo = CompletableDeferred<() -> Unit>()
        val failed = CompletableDeferred<String>()
        val refresh = CompletableDeferred<Unit>()
        var online = true
        var patches = 0
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            if (request.method == HttpMethod.Get) respond(identity) else {
                patches++
                assertTrue("\"ARCHIVED\"" in request.body.toByteArray().decodeToString())
                respond(CoreJson.encodeToString(archived))
            }
        })
        try {
            repo.recordLocalNote(key, note)
            val actions = NoteStateActions({ repo }, { key }, { core.takeIf { online } }, scope,
                { _, _, result -> if (result != null) changed.complete(result) },
                { code, callback -> if (callback != null) undo.complete(callback) else failed.complete(code) },
                { refresh.complete(Unit) })
            actions.change(note)
            assertEquals(archived, withTimeout(10_000) { changed.await() })
            withTimeout(10_000) { actions.state.first { it.isEmpty() } }
            online = false
            withTimeout(10_000) { undo.await().invoke() }
            assertEquals("CONNECTION_FAILED", withTimeout(10_000) { failed.await() })
            withTimeout(10_000) { refresh.await() }
            assertTrue(actions.state.value.isEmpty())
            assertNull(repo.action(key, note.name))
            assertEquals(archived, repo.localNote(key, note.name))

            online = true
            actions.recover(key)
            assertEquals(1, patches)
            assertTrue(repo.stateActions(key).isEmpty())
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
            db.close()
            directory.deleteRecursively()
        }
    }

    @Test
    fun replayWithLatestOppositeStateSettlesArchiveAndRestore() = runBlocking<Unit> {
        val directory = Files.createTempDirectory("moerise-opposite-replay").toFile()
        val db = testDatabase(directory.resolve("notes.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val origin = "https://notes.example"
        val key = "$origin\u0000one\u0000users/owner"
        val normal = Note("one", "memos/one", "original", "NORMAL", "original")
        val archived = normal.copy(state = "ARCHIVED", sourceHash = "archived")
        val attempts = mutableListOf<Pair<String?, String>>()
        val receipt = CompletableDeferred<Note>()
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            if (request.method == HttpMethod.Get) respond(identity) else {
                attempts += request.headers["Idempotency-Key"] to request.body.toByteArray().decodeToString()
                when (attempts.size) {
                    1, 3 -> throw IOException("Response lost")
                    2 -> respond(CoreJson.encodeToString(normal.copy(sourceHash = "web-restored")))
                    else -> respond(CoreJson.encodeToString(archived.copy(sourceHash = "web-archived")))
                }
            }
        })
        try {
            val undo = CompletableDeferred<() -> Unit>()
            val actions = NoteStateActions({ repo }, { key }, { core }, scope,
                { _, _, note -> if (note != null) receipt.complete(note) },
                { _, callback -> if (callback != null) undo.complete(callback) }, {})
            for ((index, note) in listOf(normal, archived).withIndex()) {
                repo.recordLocalNote(key, note)
                actions.change(note)
                withTimeout(10_000) { actions.state.first { it[note.name]?.unknown == true } }
                if (index == 0) withTimeout(10_000) { undo.await().invoke() } else actions.change(note)
                withTimeout(10_000) { actions.state.first { it.isEmpty() } }
                val latest = assertNotNull(repo.localNote(key, note.name))
                assertEquals(note.state, latest.state)
                assertEquals(if (index == 0) "web-restored" else "web-archived", latest.sourceHash)
                assertEquals(attempts[index * 2], attempts[index * 2 + 1])
                assertEquals((index + 1) * 2, attempts.size)
                assertNull(repo.action(key, note.name))
            }
            assertEquals("web-restored", receipt.await().sourceHash)
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
            db.close()
            directory.deleteRecursively()
        }
    }

    @Test fun goingOfflineWhileMarkingSendingRollsBackTheNewAction() = checkUnsent(offlineBeforeSend = true)

    @Test fun connectionFailureBeforeSendingRollsBackTheNewAction() = checkUnsent(offlineBeforeSend = false)

    private fun checkUnsent(offlineBeforeSend: Boolean) = runBlocking<Unit> {
        val directory = Files.createTempDirectory("moerise-unsent-action").toFile()
        val db = testDatabase(directory.resolve("notes.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val server = MockWebServer()
        server.enqueue(MockResponse().setBody(identity))
        server.start()
        val origin = server.url("/").toString().removeSuffix("/")
        val key = "$origin\u0000one\u0000users/owner"
        val note = Note("one", "memos/one", "original", "NORMAL", "original")
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()))
        try {
            core.session()
            if (!offlineBeforeSend) server.shutdown()
            repo.recordLocalNote(key, note)
            val failure = CompletableDeferred<String>()
            var checks = 0
            val actions = NoteStateActions({ repo }, { key },
                { core.takeIf { !offlineBeforeSend || ++checks <= 2 } }, scope,
                { _, _, _ -> error("Unexpected state change") },
                { code, callback -> if (callback == null) failure.complete(code) }, {})
            actions.change(note)
            assertEquals("CONNECTION_FAILED", withTimeout(10_000) { failure.await() })
            assertEquals(1, server.requestCount)
            assertEquals(note, repo.localNote(key, note.name))
            assertNull(repo.action(key, note.name))
            assertTrue(actions.state.value.isEmpty())
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
            if (offlineBeforeSend) server.shutdown()
            db.close()
            directory.deleteRecursively()
        }
    }

    @Test
    fun earlyUndoKeepsTheOriginalRankWhenItsRestoreReplyIsLost() = runBlocking<Unit> {
        val directory = Files.createTempDirectory("moerise-search-undo").toFile()
        val db = testDatabase(directory.resolve("notes.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val origin = "https://notes.example"
        val key = "$origin\u0000one\u0000users/owner"
        val note = Note("one", "memos/one", "meaning", "NORMAL", "original")
        val before = note.copy(name = "memos/before")
        val after = note.copy(name = "memos/after")
        val archived = note.copy(state = "ARCHIVED", sourceHash = "archived")
        val releaseArchive = CompletableDeferred<Unit>()
        val undo = CompletableDeferred<() -> Unit>()
        val restoreStarted = CompletableDeferred<Unit>()
        val releaseRestore = CompletableDeferred<Unit>()
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            when {
                request.method == HttpMethod.Get -> respond(identity)
                "\"ARCHIVED\"" in request.body.toByteArray().decodeToString() -> {
                    releaseArchive.await()
                    respond(CoreJson.encodeToString(archived))
                }
                else -> {
                    restoreStarted.complete(Unit)
                    releaseRestore.await()
                    throw IOException("Response lost")
                }
            }
        })
        try {
            repo.recordLocalNote(key, note)
            val query = SearchQuery(text = "concept", mode = "hybrid")
            val remaining = listOf(before, after)
            val actions = NoteStateActions({ repo }, { key }, { core }, scope, { _, _, _ -> },
                { _, callback -> if (callback != null) undo.complete(callback) }, {})
            actions.change(note, SearchHit(query, 1))
            withTimeout(10_000) { undo.await().invoke() }
            withTimeout(10_000) { actions.state.first { it[note.name]?.action?.intent?.undo == true } }
            releaseArchive.complete(Unit)
            withTimeout(10_000) { restoreStarted.await() }
            val expected = listOf(before, archived.copy(state = "NORMAL"), after)
            assertEquals(expected, applyStateActions(remaining, actions.state.value, "NORMAL", query, "UTC"))
            releaseRestore.complete(Unit)
            val pending = withTimeout(10_000) { actions.state.first { it[note.name]?.unknown == true } }
            assertEquals(expected, applyStateActions(remaining, pending, "NORMAL", query, "UTC"))
            assertEquals(remaining, applyStateActions(remaining, pending, "NORMAL", query.copy(text = "different"), "UTC"))
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
            db.close()
            directory.deleteRecursively()
        }
    }

    private fun earlyUndo(disconnectAfterReceipt: Boolean) = runBlocking<Unit> {
        val directory = Files.createTempDirectory("moerise-early-undo").toFile()
        val path = directory.resolve("notes.sqlite3").absolutePath
        var db = testDatabase(path)
        var repo = NoteRepository(db)
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val origin = "https://notes.example"
        val key = "$origin\u0000one\u0000users/owner"
        val note = Note("one", "memos/one", "draft", "NORMAL", "original", createTime = "2026-11-14T00:00:00Z")
        val archived = note.copy(state = "ARCHIVED", sourceHash = "archive-receipt")
        val restored = note.copy(sourceHash = "restore-receipt")
        val archiveStarted = CompletableDeferred<Unit>()
        val releaseArchive = CompletableDeferred<Unit>()
        val undo = CompletableDeferred<() -> Unit>()
        val archives = mutableListOf<Pair<String?, String>>()
        val restores = mutableListOf<Pair<String?, String>>()
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) {
                respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2099-01-01T00:00:00Z","csrf_token":"token"}""")
            } else {
                val body = request.body.toByteArray().decodeToString()
                val attempt = request.headers["Idempotency-Key"] to body
                if (CoreJson.parseToJsonElement(body).jsonObject.getValue("state").jsonPrimitive.content == "ARCHIVED") {
                    archives += attempt
                    archiveStarted.complete(Unit)
                    releaseArchive.await()
                    respond(CoreJson.encodeToString(archived))
                } else {
                    restores += attempt
                    respond(CoreJson.encodeToString(restored))
                }
            }
        })
        try {
            repo.recordLocalNote(key, note)
            var online = true
            val actions = NoteStateActions({ repo }, { key }, { core.takeIf { online } }, scope,
                { _, _, result -> if (disconnectAfterReceipt && result?.state == "ARCHIVED") online = false },
                { _, action -> if (action != null) undo.complete(action) }, {})
            withContext(dispatcher) { actions.change(note) }
            withTimeout(10_000) { archiveStarted.await() }
            val archiveAttempt = assertNotNull(repo.action(key, note.name))
            assertEquals("ARCHIVED", actions.state.value.getValue(note.name).action.visibleState)
            withContext(dispatcher) { undo.await().invoke() }
            withTimeout(10_000) { actions.state.first { it[note.name]?.action?.visibleState == "NORMAL" } }
            assertTrue(repo.stateActions(key).single().intent?.undo == true)
            if (disconnectAfterReceipt) {
                releaseArchive.complete(Unit)
                withTimeout(10_000) { actions.state.first { it[note.name]?.action?.target == "NORMAL" && it[note.name]?.unknown == true } }
                val pending = assertNotNull(repo.action(key, note.name))
                assertEquals("prepared", pending.status)
                assertEquals(archived, repo.localNote(key, note.name))
                assertEquals("""{"source_hash":"archive-receipt","state":"NORMAL"}""", pending.body)
                assertNotEquals(archiveAttempt.idempotencyKey, pending.idempotencyKey)
                assertTrue(restores.isEmpty())
            }
            withContext(dispatcher) { actions.stop() }
            db.close(); db = testDatabase(path); repo = NoteRepository(db)
            releaseArchive.complete(Unit)
            val resumed = NoteStateActions({ repo }, { key }, { core }, scope, { _, _, _ -> }, { _, _ -> }, {})
            withContext(dispatcher) { resumed.recover(key) }
            withTimeout(10_000) { resumed.state.first { it.isEmpty() } }
            assertEquals(restored, repo.localNote(key, note.name))
            assertNull(repo.action(key, note.name))
            assertEquals(1, restores.size)
            assertEquals("""{"source_hash":"archive-receipt","state":"NORMAL"}""", restores.single().second)
            assertEquals(if (disconnectAfterReceipt) 1 else 2, archives.size)
            assertTrue(archives.all { it == (archiveAttempt.idempotencyKey to archiveAttempt.body) })
        } finally {
            releaseArchive.complete(Unit)
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close(); dispatcher.close(); db.close(); directory.deleteRecursively()
        }
    }
}

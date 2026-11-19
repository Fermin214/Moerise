package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.presentation.NoteDetailController
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.io.IOException
import java.nio.file.Files
import java.util.concurrent.Executors
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.serialization.json.*

class NoteActionRecoveryTest {
    @Test
    fun unknownStateChangeReplaysSameBodyAndDeletionIsCheckedAfterReopen() = runBlocking {
        val folder = Files.createTempDirectory("moerise-actions-").toFile()
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val path = folder.resolve("data.sqlite3").absolutePath
        var db = jvmDatabase(path)
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
                    state = "ARCHIVED"
                    if (patches.size == 1) throw IOException("Response lost")
                    respond("""{"instance_id":"one","name":"memos/one","content":"A","state":"$state","source_hash":"hash","create_time":"2026-11-14T00:00:00Z"}""")
                }
                HttpMethod.Delete -> { deleteCount++; deleted = true; throw IOException("Delete response lost") }
                else -> if (deleted) respond("""{"error":{"code":"SOURCE_NOT_FOUND"}}""", HttpStatusCode.NotFound)
                    else respond("""{"instance_id":"one","name":"memos/one","content":"A","state":"$state","source_hash":"hash","create_time":"2026-11-14T00:00:00Z"}""")
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            fun controller() = NoteDetailController({ NoteRepository(db) }, { key }, { core }, scope) { _, _, _ -> }
            val first = controller()
            withContext(dispatcher) { first.open("memos/one") }
            withTimeout(10_000) { first.state.first { !it.loading } }
            withContext(dispatcher) { first.changeState() }
            withTimeout(10_000) { first.state.first { !it.busy } }
            assertTrue(first.state.value.pendingAction)
            db.close(); db = jvmDatabase(path)
            val reopened = controller()
            withContext(dispatcher) { reopened.open("memos/one") }
            withTimeout(10_000) { reopened.state.first { !it.loading } }
            assertTrue(reopened.state.value.pendingAction)
            withContext(dispatcher) { reopened.changeState() }
            withTimeout(10_000) { reopened.state.first { !it.busy } }
            assertEquals(patches[0], patches[1])
            assertNull(NoteRepository(db).action(key, "memos/one"))
            withContext(dispatcher) { reopened.delete() }
            withTimeout(10_000) { reopened.state.first { !it.busy } }
            assertTrue(reopened.state.value.deleteUnknown)
            db.close(); db = jvmDatabase(path)
            val afterDelete = controller()
            withContext(dispatcher) { afterDelete.open("memos/one") }
            withTimeout(10_000) { afterDelete.state.first { !it.loading } }
            assertNull(afterDelete.state.value.name)
            assertEquals(1, deleteCount)
            assertNull(NoteRepository(db).action(key, "memos/one"))
        } finally {
            scope.cancel(); core.close(); dispatcher.close(); db.close(); folder.deleteRecursively()
        }
    }
}

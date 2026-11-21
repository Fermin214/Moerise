package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.domain.*
import io.github.fermin214.moerise.presentation.TimelineController
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.nio.file.Files
import java.util.concurrent.Executors
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class TimelineRecoveryTest {
    @Test
    fun passiveRefreshKeepsCardsStableAndCancellationClearsUserRefresh() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-timeline-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val account = "https://notes.example\u0000one\u0000users/owner"
        val note = Note("one", "memos/a", "A", "NORMAL", "hash")
        var release: CompletableDeferred<Unit>? = null
        val engine = MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""")
            else {
                release?.await()
                respond(CoreJson.encodeToString(NotePage(listOf(note))))
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            val timeline = TimelineController({ repo }, { core }, scope)
            withContext(dispatcher) { timeline.reset(account); timeline.refresh() }
            val first = withTimeout(10_000) { timeline.state.first { it.fresh && !it.loading } }
            withContext(dispatcher) {
                release = CompletableDeferred()
                timeline.refresh()
                assertTrue(timeline.state.value.loading)
                assertFalse(timeline.state.value.refreshing)
            }
            release!!.complete(Unit)
            val same = withTimeout(10_000) { timeline.state.first { !it.loading } }
            assertSame(first.notes, same.notes)
            assertEquals(first.snapshotAt, same.snapshotAt)
            withContext(dispatcher) {
                release = CompletableDeferred()
                timeline.refresh(userInitiated = true)
                assertTrue(timeline.state.value.refreshing)
                timeline.reset(null)
            }
            assertFalse(timeline.state.value.refreshing)
            assertTrue(timeline.state.value.notes.isEmpty())
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close(); db.close(); dispatcher.close(); folder.deleteRecursively()
        }
    }
}

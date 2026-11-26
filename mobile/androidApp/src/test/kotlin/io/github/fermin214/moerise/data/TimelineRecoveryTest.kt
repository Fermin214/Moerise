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
    fun projectionCommittedDuringReadIsDeliveredAndHiddenAccountCannotReload() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-timeline-projection-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val account = "https://notes.example\u0000one\u0000users/owner"
        val first = Note("one", "memos/a", "A", "NORMAL", "hash-a")
        val latest = first.copy(content = "B", sourceHash = "hash-b")
        var canRead = true
        var reads = 0
        val timeline = TimelineController({ repo }, { null }, scope, visible = { canRead }, localPage = { repository, owner, state, size ->
            val page = repository.localNotesPage(owner, state, size, 0)
            if (++reads == 1) repository.recordLocalNote(owner, latest)
            page
        })
        try {
            db.dao().putSyncState(SyncStateEntity(account, "cursor", "generation", true, 1))
            repo.recordLocalNote(account, first)
            timeline.reset(account)
            timeline.refreshProjection()
            assertEquals(listOf(latest), timeline.state.value.notes)
            assertEquals(2, reads)
            timeline.refreshProjection()
            assertEquals(2, reads)

            canRead = false
            timeline.hideAccountData()
            timeline.refreshProjection()
            timeline.readCache()
            timeline.more()
            assertTrue(timeline.state.value.notes.isEmpty())
            assertEquals(2, reads)
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            db.close()
            folder.deleteRecursively()
        }
    }

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

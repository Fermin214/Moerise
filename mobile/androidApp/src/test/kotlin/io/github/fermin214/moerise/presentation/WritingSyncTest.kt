package io.github.fermin214.moerise.presentation

import androidx.room.immediateTransaction
import androidx.room.useWriterConnection
import androidx.compose.material3.SnackbarHostState
import io.github.fermin214.moerise.NetworkAvailability
import io.github.fermin214.moerise.data.AttachmentEdits
import io.github.fermin214.moerise.data.ACTIVE_NOTE_EDITOR
import io.github.fermin214.moerise.data.ConnectionProfile
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.DraftEntity
import io.github.fermin214.moerise.data.MemorySecrets
import io.github.fermin214.moerise.data.NoteRepository
import io.github.fermin214.moerise.data.SessionCookies
import io.github.fermin214.moerise.data.SyncNotesOutcome
import io.github.fermin214.moerise.data.SyncStateEntity
import io.github.fermin214.moerise.data.attachmentEdits
import io.github.fermin214.moerise.data.testDatabase
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.NotePage
import io.github.fermin214.moerise.domain.SyncNotesResponse
import io.ktor.client.engine.mock.MockEngine
import io.ktor.client.engine.mock.respond
import io.ktor.http.HttpMethod
import io.ktor.http.HttpStatusCode
import java.nio.file.Files
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotEquals
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestCoroutineScheduler
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.withContext
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class WritingSyncTest {
    private val origin = "http://127.0.0.1:1"
    private val account = "$origin\u0000instance\u0000users/owner"
    private val identity = """{"instance_id":"instance","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2099-01-01T00:00:00Z","csrf_token":"token"}"""

    private suspend fun selectedSecrets(serverOrigin: String = origin) = MemorySecrets().apply {
        val key = "$serverOrigin\u0000instance\u0000users/owner"
        write("selected-profile", CoreJson.encodeToString(ConnectionProfile(serverOrigin, key, "instance", "users/owner", "Owner", "UTC")))
    }

    @Test
    fun dismissFreezesInputWhileClearingTheActiveMarkerAndRetainsTheDraftOnFailure() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-editor-dismiss").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val editor = EditorController({ repo }, { null }, scope)
        try {
            editor.open(account)
            val id = assertNotNull(editor.state.value.draft).id
            val marker = """{"kind":"new","draft_id":"$id"}"""
            repo.putFeature(account, ACTIVE_NOTE_EDITOR, marker)
            assertTrue(editor.change("accepted before close"))
            assertFalse(editor.dismiss({ error("marker unavailable") }, { error("closed before clearing") }))
            assertFalse(editor.state.value.busy)
            assertEquals(marker, repo.feature(account, ACTIVE_NOTE_EDITOR))
            assertTrue(editor.change("latest accepted text"))
            val clearing = CompletableDeferred<Unit>()
            val release = CompletableDeferred<Unit>()
            val dismiss = async {
                editor.dismiss({ draft ->
                    clearing.complete(Unit)
                    release.await()
                    repo.clearActiveEditor(draft.accountKey, draft.id)
                }, { assertTrue(editor.state.value.busy) })
            }
            withTimeout(10_000) { clearing.await() }
            assertTrue(editor.state.value.busy)
            assertFalse(editor.change("late IME input"))
            assertFalse(editor.importing(id, true))
            assertEquals("latest accepted text", db.dao().draft(id)?.content)
            release.complete(Unit)
            assertTrue(withTimeout(10_000) { dismiss.await() })
            assertEquals("latest accepted text", editor.state.value.draft?.content)
            assertEquals("latest accepted text", db.dao().draft(id)?.content)
            assertEquals("{}", repo.feature(account, ACTIVE_NOTE_EDITOR))
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            db.close()
            folder.deleteRecursively()
        }
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test
    fun archiveNoticesKeepTheirWindowWhenAnotherArchiveAndCopyArrive() = runTest {
        val host = SnackbarHostState()
        val messages = Channel<Pair<String, ArchiveUndo?>>(Channel.UNLIMITED)
        val undone = mutableListOf<String>()
        backgroundScope.launch {
            for ((message, undo) in messages) host.showMobileNotice(message, "Undo", undo)
        }
        messages.send("Archived first" to ArchiveUndo { undone += "first" })
        runCurrent()
        advanceTimeBy(1000)
        val second = ArchiveUndo { undone += "second" }
        messages.send("Archived second" to second)
        messages.send("Copied" to null)
        runCurrent()
        advanceTimeBy(1999)
        runCurrent()
        assertEquals("Archived first", host.currentSnackbarData?.visuals?.message)
        advanceTimeBy(1)
        runCurrent()
        assertEquals("Archived second", host.currentSnackbarData?.visuals?.message)
        advanceTimeBy(2999)
        runCurrent()
        assertEquals("Archived second", host.currentSnackbarData?.visuals?.message)
        host.currentSnackbarData?.performAction()
        runCurrent()
        assertEquals(listOf("second"), undone)
        assertEquals("Copied", host.currentSnackbarData?.visuals?.message)

        val rejected = ArchiveUndo { error("Rejected undo was invoked") }
        messages.send("Rejected archive" to rejected)
        messages.send("Operation failed" to null)
        advanceTimeBy(3000)
        runCurrent()
        assertEquals("Rejected archive", host.currentSnackbarData?.visuals?.message)
        rejected.reject()
        runCurrent()
        assertEquals("Operation failed", host.currentSnackbarData?.visuals?.message)
    }

    @Test
    fun validatedNetworkEdgesCoalesceAndLossNeverRequestsSync() {
        val edges = mutableListOf<Pair<Boolean, Boolean>>()
        val network = NetworkAvailability<String> { ready, recovered -> edges += ready to recovered }
        network.available("wifi")
        network.validation("wifi", true)
        network.available("wifi")
        network.validation("wifi", true)
        network.available("mobile")
        network.lost("wifi")
        network.validation("mobile", true)
        network.lost("mobile")
        network.validation("mobile", true)
        network.available("wifi")
        network.validation("wifi", true)
        assertEquals(listOf(true to true, false to false, true to true), edges)
    }

    @Test
    fun syncTelemetryOmitsEmptyAndLegacyHeartbeatsAndKeepsMaterialResults() {
        val fallback = SyncTrigger.PERIODIC_FALLBACK
        val empty = DrainResult(drained = true, committedCount = 0, hadNewConflict = false)
        assertNull(syncTelemetryResult(fallback, empty, SyncNotesOutcome.NO_CHANGES, true))
        assertNull(syncTelemetryResult(fallback, empty, SyncNotesOutcome.UNSUPPORTED_LEGACY, true))
        assertEquals("success", syncTelemetryResult(SyncTrigger.MANUAL, empty, SyncNotesOutcome.NO_CHANGES, true))
        assertEquals("success", syncTelemetryResult(fallback, empty, SyncNotesOutcome.UPDATED, true))
        assertEquals("success", syncTelemetryResult(fallback, empty.copy(committedCount = 1), SyncNotesOutcome.NO_CHANGES, true))
        assertEquals("conflict", syncTelemetryResult(fallback, empty.copy(hadNewConflict = true), SyncNotesOutcome.NO_CHANGES, true))
        assertEquals("unknown", syncTelemetryResult(fallback, empty.copy(drained = false), null, false))
    }

    @Test
    fun filteredTimeLineReplacesOnlyMatchingNotesAndKeepsTheirKeysAndOrder() {
        val first = Note("instance", "memos/first", "old", "NORMAL", "hash")
        val second = first.copy(name = "memos/second")
        val pending = DraftEntity("edit", account, "new", 1, targetName = second.name)
        val unrelated = pending.copy(id = "create", targetName = null)
        val entries = filteredTimelineEntries(listOf(first, second), listOf(unrelated, pending), mapOf(second.name to "stable"))
        assertEquals(listOf(TimelineEntry.Remote(first), TimelineEntry.Local(pending, "stable")), entries)
        val oldQueue = SyncUiState(notes = listOf(unrelated, pending))
        val visible = oldQueue.notesFor("$origin\u0000instance\u0000users/other")
        assertEquals(listOf(TimelineEntry.Remote(second)), filteredTimelineEntries(listOf(second), visible, emptyMap()))
        assertEquals(listOf(TimelineEntry.Remote(second)), timelineEntries(listOf(second), visible, emptyMap()))
    }

    @Test
    fun committedDraftIsNotRetriedIfTheUiReceiptThrows() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-writing-receipt-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val lock = Mutex()
        val editor = EditorController({ repo }, { null }, scope, lock)
        val posts = AtomicInteger()
        val client = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(identity)
            else {
                assertEquals(HttpMethod.Post, request.method)
                posts.incrementAndGet()
                respond(CoreJson.encodeToString(Note("instance", "memos/one", "exact", "NORMAL", "hash")))
            }
        })
        try {
            editor.open(account)
            editor.change("exact")
            val id = assertNotNull(editor.state.value.draft).id
            var receiptBeforeClear = false
            assertTrue(editor.enqueue { frozen ->
                receiptBeforeClear = frozen.id == id && editor.state.value.draft?.id == id && frozen.content == "exact"
                error("receipt unavailable")
            })
            assertTrue(receiptBeforeClear)
            assertNotEquals(id, editor.state.value.draft?.id)
            assertFalse(editor.enqueue())
            val queue = NoteWriteQueue({ repo }, lock, { listOf(editor) }, { account })
            assertEquals(1, queue.drain(account, client).committedCount)
            assertEquals(0, queue.drain(account, client).committedCount)
            assertEquals(1, posts.get())
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            client.close()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun composerCommitAndLifecycleFailuresPreserveBothDraftSlots() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-writing-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val session = SessionController(selectedSecrets(), true, scope)
        val mobile = MobileController(session, { repo }, scope, initialForeground = false)
        val note = Note("instance", "memos/one", "Old body", "NORMAL", "hash")
        suspend fun sql(statement: String): Unit = db.useWriterConnection { writer ->
            writer.immediateTransaction { writer.usePrepared(statement) { it.step() }; Unit }
        }
        suspend fun save(): SaveOutcome {
            val result = CompletableDeferred<SaveOutcome>()
            mobile.saveComposer { result.complete(it) }
            return withTimeout(10_000) { result.await() }
        }
        try {
            withTimeout(10_000) { mobile.editor.state.first { it.draft != null } }
            mobile.editor.change("Top draft")
            val id = assertNotNull(mobile.editor.state.value.draft).id
            mobile.editNote(note)
            val editors = withTimeout(10_000) { mobile.noteEditors.first { it.containsKey(note.name) } }
            val inline = editors.getValue(note.name)
            inline.change("Inline revision")

            sql("CREATE TRIGGER fail_queue BEFORE INSERT ON pending_writes BEGIN SELECT RAISE(FAIL, 'offline disk'); END")
            assertEquals(SaveOutcome.NOT_COMMITTED, save())
            assertTrue(repo.queuedWrites(account).isEmpty())
            assertEquals(id, mobile.editor.state.value.draft?.id)
            assertEquals("Top draft", mobile.editor.state.value.draft?.content)
            sql("DROP TRIGGER fail_queue")

            sql("CREATE TRIGGER fail_lifecycle BEFORE UPDATE ON drafts BEGIN SELECT RAISE(FAIL, 'offline disk'); END")
            mobile.editor.change("Temporary change")
            assertFalse(mobile.editor.flushForLifecycle())
            assertEquals("LOCAL_WRITE_FAILED", mobile.editor.state.value.error)
            sql("DROP TRIGGER fail_lifecycle")
            mobile.editor.change("Top draft")
            assertTrue(mobile.editor.flushForLifecycle())

            assertEquals(SaveOutcome.COMMITTED, save())
            assertEquals("Top draft", mobile.sync.state.value.notes.single().content)
            assertEquals(id, repo.queuedWrites(account).single().draftId)
            assertNotEquals(id, mobile.editor.state.value.draft?.id)
            assertEquals("", mobile.editor.state.value.draft?.content)
            assertEquals("Inline revision", inline.state.value.draft?.content)
            assertEquals(AttachmentEdits(), inline.state.value.draft?.attachmentEdits())
            assertEquals(SaveOutcome.NOT_COMMITTED, save())
            assertEquals(1, repo.queuedWrites(account).size)
        } finally {
            session.close()
            scope.coroutineContext[Job]!!.cancelAndJoin()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun legacyPreparedHomeDraftKeepsItsComposerAndRebasesNewInputOntoOneCreatedNote() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-legacy-home-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val server = MockWebServer()
        val postStarted = CompletableDeferred<Unit>()
        val allowPost = CountDownLatch(1)
        val posts = AtomicInteger()
        val patches = AtomicInteger()
        val original = Note("instance", "memos/one", "Original", "NORMAL", "hash")
        val updated = original.copy(content = "Still writing", sourceHash = "edited")
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when {
                request.path == "/api/app/v1/session" -> MockResponse().setBody(identity)
                request.path == "/api/app/v1/notes" && request.method == "POST" -> {
                    posts.incrementAndGet()
                    postStarted.complete(Unit)
                    check(allowPost.await(10, TimeUnit.SECONDS))
                    MockResponse().setBody(CoreJson.encodeToString(original))
                }
                request.path == "/api/app/v1/notes/one" && request.method == "PATCH" -> {
                    patches.incrementAndGet()
                    MockResponse().setBody(CoreJson.encodeToString(updated))
                }
                else -> MockResponse().setResponseCode(404)
            }
        }
        server.start()
        val serverOrigin = server.url("/").toString().removeSuffix("/")
        val key = "$serverOrigin\u0000instance\u0000users/owner"
        val draft = repo.restore(key).draft.copy(content = original.content, revision = 1)
        repo.persist(draft)
        repo.prepare(draft)
        repo.putFeature(key, ACTIVE_NOTE_EDITOR, """{"key":"${draft.id}","draft_id":"${draft.id}"}""")
        val session = SessionController(selectedSecrets(serverOrigin), true, scope)
        val mobile = MobileController(session, { repo }, scope, initialForeground = false)
        try {
            val composer = withTimeout(10_000) { mobile.awaitAttachmentTarget(key, draft.id) }
            assertEquals(mobile.editor, composer)
            val opened = CompletableDeferred<EditorController?>()
            mobile.openDraft(draft.id, onComposer = { opened.complete(mobile.editor) }) {
                opened.complete(mobile.getNoteEditor(draft.id))
            }
            assertEquals(composer, withTimeout(10_000) { opened.await() })
            assertTrue(mobile.noteEditors.value.isEmpty())
            assertEquals("""{"kind":"new","draft_id":"${draft.id}"}""", repo.feature(key, ACTIVE_NOTE_EDITOR))
            val delivery = scope.async { mobile.sync.synchronizeOnce() }
            withTimeout(10_000) { postStarted.await() }
            mobile.editor.change(updated.content)
            assertTrue(mobile.editor.flushForLifecycle())
            allowPost.countDown()
            assertTrue(withTimeout(10_000) { delivery.await() })
            assertEquals(updated.content, mobile.editor.state.value.draft?.content)
            assertEquals(original.name, mobile.editor.state.value.draft?.targetName)
            assertNull(mobile.getNoteEditor(draft.id))

            val saved = CompletableDeferred<SaveOutcome>()
            mobile.saveComposer { saved.complete(it) }
            assertEquals(SaveOutcome.COMMITTED, withTimeout(10_000) { saved.await() })
            assertTrue(mobile.sync.synchronizeOnce())
            assertEquals(updated, repo.localNote(key, original.name))
            assertEquals(1, posts.get())
            assertEquals(1, patches.get())
        } finally {
            allowPost.countDown()
            session.close()
            scope.coroutineContext[Job]!!.cancelAndJoin()
            server.shutdown()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun savingInlineInvalidatesAnOlderSearchAndRemovesAnUnmatchedNoteOnConfirmation() =
        checkInlineSearchHandoff(background = false)

    @Test
    fun backgroundConfirmationRefreshesTheOriginalFilterAndRestoresPaginationOnResume() =
        checkInlineSearchHandoff(background = true)

    private fun checkInlineSearchHandoff(background: Boolean) = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-search-handoff-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val server = MockWebServer()
        val oldSearchStarted = CompletableDeferred<Unit>()
        val releaseOldSearch = CountDownLatch(1)
        val refreshed = CompletableDeferred<Unit>()
        val searches = AtomicInteger()
        val original = Note("instance", "memos/one", "Original #writing", "NORMAL", "hash", tags = listOf("writing"))
        val updated = original.copy(content = "New text", sourceHash = "edited", tags = emptyList())
        val firstMatch = original.copy(name = "memos/first-match", content = "New match #writing")
        val laterMatch = original.copy(name = "memos/later-match", content = "Later match #writing")
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when {
                request.path == "/api/app/v1/session" -> MockResponse().setBody(identity)
                request.path == "/api/app/v1/notes/one" && request.method == "PATCH" ->
                    MockResponse().setBody(CoreJson.encodeToString(updated))
                request.requestUrl?.encodedPath == "/api/app/v1/notes" && request.requestUrl?.queryParameter("tag") == "writing" -> {
                    val token = request.requestUrl?.queryParameter("page_token")
                    val attempt = searches.incrementAndGet()
                    if (attempt == 2) {
                        oldSearchStarted.complete(Unit)
                        check(releaseOldSearch.await(10, TimeUnit.SECONDS))
                    }
                    if (attempt > 2) refreshed.complete(Unit)
                    val results = when {
                        token == "fresh-page" -> NotePage(listOf(laterMatch))
                        attempt > 2 -> NotePage(listOf(firstMatch), "fresh-page")
                        else -> NotePage(listOf(original), "old-page")
                    }
                    MockResponse().setBody(CoreJson.encodeToString(results))
                }
                else -> MockResponse().setResponseCode(404)
            }
        }
        server.start()
        val serverOrigin = server.url("/").toString().removeSuffix("/")
        val session = SessionController(selectedSecrets(serverOrigin), true, scope)
        val mobile = MobileController(session, { repo }, scope, initialForeground = false)
        try {
            withTimeout(10_000) { session.state.first { it.connected } }
            withTimeout(10_000) { mobile.editor.state.first { it.draft != null } }
            mobile.page("home")
            mobile.foreground(true)
            mobile.sync.foreground(false)
            mobile.search.change(SearchQuery(tags = setOf("writing")))
            mobile.search.search()
            withTimeout(10_000) { mobile.search.state.first { it.notes == listOf(original) && it.next == "old-page" } }
            mobile.search.search()
            withTimeout(10_000) { oldSearchStarted.await() }

            mobile.editNote(original)
            val inline = withTimeout(10_000) { mobile.noteEditors.first { original.name in it } }.getValue(original.name)
            inline.change(updated.content)
            mobile.saveNote(original.name)
            val queued = withTimeout(10_000) { mobile.sync.state.first { it.notes.any { note -> note.content == updated.content } } }
            assertEquals(updated.content, queued.notes.single().content)
            if (background) mobile.foreground(false)
            assertTrue(mobile.sync.synchronizeOnce(SyncTrigger.BACKGROUND_JOB))
            assertTrue(mobile.sync.state.value.notes.isEmpty())
            assertFalse(mobile.search.state.value.notes.any { it.name == original.name })
            if (background) {
                assertEquals(2, searches.get())
                assertEquals("", mobile.search.state.value.next)
                mobile.foreground(true)
            }
            releaseOldSearch.countDown()
            withTimeout(10_000) { refreshed.await() }
            withTimeout(10_000) { mobile.search.state.first { it.notes == listOf(firstMatch) && it.next == "fresh-page" } }
            mobile.search.search(append = true)
            withTimeout(10_000) { mobile.search.state.first { !it.loading && it.notes == listOf(firstMatch, laterMatch) } }
        } finally {
            releaseOldSearch.countDown()
            session.close()
            scope.coroutineContext[Job]!!.cancelAndJoin()
            server.shutdown()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun confirmingAnEditOnTheSecondSearchPageKeepsTheLoadedRangeAndCardKey() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-search-anchor").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val server = MockWebServer()
        val original = Note("instance", "memos/second", "Original #writing", "NORMAL", "original", tags = listOf("writing"))
        val updated = original.copy(content = "Edited #writing", sourceHash = "edited")
        val first = original.copy(name = "memos/first")
        val confirmed = AtomicInteger()
        val refreshSecondPage = CountDownLatch(1)
        val refreshStarted = CompletableDeferred<Unit>()
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when {
                request.path == "/api/app/v1/session" -> MockResponse().setBody(identity)
                request.path == "/api/app/v1/notes/second" && request.method == "PATCH" -> {
                    confirmed.incrementAndGet()
                    MockResponse().setBody(CoreJson.encodeToString(updated))
                }
                request.requestUrl?.encodedPath == "/api/app/v1/notes" && request.requestUrl?.queryParameter("tag") == "writing" -> {
                    val token = request.requestUrl?.queryParameter("page_token")
                    val saved = confirmed.get() > 0
                    val results = when (token) {
                        "page-two" -> NotePage(listOf(original), "page-three")
                        "updated-page-two" -> {
                            refreshStarted.complete(Unit)
                            check(refreshSecondPage.await(10, TimeUnit.SECONDS))
                            NotePage(listOf(updated), "updated-page-three")
                        }
                        else -> NotePage(listOf(first), if (saved) "updated-page-two" else "page-two")
                    }
                    MockResponse().setBody(CoreJson.encodeToString(results))
                }
                else -> MockResponse().setResponseCode(404)
            }
        }
        server.start()
        val session = SessionController(selectedSecrets(server.url("/").toString().removeSuffix("/")), true, scope)
        val mobile = MobileController(session, { repo }, scope, initialForeground = false)
        try {
            withTimeout(10_000) { session.state.first { it.connected } }
            withTimeout(10_000) { mobile.editor.state.first { it.draft != null } }
            mobile.page("home")
            mobile.foreground(true)
            mobile.sync.foreground(false)
            mobile.search.change(SearchQuery(tags = setOf("writing")))
            mobile.search.search()
            withTimeout(10_000) { mobile.search.state.first { it.next == "page-two" } }
            mobile.search.search(append = true)
            val loaded = withTimeout(10_000) { mobile.search.state.first { it.next == "page-three" } }
            val keys = filteredTimelineEntries(loaded.notes, emptyList(), emptyMap()).map { it.key }
            mobile.editNote(original)
            val editor = withTimeout(10_000) { mobile.noteEditors.first { original.name in it } }.getValue(original.name)
            assertTrue(editor.change(updated.content))
            mobile.saveNote(original.name)
            withTimeout(10_000) { mobile.sync.state.first { it.notes.any { note -> note.content == updated.content } } }
            assertTrue(mobile.sync.synchronizeOnce(SyncTrigger.BACKGROUND_JOB))
            withTimeout(10_000) { refreshStarted.await() }
            assertEquals(listOf(first, updated), mobile.search.state.value.notes)
            refreshSecondPage.countDown()
            val refreshed = withTimeout(10_000) { mobile.search.state.first { it.next == "updated-page-three" } }
            assertEquals(listOf(first, updated), refreshed.notes)
            assertEquals(keys, filteredTimelineEntries(refreshed.notes, emptyList(), emptyMap()).map { it.key })
        } finally {
            refreshSecondPage.countDown()
            session.close()
            scope.coroutineContext[Job]!!.cancelAndJoin()
            server.shutdown()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun signingOutHidesQueuedBodiesBeforeAnOldCycleCanFinishCancelling() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-queue-visibility-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        db.dao().putSyncState(SyncStateEntity(account, "cursor", "generation", true, 100L))
        val session = SessionController(selectedSecrets(), true, scope)
        val entered = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        val lock = Mutex()
        val writes = NoteWriteQueue({ repo }, lock, { emptyList() }, { session.state.value.profile?.accountKey })
        val sync = SyncController(session, { repo }, writes, scope, {}, initialForeground = false, draftLock = lock,
            checkConnection = {
                entered.complete(Unit)
                withContext(NonCancellable) { release.await() }
                false
            })
        try {
            val draft = repo.restore(account).draft.copy(content = "Only for this account", revision = 1)
            repo.persist(draft)
            repo.submit(draft)
            withTimeout(10_000) { sync.state.first { it.lastSyncAt == 100L } }
            val delivery = scope.async { sync.synchronizeOnce() }
            withTimeout(10_000) { entered.await() }
            assertEquals(draft.content, sync.state.value.notes.single().content)
            session.logout()
            withTimeout(10_000) { sync.state.first { it.notes.isEmpty() } }
            assertFalse(delivery.isCompleted)
            release.complete(Unit)
            withTimeout(10_000) { delivery.join() }
            assertTrue(sync.state.value.notes.isEmpty())
        } finally {
            release.complete(Unit)
            session.close()
            scope.coroutineContext[Job]!!.cancelAndJoin()
            db.close()
            folder.deleteRecursively()
        }
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test
    fun foregroundSkipsTheSaveTriggerWhenTheBackgroundJobAlreadyDeliveredIt() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-save-wake-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scheduler = TestCoroutineScheduler()
        val scope = CoroutineScope(SupervisorJob() + StandardTestDispatcher(scheduler))
        val server = MockWebServer()
        val postStarted = CompletableDeferred<Unit>()
        val allowPost = CountDownLatch(1)
        val cycles = AtomicInteger()
        val created = Note("instance", "memos/created", "Saved from composer", "NORMAL", "hash")
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when (request.requestUrl?.encodedPath) {
                "/api/app/v1/session" -> MockResponse().setBody(identity)
                "/api/app/v1/notes" -> if (request.method == "POST") {
                    postStarted.complete(Unit)
                    check(allowPost.await(10, TimeUnit.SECONDS))
                    MockResponse().setBody(CoreJson.encodeToString(created))
                } else MockResponse().setResponseCode(404)
                "/api/app/v1/sync/notes" -> {
                    cycles.incrementAndGet()
                    MockResponse().setResponseCode(404)
                }
                else -> MockResponse().setResponseCode(404)
            }
        }
        server.start()
        val session = SessionController(selectedSecrets(server.url("/").toString().removeSuffix("/")), true, scope)
        val mobile = MobileController(session, { repo }, scope, initialForeground = false)
        suspend fun pumpUntil(ready: () -> Boolean) = withTimeout(10_000) {
            do {
                scheduler.runCurrent()
                if (!ready()) kotlinx.coroutines.delay(5)
            } while (!ready())
        }
        try {
            pumpUntil { session.state.value.connected && mobile.editor.state.value.draft != null }
            mobile.editor.change(created.content)
            val saved = CompletableDeferred<SaveOutcome>()
            mobile.saveComposer { saved.complete(it) }
            pumpUntil { saved.isCompleted }
            assertEquals(SaveOutcome.COMMITTED, saved.await())
            val background = scope.async { mobile.sync.synchronizeOnce(SyncTrigger.BACKGROUND_JOB) }
            pumpUntil { postStarted.isCompleted }
            mobile.foreground(true)
            scheduler.runCurrent()
            val behindForeground = scope.async { mobile.sync.synchronizeOnce(SyncTrigger.FOREGROUND_RESUME) }
            scheduler.runCurrent()
            assertFalse(behindForeground.isCompleted)
            allowPost.countDown()
            pumpUntil { background.isCompleted && behindForeground.isCompleted }
            assertTrue(background.await())
            assertTrue(behindForeground.await())
            assertTrue(mobile.sync.state.value.notes.isEmpty())
            assertEquals(1, cycles.get())
        } finally {
            allowPost.countDown()
            session.close()
            val job = scope.coroutineContext[Job]!!
            job.cancel()
            pumpUntil { job.isCompleted }
            server.shutdown()
            db.close()
            folder.deleteRecursively()
        }
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test
    fun foregroundUsesOneMonotonicWakeAndIdleFallbackWithLocalPendingOverride() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-sync-clock-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val scheduler = TestCoroutineScheduler()
        val dispatcher = StandardTestDispatcher(scheduler)
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val session = SessionController(selectedSecrets(), true, scope)
        val checks = AtomicInteger()
        val remoteWrites = AtomicInteger()
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            when {
                request.url.encodedPath.endsWith("/session") -> respond(identity)
                request.method == HttpMethod.Post -> {
                    remoteWrites.incrementAndGet()
                    respond(CoreJson.encodeToString(Note("instance", "memos/one", "pending", "NORMAL", "hash")))
                }
                else -> respond(CoreJson.encodeToString(SyncNotesResponse("delta", nextCursor = "next", syncGeneration = "generation")))
            }
        })
        val lock = Mutex()
        val writes = NoteWriteQueue({ repo }, lock, { emptyList() }, { account })
        val cycles = MutableStateFlow(0)
        var blockedCheck: CompletableDeferred<Unit>? = null
        suspend fun pumpUntil(ready: () -> Boolean) {
            withTimeout(10_000) {
                do {
                    scheduler.runCurrent()
                    if (!ready()) kotlinx.coroutines.delay(5)
                } while (!ready())
            }
        }
        val sync = SyncController(session, { repo }, writes, scope, { cycles.value++ },
            initialForeground = false, clockMillis = { scheduler.currentTime }, draftLock = lock,
            checkConnection = { checks.incrementAndGet(); blockedCheck?.await(); true }, core = { core })
        try {
            db.dao().putSyncState(SyncStateEntity(account, "initial", "generation", true, 9_000_000L))
            pumpUntil { session.state.value.profile?.accountKey == account }
            sync.foreground(true)
            pumpUntil { cycles.value == 1 }
            sync.foreground(false)
            scheduler.runCurrent()
            scheduler.advanceTimeBy(2000)
            sync.foreground(true)
            scheduler.runCurrent()
            assertEquals(1, checks.get())
            sync.foreground(false)
            scheduler.runCurrent()
            scheduler.advanceTimeBy(8001)
            sync.foreground(true)
            pumpUntil { cycles.value == 2 }
            scheduler.advanceTimeBy(1)
            assertEquals(2, checks.get())

            sync.networkUnavailable()
            scheduler.runCurrent()
            assertEquals(2, checks.get())
            scheduler.advanceTimeBy(4_999)
            scheduler.runCurrent()
            assertEquals(2, checks.get())
            scheduler.advanceTimeBy(1)
            pumpUntil { cycles.value == 3 }
            assertEquals(3, checks.get())

            sync.foreground(false)
            scheduler.runCurrent()
            for (backgroundFirst in listOf(true, false)) {
                val draft = repo.restore(account).draft.copy(content = "pending", revision = 1)
                repo.persist(draft)
                repo.submit(draft)
                val before = checks.get()
                val allowCycle = CompletableDeferred<Unit>()
                blockedCheck = allowCycle
                fun background() = scope.async { sync.synchronizeOnce(SyncTrigger.BACKGROUND_JOB) }
                val task = if (backgroundFirst) {
                    background().also {
                        pumpUntil { checks.get() == before + 1 }
                        sync.foreground(true)
                    }
                } else {
                    sync.foreground(true)
                    pumpUntil { checks.get() == before + 1 }
                    background()
                }
                scheduler.runCurrent()
                allowCycle.complete(Unit)
                pumpUntil { task.isCompleted && cycles.value == before + 1 }
                assertTrue(task.await())
                scheduler.runCurrent()
                assertEquals(before + 1, checks.get())
                if (backgroundFirst) {
                    sync.foreground(false)
                    scheduler.runCurrent()
                }
            }
            assertEquals(2, remoteWrites.get())
            assertEquals(5, checks.get())
            scheduler.runCurrent()
            scheduler.advanceTimeBy(179_999)
            assertEquals(5, checks.get())
            scheduler.advanceTimeBy(1)
            pumpUntil { cycles.value == 6 }
            assertEquals(6, checks.get())
        } finally {
            session.close()
            val job = scope.coroutineContext[Job]!!
            job.cancel()
            pumpUntil { job.isCompleted }
            core.close()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun legacySyncIsDistinctFromAnEmptyIncrement() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-sync-outcomes-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        var legacy = true
        val client = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            when {
                request.url.encodedPath.endsWith("/session") -> respond(identity)
                legacy -> respond("{}", HttpStatusCode.NotFound)
                else -> respond(CoreJson.encodeToString(SyncNotesResponse("delta", nextCursor = "next", syncGeneration = "generation")))
            }
        })
        try {
            assertEquals(SyncNotesOutcome.UNSUPPORTED_LEGACY, repo.syncNotes(account, client))
            assertFalse(repo.isNotesSynced(account))
            legacy = false
            db.dao().putSyncState(SyncStateEntity(account, "old", "generation", true, 1))
            val projection = repo.projection.value
            assertEquals(SyncNotesOutcome.NO_CHANGES, repo.syncNotes(account, client))
            assertEquals(projection, repo.projection.value)
            assertEquals("next", db.dao().syncState(account)?.cursor)
            assertTrue(requireNotNull(repo.lastNoteSync(account)) > 1L)
        } finally {
            client.close()
            db.close()
            folder.deleteRecursively()
        }
    }
}

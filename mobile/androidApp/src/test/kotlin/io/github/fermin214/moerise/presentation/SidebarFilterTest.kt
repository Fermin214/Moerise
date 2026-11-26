package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.SyncNotesResponse
import io.ktor.client.engine.mock.*
import java.nio.file.Files
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.sync.Mutex
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class SidebarFilterTest {
    @Test
    fun calendarProjectsOnlyTheServiceMonthAcrossDaylightSavingChanges() = runBlocking {
        val folder = Files.createTempDirectory("moerise-calendar").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        try {
            suspend fun date(account: String, stamp: String, state: String = "NORMAL") {
                db.dao().putNote(NoteEntity(account, "$stamp:$state", "instance", "", state, "hash",
                    sortableTime(stamp), sortableTime(stamp), "unused JSON"))
            }
            date("account", "2026-03-01T04:59:59.999999999Z")
            date("account", "2026-03-01T05:00:00Z")
            date("account", "2026-03-08T06:59:59.999999999Z")
            date("account", "2026-03-08T07:00:00Z")
            date("account", "2026-03-08T07:00:00Z", "ARCHIVED")
            date("account", "2026-04-01T03:59:59.999999999Z")
            date("account", "2026-04-01T04:00:00Z")
            date("other", "2026-03-08T07:00:00Z")
            date("account", "2026-11-01T05:30:00Z")
            date("account", "2026-11-01T06:30:00Z")
            date("historic", "1969-11-30T23:59:59.999999999Z")
            date("historic", "1969-12-01T00:00:00Z")
            date("historic", "1969-12-31T18:29:59.999999999Z")
            date("historic", "1969-12-31T18:30:00Z")
            date("historic", "1969-12-31T23:59:59.999999999Z")
            date("historic", "1970-01-01T00:00:00Z")

            val repository = NoteRepository(db)
            val zone = "America/New_York"
            val march = repository.localCalendarPage("account", "NORMAL", "2026-03", zone)
            assertEquals(listOf(DayCount("2026-03-01", 1), DayCount("2026-03-08", 2), DayCount("2026-03-31", 1)), march.days)
            assertEquals(3, repository.localCalendarPage("account", "ALL", "2026-03", zone).days[1].count)
            assertEquals(listOf(DayCount("2026-04-01", 1)), repository.localCalendarPage("account", "NORMAL", "2026-04", zone).days)
            assertEquals(listOf(DayCount("2026-11-01", 2)), repository.localCalendarPage("account", "NORMAL", "2026-11", zone).days)
            assertEquals(listOf(DayCount("1969-12-01", 1), DayCount("1969-12-31", 3)),
                repository.localCalendarPage("historic", "NORMAL", "1969-12", "UTC").days)
            assertEquals(listOf(DayCount("1970-01-01", 3)),
                repository.localCalendarPage("historic", "ALL", "1970-01", "Asia/Kolkata").days)
            assertEquals(listOf(DayCount("1970-01-01", 1)),
                repository.localCalendarPage("historic", "NORMAL", "1970-01", "UTC").days)
        } finally { db.close(); folder.deleteRecursively() }
    }

    @Test
    fun sidebarConsumesPrewarmedResultsAndInvalidatesOnLocalChangesAndIdentity() = runBlocking {
        val folder = Files.createTempDirectory("moerise-filter-cache").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        try {
            val repository = NoteRepository(db)
            val original = Note("instance", "memos/one", "hello", "NORMAL", "hash",
                createTime = "2026-03-01T02:00:00Z", tags = listOf("old"))
            db.dao().putSyncState(SyncStateEntity("account", "cursor", "generation", true, 1))
            db.dao().putSyncState(SyncStateEntity("other", "cursor", "generation", true, 1))
            repository.recordLocalNote("account", original)
            repository.recordLocalNote("other", original.copy(tags = listOf("other")))
            var account = "account"
            var zone = "UTC"
            var readsFromRepository = 0
            val reads = FilterReads({ readsFromRepository++; repository }, { null }, { account }, { zone }, scope)
            val sidebar = SearchController({ null }, { account }, scope, reads)
            val month = "2026-03"
            val prewarmed = reads.filters("NORMAL", month)
            assertEquals(listOf(TagCount("old", 1)), prewarmed.tags.tags)
            sidebar.filters(month)
            assertFalse(sidebar.state.value.filtersLoading)
            assertEquals(prewarmed.calendar, sidebar.state.value.calendar)
            assertEquals(1, readsFromRepository)
            assertSame(prewarmed, reads.filters("NORMAL", month))
            assertEquals(1, readsFromRepository)

            val retagged = original.copy(tags = listOf("new"))
            repository.recordLocalNote(account, retagged)
            val revision = reads.revision.value
            reads.changed(original, retagged)
            assertTrue(reads.revision.value > revision)
            assertNull(reads.cached("NORMAL", month))
            sidebar.filters(month)
            withTimeout(5_000) { sidebar.state.first { !it.filtersLoading && it.tags == listOf(TagCount("new", 1)) } }

            val archived = retagged.copy(state = "ARCHIVED")
            repository.recordLocalNote(account, archived)
            reads.changed(retagged, archived)
            assertTrue(reads.filters("NORMAL", month).calendar.days.isEmpty())
            assertEquals(listOf(DayCount("2026-03-01", 1)), reads.filters("ARCHIVED", month).calendar.days)
            zone = "America/New_York"
            assertNull(reads.cached("ARCHIVED", month))
            assertTrue(reads.filters("ARCHIVED", month).calendar.days.isEmpty())

            account = "other"
            assertNull(reads.cached("NORMAL", month))
            assertEquals(listOf(TagCount("other", 1)), reads.filters("NORMAL", month).tags.tags)
            reads.clear()
            assertNull(reads.cached("NORMAL", month))
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun committedIncrementAndQueuedSaveInvalidatePrewarmedCountsEvenIfTheirCallersAreCancelled() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-filter-cancel").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repository = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val origin = "https://notes.example"
        val account = "$origin\u0000instance\u0000users/owner"
        val original = Note("instance", "memos/original", "old", "NORMAL", "hash",
            createTime = "2026-03-01T02:00:00Z", tags = listOf("old"))
        val saved = original.copy(name = "memos/saved", content = "entry", tags = listOf("saved"))
        val engine = MockEngine { request ->
            respond(when (request.url.encodedPath) {
                "/api/app/v1/session" -> """{"instance_id":"instance","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}"""
                "/api/app/v1/sync/notes" -> CoreJson.encodeToString(SyncNotesResponse("delta",
                    items = listOf(original.copy(tags = listOf("new"))), nextCursor = "second", syncGeneration = "generation"))
                "/api/app/v1/notes" -> CoreJson.encodeToString(saved)
                else -> error(request.url.encodedPath)
            })
        }
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), engine)
        try {
            db.dao().putSyncState(SyncStateEntity(account, "first", "generation", true, 1))
            repository.recordLocalNote(account, original)
            val reads = FilterReads({ repository }, { null }, { account }, { "UTC" }, scope)
            val sidebar = SearchController({ null }, { account }, scope, reads)
            val month = "2026-03"
            suspend fun refreshSidebar(expected: Set<String>) {
                sidebar.filters(month)
                withTimeout(5_000) {
                    sidebar.state.first { !it.filtersLoading && it.tags.map { tag -> tag.tag }.toSet() == expected }
                }
            }
            suspend fun cancelAfterCommit(work: suspend () -> Unit) = coroutineScope {
                val operation = launch(start = CoroutineStart.LAZY) { work() }
                val observer = launch(Dispatchers.Unconfined, start = CoroutineStart.UNDISPATCHED) {
                    repository.projection.drop(1).first()
                    operation.cancel()
                }
                operation.start()
                withTimeout(5_000) { operation.join(); observer.join() }
                assertTrue(operation.isCancelled)
            }
            refreshSidebar(setOf("old"))
            cancelAfterCommit { repository.syncNotes(account, core) }
            assertEquals("second", db.dao().syncState(account)?.cursor)
            assertNull(reads.cached("NORMAL", month))
            refreshSidebar(setOf("new"))

            val draft = repository.restore(account).draft.copy(content = "entry", revision = 1)
            repository.persist(draft)
            repository.enqueue(draft, repository.prepare(draft))
            val queue = NoteWriteQueue({ repository }, Mutex(), { emptyList() }, { account })
            cancelAfterCommit { queue.drain(account, core) }
            assertTrue(repository.queuedWrites(account).isEmpty())
            assertNull(reads.cached("NORMAL", month))
            refreshSidebar(setOf("new", "saved"))
            assertEquals(listOf(DayCount("2026-03-01", 2)), sidebar.state.value.calendar?.days)
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close(); db.close(); folder.deleteRecursively()
        }
    }

    @Test
    fun cancelledArchiveCannotReviveServerFilterSnapshotsBeforeTheFirstSync() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-filter-archive").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repository = NoteRepository(db)
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val origin = "https://notes.example"
        val account = "$origin\u0000instance\u0000users/owner"
        val otherAccount = "$origin\u0000instance\u0000users/other"
        val month = "2026-03"
        val original = Note("instance", "memos/original", "entry", "NORMAL", "hash",
            createTime = "$month-01T02:00:00Z", tags = listOf("old"))
        val archived = original.copy(state = "ARCHIVED", sourceHash = "updated")
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            respond(when (request.url.encodedPath) {
                "/api/app/v1/session" -> """{"instance_id":"instance","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}"""
                "/api/app/v1/notes/original" -> CoreJson.encodeToString(archived)
                else -> error(request.url.encodedPath)
            })
        })
        try {
            val tags = SnapshotEntity(account, "UTC:tags:NORMAL",
                CoreJson.encodeToString(TagPage(listOf(TagCount("old", 1)))), epochMillis())
            val calendar = SnapshotEntity(account, "UTC:calendar:NORMAL:$month",
                CoreJson.encodeToString(CalendarPage(month, listOf(DayCount("$month-01", 1)))), epochMillis())
            val other = tags.copy(accountKey = otherAccount)
            for (snapshot in listOf(tags, calendar, other)) repository.putSnapshot(snapshot)
            assertFalse(repository.isNotesSynced(account))
            val reads = FilterReads({ repository }, { null }, { account }, { "UTC" }, scope)
            val sidebar = SearchController({ null }, { account }, scope, reads)
            sidebar.filters(month)
            withTimeout(5_000) { sidebar.state.first { it.tags == listOf(TagCount("old", 1)) } }
            val version = repository.projection.value
            var changed = false
            val controller = NoteDetailController({ repository }, { account }, { core }, scope) { _, _, _ -> changed = true }
            val interrupt = launch(Dispatchers.Unconfined, start = CoroutineStart.UNDISPATCHED) {
                repository.projection.drop(1).first()
                controller.clear()
            }
            controller.changeState(original)
            withTimeout(5_000) { interrupt.join() }
            assertEquals(archived, repository.localNote(account, original.name))
            assertFalse(changed)
            assertNull(repository.readSnapshot(account, tags.filterKey))
            assertNull(repository.readSnapshot(account, calendar.filterKey))
            assertEquals(other, repository.readSnapshot(otherAccount, other.filterKey))
            assertFailsWith<CancellationException> { repository.putFilterSnapshot(tags, version) }
            sidebar.filters(month)
            assertTrue(sidebar.state.value.tags.isEmpty())
            val unavailable = withTimeout(5_000) { sidebar.state.first { it.filtersError != null } }
            assertTrue(unavailable.tags.isEmpty())
            assertNull(unavailable.calendar)
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close(); db.close(); folder.deleteRecursively()
        }
    }
}

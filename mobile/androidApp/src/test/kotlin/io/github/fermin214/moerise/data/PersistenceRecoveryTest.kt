package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import androidx.room.immediateTransaction
import androidx.room.useWriterConnection
import androidx.sqlite.driver.AndroidSQLiteDriver
import androidx.sqlite.execSQL
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.SyncNotesResponse
import io.github.fermin214.moerise.presentation.SearchQuery
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.nio.file.Files
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.encodeToString

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class PersistenceRecoveryTest {
    @Test
    fun frozenWriteSurvivesCloseAndReopenWithoutCrossingAccounts() = runBlocking {
        val folder = Files.createTempDirectory("moerise-room-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        try {
            val first = testDatabase(path)
            val draft = DraftEntity("draft-a", "account-a", "提交后继续输入", 2)
            val write = PendingWriteEntity("op-a", draft.id, draft.accountKey, "fixed-key", "POST", "/notes", "{\"content\":\"已提交\"}", 1, "sending")
            first.useWriterConnection { transaction ->
                transaction.immediateTransaction {
                    first.dao().putDraft(draft)
                    first.dao().putWrite(write)
                }
            }
            first.close()
            val reopened = testDatabase(path)
            try {
                reopened.dao().recoverSending("account-a")
                assertEquals(draft, reopened.dao().homeDraft("account-a"))
                assertNull(reopened.dao().homeDraft("account-b"))
                assertEquals(write.copy(status = "unknown"), reopened.dao().pending(draft.id))
            } finally {
                reopened.close()
            }
        } finally {
            folder.deleteRecursively()
        }
    }

    @Test
    fun sqliteV1ToV2MigrationPreservesLegacyRowsAndCreatesSyncTables() = runBlocking {
        val folder = Files.createTempDirectory("moerise-room-v1-v2-").toFile()
        val path = folder.resolve("legacy.sqlite3").absolutePath
        try {
            val sqlite = AndroidSQLiteDriver().open(path)
            try {
                sqlite.execSQL("CREATE TABLE IF NOT EXISTS `profiles` (`accountKey` TEXT NOT NULL, `origin` TEXT NOT NULL, `instanceId` TEXT NOT NULL, `userName` TEXT NOT NULL, `displayName` TEXT NOT NULL, `timezone` TEXT NOT NULL, PRIMARY KEY(`accountKey`))")
                sqlite.execSQL("CREATE TABLE IF NOT EXISTS `drafts` (`id` TEXT NOT NULL, `accountKey` TEXT NOT NULL, `content` TEXT NOT NULL, `revision` INTEGER NOT NULL, `targetName` TEXT, `sourceHash` TEXT, `attachmentsJson` TEXT NOT NULL, `conflictJson` TEXT, `baselineJson` TEXT DEFAULT NULL, `slot` TEXT NOT NULL DEFAULT 'home', `createdAt` INTEGER NOT NULL DEFAULT 0, `queuedRevisionJson` TEXT, PRIMARY KEY(`id`))")
                sqlite.execSQL("CREATE TABLE IF NOT EXISTS `pending_writes` (`id` TEXT NOT NULL, `draftId` TEXT NOT NULL, `accountKey` TEXT NOT NULL, `idempotencyKey` TEXT NOT NULL, `method` TEXT NOT NULL, `path` TEXT NOT NULL, `body` TEXT NOT NULL, `draftRevision` INTEGER NOT NULL, `status` TEXT NOT NULL, `submittedDraftJson` TEXT DEFAULT NULL, PRIMARY KEY(`id`))")
                sqlite.execSQL("CREATE TABLE IF NOT EXISTS `snapshots` (`accountKey` TEXT NOT NULL, `filterKey` TEXT NOT NULL, `body` TEXT NOT NULL, `fetchedAt` INTEGER NOT NULL, PRIMARY KEY(`accountKey`, `filterKey`))")
                sqlite.execSQL("CREATE TABLE IF NOT EXISTS `feature_states` (`accountKey` TEXT NOT NULL, `feature` TEXT NOT NULL, `body` TEXT NOT NULL, PRIMARY KEY(`accountKey`, `feature`))")
                sqlite.execSQL("CREATE TABLE IF NOT EXISTS room_master_table (id INTEGER PRIMARY KEY, identity_hash TEXT)")
                sqlite.execSQL("INSERT OR REPLACE INTO room_master_table (id, identity_hash) VALUES (42, 'ffef3573630fbb5f56f3433350406d2a')")
                sqlite.execSQL("INSERT INTO profiles VALUES ('acct-1', 'https://notes.example', 'one', 'users/owner', 'Owner 中文', 'Asia/Shanghai')")
                sqlite.execSQL("INSERT INTO drafts VALUES ('draft-1', 'acct-1', '中文草稿内容', 3, NULL, NULL, 'not-json', NULL, '', 'home', 1234, NULL)")
                sqlite.execSQL("INSERT INTO pending_writes VALUES ('op-1', 'draft-1', 'acct-1', 'idem-1', 'POST', '/notes', '{\"content\":\"中文\"}', 3, 'pending', NULL)")
                sqlite.execSQL("PRAGMA user_version = 1")
            } finally {
                sqlite.close()
            }
            val db = testDatabase(path)
            try {
                assertEquals("Owner 中文", db.dao().profile("acct-1")?.displayName)
                assertEquals("中文草稿内容", db.dao().homeDraft("acct-1")?.content)
                assertEquals("idem-1", db.dao().pending("draft-1")?.idempotencyKey)
                val repo = NoteRepository(db)
                assertFalse(repo.isNotesSynced("acct-1"))
                repo.putCachedSnapshot("acct-1", "map", """{"status":"empty"}""")
                assertEquals("""{"status":"empty"}""", repo.readCachedSnapshot("acct-1", "map")?.body)
            } finally {
                db.close()
            }
        } finally {
            folder.deleteRecursively()
        }
    }

    @Test
    fun incrementalSyncMaintainsLocalReadModelsAcrossAccountsAndReset() = runBlocking {
        val folder = Files.createTempDirectory("moerise-sync-notes-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val keyA = "https://notes.example\u0000one\u0000users/owner"
        val keyB = "https://notes.example\u0000two\u0000users/other"
        var phase = 0
        val engine = MockEngine { request ->
            when {
                request.url.encodedPath.endsWith("/session") ->
                    respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"Asia/Shanghai","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""")
                request.url.encodedPath.endsWith("/sync/notes") -> {
                    val cursor = request.url.parameters["cursor"].orEmpty()
                    val body = when (phase) {
                        0 -> if (cursor.isEmpty()) {
                            SyncNotesResponse(
                                mode = "full",
                                items = listOf(
                                    Note("one", "memos/a", "晨间日记 #日记", "NORMAL", "h1", "2026-11-10T01:00:00Z", tags = listOf("日记")),
                                    Note("one", "memos/b", "旧归档 #档案", "ARCHIVED", "h2", "2026-11-09T01:00:00Z", tags = listOf("档案")),
                                ),
                                nextCursor = "c1",
                                hasMore = true,
                                syncGeneration = "g1",
                            )
                        } else {
                            SyncNotesResponse(
                                mode = "full",
                                items = listOf(
                                    Note("one", "memos/c", "晚间复盘 #日记", "NORMAL", "h3", "2026-11-10T18:00:00+02:00", tags = listOf("日记")),
                                ),
                                nextCursor = "c2",
                                hasMore = false,
                                syncGeneration = "g1",
                            )
                        }
                        1 -> {
                            assertEquals("c2", cursor)
                            SyncNotesResponse(
                                mode = "delta",
                                items = listOf(
                                    Note("one", "memos/a", "晨间日记更新 #反思", "NORMAL", "h1b", "2026-11-10T01:00:00Z", tags = listOf("反思")),
                                ),
                                deleted = listOf("memos/c"),
                                nextCursor = "c3",
                                hasMore = false,
                                syncGeneration = "g1",
                            )
                        }
                        else -> if (cursor == "c3") {
                            SyncNotesResponse(
                                mode = "reset",
                                items = listOf(
                                    Note("one", "memos/d", "全新重建第一页 #新篇", "NORMAL", "h4", "2026-11-11T00:00:00Z", tags = listOf("新篇")),
                                ),
                                nextCursor = "c4a",
                                hasMore = true,
                                syncGeneration = "g2",
                            )
                        } else {
                            assertEquals("c4a", cursor)
                            SyncNotesResponse(
                                mode = "reset",
                                items = listOf(
                                    Note("one", "memos/e", "全新重建第二页 #新篇", "NORMAL", "h5", "2026-11-12T00:00:00Z", tags = listOf("新篇")),
                                ),
                                nextCursor = "c4b",
                                hasMore = false,
                                syncGeneration = "g2",
                            )
                        }
                    }
                    respond(CoreJson.encodeToString(body))
                }
                else -> respond("""{}""", HttpStatusCode.NotFound)
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            assertEquals(SyncNotesOutcome.UPDATED, repo.syncNotes(keyA, core))
            assertTrue(repo.isNotesSynced(keyA))
            val (normalPage, hasMoreNormal) = repo.localNotesPage(keyA, "NORMAL", 10, 0)
            assertFalse(hasMoreNormal)
            assertEquals(listOf("memos/c", "memos/a"), normalPage.map { it.name })
            assertEquals(listOf("日记" to 2), repo.localTagPage(keyA, "NORMAL").tags.map { it.tag to it.count })
            assertEquals(listOf("memos/a"), repo.localSearch(keyA, SearchQuery(text = "晨间"), "Asia/Shanghai", 10, 0).first.map { it.name })
            assertEquals(listOf("2026-11-10" to 1, "2026-11-11" to 1), repo.localCalendarPage(keyA, "NORMAL", "2026-11", "Asia/Shanghai").days.map { it.date to it.count })

            phase = 1
            assertEquals(SyncNotesOutcome.UPDATED, repo.syncNotes(keyA, core))
            assertNull(repo.localNote(keyA, "memos/c"))
            assertEquals(listOf("反思" to 1), repo.localTagPage(keyA, "NORMAL").tags.map { it.tag to it.count })

            repo.putCachedSnapshot(keyB, "walk", """{"ok":true}""")
            phase = 2
            assertEquals(SyncNotesOutcome.UPDATED, repo.syncNotes(keyA, core))
            assertNull(repo.localNote(keyA, "memos/a"))
            assertNull(repo.localNote(keyA, "memos/b"))
            assertEquals(listOf("memos/e", "memos/d"), repo.localNotesPage(keyA, "NORMAL", 10, 0).first.map { it.name })

            repo.clearAccount(keyA)
            assertFalse(repo.isNotesSynced(keyA))
            assertNull(repo.localNote(keyA, "memos/d"))
            assertEquals("""{"ok":true}""", repo.readCachedSnapshot(keyB, "walk")?.body)
        } finally {
            core.close()
            db.close()
            folder.deleteRecursively()
        }
    }
}

package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import androidx.sqlite.driver.AndroidSQLiteDriver
import androidx.test.core.app.ApplicationProvider
import java.nio.file.Files
import java.util.concurrent.Executors
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.drop
import io.github.fermin214.moerise.presentation.EditorController
import io.github.fermin214.moerise.presentation.MobileController
import io.github.fermin214.moerise.presentation.SessionController
import io.github.fermin214.moerise.presentation.clearAttachmentCache
import io.ktor.http.Cookie
import io.ktor.http.Url

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class LocalDraftRecoveryTest {
    @Test
    fun committedAccountRemovalSignsOutBeforeRestoreWithOrWithoutStagedAttachments() = runBlocking<Unit> {
        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        val origin = "http://127.0.0.1:1"
        val key = "$origin\u0000instance\u0000users/owner"
        val profile = ConnectionProfile(origin, key, "instance", "users/owner", "Owner", "UTC")
        val otherOrigin = "https://other.example"
        val pendingUrl = Url("$origin/api/app/v1/session")
        val otherUrl = Url("$otherOrigin/api/app/v1/session")
        for (withAttachment in listOf(false, true)) {
            val folder = Files.createTempDirectory("moerise-pending-logout").toFile()
            val path = folder.resolve("data.sqlite3").absolutePath
            val files = AttachmentFiles(folder.resolve("files").absolutePath)
            var db = testDatabase(path)
            val scope = CoroutineScope(coroutineContext + SupervisorJob())
            var session: SessionController? = null
            try {
                val repo = NoteRepository(db, files)
                val staged = if (withAttachment) listOf(files.stage("draft.txt", "text/plain", byteArrayOf(1))) else emptyList()
                repo.persist(DraftEntity("draft", key, "待清理", 1, attachmentsJson = CoreJson.encodeToString(AttachmentEdits(staged))))
                val other = "$otherOrigin\u0000instance\u0000users/owner"
                repo.persist(DraftEntity("other", other, "保留", 1))
                val secrets = MemorySecrets()
                secrets.write("selected-profile", CoreJson.encodeToString(profile))
                SessionCookies(origin, secrets).addCookie(pendingUrl,
                    Cookie("moerise_local_session", "old", maxAge = 300, path = "/", httpOnly = true))
                SessionCookies(otherOrigin, secrets).addCookie(otherUrl,
                    Cookie("__Host-moerise_session", "keep", maxAge = 300, path = "/", secure = true, httpOnly = true))
                val cache = context.cacheDir.resolve("attachments").apply { mkdirs() }.resolve("protected.txt")
                cache.writeText("protected")

                val purge = launch(start = CoroutineStart.LAZY) { repo.clearAccountForLogout(key) }
                val interrupt = launch(Dispatchers.Unconfined, start = CoroutineStart.UNDISPATCHED) {
                    repo.projection.drop(1).first()
                    purge.cancel()
                }
                purge.start()
                withTimeout(5_000) { purge.join(); interrupt.join() }
                assertTrue(purge.isCancelled)
                assertTrue(repo.hasPendingLogout(key))
                assertNull(db.dao().draft("draft"))
                assertTrue(cache.exists())
                db.close()

                db = testDatabase(path)
                val reopened = NoteRepository(db, files)
                var requests = 0
                var failDownloadCleanup = withAttachment
                val restored = SessionController(secrets, true, scope, removalRepository = { reopened }, clearProtectedDownloads = {
                    if (failDownloadCleanup) throw java.io.IOException("cache unavailable")
                    clearAttachmentCache(context)
                }, onNetworkRequest = { requests++ })
                session = restored
                withTimeout(5_000) { restored.state.first { !it.loading } }
                if (withAttachment) {
                    assertEquals("LOCAL_REMOVE_FAILED", restored.state.value.error)
                    assertTrue(reopened.hasPendingLogout(key))
                    assertFalse(restored.state.value.connected)
                    assertNull(restored.state.value.profile)
                    assertEquals(0, requests)
                    failDownloadCleanup = false
                    restored.retrySession()
                    withTimeout(5_000) { restored.state.first { !it.loading && it.error == null } }
                }
                assertNull(restored.state.value.profile)
                assertEquals(0, requests)
                assertNull(secrets.read("selected-profile"))
                assertTrue(SessionCookies(origin, secrets).get(pendingUrl).isEmpty())
                assertEquals("keep", SessionCookies(otherOrigin, secrets).get(otherUrl).single().value)
                assertFalse(cache.exists())
                assertFalse(reopened.hasPendingLogout(key))
                assertNotNull(db.dao().draft("other"))
                reopened.resumeAttachmentRemovals()
                for (item in staged) assertFalse(folder.resolve("files").resolve(item.id).exists())
            } finally {
                session?.close()
                scope.coroutineContext[Job]!!.cancelAndJoin()
                db.close(); folder.deleteRecursively(); clearAttachmentCache(context)
            }
        }
    }

    @Test
    fun accountAttachmentCleanupResumesAfterTheDatabaseCommitAndKeepsOtherAccountsFiles() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-logout-interrupted").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        val files = AttachmentFiles(folder.resolve("files").absolutePath)
        var db = testDatabase(path)
        try {
            val repo = NoteRepository(db, files)
            val removed = files.stage("removed.txt", "text/plain", byteArrayOf(1))
            val shared = files.stage("shared.txt", "text/plain", byteArrayOf(2))
            fun draft(account: String, vararg items: StagedAttachment) = DraftEntity(account, account, "draft", 1,
                attachmentsJson = CoreJson.encodeToString(AttachmentEdits(items.toList())))
            repo.persist(draft("remove", removed, shared))
            repo.persist(draft("keep", shared))
            val purge = launch(start = CoroutineStart.LAZY) { repo.clearAccount("remove") }
            val cancelAfterCommit = launch(Dispatchers.Unconfined, start = CoroutineStart.UNDISPATCHED) {
                repo.projection.drop(1).first()
                purge.cancel()
            }
            purge.start()
            withTimeout(5_000) { purge.join(); cancelAfterCommit.join() }
            assertTrue(purge.isCancelled)
            assertNull(db.dao().draft("remove"))
            assertEquals("AQ==", files.encoded(removed))
            db.close()
            db = testDatabase(path)
            val reopened = NoteRepository(db, files)
            val staged = folder.resolve("files").resolve(removed.id)
            assertTrue(staged.delete())
            assertTrue(staged.mkdir())
            val blocked = staged.resolve("child").apply { writeText("busy") }
            assertFailsWith<java.io.IOException> { reopened.resumeAttachmentRemovals() }
            assertTrue(blocked.delete())
            reopened.resumeAttachmentRemovals()
            reopened.resumeAttachmentRemovals()
            assertFalse(staged.exists())
            assertEquals("Ag==", files.encoded(shared))
            assertNotNull(db.dao().draft("keep"))
        } finally { db.close(); folder.deleteRecursively() }
    }

    @Test
    fun logoutKeepsLocalDataUnlessTheAccountScopedCleanupIsConfirmedTwice() = runBlocking<Unit> {
        val context = ApplicationProvider.getApplicationContext<android.content.Context>()
        val folder = Files.createTempDirectory("moerise-logout").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val files = AttachmentFiles(folder.resolve("files").absolutePath)
        val repository = NoteRepository(db, files)
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val secrets = MemorySecrets()
        val origin = "http://127.0.0.1:1"
        val profile = ConnectionProfile(origin, "$origin\u0000instance\u0000users/first", "instance", "users/first", "First", "UTC")
        val other = "$origin\u0000instance\u0000users/other"
        val key = profile.accountKey
        val cache = context.cacheDir.resolve("attachments").apply { mkdirs() }.resolve("protected.txt")
        val session = withContext(dispatcher) {
            secrets.write("selected-profile", CoreJson.encodeToString(profile))
            SessionController(secrets, true, scope)
        }
        try {
            val file = files.stage("mine.txt", "text/plain", byteArrayOf(1))
            val otherFile = files.stage("theirs.txt", "text/plain", byteArrayOf(2))
            val queued = DraftEntity("queued", key, "待发", 1, slot = "queued:queued")
            repository.persist(queued)
            repository.persist(DraftEntity("conflict", key, "冲突", 1, conflictJson = "{}",
                attachmentsJson = CoreJson.encodeToString(AttachmentEdits(listOf(file))), slot = "conflict:conflict"))
            repository.persist(DraftEntity("other", other, "另一个账号", 1,
                attachmentsJson = CoreJson.encodeToString(AttachmentEdits(listOf(otherFile)))))
            db.dao().putWrite(PendingWriteEntity("write", queued.id, key, "idempotency", "POST", "/notes", "{}", 1))
            repository.putSnapshot(SnapshotEntity(key, "home", "{}", 1))
            cache.writeText("protected")

            val mobile = withContext(dispatcher) {
                MobileController(session, { repository }, scope, clearFiles = { clearAttachmentCache(context) }, initialForeground = false)
            }
            suspend fun ready() {
                withTimeout(10_000) { mobile.editor.state.first { it.accountKey == key && it.draft != null } }
            }
            suspend fun signedOut() {
                withTimeout(10_000) { session.state.first { it.profile == null } }
                withTimeout(10_000) { mobile.clearing.first { !it } }
            }
            ready()
            withContext(dispatcher) {
                mobile.editor.change("未提交草稿")
                mobile.askLogout()
                mobile.confirmLogout(clearLocal = false)
            }
            signedOut()
            assertFalse(cache.exists())
            assertEquals("未提交草稿", db.dao().homeDraft(key)?.content)
            assertEquals("write", repository.queuedWrites(key).single().id)
            assertNotNull(db.dao().snapshot(key, "home"))
            assertEquals("AQ==", files.encoded(file))

            withContext(dispatcher) {
                secrets.write("selected-profile", CoreJson.encodeToString(profile))
                session.retrySession()
            }
            ready()
            withContext(dispatcher) { mobile.askLogout(); mobile.confirmLogout(clearLocal = true) }
            val warning = withTimeout(10_000) { mobile.logoutState.first { it?.counts != null } }
            withTimeout(10_000) { mobile.clearing.first { !it } }
            assertEquals(LocalRemovalCounts(1, 1, 1), warning?.counts)
            assertEquals(key, session.state.value.profile?.accountKey)
            assertNotNull(db.dao().draft("queued"))
            withContext(dispatcher) { mobile.dismissLogout() }
            assertNotNull(db.dao().draft("conflict"))

            withContext(dispatcher) { mobile.askLogout(); mobile.confirmLogout(clearLocal = true) }
            withTimeout(10_000) { mobile.logoutState.first { it?.counts != null } }
            withTimeout(10_000) { mobile.clearing.first { !it } }
            val blockedFile = folder.resolve("files").resolve(file.id)
            assertTrue(blockedFile.delete())
            assertTrue(blockedFile.mkdir())
            val blocker = blockedFile.resolve("busy").apply { writeText("busy") }
            withContext(dispatcher) { mobile.confirmLogout(clearLocal = true) }
            signedOut()
            assertTrue(db.dao().drafts(key).isEmpty())
            assertTrue(db.dao().accountWrites(key).isEmpty())
            assertNull(db.dao().snapshot(key, "home"))
            assertEquals("LOCAL_REMOVE_FAILED", session.state.value.error)
            assertNull(secrets.read("selected-profile"))
            assertTrue(blocker.delete())
            repository.resumeAttachmentRemovals()
            assertFalse(blockedFile.exists())
            assertEquals("另一个账号", db.dao().homeDraft(other)?.content)
            assertEquals("Ag==", files.encoded(otherFile))
        } finally {
            withContext(dispatcher) { session.close() }
            scope.coroutineContext[Job]!!.cancelAndJoin()
            db.close(); dispatcher.close(); folder.deleteRecursively(); clearAttachmentCache(context)
        }
    }

    @Test
    fun restoredAccountSwitchClearsProtectedDownloadsAndKeepsThePreviousDraft() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-switch-account-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repository = NoteRepository(db)
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val secrets = object : SecretStore {
            val values = java.util.concurrent.ConcurrentHashMap<String, String>()
            override suspend fun read(key: String) = values[key]
            override suspend fun write(key: String, value: String?) {
                if (value == null) values.remove(key) else values[key] = value
            }
        }
        fun profile(user: String) = ConnectionProfile("http://127.0.0.1:1",
            "http://127.0.0.1:1\u0000instance\u0000users/$user", "instance", "users/$user", user, "UTC")
        val first = profile("first"); val second = profile("second")
        val cleared = java.util.concurrent.atomic.AtomicInteger()
        secrets.write("selected-profile", CoreJson.encodeToString(first))
        val session = SessionController(secrets, true, scope)
        try {
            val mobile = MobileController(session, { repository }, scope, clearFiles = { cleared.incrementAndGet(); Unit })
            withTimeout(5000) { mobile.editor.state.first { it.accountKey == first.accountKey && it.draft != null } }
            assertEquals(0, cleared.get())
            withContext(dispatcher) {
                mobile.editor.change("Previous account draft"); mobile.editor.flush()
                secrets.write("selected-profile", CoreJson.encodeToString(second))
                session.retrySession()
            }
            withTimeout(5000) { mobile.editor.state.first { it.accountKey == second.accountKey && it.draft != null } }
            assertEquals(1, cleared.get())
            assertEquals("Previous account draft", repository.drafts(first.accountKey).single().content)
        } finally {
            withContext(dispatcher) { session.close() }
            scope.coroutineContext[Job]!!.cancelAndJoin()
            db.close(); dispatcher.close(); folder.deleteRecursively()
        }
    }

    @Test
    fun conflictForkKeepsOriginalRequestAndAccountPurgePreservesOtherAccountsAndFiles() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-local-drafts-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        var db = testDatabase(path)
        val files = AttachmentFiles(folder.resolve("files").absolutePath)
        val first = "https://notes.example\u0000one\u0000users/a"
        val second = "https://notes.example\u0000one\u0000users/b"
        try {
            var repo = NoteRepository(db, files)
            val file = files.stage("first.txt", "text/plain", "first".encodeToByteArray())
            val otherFile = files.stage("second.txt", "text/plain", "second".encodeToByteArray())
            val orphan = files.stage("interrupted.txt", "text/plain", "unbound".encodeToByteArray())
            val home = DraftEntity("home", first, "原主页草稿", 3)
            val conflict = DraftEntity("conflict", first, "冲突正文", 7, targetName = "memos/deleted", sourceHash = "hash",
                attachmentsJson = CoreJson.encodeToString(AttachmentEdits(listOf(file))), conflictJson = """{"submitted":{"content":"原提交"}}""",
                slot = "memos/deleted")
            val other = DraftEntity("other", second, "另一个账号", 2, attachmentsJson = CoreJson.encodeToString(AttachmentEdits(listOf(otherFile))))
            val originalBody = """{"content":"原提交","source_hash":"hash"}"""
            val write = PendingWriteEntity("write", conflict.id, first, "fixed-key", "PATCH", "/notes/deleted", originalBody, 6, "conflict")
            repo.persist(home); repo.persist(conflict); repo.persist(other); db.dao().putWrite(write)
            repo.putFeature(first, "map-rebuild", "pending"); repo.putFeature(second, "insights", "kept")
            val forked = repo.forkConflict(conflict, "恢复正文", AttachmentEdits(listOf(file), listOf("attachments/removed"))).draft
            assertNotEquals(conflict.id, forked.id)
            assertEquals("home", forked.slot)
            assertNull(forked.targetName); assertNull(forked.sourceHash); assertNull(forked.conflictJson)
            assertTrue(forked.attachmentEdits().removed.isEmpty())
            assertEquals(listOf(file), forked.attachmentEdits().staged)
            assertEquals(forked.id, db.dao().homeDraft(first)?.id)
            assertEquals(setOf(home.id, conflict.id, forked.id), repo.drafts(first).map { it.id }.toSet())
            assertEquals("原主页草稿", repo.storedDraft(first, home.id).draft.content)
            assertFailsWith<IllegalArgumentException> { repo.storedDraft(second, conflict.id) }
            AndroidSQLiteDriver().open(path).use { connection ->
                connection.prepare("SELECT body, status FROM pending_writes WHERE id = 'write'").use { statement ->
                    assertTrue(statement.step())
                    assertEquals(originalBody, statement.getText(0))
                    assertEquals("conflict", statement.getText(1))
                }
            }
            repo.clearAccount(first)
            assertTrue(repo.drafts(first).isEmpty())
            assertNull(repo.feature(first, "map-rebuild"))
            assertEquals(other, repo.storedDraft(second, other.id).draft)
            assertEquals("kept", repo.feature(second, "insights"))
            assertFails { files.encoded(file) }
            assertEquals("dW5ib3VuZA==", files.encoded(orphan))
            assertEquals("c2Vjb25k", files.encoded(otherFile))
            db.close(); db = testDatabase(path); repo = NoteRepository(db, files)
            assertTrue(repo.drafts(first).isEmpty())
            assertEquals(other, repo.storedDraft(second, other.id).draft)
        } finally { db.close(); folder.deleteRecursively() }
    }

    @Test
    fun accountRemovalWaitsForImportCleanupAndRecordsAcceptedFilesBeforePurge() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-remove-import-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val files = AttachmentFiles(folder.resolve("files").absolutePath)
        val repository = NoteRepository(db, files)
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        try {
            val editor = EditorController({ repository }, { null }, scope)
            withContext(dispatcher) { editor.open("account") }
            val started = CompletableDeferred<StagedAttachment>()
            val cleaned = CompletableDeferred<Unit>()
            val copying = editor.attachmentScope.launch {
                val file = files.stage("copy.txt", "text/plain", "copy".encodeToByteArray())
                started.complete(file)
                try { awaitCancellation() }
                finally { withContext(NonCancellable) { files.remove(file); cleaned.complete(Unit) } }
            }
            val copyingFile = withTimeout(5000) { started.await() }
            withContext(dispatcher) { editor.stopForRemoval() }
            assertTrue(copying.isCompleted && cleaned.isCompleted)
            assertFails { files.encoded(copyingFile) }

            val accepted = files.stage("accepted.txt", "text/plain", "accepted".encodeToByteArray())
            withContext(dispatcher) {
                editor.open("account")
                val draft = editor.state.value.draft!!
                assertTrue(editor.attach(draft.accountKey, draft.id, listOf(accepted)))
                editor.stopForRemoval()
            }
            assertTrue(repository.drafts("account").any { accepted in it.attachmentEdits().staged })
            repository.clearAccount("account")
            assertFails { files.encoded(accepted) }
            assertTrue(repository.drafts("account").isEmpty())
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            db.close(); dispatcher.close(); folder.deleteRecursively()
        }
    }
}

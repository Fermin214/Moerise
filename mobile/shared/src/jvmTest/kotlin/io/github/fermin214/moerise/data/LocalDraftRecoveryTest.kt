package io.github.fermin214.moerise.data

import androidx.sqlite.driver.bundled.BundledSQLiteDriver
import java.nio.file.Files
import kotlin.test.*
import kotlinx.coroutines.runBlocking

class LocalDraftRecoveryTest {
    @Test
    fun conflictForkKeepsOriginalRequestAndAccountPurgePreservesOtherAccountsAndFiles() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-local-drafts-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        var db = jvmDatabase(path)
        val files = AttachmentFiles(folder.resolve("files").absolutePath)
        val first = "https://notes.example\u0000one\u0000users/a"
        val second = "https://notes.example\u0000one\u0000users/b"
        try {
            var repo = NoteRepository(db, files)
            val file = files.stage("first.txt", "text/plain", "first".encodeToByteArray())
            val otherFile = files.stage("second.txt", "text/plain", "second".encodeToByteArray())
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
            BundledSQLiteDriver().open(path).use { connection ->
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
            assertEquals("c2Vjb25k", files.encoded(otherFile))
            db.close(); db = jvmDatabase(path); repo = NoteRepository(db, files)
            assertTrue(repo.drafts(first).isEmpty())
            assertEquals(other, repo.storedDraft(second, other.id).draft)
        } finally { db.close(); folder.deleteRecursively() }
    }
}

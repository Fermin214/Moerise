package io.github.fermin214.moerise.data

import androidx.room.immediateTransaction
import androidx.room.useWriterConnection
import java.nio.file.Files
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlinx.coroutines.runBlocking

class PersistenceRecoveryTest {
    @Test
    fun frozenWriteSurvivesCloseAndReopenWithoutCrossingAccounts() = runBlocking {
        val folder = Files.createTempDirectory("moerise-room-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        try {
            val first = jvmDatabase(path)
            val draft = DraftEntity("draft-a", "account-a", "提交后继续输入", 2)
            val write = PendingWriteEntity("op-a", draft.id, draft.accountKey, "fixed-key", "POST", "/notes", "{\"content\":\"已提交\"}", 1, "sending")
            first.useWriterConnection { transaction ->
                transaction.immediateTransaction {
                    first.dao().putDraft(draft)
                    first.dao().putWrite(write)
                }
            }
            first.close()
            val reopened = jvmDatabase(path)
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
}

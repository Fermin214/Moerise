package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.presentation.EditorController
import java.nio.file.Files
import java.util.concurrent.Executors
import kotlin.test.*
import kotlinx.coroutines.*

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class ContinuousDraftTest {
    @Test
    fun continuousTypingCommitsBeforeTypingStopsInRealRoom() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-continuous-").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        try {
            val repository = NoteRepository(db)
            val editor = EditorController({ repository }, { null }, scope)
            withContext(dispatcher) { editor.open("account") }
            val typing = scope.launch {
                repeat(60) { index -> editor.change("持续输入🌌-$index"); delay(40) }
            }
            delay(1400)
            assertTrue(typing.isActive)
            val during = db.dao().homeDraft("account")!!
            assertTrue(during.revision > 0)
            assertTrue(during.content.startsWith("持续输入🌌-"))
            typing.join()
            withContext(dispatcher) { assertTrue(editor.flushForLifecycle()) }
            assertEquals("持续输入🌌-59", db.dao().homeDraft("account")?.content)
            assertEquals(editor.state.value.draft?.revision, editor.state.value.savedRevision)
        } finally { scope.coroutineContext[Job]!!.cancelAndJoin(); db.close(); dispatcher.close(); folder.deleteRecursively() }
    }
}

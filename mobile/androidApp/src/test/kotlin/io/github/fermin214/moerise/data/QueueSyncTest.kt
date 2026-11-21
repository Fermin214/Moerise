package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.presentation.EditorController
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.io.IOException
import java.nio.file.Files
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.serialization.json.*

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class QueueSyncTest {
    private val origin = "https://notes.example"
    private val account = "$origin\u0000one\u0000users/owner"
    private val identity = """{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}"""
    private fun note(content: String, name: String = "memos/one") =
        Note("one", name, content, "NORMAL", "hash-$content", "2026-11-15T00:00:00Z")

    @Test
    fun offlineSavesSurviveDatabaseReopenAndDrainWithoutUploadingUnfinishedInput() = runBlocking {
        val folder = Files.createTempDirectory("moerise-queue-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        var db = testDatabase(path)
        var repo = NoteRepository(db)
        val requests = mutableListOf<String>()
        val engine = MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(identity)
            else {
                val content = CoreJson.parseToJsonElement(request.body.toByteArray().decodeToString())
                    .jsonObject.getValue("content").jsonPrimitive.content
                requests += content
                respond(CoreJson.encodeToString(note(content, "memos/n${requests.size}")))
            }
        }
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), engine)
        try {
            val first = EditorController({ repo }, { null }, scope)
            first.open(account)
            first.change("离线一")
            assertTrue(first.enqueue())
            first.change("离线二")
            assertTrue(first.enqueue())
            first.change("还没写完")
            first.flush()
            assertEquals(2, repo.queuedWrites(account).size)
            assertTrue(requests.isEmpty())
            db.close()
            db = testDatabase(path)
            repo = NoteRepository(db)
            val restored = DraftQueueFixture({ repo }, core, scope)
            val reopened = restored.composer
            reopened.open(account)
            assertEquals("还没写完", reopened.state.value.draft?.content)
            assertTrue(restored.queue.drain(account, core))
            assertEquals(listOf("离线一", "离线二"), requests)
            assertEquals("还没写完", reopened.state.value.draft?.content)
            assertTrue(repo.queuedNotes(account).isEmpty())
            assertEquals(2, repo.snapshot(account)?.notes?.size)
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun lostResponseReplaysSameKeyAndBodyAndKeepsQueueBoundToItsAccount() = runBlocking {
        val folder = Files.createTempDirectory("moerise-queue-replay-").toFile()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val requests = mutableListOf<Pair<String?, String>>()
        val engine = MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(identity)
            else {
                requests += request.headers["Idempotency-Key"] to request.body.toByteArray().decodeToString()
                if (requests.size == 1) throw IOException("response lost")
                respond(CoreJson.encodeToString(note("保留原文")))
            }
        }
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), engine)
        try {
            val fixture = DraftQueueFixture({ repo }, core, scope)
            val editor = fixture.composer
            editor.open(account)
            editor.change("保留原文")
            assertTrue(editor.enqueue())
            assertFailsWith<IOException> { fixture.queue.drain(account, core) }
            val original = repo.queuedWrites(account).single()
            assertEquals("unknown", original.status)
            editor.open("$origin\u0000one\u0000users/other")
            assertFalse(fixture.queue.drain(account, core))
            assertEquals(1, requests.size)
            editor.open(account)
            assertTrue(fixture.queue.drain(account, core))
            assertEquals(requests[0], requests[1])
            assertTrue(repo.queuedNotes(account).isEmpty())
            // 迟到的取消记账不能重新插入已经完成的队列项。
            repo.status(original, "unknown")
            assertTrue(repo.queuedWrites(account).isEmpty())
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun replayWithChangedRemoteContentKeepsOriginalAsConflict() = runBlocking {
        val folder = Files.createTempDirectory("moerise-queue-conflict-").toFile()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(identity)
            else respond(CoreJson.encodeToString(note("外部修改")))
        })
        try {
            val fixture = DraftQueueFixture({ repo }, core, scope)
            val editor = fixture.composer
            editor.open(account)
            editor.change("保留原文")
            assertTrue(editor.enqueue())
            val original = repo.queuedWrites(account).single()
            assertTrue(fixture.queue.drain(account, core))
            val conflict = repo.queuedNotes(account).single()
            assertEquals("保留原文", conflict.content)
            assertNotNull(conflict.conflictJson)
            repo.status(original, "unknown")
            assertTrue(repo.queuedWrites(account).isEmpty())
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun editingQueuedNoteDoesNotSubmitUnfinishedChangesOrLoseThemAfterConfirmation() = runBlocking {
        val folder = Files.createTempDirectory("moerise-queue-edit-").toFile()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val enteredRequest = CompletableDeferred<Unit>()
        val response = CompletableDeferred<Unit>()
        val requests = mutableListOf<String>()
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(identity)
            else {
                val content = CoreJson.parseToJsonElement(request.body.toByteArray().decodeToString())
                    .jsonObject.getValue("content").jsonPrimitive.content
                requests += content
                enteredRequest.complete(Unit)
                response.await()
                respond(CoreJson.encodeToString(note(content)))
            }
        })
        try {
            val fixture = DraftQueueFixture({ repo }, core, scope)
            val editor = fixture.composer
            editor.open(account)
            editor.change("点击保存的正文")
            assertTrue(editor.enqueue())
            val queued = repo.queuedNotes(account).single()
            assertTrue(queued.createdAt > 0)
            editor.openStored(account, queued.id)
            val sending = async { fixture.queue.drain(account, core) }
            enteredRequest.await()
            editor.change("仍在编辑，未点击保存")
            editor.flush()
            // Opening a note must not mark an active sender as recovered/unknown.
            assertEquals("sending", repo.storedDraft(account, queued.id).pending?.status)
            response.complete(Unit)
            assertTrue(sending.await())
            assertEquals(listOf("点击保存的正文"), requests)
            assertEquals("仍在编辑，未点击保存", editor.state.value.draft?.content)
            assertTrue(repo.queuedWrites(account).isEmpty())
            val preserved = repo.queuedNotes(account).single()
            assertEquals("仍在编辑，未点击保存", preserved.content)
            assertEquals("memos/one", preserved.targetName)
            assertNull(preserved.queuedRevisionJson)
            assertTrue(fixture.queue.drain(account, core))
            assertEquals(1, requests.size)
        } finally {
            response.complete(Unit)
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun savingAgainQueuesExactlyThatRevisionAndPreservesLaterUnfinishedInput() = runBlocking {
        val folder = Files.createTempDirectory("moerise-queue-revision-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        var db = testDatabase(path)
        var repo = NoteRepository(db)
        val requests = mutableListOf<Pair<HttpMethod, String>>()
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(identity)
            else {
                val body = CoreJson.parseToJsonElement(request.body.toByteArray().decodeToString()).jsonObject
                val content = body.getValue("content").jsonPrimitive.content
                requests += request.method to content
                if (request.method == HttpMethod.Patch)
                    assertEquals("hash-第一次保存", body.getValue("source_hash").jsonPrimitive.content)
                respond(CoreJson.encodeToString(note(content)))
            }
        })
        try {
            val fixture = DraftQueueFixture({ repo }, core, scope)
            val editor = fixture.composer
            editor.open(account)
            editor.change("第一次保存")
            assertTrue(editor.enqueue())
            val id = repo.queuedNotes(account).single().id
            editor.openStored(account, id)
            editor.change("第二次保存")
            assertTrue(editor.enqueue())
            editor.openStored(account, id)
            editor.change("第三次输入没有保存")
            editor.flush()
            scope.coroutineContext[Job]!!.children.toList().forEach { it.cancelAndJoin() }
            db.close()
            db = testDatabase(path)
            repo = NoteRepository(db)
            val restored = DraftQueueFixture({ repo }, core, scope)
            val reopened = restored.composer
            reopened.open(account)
            assertTrue(restored.queue.drain(account, core))
            assertEquals(listOf(HttpMethod.Post to "第一次保存", HttpMethod.Patch to "第二次保存"), requests)
            assertTrue(repo.queuedWrites(account).isEmpty())
            assertEquals("第三次输入没有保存", repo.queuedNotes(account).single().content)
            assertNull(repo.queuedNotes(account).single().queuedRevisionJson)
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun openingAnotherEditorDuringSyncKeepsItsInputAndDoesNotReplayTheCreate() = runBlocking {
        val folder = Files.createTempDirectory("moerise-queue-registry-").toFile()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repo = NoteRepository(db)
        val entered = CompletableDeferred<Unit>()
        val response = CompletableDeferred<Unit>()
        val requests = mutableListOf<String>()
        val core = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(identity)
            else {
                val content = CoreJson.parseToJsonElement(request.body.toByteArray().decodeToString())
                    .jsonObject.getValue("content").jsonPrimitive.content
                requests += content
                entered.complete(Unit)
                response.await()
                respond(CoreJson.encodeToString(note(content)))
            }
        })
        try {
            val fixture = DraftQueueFixture({ repo }, core, scope)
            val composer = fixture.composer
            composer.open(account)
            composer.change("已点击保存")
            assertTrue(composer.enqueue())
            composer.change("顶部新建仍在写")
            val id = repo.queuedNotes(account).single().id
            val sending = async { fixture.queue.drain(account, core) }
            entered.await()
            val editing = fixture.newEditor()
            editing.openStored(account, id)
            fixture.otherEditors.value = listOf(editing)
            editing.change("卡片内未点击保存的续写")
            response.complete(Unit)
            assertTrue(sending.await())
            assertEquals("卡片内未点击保存的续写", editing.state.value.draft?.content)
            assertEquals("memos/one", editing.state.value.draft?.targetName)
            assertEquals("顶部新建仍在写", composer.state.value.draft?.content)
            assertTrue(editing.close())
            assertTrue(fixture.queue.drain(account, core))
            assertEquals(listOf("已点击保存"), requests)
            assertFalse(repo.automaticWritesPending(account))
            assertEquals("卡片内未点击保存的续写", repo.queuedNotes(account).single().content)
        } finally {
            response.complete(Unit)
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
            db.close()
            folder.deleteRecursively()
        }
    }
}

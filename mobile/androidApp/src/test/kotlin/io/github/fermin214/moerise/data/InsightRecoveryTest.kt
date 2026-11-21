package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.domain.*
import io.github.fermin214.moerise.presentation.*
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.nio.file.Files
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class InsightRecoveryTest {
    @Test
    fun lostCreationAndInterruptedGenerationPersistAcrossRoomReopenWithoutAutomaticModelCall() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-insights-").toFile()
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        var scope = CoroutineScope(SupervisorJob() + dispatcher)
        val path = folder.resolve("data.sqlite3").absolutePath
        var db = testDatabase(path)
        var repository = NoteRepository(db)
        val key = "https://notes.example\u0000one\u0000users/owner"
        val id = "12345678-1234-1234-1234-123456789abc"
        val createBodies = CopyOnWriteArrayList<String>()
        val generationBodies = CopyOnWriteArrayList<ChatGenerate>()
        val gets = AtomicInteger()
        val note = Note("one", "memos/a", "材料🌌", "NORMAL", "hash")
        val material = ChatMaterial(note.name, note.sourceHash)
        var remote = ChatSession(id, "洞察", 1, listOf(material), emptyList())
        fun engine() = MockEngine { request ->
            val route = request.url.encodedPath.removePrefix("/api/app/v1")
            when {
                route == "/capabilities" -> respond("""{"login_methods":["password"],"notes":"ready"}""")
                route == "/session" -> respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""")
                route == "/chats" && request.method == HttpMethod.Get -> respond(CoreJson.encodeToString(ChatPage(emptyList())))
                route == "/chat-materials/resolve" -> respond(CoreJson.encodeToString(ChatMaterials(listOf(note), listOf(material), 3, 20_000)))
                route == "/chats" -> {
                    createBodies += request.body.toByteArray().decodeToString()
                    if (createBodies.size == 1) throw java.io.IOException("lost response")
                    respond(CoreJson.encodeToString(remote), HttpStatusCode.Created)
                }
                route == "/chats/$id" -> { gets.incrementAndGet(); respond(CoreJson.encodeToString(remote)) }
                route == "/chats/$id/messages" -> {
                    val input = CoreJson.decodeFromString<ChatGenerate>(request.body.toByteArray().decodeToString())
                    generationBodies += input
                    val running = ChatTurn("turn-${generationBodies.size}", input.requestId, input.kind,
                        ChatMessage("u", "user", input.content, 1, "completed"), ChatMessage("a", "assistant", "", 1, "running"))
                    remote = remote.copy(turns = remote.turns + running.copy(
                        assistant = running.assistant.copy(status = if (generationBodies.size == 1) "interrupted" else "completed", content = "部分🌌")))
                    fun frame(kind: String, event: ChatEvent) = "event: $kind\ndata: ${CoreJson.encodeToString(event)}\n\n"
                    val text = frame("message", ChatEvent("message", running)) + frame("delta", ChatEvent("delta", text = "部分🌌")) +
                        if (generationBodies.size == 1) "event: complete\ndata: {" else frame("complete", ChatEvent("complete", remote.turns.last()))
                    respond(text, headers = headersOf(HttpHeaders.ContentType, "text/event-stream"))
                }
                else -> error("Unexpected $route")
            }
        }
        var core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine())
        try {
            var controller = InsightController({ repository }, { core }, { key }, scope, { "zh-CN" }) { _, _ -> }
            withContext(dispatcher) { controller.visible(true) }
            withTimeout(10_000) { controller.state.first { !it.busy } }
            withContext(dispatcher) { controller.seed(note.name); controller.applyMaterials() }
            withTimeout(10_000) { controller.state.first { !it.busy && it.error != null } }
            val pendingCreate = CoreJson.decodeFromString<InsightRecovery>(repository.feature(key, "insights")!!)
            assertNotNull(pendingCreate.creation)
            assertEquals(1, createBodies.size)
            assertNull(repository.feature("another-account", "insights"))
            withContext(dispatcher) { controller.clear() }
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close(); db.close()
            db = testDatabase(path); repository = NoteRepository(db)
            core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine())
            scope = CoroutineScope(SupervisorJob() + dispatcher)
            controller = InsightController({ repository }, { core }, { key }, scope, { "zh-CN" }) { _, _ -> }
            withContext(dispatcher) { controller.visible(true) }
            withTimeout(10_000) { controller.state.first { !it.busy } }
            assertEquals(1, createBodies.size)
            withContext(dispatcher) { controller.applyMaterials() }
            withTimeout(10_000) { controller.state.first { !it.busy && it.chat != null } }
            assertEquals(createBodies[0], createBodies[1])
            assertNull(controller.state.value.recovery.creation)
            withContext(dispatcher) { controller.question("问题🌌"); controller.generate("question") }
            withTimeout(10_000) { controller.state.first { !it.busy && it.error == "CHAT_INTERRUPTED" } }
            assertEquals("部分🌌", controller.state.value.recovery.partial?.assistant?.content)
            assertEquals(generationBodies.single(), CoreJson.decodeFromString<InsightRecovery>(repository.feature(key, "insights")!!).generation?.input)
            withContext(dispatcher) { controller.clear() }
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close(); db.close()
            db = testDatabase(path); repository = NoteRepository(db)
            core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine())
            scope = CoroutineScope(SupervisorJob() + dispatcher)
            controller = InsightController({ repository }, { core }, { key }, scope, { "zh-CN" }) { _, _ -> }
            withContext(dispatcher) { controller.visible(true) }
            withTimeout(10_000) { controller.state.first { !it.busy && it.chat?.turns?.lastOrNull()?.assistant?.status == "interrupted" } }
            assertTrue(gets.get() > 0)
            assertEquals(1, generationBodies.size)
            withContext(dispatcher) { controller.retryGeneration() }
            withTimeout(10_000) { controller.state.first { !it.busy && it.recovery.generation == null } }
            assertEquals(2, generationBodies.size)
            assertNotEquals(generationBodies[0].requestId, generationBodies[1].requestId)
            assertEquals(1, generationBodies[1].version)
            assertEquals("问题🌌", generationBodies[1].content)
            assertEquals("", controller.state.value.recovery.question)
            assertEquals("completed", controller.state.value.chat?.turns?.last()?.assistant?.status)
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close(); db.close(); dispatcher.close(); folder.deleteRecursively()
        }
    }
}

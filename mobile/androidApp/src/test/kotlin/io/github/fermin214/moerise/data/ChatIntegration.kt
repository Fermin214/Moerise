package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.*
import io.ktor.http.HttpMethod
import java.io.File
import java.util.UUID
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.serialization.json.*

internal suspend fun checkLiveChat(core: CoreClient, material: Note, stats: File) = coroutineScope {
    val unicode = "Android unicode material ${UUID.randomUUID()} 🌌"
    val created = CoreJson.decodeFromString<Note>(core.api(HttpMethod.Post, "/notes",
        buildJsonObject { put("content", unicode) }.toString(), UUID.randomUUID().toString()))
    val archived = CoreJson.decodeFromString<Note>(core.api(HttpMethod.Patch, "/notes/${created.name.removePrefix("memos/")}",
        buildJsonObject { put("source_hash", created.sourceHash); put("state", "ARCHIVED") }.toString(), UUID.randomUUID().toString()))
    val names = listOf(material.name, archived.name)
    val picker = CoreJson.decodeFromString<ChatMaterialPage>(core.api(HttpMethod.Get, "/chat-materials?state=ARCHIVED&page_size=100"))
    assertTrue(picker.notes.any { it.name == archived.name })
    val compact = CoreJson.decodeFromString<CompactMaterialPage>(core.api(HttpMethod.Get,
        "/chat-materials?state=ARCHIVED&page_size=100&format=compact&tag_match=any"))
    val candidate = compact.candidates.single { it.name == archived.name }
    assertEquals(archived.sourceHash, candidate.sourceHash)
    assertEquals(unicode.codePointCount(0, unicode.length), candidate.characters)
    val resolved = CoreJson.decodeFromString<ChatMaterials>(core.api(HttpMethod.Post, "/chat-materials/resolve",
        buildJsonObject { put("names", JsonArray(names.map(::JsonPrimitive))) }.toString()))
    assertEquals(material.content.codePointCount(0, material.content.length) + unicode.codePointCount(0, unicode.length), resolved.characters)
    assertEquals(20_000, resolved.limit)
    assertEquals(material.sourceHash, resolved.materials.first().sourceHash)
    val creation = buildJsonObject {
        put("request_id", UUID.randomUUID().toString())
        put("title", "Android fixture chat")
        put("names", CoreJson.encodeToJsonElement(names))
    }.toString()
    var chat = CoreJson.decodeFromString<ChatSession>(core.api(HttpMethod.Post, "/chats", creation))
    val id = chat.id
    assertEquals(chat, CoreJson.decodeFromString<ChatSession>(core.api(HttpMethod.Post, "/chats", creation)))
    fun calls() = CoreJson.parseToJsonElement(stats.readText()).jsonObject.getValue("calls").jsonPrimitive.int
    suspend fun read() = CoreJson.decodeFromString<ChatSession>(core.api(HttpMethod.Get, "/chats/$id"))
    suspend fun send(kind: String, content: String): Pair<ChatGenerate, List<ChatEvent>> {
        chat = read()
        val request = ChatGenerate(UUID.randomUUID().toString(), chat.version, kind, content, "zh-CN")
        val events = mutableListOf<ChatEvent>()
        core.chatStream(id, request) { events += it }
        chat = read()
        return request to events
    }
    val saved = mutableListOf<Note>()
    suspend fun save(input: ChatSaveSelection) = CoreJson.decodeFromString<Note>(
        core.api(HttpMethod.Post, "/chats/$id/save-selection", CoreJson.encodeToString(input)))
    var primaryFailure: Throwable? = null
    try {
        val (generation, events) = send("insight", "")
        assertEquals("message", events.first().type)
        assertEquals("complete", events.last().type)
        assertTrue(events.any { it.type == "delta" && it.text.contains("🌌") })
        assertEquals("completed", chat.turns.last().assistant.status)
        val count = calls()
        core.chatStream(id, generation) {}
        assertEquals(count, calls())
        val turn = chat.turns.last()
        val saveBody = buildJsonObject { put("version", turn.assistant.version) }.toString()
        val path = "/chats/$id/messages/${turn.requestId}/save"
        val savedNote = CoreJson.decodeFromString<Note>(core.api(HttpMethod.Post, path, saveBody))
        saved += savedNote
        assertEquals(turn.assistant.content, savedNote.content)
        assertEquals(savedNote.name, CoreJson.decodeFromString<Note>(core.api(HttpMethod.Post, path, saveBody)).name)
        assertEquals(savedNote.content, CoreJson.decodeFromString<Note>(core.api(HttpMethod.Get, "/notes/${savedNote.name.removePrefix("memos/")}")).content)
        val answer = ChatMessageRef(turn.requestId, "assistant", turn.assistant.version)
        assertEquals(savedNote.name, save(ChatSaveSelection(UUID.randomUUID().toString(), "en", listOf(answer), false)).name)
        chat = read()
        core.api(HttpMethod.Patch, "/chats/$id", buildJsonObject {
            put("version", chat.version); put("title", chat.title)
            put("materials", CoreJson.encodeToJsonElement(listOf(resolved.materials.first())))
        }.toString())
        val (_, followUp) = send("question", "联系两条笔记")
        assertEquals("complete", followUp.last().type)
        val second = chat.turns.last()
        assertTrue(second.materialVersion > turn.materialVersion)
        assertEquals(resolved.materials, chat.turns.first().materials)
        assertEquals(listOf(resolved.materials.first()), second.materials)
        val choice = ChatSaveSelection(UUID.randomUUID().toString(), "zh-CN",
            listOf(ChatMessageRef(second.requestId, "user", second.user.version), answer), false)
        val combined = save(choice)
        saved += combined
        assertEquals("**Moerise**\n\n${turn.assistant.content}\n\n**我**\n\n${second.user.content}", combined.content)
        val receipt = CoreJson.parseToJsonElement(core.api(HttpMethod.Get, "/chats/$id/save-selection/${choice.operationKey}")).jsonObject
        assertEquals(combined.name, receipt["name"]?.jsonPrimitive?.content)
        assertEquals(combined.name, save(choice).name)
        val silentStart = System.nanoTime()
        val (_, silent) = send("question", "ANDROID_SILENT")
        assertEquals("complete", silent.last().type)
        assertTrue((System.nanoTime() - silentStart) / 1_000_000 >= 15_000)
        chat = read()
        val failing = ChatGenerate(UUID.randomUUID().toString(), chat.version, "question", "ANDROID_FAIL", "zh-CN")
        assertEquals("LLM_PROTOCOL", assertFailsWith<CoreException> { core.chatStream(id, failing) {} }.detail.code)
        chat = read()
        assertEquals("failed", chat.turns.last().assistant.status)
        assertTrue(chat.turns.last().assistant.content.isNotEmpty())
        val held = ChatGenerate(UUID.randomUUID().toString(), chat.version, "question", "ANDROID_HOLD", "zh-CN")
        val started = CompletableDeferred<Unit>()
        val stream = async {
            runCatching { core.chatStream(id, held) { if (it.type == "delta") started.complete(Unit) } }
                .onFailure { started.completeExceptionally(it) }.exceptionOrNull()
        }
        withTimeout(55_000) { started.await() }
        core.api(HttpMethod.Post, "/chats/$id/messages/${held.requestId}/cancel", "{}")
        val cancellation = withTimeout(10_000) { stream.await() }
        assertNotNull(cancellation)
        chat = read()
        assertEquals("canceled", chat.turns.last().assistant.status)
        val stopped = chat.turns.last()
        val partial = ChatSaveSelection(UUID.randomUUID().toString(), "zh-CN",
            listOf(ChatMessageRef(stopped.requestId, "assistant", stopped.assistant.version)), false)
        assertEquals("CHAT_MESSAGE_INCOMPLETE", assertFailsWith<CoreException> { save(partial) }.detail.code)
        val incomplete = save(partial.copy(confirmIncomplete = true))
        saved += incomplete
        assertEquals(stopped.assistant.content, incomplete.content)
        val afterStop = calls()
        repeat(2) { read() }
        assertEquals(afterStop, calls())
        assertTrue(CoreJson.decodeFromString<ChatPage>(core.api(HttpMethod.Get, "/chats?page_size=20")).chats.any { it.id == id })
    } catch (failure: Throwable) {
        primaryFailure = failure
        throw failure
    } finally {
        try {
            core.api(HttpMethod.Delete, "/notes/${archived.name.removePrefix("memos/")}",
                buildJsonObject { put("source_hash", archived.sourceHash) }.toString())
            chat = read()
            core.api(HttpMethod.Delete, "/chats/$id", buildJsonObject { put("version", chat.version) }.toString())
            assertEquals("CHAT_NOT_FOUND", assertFailsWith<CoreException> { read() }.detail.code)
            saved.forEach {
                val path = "/notes/${it.name.removePrefix("memos/")}"
                assertEquals(it.content, CoreJson.decodeFromString<Note>(core.api(HttpMethod.Get, path)).content)
                core.api(HttpMethod.Delete, path, buildJsonObject { put("source_hash", it.sourceHash) }.toString())
            }
        } catch (cleanup: Throwable) {
            if (primaryFailure != null) primaryFailure.addSuppressed(cleanup) else throw cleanup
        }
    }
}

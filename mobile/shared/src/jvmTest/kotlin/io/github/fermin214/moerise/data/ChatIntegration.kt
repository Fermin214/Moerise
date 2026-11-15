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
    val resolved = CoreJson.decodeFromString<ChatMaterials>(core.api(HttpMethod.Post, "/chat-materials/resolve",
        buildJsonObject { put("names", JsonArray(names.map(::JsonPrimitive))) }.toString()))
    assertEquals(material.content.codePointCount(0, material.content.length) + unicode.codePointCount(0, unicode.length), resolved.characters)
    assertEquals(20_000, resolved.limit)
    assertEquals(material.sourceHash, resolved.materials.first().sourceHash)
    val input = ChatCreate(UUID.randomUUID().toString(), "Android fixture chat", names)
    val creation = CoreJson.encodeToString(input)
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
    var saved: Note? = null
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
        saved = savedNote
        assertEquals(turn.assistant.content, savedNote.content)
        assertEquals(savedNote.name, CoreJson.decodeFromString<Note>(core.api(HttpMethod.Post, path, saveBody)).name)
        assertEquals(savedNote.content, CoreJson.decodeFromString<Note>(core.api(HttpMethod.Get, "/notes/${savedNote.name.removePrefix("memos/")}")).content)
        val (_, followUp) = send("question", "联系两条笔记")
        assertEquals("complete", followUp.last().type)
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
            runCatching { core.chatStream(id, held) { if (it.type == "delta") started.complete(Unit) } }.exceptionOrNull()
        }
        withTimeout(10_000) { started.await() }
        core.api(HttpMethod.Post, "/chats/$id/messages/${held.requestId}/cancel", "{}")
        val cancellation = withTimeout(10_000) { stream.await() }
        assertNotNull(cancellation)
        chat = read()
        assertEquals("canceled", chat.turns.last().assistant.status)
        val afterStop = calls()
        repeat(2) { read() }
        assertEquals(afterStop, calls())
        assertTrue(CoreJson.decodeFromString<ChatPage>(core.api(HttpMethod.Get, "/chats?page_size=20")).chats.any { it.id == id })
    } finally {
        core.api(HttpMethod.Delete, "/notes/${archived.name.removePrefix("memos/")}",
            buildJsonObject { put("source_hash", archived.sourceHash) }.toString())
        saved?.let { core.api(HttpMethod.Delete, "/notes/${it.name.removePrefix("memos/")}",
            buildJsonObject { put("source_hash", it.sourceHash) }.toString()) }
        chat = read()
        core.api(HttpMethod.Delete, "/chats/$id", buildJsonObject { put("version", chat.version) }.toString())
        assertEquals("CHAT_NOT_FOUND", assertFailsWith<CoreException> { read() }.detail.code)
    }
}

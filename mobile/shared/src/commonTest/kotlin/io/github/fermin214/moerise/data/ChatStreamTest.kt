package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.*
import kotlin.test.*
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*

class ChatStreamTest {
    private fun turn(status: String, content: String) = ChatTurn("turn", "request", "question",
        ChatMessage("user", "user", "问题", 1, "completed"), ChatMessage("assistant", "assistant", content, 2, status))
    private fun frame(kind: String, value: ChatEvent, newline: String = "\r\n") =
        "event: $kind${newline}data: ${CoreJson.encodeToString(value)}$newline$newline"

    @Test
    fun singleByteChunksKeepUtf8CrLfAndMultilineFrames() = runBlocking<Unit> {
        val events = mutableListOf<ChatEvent>()
        val parser = ChatStreamParser("request") { events += it }
        val initial = frame("message", ChatEvent("message", turn("running", "")))
        val delta = "event: delta\r\ndata: {\"type\":\"delta\",\r\ndata: \"text\":\"中文🌌\"}\r\n\r\n"
        val complete = frame("complete", ChatEvent("complete", turn("completed", "中文🌌")))
        val input = ("\uFEFF:comment\r\n" + initial + delta + complete).encodeToByteArray()
        for (byte in input) parser.feed(byteArrayOf(byte))
        parser.finish()
        assertEquals(listOf("message", "delta", "complete"), events.map { it.type })
        assertEquals("中文🌌", events[1].text)
        assertEquals("completed", events[2].turn?.assistant?.status)
    }

    @Test
    fun interruptedInvalidAndErrorStreamsCannotComplete() = runBlocking<Unit> {
        val initial = frame("message", ChatEvent("message", turn("running", "")), "\n")
        val valid = frame("complete", ChatEvent("complete", turn("completed", "已完成")), "\n")
        val interrupted = listOf(initial, initial + valid.dropLast(1), initial + "event: delta\ndata: {",
            initial + "data: {\"type\":\"delta\",\"text\":\"x\"}\n\n")
        for (input in interrupted) {
            val parser = ChatStreamParser("request") {}
            assertFailsWith<IllegalArgumentException> { parser.feed(input.encodeToByteArray()); parser.finish() }
        }
        val wrongId = frame("complete", ChatEvent("complete", turn("completed", "x").copy(requestId = "other")))
        assertEquals("CHAT_PROTOCOL", assertFailsWith<IllegalArgumentException> {
            ChatStreamParser("request") {}.feed((initial + wrongId).encodeToByteArray())
        }.message)
        assertEquals("CHAT_PROTOCOL", assertFailsWith<IllegalArgumentException> {
            ChatStreamParser("request") {}.feed((initial + valid + valid).encodeToByteArray())
        }.message)
        assertEquals("CHAT_PROTOCOL", assertFailsWith<IllegalArgumentException> {
            ChatStreamParser("request") {}.feed(byteArrayOf(0xff.toByte(), 10))
        }.message)
        val events = mutableListOf<ChatEvent>()
        val parser = ChatStreamParser("request") { events += it }
        val error = """event: error
data: {"error":{"code":"LLM_PROTOCOL"},"turn":${CoreJson.encodeToString(turn("failed", "部分"))}}

"""
        val thrown = assertFailsWith<CoreException> { parser.feed((initial + error).encodeToByteArray()) }
        assertEquals("LLM_PROTOCOL", thrown.detail.code)
        assertEquals("部分", events.last().turn?.assistant?.content)
    }
}

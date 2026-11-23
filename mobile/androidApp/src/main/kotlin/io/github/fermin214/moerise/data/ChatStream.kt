package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.*
import kotlinx.serialization.json.*

/**
 * Consumes bytes so UTF-8 code points split across network chunks are decoded only after a complete line.
 * A frame is delivered only after its terminating blank line.
 */
class ChatStreamParser(private val requestId: String, private val onEvent: suspend (ChatEvent) -> Unit) {
    private var bytes = ByteArray(4096)
    private var size = 0
    private var carriageReturn = false
    private var firstLine = true
    private var kind = "message"
    private val data = mutableListOf<String>()
    private var frameBytes = 0
    private var completed = false
    private var started = false
    private var turnId: String? = null

    suspend fun feed(chunk: ByteArray, count: Int = chunk.size) {
        for (index in 0 until count) {
            val byte = chunk[index]
            if (carriageReturn) {
                carriageReturn = false
                if (byte == '\n'.code.toByte()) continue
            }
            if (byte == '\r'.code.toByte() || byte == '\n'.code.toByte()) {
                line()
                carriageReturn = byte == '\r'.code.toByte()
            } else {
                protocol(frameBytes + size < 8 * 1024 * 1024)
                if (size == bytes.size) bytes = bytes.copyOf(bytes.size * 2)
                bytes[size++] = byte
            }
        }
    }

    private suspend fun line() {
        var value = try { bytes.decodeToString(0, size, throwOnInvalidSequence = true) }
        catch (_: Exception) { throw IllegalArgumentException("CHAT_PROTOCOL") }
        frameBytes += size
        size = 0
        if (firstLine) { value = value.removePrefix("\uFEFF"); firstLine = false }
        if (value.isEmpty()) {
            if (data.isNotEmpty()) dispatch()
            kind = "message"; data.clear(); frameBytes = 0
            return
        }
        if (value.startsWith(":")) return
        val separator = value.indexOf(':')
        val field = if (separator < 0) value else value.substring(0, separator)
        val content = if (separator < 0) "" else value.substring(separator + 1).removePrefix(" ")
        when (field) {
            "event" -> kind = content
            "data" -> data += content
        }
    }

    private suspend fun dispatch() {
        protocol(!completed && kind in setOf("message", "delta", "complete", "error"))
        val json = try { CoreJson.parseToJsonElement(data.joinToString("\n")).jsonObject }
        catch (_: Exception) { throw IllegalArgumentException("CHAT_PROTOCOL") }
        if (kind == "error") {
            val detail = try { CoreJson.decodeFromJsonElement<ApiError>(json.getValue("error")) }
            catch (_: Exception) { throw IllegalArgumentException("CHAT_PROTOCOL") }
            protocol(detail.code.isNotBlank())
            json["turn"]?.takeIf { it is JsonObject }?.let {
                val turn = CoreJson.decodeFromJsonElement<ChatTurn>(it)
                validateTurn(turn)
                onEvent(ChatEvent("message", turn))
            }
            throw CoreException(200, detail)
        }
        val event = try { CoreJson.decodeFromJsonElement<ChatEvent>(json) }
        catch (_: Exception) { throw IllegalArgumentException("CHAT_PROTOCOL") }
        protocol(event.type == kind)
        if (kind == "delta") protocol(started && event.text.isNotEmpty())
        else {
            val turn = event.turn ?: throw IllegalArgumentException("CHAT_PROTOCOL")
            validateTurn(turn)
            protocol(kind != "complete" || turn.assistant.status in setOf("completed", "truncated"))
            started = true
        }
        onEvent(event)
        completed = kind == "complete"
    }

    private fun validateTurn(turn: ChatTurn) {
        protocol(turn.id.isNotBlank() && turn.requestId == requestId && turn.user.role == "user" &&
            turn.assistant.role == "assistant" && turn.user.id.isNotBlank() && turn.assistant.id.isNotBlank() &&
            (turnId == null || turnId == turn.id))
        turnId = turn.id
    }

    fun finish() {
        if (size != 0 || data.isNotEmpty() || !completed) throw IllegalArgumentException("CHAT_INTERRUPTED")
    }
    private fun protocol(condition: Boolean) { require(condition) { "CHAT_PROTOCOL" } }
}

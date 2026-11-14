package io.github.fermin214.moerise.domain

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable data class ChatMaterial(val name: String, @SerialName("source_hash") val sourceHash: String)
@Serializable data class ChatMessage(
    val id: String, val role: String, val content: String, val version: Long, val status: String,
    @SerialName("error_code") val errorCode: String = "", @SerialName("saved_name") val savedName: String = "",
)
@Serializable data class ChatTurn(
    val id: String, @SerialName("request_id") val requestId: String, val kind: String,
    val user: ChatMessage, val assistant: ChatMessage,
)
@Serializable data class ChatSummary(
    val id: String, val title: String, val preview: String, val version: Long,
    @SerialName("material_count") val materialCount: Int, @SerialName("updated_at") val updatedAt: String,
)
@Serializable data class ChatSession(
    val id: String, val title: String, val version: Long, val materials: List<ChatMaterial>, val turns: List<ChatTurn>,
)
@Serializable data class ChatPage(val chats: List<ChatSummary>, @SerialName("next_page_token") val nextPageToken: String = "")
@Serializable data class ChatMaterials(val notes: List<Note>, val materials: List<ChatMaterial>, val characters: Int, val limit: Int)
@Serializable data class ChatMaterialPage(val notes: List<Note>, @SerialName("next_page_token") val nextPageToken: String = "")
@Serializable data class ChatEvent(val type: String, val turn: ChatTurn? = null, val text: String = "")
@Serializable data class ChatCreate(
    @SerialName("request_id") val requestId: String, val title: String, val names: List<String>,
)
@Serializable data class ChatGenerate(
    @SerialName("request_id") val requestId: String, val version: Long, val kind: String, val content: String, val language: String,
)
@Serializable data class ChatGeneration(val chatId: String, val input: ChatGenerate)
@Serializable data class ChatMutation(val method: String, val path: String, val body: String)
@Serializable data class InsightRecovery(
    val chatId: String? = null, val selected: List<String> = emptyList(), val question: String = "",
    val creation: ChatCreate? = null, val generation: ChatGeneration? = null, val mutation: ChatMutation? = null,
    val partial: ChatTurn? = null,
)

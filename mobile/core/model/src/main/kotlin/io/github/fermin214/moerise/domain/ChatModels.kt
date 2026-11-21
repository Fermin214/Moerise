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
    @SerialName("material_version") val materialVersion: Long = 1,
    val materials: List<ChatMaterial> = emptyList(),
)
@Serializable data class ChatSummary(
    val id: String, val title: String, val preview: String, val version: Long,
    @SerialName("material_count") val materialCount: Int, @SerialName("updated_at") val updatedAt: String,
    @SerialName("preview_kind") val previewKind: String = "",
)
@Serializable data class ChatSession(
    val id: String, val title: String, val version: Long, val materials: List<ChatMaterial>, val turns: List<ChatTurn>,
    @SerialName("material_version") val materialVersion: Long = 1,
)
@Serializable data class ChatPage(val chats: List<ChatSummary>, @SerialName("next_page_token") val nextPageToken: String = "")
@Serializable data class ChatMaterials(val notes: List<Note>, val materials: List<ChatMaterial>, val characters: Int, val limit: Int)
@Serializable data class ChatMaterialPage(val notes: List<Note>, @SerialName("next_page_token") val nextPageToken: String = "")
@Serializable data class MaterialCandidate(
    val name: String, @SerialName("source_hash") val sourceHash: String, val characters: Int,
    val preview: String, @SerialName("create_time") val createTime: String, val state: String,
) {
    fun reference() = ChatMaterial(name, sourceHash)
}
@Serializable data class CompactMaterialPage(
    val candidates: List<MaterialCandidate>, @SerialName("next_page_token") val nextPageToken: String = "",
    @SerialName("character_limit") val characterLimit: Int, @SerialName("source_limit") val sourceLimit: Int,
)
@Serializable data class MaterialDateGroup(
    val from: String = "", val through: String = "", val automatic: Set<String> = emptySet(),
    val excluded: Set<String> = emptySet(),
)
@Serializable data class ChatEvent(val type: String, val turn: ChatTurn? = null, val text: String = "")
@Serializable data class ChatCreate(
    @SerialName("request_id") val requestId: String, val title: String, val names: List<String>,
    val materials: List<ChatMaterial>? = null,
)
@Serializable data class ChatMessageRef(@SerialName("request_id") val requestId: String, val role: String, val version: Long)
@Serializable data class ChatSaveSelection(
    @SerialName("operation_key") val operationKey: String, val language: String, val messages: List<ChatMessageRef>,
    @SerialName("confirm_incomplete") val confirmIncomplete: Boolean,
)
@Serializable data class ChatSaveIntent(val chatId: String, val input: ChatSaveSelection)
@Serializable data class ChatGenerate(
    @SerialName("request_id") val requestId: String, val version: Long, val kind: String, val content: String, val language: String,
)
@Serializable data class ChatGeneration(val chatId: String, val input: ChatGenerate)
@Serializable data class ChatMutation(val method: String, val path: String, val body: String)
@Serializable data class InsightRecovery(
    val chatId: String? = null, val selected: List<String> = emptyList(), val question: String = "",
    val creation: ChatCreate? = null, val generation: ChatGeneration? = null, val mutation: ChatMutation? = null,
    val partial: ChatTurn? = null,
    val selectionInitialized: Boolean = false,
    val dateGroup: MaterialDateGroup = MaterialDateGroup(),
    val candidates: List<MaterialCandidate> = emptyList(),
)

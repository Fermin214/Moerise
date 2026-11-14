package io.github.fermin214.moerise.domain

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject

@Serializable
data class SessionUser(val name: String, @SerialName("display_name") val displayName: String = "")

@Serializable
data class SessionIdentity(
    @SerialName("instance_id") val instanceId: String,
    val user: SessionUser,
    val timezone: String,
    @SerialName("expires_at") val expiresAt: String,
    @SerialName("csrf_token") val csrfToken: String,
) {
    fun accountKey(origin: String): String = "$origin\u0000$instanceId\u0000${user.name}"
}

@Serializable
data class Capabilities(@SerialName("login_methods") val loginMethods: List<String>, val notes: String)

@Serializable
data class Note(
    @SerialName("instance_id") val instanceId: String,
    val name: String,
    val content: String,
    val state: String,
    @SerialName("source_hash") val sourceHash: String,
    @SerialName("create_time") val createTime: String = "",
    @SerialName("update_time") val updateTime: String = "",
    val tags: List<String> = emptyList(),
    val attachments: List<JsonObject> = emptyList(),
)

@Serializable
data class NotePage(val notes: List<Note>, @SerialName("next_page_token") val nextPageToken: String = "")

@Serializable
data class ApiError(val code: String, val message: String = "", val retryable: Boolean = false, @SerialName("request_id") val requestId: String = "")

@Serializable
data class ErrorEnvelope(val error: ApiError)

class CoreException(val status: Int, val detail: ApiError) : Exception(detail.code)
class IdentityChangedException : Exception("ACCOUNT_CHANGED")

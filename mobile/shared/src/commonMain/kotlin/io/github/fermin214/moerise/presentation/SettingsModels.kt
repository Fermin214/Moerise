package io.github.fermin214.moerise.presentation

import io.ktor.http.Url
import kotlinx.serialization.json.*

data class SecretEdit(val action: String = "keep", val value: String = "") {
    override fun toString(): String = "SecretEdit(action=$action)"
}

data class SettingsUiState(
    val snapshot: JsonObject? = null,
    val values: Map<String, String> = emptyMap(),
    val secrets: Map<String, SecretEdit> = emptyMap(),
    val times: List<String> = emptyList(),
    val loading: Boolean = false,
    val busy: Boolean = false,
    val conflict: Boolean = false,
    val error: String? = null,
    val status: String? = null,
    val models: JsonObject? = null,
    val checking: Set<String> = emptySet(),
    val checks: Map<String, JsonObject> = emptyMap(),
    val checkErrors: Map<String, String> = emptyMap(),
    val deliveries: List<JsonObject>? = null,
    val resending: Set<String> = emptySet(),
)

internal object SettingsForm {
    val groups = linkedMapOf(
        "review" to listOf("count", "min_age_hours", "recent_days", "include_archived"),
        "notifications" to listOf("enabled"),
        "stargazing" to listOf("enabled"),
        "llm" to listOf("enabled", "protocol", "base_url", "model", "timeout_seconds",
            "max_concurrency", "max_retries", "max_output_tokens"),
        "embedding" to listOf("enabled", "provider", "protocol", "base_url", "model", "dimensions",
            "send_dimensions", "batch_size", "timeout_seconds", "max_concurrency", "max_retries", "query_instruction"),
        "channel" to listOf("kind", "app_id", "user_open_id", "destination", "qq_app_id", "feishu_mode", "webhook_images", "webhook_app_id"),
        "insights" to listOf("default_prompt"),
    )
    val paths = setOf("timezone") + groups.flatMap { (group, names) -> names.map { "$group.$it" } }
    val secretNames = setOf("llm_api_key", "embedding_api_key", "app_secret", "verification_token",
        "encrypt_key", "webhook_url", "webhook_secret", "bot_token", "qq_app_secret", "webhook_app_secret")
    val modelGroups = setOf("llm", "embedding")
    private val booleans = setOf("review.include_archived", "notifications.enabled",
        "llm.enabled", "embedding.enabled", "embedding.send_dimensions", "stargazing.enabled", "channel.webhook_images")
    private val nullable = setOf("llm.base_url", "llm.model", "embedding.base_url", "embedding.model",
        "embedding.query_instruction", "channel.app_id", "channel.user_open_id", "channel.destination",
        "channel.qq_app_id", "channel.webhook_app_id")
    private val choices = mapOf(
        "llm.protocol" to setOf("openai-compatible", "openai-responses", "anthropic"),
        "embedding.provider" to setOf("bailian", "openai-compatible"),
        "embedding.protocol" to setOf("dashscope", "openai-compatible"),
        "channel.kind" to setOf("none", "qq", "feishu-app", "feishu-webhook", "telegram"),
        "channel.feishu_mode" to setOf("callback", "websocket"),
    )
    private val ranges = mapOf(
        "review.count" to 1.0..20.0,
        "review.min_age_hours" to 0.0..87600.0,
        "review.recent_days" to 0.0..3650.0,
        "llm.timeout_seconds" to 1.0..110.0,
        "llm.max_concurrency" to 1.0..8.0,
        "llm.max_retries" to 0.0..3.0,
        "llm.max_output_tokens" to 32.0..16384.0,
        "embedding.dimensions" to 1.0..16384.0,
        "embedding.batch_size" to 1.0..20.0,
        "embedding.timeout_seconds" to 1.0..120.0,
        "embedding.max_concurrency" to 1.0..8.0,
        "embedding.max_retries" to 0.0..3.0,
    )
    private val decimal = Regex("-?(?:[0-9]+(?:\\.[0-9]+)?|\\.[0-9]+)(?:[eE][+-]?[0-9]+)?")
    private val clock = Regex("(?:[01][0-9]|2[0-3]):[0-5][0-9]")
    private val scheme = Regex("^[A-Za-z][A-Za-z0-9+.-]*:")
    private val zoneName = Regex("[A-Za-z][A-Za-z0-9._+/-]*")

    fun version(snapshot: JsonObject, positive: Boolean = true): Long {
        val value = (snapshot["version"] as? JsonPrimitive)?.takeUnless { it.isString }?.longOrNull
        require(value != null && value in 0L..9007199254740991L &&
            (!positive || value >= 1)) { "SETTINGS_INPUT_INVALID" }
        return value
    }

    fun original(snapshot: JsonObject, path: String): JsonElement? {
        val settings = snapshot["settings"] as? JsonObject ?: return null
        val group = path.substringBefore('.')
        val name = path.substringAfter('.', "")
        return if (name.isEmpty()) settings[group] else (settings[group] as? JsonObject)?.get(name)
    }

    fun readSnapshot(response: JsonObject): JsonObject {
        version(response, positive = false)
        val settings = response["settings"] as? JsonObject ?: invalid()
        require(groups.keys.all { settings[it] is JsonObject }) { "SETTINGS_INPUT_INVALID" }
        val public = buildJsonObject {
            put("timezone", settings["timezone"] as? JsonPrimitive ?: invalid())
            for ((group, names) in groups) put(group, buildJsonObject {
                val source = settings.getValue(group).jsonObject
                for (name in names) put(name, source[name] ?: when ("$group.$name") {
                    "channel.qq_app_id", "channel.webhook_app_id" -> JsonNull
                    "channel.feishu_mode" -> JsonPrimitive("websocket")
                    "channel.webhook_images" -> JsonPrimitive(false)
                    else -> invalid()
                })
                if (group == "notifications") {
                    val times = source["times"] as? JsonArray ?: invalid()
                    require(times.all { it is JsonPrimitive && it.isString }) { "SETTINGS_INPUT_INVALID" }
                    put("times", times)
                }
            })
        }
        for (path in paths) {
            val group = path.substringBefore('.')
            val name = path.substringAfter('.', "")
            val value = if (name.isEmpty()) public[group] else public.getValue(group).jsonObject[name]
            require(value is JsonPrimitive) { "SETTINGS_INPUT_INVALID" }
            when {
                path in booleans -> require(!value.isString && value.booleanOrNull != null) { "SETTINGS_INPUT_INVALID" }
                path in ranges -> require(!value.isString && value.doubleOrNull?.isFinite() == true) { "SETTINGS_INPUT_INVALID" }
                value == JsonNull -> require(path in nullable) { "SETTINGS_INPUT_INVALID" }
                else -> require(value.isString) { "SETTINGS_INPUT_INVALID" }
            }
        }
        // 表单快照仅接收公开字段与密钥配置状态。
        return buildJsonObject {
            put("version", version(response, positive = false))
            put("settings", public)
            put("channel_status", channelStatus(response))
            put("secrets", buildJsonObject {
                val source = response["secrets"] as? JsonObject
                for (name in secretNames) put(name, buildJsonObject {
                    put("configured", ((source?.get(name) as? JsonObject)?.get("configured") as? JsonPrimitive)
                        ?.takeUnless { it.isString }?.booleanOrNull == true)
                })
            })
            put("channel_capabilities", buildJsonObject {
                val source = response["channel_capabilities"] as? JsonObject
                for (name in listOf("text", "images", "record"))
                    put(name, (source?.get(name) as? JsonPrimitive)?.takeUnless { it.isString }?.booleanOrNull == true)
            })
            (response["default_insight_prompt"] as? JsonPrimitive)?.takeIf { it.isString }?.let {
                put("default_insight_prompt", it)
            }
        }
    }

    fun values(snapshot: JsonObject): Map<String, String> = normalize(paths.associateWith {
        (original(snapshot, it) as? JsonPrimitive)?.contentOrNull ?: ""
    })

    fun times(snapshot: JsonObject): List<String> =
        original(snapshot, "notifications.times")!!.jsonArray.map { it.jsonPrimitive.content }

    fun normalize(values: Map<String, String>): Map<String, String> = values.toMutableMap().apply {
        if (this["embedding.protocol"] == "openai-compatible") this["embedding.query_instruction"] = ""
        else this["embedding.send_dimensions"] = "false"
        if (this["channel.kind"] == "none") this["notifications.enabled"] = "false"
    }

    fun channelStatus(response: JsonObject): JsonObject = buildJsonObject {
        val statuses = response["channel_status"] as? JsonObject ?: return@buildJsonObject
        for (kind in listOf("qq", "telegram", "feishu-app")) {
            val channel = statuses[kind] as? JsonObject ?: continue
            put(kind, buildJsonObject {
                for (key in listOf("state", "detail", "link", "peer")) {
                    (channel[key] as? JsonPrimitive)?.takeIf { it.isString }?.let { put(key, it) }
                }
            })
        }
    }

    fun value(path: String, text: String): JsonPrimitive = when {
        path in choices -> {
            require(text in choices.getValue(path)) { "SETTINGS_INPUT_INVALID" }
            JsonPrimitive(text)
        }
        path in booleans -> {
            require(text == "true" || text == "false") { "SETTINGS_INPUT_INVALID" }
            JsonPrimitive(text == "true")
        }
        path in ranges -> {
            require(decimal.matches(text)) { "SETTINGS_INPUT_INVALID" }
            val number = text.toDoubleOrNull() ?: invalid()
            require(number.isFinite() && number in ranges.getValue(path)) { "SETTINGS_INPUT_INVALID" }
            if (path.endsWith(".timeout_seconds")) JsonPrimitive(number)
            else {
                require(number % 1.0 == 0.0) { "SETTINGS_INPUT_INVALID" }
                JsonPrimitive(number.toInt())
            }
        }
        else -> JsonPrimitive(text)
    }

    fun same(first: JsonElement?, second: JsonElement?): Boolean {
        if (first is JsonPrimitive && second is JsonPrimitive &&
            first != JsonNull && second != JsonNull && !first.isString && !second.isString) {
            val number = first.doubleOrNull
            if (number != null) return number == second.doubleOrNull
        }
        return first == second
    }

    fun patch(snapshot: JsonObject, state: SettingsUiState): JsonObject {
        val version = version(snapshot)
        val timezone = state.values["timezone"] ?: invalid()
        require(zoneName.matches(timezone) && runCatching { serviceMonth(timezone) }.isSuccess) { "SETTINGS_INPUT_INVALID" }
        require(state.times.size in 1..24 && state.times.all(clock::matches) &&
            state.times.distinct().size == state.times.size) { "SETTINGS_INPUT_INVALID" }
        val images = state.values["channel.webhook_images"] == "true"
        val changes = linkedMapOf<String, JsonElement>()
        for (path in paths) {
            if (path == "channel.webhook_app_id" && !images) continue
            val text = state.values[path] ?: invalid()
            val previous = original(snapshot, path)
            if (text.isEmpty() && (previous == null || previous == JsonNull)) continue
            if (path in choices && same(JsonPrimitive(text), previous)) continue
            val next = value(path, text)
            if (path.endsWith(".base_url") && text.isNotEmpty()) {
                require(scheme.containsMatchIn(text) &&
                    runCatching {
                        val url = Url(text)
                        url.protocol.name !in setOf("http", "https", "ftp", "ws", "wss") || url.host.isNotBlank()
                    }.getOrDefault(false)) { "SETTINGS_INPUT_INVALID" }
            }
            if (!same(next, previous)) changes[path] = next
        }
        val clocks = JsonArray(state.times.map(::JsonPrimitive))
        if (clocks != original(snapshot, "notifications.times")) changes["notifications.times"] = clocks
        return buildJsonObject {
            put("version", version)
            put("settings", buildJsonObject {
                changes["timezone"]?.let { put("timezone", it) }
                for (group in groups.keys) {
                    val fields = changes.filterKeys { it.startsWith("$group.") }
                    if (fields.isNotEmpty()) put(group, buildJsonObject {
                        for ((path, value) in fields) put(path.substringAfter('.'), value)
                    })
                }
            })
            put("secrets", buildJsonObject {
                for (name in secretNames) {
                    if (name in setOf("verification_token", "encrypt_key") && state.values["channel.feishu_mode"] != "callback") continue
                    val edit = state.secrets[name] ?: continue
                    if (name == "webhook_app_secret" && !images && edit.action != "clear") continue
                    if (edit.action == "keep") continue
                    require(edit.action in setOf("keep", "replace", "clear")) { "SETTINGS_INPUT_INVALID" }
                    if (edit.action == "replace")
                        require(edit.value.isNotBlank() && edit.value.none { it in "*•●…" }) { "SETTINGS_INPUT_INVALID" }
                    else require(edit.value.isEmpty()) { "SETTINGS_INPUT_INVALID" }
                    put(name, buildJsonObject {
                        put("action", edit.action)
                        if (edit.action == "replace") put("value", edit.value)
                    })
                }
            })
        }
    }

    private fun invalid(): Nothing = throw IllegalArgumentException("SETTINGS_INPUT_INVALID")
}

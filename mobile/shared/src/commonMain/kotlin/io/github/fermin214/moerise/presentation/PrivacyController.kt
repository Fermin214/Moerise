package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.*
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

interface InstallPreferences {
    fun read(key: String): String?
    fun write(key: String, value: String?)
    val clientVersion: String
    val systemMajor: String
    fun processExits(since: Long): List<Pair<Long, String>> = emptyList()
}

@Serializable data class UsageRow(val operation: String, val result: String, val duration: String, val source: String, val count: Int)
@Serializable data class UsageBatch(
    val schema: Int = 1, val platform: String = "android", @SerialName("client_version") val clientVersion: String,
    @SerialName("core_version") val coreVersion: String, @SerialName("system_major") val systemMajor: String,
    val rows: List<UsageRow>,
)
@Serializable private data class UsageWindow(val start: Long, val batch: UsageBatch)
data class PrivacyState(val welcome: Boolean = false, val enabled: Boolean = false, val allowed: Boolean? = null,
    val coreVersion: String = "unknown", val diagnostic: String = "")

class PrivacyController(
    private val preferences: InstallPreferences?, private val session: SessionController, private val scope: CoroutineScope,
) {
    private val mutable = MutableStateFlow(PrivacyState(welcome = preferences != null && preferences.read("choice-v1") == null,
        enabled = preferences?.read("choice-v1") == "on"))
    val state = mutable.asStateFlow()
    private var owner: String? = null
    private var active: CoreClient? = null
    private var policy: Job? = null
    private var sender: Job? = null
    private var foreground = false
    private var recentlyConnected = false
    private var diagnosticAt = 0L
    private var version = 0L
    private var grantedAt = 0L
    private var editorReady = false
    private var startup: Pair<Long, Long>? = null
    private val interruptedWrite = preferences?.read("in-flight")
    private var recovered = false
    private val day = 86_400_000L
    private val codes = setOf("CHAT_CONFLICT", "SOURCE_CHANGED", "CHAT_RESOURCE_LIMIT", "CHAT_MATERIAL_LIMIT", "SOURCE_NOT_FOUND",
        "WRITE_CONFLICT", "REQUEST_TIMEOUT", "CONNECTION_FAILED", "LLM_PROTOCOL", "CHAT_INTERRUPTED", "CHAT_CANCELED", "SESSION_EXPIRED")
    private val operations = setOf("create", "edit", "enqueue", "save_ack", "sync", "search_keyword", "search_semantic", "search_hybrid",
        "review", "walk", "stars", "insight", "material_date", "material_tags", "material_manual", "save_answer", "save_multiple",
        "save_question", "startup", "client_error", "request_error", "crash", "anr")
    private val results = setOf("success", "failure", "canceled", "conflict", "unknown", "timeout", "over_limit")

    init {
        if (preferences != null) scope.launch {
            session.state.collect { connection ->
                if (connection.loading) return@collect
                val key = connection.profile?.accountKey
                val client = session.core
                if (key != owner || active !== client || connection.needsLogin) {
                    policy?.cancel(); sender?.cancel(); version++
                    if (preferences.read("owner") != key || connection.needsLogin) {
                        discard(); preferences.write("exit-after", null); preferences.write("owner", key)
                    }
                    owner = key; active = client
                    recentlyConnected = false
                    grantedAt = 0
                    mutable.update { it.copy(allowed = null, coreVersion = "unknown", diagnostic = "") }
                }
                if (key != null && editorReady && connection.connected && !connection.needsLogin && (!recentlyConnected || state.value.allowed == null) &&
                    policy?.isActive != true) refreshPolicy()
                recentlyConnected = connection.connected
            }
        }
    }

    fun foreground(value: Boolean) {
        foreground = value
        if (value) {
            if (editorReady) refreshPolicy()
            if (epochMillis() - diagnosticAt >= day) mutable.update { it.copy(diagnostic = "") }
        } else sender?.cancel()
    }

    fun editorDisplayed(start: Long?) {
        editorReady = true
        if (start != null && state.value.enabled && preferences?.read("owner") == session.state.value.profile?.accountKey)
            startup = start to epochMillis()
        refreshPolicy()
    }

    private fun refreshPolicy() {
        val key = owner ?: return
        val client = active ?: return
        if (!session.state.value.connected || policy?.isActive == true || preferences == null) return
        policy = scope.launch {
            val cap = try {
                if (state.value.allowed == null) client.lastCapabilities ?: client.capabilities() else client.capabilities()
            } catch (failure: Exception) {
                if (failure is CancellationException) throw failure
                return@launch
            }
            if (owner != key || active !== client || !session.state.value.connected) return@launch
            val allowed = cap.telemetry.schema == 1 && cap.telemetry.allowed
            val coreVersion = safeVersion(cap.version)
            if (!allowed || state.value.coreVersion !in listOf("unknown", coreVersion)) {
                version++; sender?.cancel(); discard(); preferences.write("exit-after", null)
                grantedAt = epochMillis()
                if (!allowed) startup = null
            }
            mutable.update { it.copy(allowed = allowed, coreVersion = coreVersion) }
            if (allowed && state.value.enabled) {
                if (grantedAt == 0L) grantedAt = preferences.read("consent-at")?.toLongOrNull() ?: epochMillis()
                cleanExpired()
                if (!recovered) {
                    recovered = true
                    if (interruptedWrite != null && preferences.read("in-flight") == interruptedWrite)
                        endWrite(interruptedWrite, "unknown")
                }
                startup?.let { (start, finish) -> record("startup", started = start, finished = finish) }
                startup = null
                countProcessExits()
                sendIfDue()
            }
        }
    }

    fun choose(enabled: Boolean) {
        val on = enabled && state.value.allowed != false
        val prefs = preferences ?: return
        version++; sender?.cancel(); discard()
        startup = null
        grantedAt = if (on) epochMillis() else 0L
        prefs.write("consent-at", grantedAt.takeIf { on }?.toString())
        prefs.write("exit-after", grantedAt.takeIf { on }?.toString())
        prefs.write("choice-v1", if (on) "on" else "off")
        mutable.update { it.copy(welcome = false, enabled = on) }
        if (on) refreshPolicy()
    }

    private fun discard() {
        preferences?.write("aggregate", null)
        preferences?.write("pending", null)
        preferences?.write("in-flight", null)
    }

    private fun safeVersion(value: String) = value.takeIf { it.matches(Regex("(0|[1-9][0-9]{0,2})(\\.(0|[1-9][0-9]{0,2})){2}")) } ?: "unknown"
    private fun duration(elapsed: Long?) = when {
        elapsed == null || elapsed < 0 -> "unknown"
        elapsed < 300 -> "lt300ms"
        elapsed < 1000 -> "lt1s"
        elapsed < 3000 -> "lt3s"
        elapsed < 10_000 -> "lt10s"
        else -> "ge10s"
    }

    fun record(operation: String, result: String = "success", started: Long? = null, source: String = "user",
        error: Exception? = null, code: String? = null, finished: Long = epochMillis()) {
        if (operation !in operations || result !in results || source !in setOf("user", "background", "prefetch")) return
        val now = epochMillis()
        val elapsed = duration(started?.let { finished - it })
        if (result in setOf("failure", "conflict", "timeout", "unknown", "over_limit")) {
            val detail = (error as? CoreException)?.detail
            val known = (detail?.code ?: code)?.takeIf { it in codes } ?: "unknown"
            val request = detail?.requestId?.takeIf { it.matches(Regex("[a-fA-F0-9-]{16,64}")) }
            diagnosticAt = now
            mutable.update { it.copy(diagnostic = listOfNotNull("time=$now", "operation=$operation", "result=$result", "code=$known",
                "duration=$elapsed", "android=${safeVersion(preferences?.clientVersion.orEmpty())}", "core=${it.coreVersion}",
                request?.let { id -> "request_id=$id" }).joinToString("\n")) }
        }
        val prefs = preferences ?: return
        if (!permitted() || started != null && started < grantedAt) return
        val old = window(now)
        val rows = old.batch.rows.toMutableList()
        val row = UsageRow(operation, result, elapsed, source, 1)
        val index = rows.indexOfFirst { it.copy(count = 1) == row }
        if (index >= 0) rows[index] = rows[index].copy(count = (rows[index].count + 1).coerceAtMost(65_535))
        else if (rows.size < 256) rows += row
        prefs.write("aggregate", CoreJson.encodeToString(old.copy(batch = old.batch.copy(rows = rows))))
        sendIfDue()
    }

    private fun permitted() = state.value.enabled && state.value.allowed == true && owner != null &&
        owner == session.state.value.profile?.accountKey && !session.state.value.needsLogin

    fun beginWrite(localKey: String, started: Long) {
        if (!permitted() || started < grantedAt) return
        val pending = pendingWrites().toMutableMap()
        if (pending.size < 1000 && localKey !in pending) pending[localKey] = started
        preferences?.write("pending", CoreJson.encodeToString(pending))
    }

    fun endWrite(localKey: String, result: String, error: Exception? = null) {
        val prefs = preferences ?: return
        if (prefs.read("in-flight") == localKey) prefs.write("in-flight", null)
        val pending = pendingWrites().toMutableMap()
        val started = pending.remove(localKey) ?: return
        preferences?.write("pending", CoreJson.encodeToString(pending))
        record("save_ack", result, started, error = error)
    }

    fun sendingWrite(localKey: String) {
        if (permitted() && localKey in pendingWrites()) preferences?.write("in-flight", localKey)
    }

    private fun pendingWrites(): Map<String, Long> = preferences?.read("pending")?.let {
        runCatching { CoreJson.decodeFromString<Map<String, Long>>(it) }.getOrNull()
    }.orEmpty().filterValues { epochMillis() - it in 0L until 2 * day }

    private fun cleanExpired() {
        val prefs = preferences ?: return
        val batch = window(epochMillis())
        if (batch.batch.rows.isEmpty()) prefs.write("aggregate", null)
        if (prefs.read("pending") != null) prefs.write("pending", pendingWrites().takeIf { it.isNotEmpty() }?.let { CoreJson.encodeToString(it) })
    }

    private fun window(now: Long): UsageWindow {
        val prefs = requireNotNull(preferences)
        val batch = UsageBatch(clientVersion = safeVersion(prefs.clientVersion), coreVersion = state.value.coreVersion,
            systemMajor = prefs.systemMajor.takeIf { it.matches(Regex("[1-9][0-9]{0,2}")) } ?: "unknown", rows = emptyList())
        return prefs.read("aggregate")?.let { runCatching { CoreJson.decodeFromString<UsageWindow>(it) }.getOrNull() }
            ?.takeIf { now - it.start in 0L until 2 * day && it.batch.copy(rows = emptyList()) == batch } ?: UsageWindow(now, batch)
    }

    private fun sendIfDue() {
        val prefs = preferences ?: return
        val client = active ?: return
        val now = epochMillis()
        if (!foreground || !permitted() || !session.state.value.connected || sender?.isActive == true ||
            now - (prefs.read("last-attempt")?.toLongOrNull() ?: 0L) < day) return
        val batch = window(now).batch
        if (batch.rows.isEmpty()) return
        val stamp = version
        sender = scope.launch {
            delay(500)
            if (!permitted() || stamp != version || !foreground || client !== session.core || !session.state.value.connected) return@launch
            val latest = window(epochMillis()).batch
            prefs.write("last-attempt", epochMillis().toString()); prefs.write("aggregate", null)
            try { withTimeout(5000) { client.api(HttpMethod.Post, "/telemetry", CoreJson.encodeToString(latest)) } }
            catch (failure: Exception) { if (failure is CancellationException) throw failure }
        }
    }

    private fun countProcessExits() {
        val prefs = preferences ?: return
        val now = epochMillis()
        val since = prefs.read("exit-after")?.toLongOrNull()?.coerceAtLeast(now - 2 * day) ?: now
        prefs.write("exit-after", now.toString())
        prefs.processExits(since).filter { it.first in (since + 1)..now && it.second in setOf("crash", "anr") }
            .forEach { record(it.second, "failure", source = "background") }
    }
}

internal fun usageResult(error: Exception): String = when {
    error is TimeoutCancellationException || error is io.ktor.client.plugins.HttpRequestTimeoutException -> "timeout"
    error is CancellationException -> "canceled"
    else -> when ((error as? CoreException)?.detail?.code ?: (error as? IllegalArgumentException)?.message) {
        "REQUEST_TIMEOUT" -> "timeout"
        "SOURCE_CHANGED", "CHAT_CONFLICT", "WRITE_CONFLICT", "IDEMPOTENCY_CONFLICT" -> "conflict"
        "CHAT_RESOURCE_LIMIT", "CHAT_MATERIAL_LIMIT", "CONTENT_TOO_LARGE" -> "over_limit"
        "CONNECTION_FAILED", "CHAT_OUTCOME_UNKNOWN", "CHAT_INTERRUPTED", null -> "unknown"
        else -> "failure"
    }
}

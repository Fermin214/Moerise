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
    fun writeBatch(vararg entries: Pair<String, String?>) {
        entries.forEach { (key, value) -> write(key, value) }
    }
    fun persist(vararg entries: Pair<String, String?>) {
        writeBatch(*entries)
    }
    fun consentWithdrawn(): Boolean = false
    fun markConsentWithdrawn(withdrawn: Boolean) = Unit
    val clientVersion: String
    val systemMajor: String
    val systemVersion: String? get() = null
    fun processExits(since: Long): List<Pair<Long, String>> = emptyList()
}

@Serializable data class UsageRow(val operation: String, val result: String, val duration: String, val source: String, val count: Int)
@Serializable data class UsageBatch(
    val schema: Int = 1, val platform: String = "android", @SerialName("client_version") val clientVersion: String,
    @SerialName("core_version") val coreVersion: String, @SerialName("system_major") val systemMajor: String,
    val rows: List<UsageRow>,
)
@Serializable internal data class UsageWindow(val start: Long, val batch: UsageBatch)
data class PrivacyState(val welcome: Boolean = false, val enabled: Boolean = false, val allowed: Boolean? = null,
    val coreVersion: String = "unknown", val diagnostic: String = "", val choiceSaveFailed: Boolean = false)

class PrivacyController(
    private val preferences: InstallPreferences?, private val session: SessionController, private val scope: CoroutineScope,
    private val clock: () -> Long = ::epochMillis,
) {
    private val expiryRevision = MutableStateFlow(0L)
    private val outbox = preferences?.let { UsageOutbox(it, ::rescheduleExpiry) }
    private val savedChoice = preferences?.read("choice-v1")
    private val withdrawn = preferences?.consentWithdrawn() == true
    private val mutable = MutableStateFlow(PrivacyState(welcome = preferences != null && savedChoice == null && !withdrawn,
        enabled = savedChoice == "on" && !withdrawn))
    val state = mutable.asStateFlow()
    private var pendingChoice: Boolean? = null
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
    private val codes = setOf("SOURCE_CHANGED", "SOURCE_NOT_FOUND",
        "WRITE_CONFLICT", "REQUEST_TIMEOUT", "CONNECTION_FAILED", "SESSION_EXPIRED")
    private val operations = setOf("create", "edit", "enqueue", "save_ack", "sync", "search_keyword", "search_semantic", "search_hybrid",
        "review", "walk", "stars", "startup", "client_error", "request_error", "crash", "anr")
    private val results = setOf("success", "failure", "canceled", "conflict", "unknown", "timeout", "over_limit")

    init {
        if (withdrawn && savedChoice != "off") choose(false, force = true)
        cleanExpired()
        outbox?.let { queue ->
            scope.launch {
                expiryRevision.collectLatest {
                    while (isActive) {
                        val deadline = queue.nextExpiry() ?: break
                        val wait = deadline - clock()
                        if (wait > 0) delay(wait)
                        else if (!cleanExpired()) delay(60_000)
                    }
                }
            }
        }
        if (preferences != null) scope.launch {
            session.state.collect { connection ->
                if (connection.loading) return@collect
                val key = connection.profile?.accountKey
                val client = session.core
                if (key != owner || active !== client || connection.needsLogin) {
                    policy?.cancel(); sender?.cancel(); version++
                    if (preferences.read("owner") != key) {
                        startup = null
                        mutable.update { it.copy(allowed = null) }
                        if (runCatching { outbox?.discard("exit-after" to null, "owner" to key) }.isFailure) return@collect
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
            cleanExpired()
            rescheduleExpiry()
            if (editorReady) refreshPolicy()
            if (clock() - diagnosticAt >= day) mutable.update { it.copy(diagnostic = "") }
        } else sender?.cancel()
    }

    fun editorDisplayed(start: Long?) {
        editorReady = true
        if (start != null && state.value.enabled && preferences?.read("owner") == session.state.value.profile?.accountKey)
            startup = start to clock()
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
            mutable.update { it.copy(allowed = allowed, coreVersion = coreVersion) }
            if (!allowed) {
                version++; sender?.cancel()
                runCatching { outbox?.discard("exit-after" to null) }
                grantedAt = clock()
                startup = null
            }
            if (allowed && state.value.enabled) {
                if (grantedAt == 0L) grantedAt = preferences.read("consent-at")?.toLongOrNull() ?: clock()
                if (runCatching { outbox?.initialize(grantedAt) }.isFailure) return@launch
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

    fun choose(enabled: Boolean) = choose(enabled, force = false)

    fun retryChoice() { pendingChoice?.let { choose(it, force = true) } }

    private fun choose(enabled: Boolean, force: Boolean) {
        val on = enabled && state.value.allowed != false
        val prefs = preferences ?: return
        val queue = outbox ?: return
        if (!force && !state.value.welcome && on == state.value.enabled) return
        version++; sender?.cancel()
        startup = null
        mutable.update { it.copy(enabled = false) }
        grantedAt = if (on) clock() else 0L
        val baseline = grantedAt.takeIf { on }?.toString()
        if (!on) runCatching { prefs.markConsentWithdrawn(true) }
        val saved = runCatching {
            queue.discard("consent-at" to baseline, "exit-after" to baseline,
                "choice-v1" to if (on) "on" else "off", "last-attempt" to baseline)
            if (on) prefs.markConsentWithdrawn(false)
        }.isSuccess
        pendingChoice = if (saved) null else on
        mutable.update { it.copy(welcome = it.welcome && !saved, enabled = saved && on, choiceSaveFailed = !saved) }
        if (saved && on) refreshPolicy()
    }

    fun diagnosticSummary(strings: Strings): String {
        val connection = session.state.value
        val status = when {
            connection.needsLogin -> strings.text("需要重新登录", "Sign in required")
            connection.connected -> strings.text("已连接", "Connected")
            connection.profile != null -> strings.text("离线", "Offline")
            else -> strings.text("未连接", "Not connected")
        }
        val diagnostic = state.value.diagnostic.takeIf { clock() - diagnosticAt in 0 until day }.orEmpty()
        val sdk = preferences?.systemMajor ?: "unknown"
        val android = preferences?.systemVersion?.let { "Android $it (SDK $sdk)" } ?: "Android SDK $sdk"
        return listOf("Moerise ${safeVersion(preferences?.clientVersion.orEmpty())}", "Core ${state.value.coreVersion}", android, status,
            diagnostic.ifEmpty { strings.text("近期无错误", "No recent errors") }).joinToString("\n")
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
        error: Exception? = null, code: String? = null, finished: Long = clock()) {
        if (operation !in operations || result !in results || source !in setOf("user", "background", "prefetch")) return
        val now = clock()
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
        val queue = outbox ?: return
        if (!permitted() || started != null && started < grantedAt) return
        queue.record(now, batchIdentity(), UsageRow(operation, result, elapsed, source, 1))
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
    }.orEmpty().filterValues { clock() - it in 0L until 2 * day }

    private fun rescheduleExpiry() { expiryRevision.update { it + 1 } }

    private fun cleanExpired(): Boolean {
        val prefs = preferences ?: return true
        return runCatching {
            outbox?.expire(clock())
            if (prefs.read("pending") != null) {
                val pending = pendingWrites()
                prefs.persist("pending" to pending.takeIf { it.isNotEmpty() }?.let { CoreJson.encodeToString(it) },
                    "in-flight" to prefs.read("in-flight")?.takeIf { it in pending })
            }
        }.isSuccess
    }

    private fun batchIdentity(): UsageBatch {
        val prefs = requireNotNull(preferences)
        return UsageBatch(clientVersion = safeVersion(prefs.clientVersion), coreVersion = state.value.coreVersion,
            systemMajor = prefs.systemMajor.takeIf { it.matches(Regex("[1-9][0-9]{0,2}")) } ?: "unknown", rows = emptyList())
    }

    private fun sendIfDue() {
        val queue = outbox ?: return
        val client = active ?: return
        val now = clock()
        if (!foreground || !permitted() || !session.state.value.connected || sender?.isActive == true ||
            !queue.ready(now)) return
        val stamp = version
        sender = scope.launch {
            delay(500)
            if (!permitted() || stamp != version || !foreground || client !== session.core || !session.state.value.connected) return@launch
            try {
                val attempt = queue.freeze(clock()) ?: return@launch
                val accepted = withTimeoutOrNull(5000) {
                    client.api(HttpMethod.Post, "/telemetry", CoreJson.encodeToString(attempt.batch))
                    true
                }
                if (accepted == true && stamp == version && permitted()) queue.acknowledge(clock(), attempt)
            }
            catch (failure: Exception) { if (failure is CancellationException) throw failure }
        }
    }

    private fun countProcessExits() {
        val prefs = preferences ?: return
        val now = clock()
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
        "SOURCE_CHANGED", "WRITE_CONFLICT", "IDEMPOTENCY_CONFLICT" -> "conflict"
        "CONTENT_TOO_LARGE" -> "over_limit"
        "CONNECTION_FAILED", null -> "unknown"
        else -> "failure"
    }
}

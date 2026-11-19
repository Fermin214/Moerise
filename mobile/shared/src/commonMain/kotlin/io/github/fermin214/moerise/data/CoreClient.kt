package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.*
import io.ktor.client.HttpClient
import io.ktor.client.engine.HttpClientEngine
import io.ktor.client.plugins.HttpTimeout
import io.ktor.client.plugins.timeout
import io.ktor.client.plugins.cookies.HttpCookies
import io.ktor.client.request.*
import io.ktor.client.statement.bodyAsText
import io.ktor.client.statement.bodyAsChannel
import io.ktor.http.*
import io.ktor.utils.io.readAvailable
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.io.EOFException
import kotlinx.io.IOException
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlin.time.TimeSource

expect fun platformHttpEngine(): HttpClientEngine
internal expect suspend fun discardIdleHttpConnections(engine: HttpClientEngine)
internal expect fun logClientFailure(operation: String, error: Exception)

val CoreJson = Json { ignoreUnknownKeys = true; encodeDefaults = true }
const val CORE_REQUEST_TIMEOUT_MILLIS = 65_000L
data class RemoteLogout(val cookie: String?, val csrf: String?)

class CoreClient(
    val origin: String,
    private val cookies: SessionCookies,
    private val engine: HttpClientEngine = platformHttpEngine(),
    private val onUnauthorized: () -> Unit = {},
    private val onRequestFailure: (Long, Exception) -> Unit = { _, _ -> },
    private val onRequest: () -> Unit = {},
) : AutoCloseable {
    private val sessionLock = Mutex()
    private val attachmentDownloads = Semaphore(2)
    private var identity: SessionIdentity? = null
    private var expectedAccount: String? = null
    private val client = HttpClient(engine) {
        followRedirects = false
        expectSuccess = false
        install(HttpCookies) { storage = cookies }
        install(HttpTimeout) {
            requestTimeoutMillis = CORE_REQUEST_TIMEOUT_MILLIS
            connectTimeoutMillis = 10_000
            socketTimeoutMillis = CORE_REQUEST_TIMEOUT_MILLIS
        }
    }

    fun bindAccount(accountKey: String) { expectedAccount = accountKey }

    var lastCapabilities: Capabilities? = null
        private set
    suspend fun capabilities(): Capabilities = CoreJson.decodeFromString<Capabilities>(send(HttpMethod.Get, "/capabilities"))
        .also { lastCapabilities = it }

    suspend fun login(username: String, password: String): SessionIdentity = sessionLock.withLock {
        val body = buildJsonObject { put("username", username); put("password", password) }.toString()
        val next = CoreJson.decodeFromString<SessionIdentity>(send(HttpMethod.Post, "/session", body))
        require(next.csrfToken.isNotBlank() && next.instanceId.isNotBlank() && next.user.name.isNotBlank())
        expectedAccount = next.accountKey(origin)
        identity = next
        next
    }

    suspend fun session(): SessionIdentity = sessionLock.withLock { refreshSession() }

    suspend fun boundedMap(): String? {
        sessionLock.withLock { identity ?: refreshSession() }
        onRequest()
        return client.prepareGet("$origin/api/app/v1/map").execute { response ->
            if (response.status.value == 401) { identity = null; onUnauthorized() }
            if (!response.status.isSuccess()) return@execute null
            val bytes = ByteArray(2 * 1024 * 1024 + 1)
            val channel = response.bodyAsChannel()
            var size = 0
            while (size < bytes.size) {
                val count = channel.readAvailable(bytes, size, bytes.size - size)
                if (count < 0) break
                size += count
            }
            if (size < bytes.size) bytes.decodeToString(0, size) else null
        }
    }

    private suspend fun refreshSession(): SessionIdentity {
        val next = withTimeout(CORE_REQUEST_TIMEOUT_MILLIS) {
            CoreJson.decodeFromString<SessionIdentity>(send(HttpMethod.Get, "/session"))
        }
        val key = next.accountKey(origin)
        if (expectedAccount != null && expectedAccount != key) {
            identity = null
            onUnauthorized()
            throw IdentityChangedException()
        }
        expectedAccount = key
        identity = next
        return next
    }

    suspend fun api(method: HttpMethod, path: String, body: String? = null, idempotencyKey: String? = null): String {
        val started = epochMillis()
        return try { authenticated(method, path, body, idempotencyKey) } catch (error: Exception) {
            if (path != "/telemetry" && error !is kotlinx.coroutines.CancellationException) onRequestFailure(started, error)
            throw error
        }
    }

    private suspend fun authenticated(method: HttpMethod, path: String, body: String?, idempotencyKey: String?): String {
        val current = sessionLock.withLock { identity ?: refreshSession() }
        try {
            return send(method, path, body, current.csrfToken, idempotencyKey)
        } catch (error: CoreException) {
            if (error.detail.code != "CSRF_REJECTED") throw error
            val refreshed = sessionLock.withLock {
                if (identity?.csrfToken == current.csrfToken) refreshSession() else identity ?: refreshSession()
            }
            return send(method, path, body, refreshed.csrfToken, idempotencyKey)
        }
    }

    suspend fun chatStream(chatId: String, input: ChatGenerate, onEvent: suspend (ChatEvent) -> Unit) {
        require(Regex("^[0-9a-fA-F-]{36}$").matches(chatId) &&
            Regex("^[A-Za-z0-9_-]{8,128}$").matches(input.requestId) && input.language in setOf("", "zh-CN", "en")) { "CHAT_PROTOCOL" }
        var current = sessionLock.withLock { identity ?: refreshSession() }
        val callerContext = currentCoroutineContext()
        for (attempt in 0..1) {
            var accepted = false
            try {
                discardIdleHttpConnections(engine)
                onRequest()
                client.preparePost("$origin/api/app/v1/chats/$chatId/messages") {
                    header(HttpHeaders.Origin, origin)
                    header("X-CSRF-Token", current.csrfToken)
                    header(HttpHeaders.Accept, "text/event-stream")
                    contentType(ContentType.Application.Json)
                    setBody(CoreJson.encodeToString(input))
                    timeout { requestTimeoutMillis = 135_000; socketTimeoutMillis = 135_000; connectTimeoutMillis = 10_000 }
                }.execute { response ->
                    if (response.status.value !in 200..299) {
                        val detail = runCatching { CoreJson.decodeFromString<ErrorEnvelope>(response.bodyAsText()).error }
                            .getOrElse { ApiError("HTTP_${response.status.value}") }
                        if (response.status.value == 401) { identity = null; onUnauthorized() }
                        throw CoreException(response.status.value, detail)
                    }
                    accepted = true
                    require(response.contentType()?.match(ContentType.Text.EventStream) == true) { "CHAT_PROTOCOL" }
                    val parser = ChatStreamParser(input.requestId) { event -> withContext(callerContext) { onEvent(event) } }
                    val channel = response.bodyAsChannel()
                    val buffer = ByteArray(16 * 1024)
                    while (true) {
                        val count = channel.readAvailable(buffer, 0, buffer.size)
                        if (count < 0) break
                        if (count > 0) withContext(Dispatchers.Default) { parser.feed(buffer, count) }
                    }
                    parser.finish()
                }
                return
            } catch (error: IOException) {
                logClientFailure("POST chat_message", error)
                throw error
            } catch (error: CoreException) {
                if (accepted || attempt != 0 || error.detail.code != "CSRF_REJECTED") throw error
                current = sessionLock.withLock {
                    if (identity?.csrfToken == current.csrfToken) refreshSession() else identity ?: refreshSession()
                }
            }
        }
    }

    suspend fun downloadAttachment(
        note: Note,
        attachment: JsonObject,
        maximumBytes: Long = 128L * 1024 * 1024,
        onChunk: suspend (ByteArray, Int) -> Unit,
    ) = attachmentDownloads.withPermit {
        require(Regex("^memos/[A-Za-z0-9_-]+$").matches(note.name))
        val name = attachment["name"]?.jsonPrimitive?.content ?: error("INVALID_ATTACHMENT")
        require(Regex("^attachments/[A-Za-z0-9_-]+$").matches(name) &&
            note.attachments.any { it["name"]?.jsonPrimitive?.content == name })
        val current = sessionLock.withLock { identity ?: refreshSession() }
        require(note.instanceId == current.instanceId)
        val path = "/notes/${note.name.removePrefix("memos/")}/attachments/${name.removePrefix("attachments/")}/content"
        onRequest()
        client.prepareGet("$origin/api/app/v1$path").execute { response ->
            if (response.status.value !in 200..299) {
                val detail = runCatching { CoreJson.decodeFromString<ErrorEnvelope>(response.bodyAsText()).error }
                    .getOrElse { ApiError("HTTP_${response.status.value}") }
                if (response.status.value == 401) { identity = null; onUnauthorized() }
                throw CoreException(response.status.value, detail)
            }
            val length = response.headers[HttpHeaders.ContentLength]?.toLongOrNull()
            require(length == null || length in 0..maximumBytes) { "ATTACHMENT_DOWNLOAD_TOO_LARGE" }
            val channel = response.bodyAsChannel()
            val buffer = ByteArray(16 * 1024)
            var total = 0L
            while (true) {
                val count = channel.readAvailable(buffer, 0, buffer.size)
                if (count < 0) break
                if (count == 0) continue
                total += count
                require(total <= maximumBytes) { "ATTACHMENT_DOWNLOAD_TOO_LARGE" }
                onChunk(buffer, count)
            }
            require(length == null || total == length) { "ATTACHMENT_DOWNLOAD_INCOMPLETE" }
        }
    }

    suspend fun attachmentBytes(note: Note, attachment: JsonObject, maximumBytes: Int): ByteArray {
        val chunks = mutableListOf<ByteArray>()
        var size = 0
        downloadAttachment(note, attachment, maximumBytes.toLong()) { buffer, count ->
            chunks += buffer.copyOf(count)
            size += count
        }
        val result = ByteArray(size)
        var offset = 0
        for (chunk in chunks) { chunk.copyInto(result, offset); offset += chunk.size }
        return result
    }

    private suspend fun send(method: HttpMethod, path: String, body: String? = null, csrf: String? = null, idempotencyKey: String? = null, cookie: String? = null): String {
        val started = TimeSource.Monotonic.markNow()
        val readOnly = method == HttpMethod.Get ||
            method == HttpMethod.Post && path in setOf("/search", "/chat-materials/resolve")
        if (!readOnly) discardIdleHttpConnections(engine)
        try {
            return sendOnce(method, path, body, csrf, idempotencyKey, cookie)
        } catch (error: IOException) {
            currentCoroutineContext().ensureActive()
            val remaining = CORE_REQUEST_TIMEOUT_MILLIS - started.elapsedNow().inWholeMilliseconds
            // 只读请求遇到 EOF 时清除失效的空闲连接再重连；写操作不重放，两个请求共用时限。
            if (!readOnly || remaining <= 0 ||
                generateSequence<Throwable>(error) { it.cause }.take(5).none { it is EOFException }) throw error
            discardIdleHttpConnections(engine)
            return sendOnce(method, path, body, csrf, idempotencyKey, cookie, remaining)
        }
    }

    private suspend fun sendOnce(method: HttpMethod, path: String, body: String?, csrf: String?, idempotencyKey: String?, cookie: String?, timeoutMillis: Long = CORE_REQUEST_TIMEOUT_MILLIS): String {
        require(path.startsWith("/") && !path.startsWith("//") && '#' !in path)
        onRequest()
        try {
            val response = client.request("$origin/api/app/v1$path") {
                this.method = method
                timeout { requestTimeoutMillis = timeoutMillis; socketTimeoutMillis = timeoutMillis }
                if (method != HttpMethod.Get && method != HttpMethod.Head) {
                    header(HttpHeaders.Origin, origin)
                    csrf?.let { header("X-CSRF-Token", it) }
                }
                idempotencyKey?.let { header("Idempotency-Key", it) }
                cookie?.let { header(HttpHeaders.Cookie, it) }
                if (body != null) { contentType(ContentType.Application.Json); setBody(body) }
            }
            val text = response.bodyAsText()
            if (response.status.value !in 200..299) {
                val detail = runCatching { CoreJson.decodeFromString<ErrorEnvelope>(text).error }
                    .getOrElse { ApiError("HTTP_${response.status.value}") }
                if (response.status.value == 401) { identity = null; onUnauthorized() }
                throw CoreException(response.status.value, detail)
            }
            return text
        } catch (error: IOException) {
            logClientFailure("${method.value} ${path.substringBefore('?').split('/')[1]}", error)
            throw error
        }
    }

    suspend fun clearLocalSession(): RemoteLogout {
        val csrf = identity?.csrfToken
        identity = null
        expectedAccount = null
        return RemoteLogout(cookies.detach()?.let(::renderCookieHeader), csrf)
    }

    suspend fun revokeSession(request: RemoteLogout) {
        check(request.cookie == null || request.csrf != null) { "REMOTE_LOGOUT_INCOMPLETE" }
        if (request.cookie != null && request.csrf != null) {
            send(HttpMethod.Delete, "/session", csrf = request.csrf, cookie = request.cookie)
        }
    }

    suspend fun logout() {
        revokeSession(clearLocalSession())
    }

    override fun close() { identity = null; client.close() }
}

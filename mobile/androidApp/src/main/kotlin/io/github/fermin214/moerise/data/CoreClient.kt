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
import java.util.concurrent.atomic.AtomicReference
import kotlin.time.TimeSource


val CoreJson = Json { ignoreUnknownKeys = true; encodeDefaults = true }
const val CORE_REQUEST_TIMEOUT_MILLIS = 65_000L
data class RemoteLogout(val cookie: String?, val csrf: String?)

class CoreClient(
    val origin: String,
    private val cookies: SessionCookies,
    private val engine: HttpClientEngine = platformHttpEngine(),
    private val onUnauthorized: suspend (String) -> Unit = {},
    private val onRequestFailure: (Long, Exception) -> Unit = { _, _ -> },
    private val onRequest: () -> Unit = {},
) : AutoCloseable {
    private data class SessionState(
        val account: String? = null,
        val identity: SessionIdentity? = null,
        val generation: Long = 0,
        val revoked: Boolean = false,
    )

    private val sessionLock = Mutex()
    private val attachmentCommitLock = Any()
    private var attachmentCommitsPaused = false
    private val attachmentDownloads = Semaphore(2)
    private val sessionState = AtomicReference(SessionState())
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

    fun bindAccount(accountKey: String) = synchronized(attachmentCommitLock) {
        sessionState.updateAndGet { it.copy(account = accountKey, generation = if (it.account == accountKey) it.generation else it.generation + 1) }
        Unit
    }
    internal val sessionGeneration get() = sessionState.get().generation
    internal fun isSessionCurrent(generation: Long): Boolean =
        sessionState.get().let { it.generation == generation && !it.revoked }

    internal val attachmentAccountKey get() = sessionState.get().let { if (it.revoked) null else it.account }
    internal suspend fun attachmentAccount(): String {
        val current = sessionState.get()
        require(!current.revoked) { "SESSION_UNAUTHORIZED" }
        return current.account ?: session().accountKey(origin)
    }

    internal data class AttachmentAccess(val account: String, val generation: Long)

    internal suspend fun imageExportAccess(note: Note, item: JsonObject): AttachmentAccess {
        attachmentResource(note, item)
        require(item in note.attachments && item["type"]?.jsonPrimitive?.content?.matches(imageMimeType) == true)
        val account = attachmentAccount()
        val generation = sessionGeneration
        return withAttachmentAccess(AttachmentAccess(account, generation)) {
            require(account.split('\u0000', limit = 3).getOrNull(1) == note.instanceId)
            AttachmentAccess(account, generation)
        }
    }

    internal fun <T> withAttachmentAccess(access: AttachmentAccess, action: () -> T): T = synchronized(attachmentCommitLock) {
        val current = sessionState.get()
        if (attachmentCommitsPaused || current.revoked || current.generation != access.generation || current.account != access.account)
            throw CancellationException("Attachment session changed")
        action()
    }

    internal fun pauseAttachmentCommits() = synchronized(attachmentCommitLock) { attachmentCommitsPaused = true }

    private suspend fun invalidateSession() {
        val previous = revokeLocalSession()
        previous.account?.let { onUnauthorized(it) }
    }

    private fun revokeLocalSession(clearAccount: Boolean = false) = synchronized(attachmentCommitLock) {
        sessionState.getAndUpdate {
            it.copy(account = if (clearAccount) null else it.account, identity = null, generation = it.generation + 1, revoked = true)
        }
    }

    private fun sessionUnauthorized() = CoreException(401, ApiError("SESSION_UNAUTHORIZED"))

    var lastCapabilities: Capabilities? = null
        private set
    suspend fun capabilities(): Capabilities = CoreJson.decodeFromString<Capabilities>(send(HttpMethod.Get, "/capabilities"))
        .also { lastCapabilities = it }

    suspend fun login(username: String, password: String): SessionIdentity = sessionLock.withLock {
        val before = sessionState.get()
        val body = buildJsonObject { put("username", username); put("password", password) }.toString()
        val next = CoreJson.decodeFromString<SessionIdentity>(send(HttpMethod.Post, "/session", body))
        require(next.csrfToken.isNotBlank() && next.instanceId.isNotBlank() && next.user.name.isNotBlank())
        if (!synchronized(attachmentCommitLock) {
                sessionState.compareAndSet(before, before.copy(account = next.accountKey(origin), identity = next,
                    generation = before.generation + 1, revoked = false))
            })
            throw sessionUnauthorized()
        next
    }

    suspend fun session(): SessionIdentity = sessionLock.withLock { refreshSession() }

    suspend fun boundedMap(): String? {
        sessionLock.withLock { sessionState.get().identity ?: refreshSession() }
        onRequest()
        return client.prepareGet("$origin/api/app/v1/map").execute { response ->
            if (response.status.value == 401) invalidateSession()
            if (!response.status.isSuccess()) return@execute null
            val bytes = ByteArray(8 * 1024 * 1024 + 1)
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

    suspend fun syncNotes(cursor: String = "", limit: Int = 100): SyncNotesResponse {
        val query = buildString {
            append("/sync/notes?limit=")
            append(limit.coerceIn(1, 200))
            if (cursor.isNotEmpty()) {
                append("&cursor=")
                append(cursor.encodeURLQueryComponent(encodeFull = true))
            }
        }
        return CoreJson.decodeFromString(api(HttpMethod.Get, query))
    }

    private suspend fun refreshSession(): SessionIdentity {
        val before = sessionState.get()
        if (before.revoked) throw sessionUnauthorized()
        val next = withTimeout(CORE_REQUEST_TIMEOUT_MILLIS) {
            CoreJson.decodeFromString<SessionIdentity>(send(HttpMethod.Get, "/session"))
        }
        if (!isSessionCurrent(before.generation)) throw sessionUnauthorized()
        val key = next.accountKey(origin)
        if (before.account != null && before.account != key) {
            invalidateSession()
            throw IdentityChangedException()
        }
        if (!synchronized(attachmentCommitLock) { sessionState.compareAndSet(before, before.copy(account = key, identity = next)) })
            throw sessionUnauthorized()
        return next
    }

    suspend fun api(method: HttpMethod, path: String, body: String? = null, idempotencyKey: String? = null): String {
        val started = epochMillis()
        return try { authenticated(method, path, body, idempotencyKey) } catch (error: Exception) {
            if (path != "/telemetry" && error !is kotlinx.coroutines.CancellationException) onRequestFailure(started, error)
            throw error
        }
    }

    internal suspend fun patchNoteState(path: String, body: String, key: String, online: () -> Boolean): String {
        val started = epochMillis()
        return try { authenticated(HttpMethod.Patch, path, body, key, online) }
        catch (error: Exception) {
            if (error !is CancellationException) onRequestFailure(started, error)
            throw error
        }
    }

    private suspend fun authenticated(method: HttpMethod, path: String, body: String?, idempotencyKey: String?,
        online: (() -> Boolean)? = null): String {
        suspend fun preflight(previous: SessionIdentity? = null): SessionIdentity = try {
            sessionLock.withLock {
                sessionState.get().identity?.takeUnless { it.csrfToken == previous?.csrfToken } ?: refreshSession()
            }
        } catch (error: Exception) {
            if (online == null || error is CancellationException && !currentCoroutineContext().isActive) throw error
            throw StateRequestNotSent(error)
        }

        val current = preflight()
        try {
            return send(method, path, body, current.csrfToken, idempotencyKey, online = online)
        } catch (error: CoreException) {
            if (error.detail.code != "CSRF_REJECTED" || path == "/telemetry") throw error
            val refreshed = preflight(current)
            return send(method, path, body, refreshed.csrfToken, idempotencyKey, online = online)
        }
    }

    private fun attachmentResource(note: Note, attachment: JsonObject): String {
        require(noteResource.matches(note.name))
        val name = attachment["name"]?.jsonPrimitive?.content ?: error("INVALID_ATTACHMENT")
        require(attachmentResourceName.matches(name) &&
            note.attachments.any { it["name"]?.jsonPrimitive?.content == name })
        return name
    }

    suspend fun downloadAttachment(
        note: Note,
        attachment: JsonObject,
        maximumBytes: Long = 128L * 1024 * 1024,
        onChunk: suspend (ByteArray, Int) -> Unit,
    ) = attachmentDownloads.withPermit {
        val name = attachmentResource(note, attachment)
        val current = sessionLock.withLock { sessionState.get().identity ?: refreshSession() }
        require(note.instanceId == current.instanceId)
        val path = "/notes/${note.name.removePrefix("memos/")}/attachments/${name.removePrefix("attachments/")}/content"
        onRequest()
        client.prepareGet("$origin/api/app/v1$path").execute { response ->
            if (response.status.value == 401) invalidateSession()
            if (response.status.value !in 200..299) {
                val detail = runCatching { CoreJson.decodeFromString<ErrorEnvelope>(response.bodyAsText()).error }
                    .getOrElse { ApiError("HTTP_${response.status.value}") }
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

    private suspend fun send(method: HttpMethod, path: String, body: String? = null, csrf: String? = null,
        idempotencyKey: String? = null, cookie: String? = null, online: (() -> Boolean)? = null): String {
        val started = TimeSource.Monotonic.markNow()
        val readOnly = method == HttpMethod.Get ||
            method == HttpMethod.Post && path == "/search"
        if (!readOnly) discardIdleHttpConnections(engine)
        if (online?.invoke() == false) throw StateRequestNotSent()
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
            return client.prepareRequest("$origin/api/app/v1$path") {
                this.method = method
                timeout { requestTimeoutMillis = timeoutMillis; socketTimeoutMillis = timeoutMillis }
                if (method != HttpMethod.Get && method != HttpMethod.Head) {
                    header(HttpHeaders.Origin, origin)
                    csrf?.let { header("X-CSRF-Token", it) }
                }
                idempotencyKey?.let { header("Idempotency-Key", it) }
                cookie?.let { header(HttpHeaders.Cookie, it) }
                if (body != null) { contentType(ContentType.Application.Json); setBody(body) }
            }.execute { response ->
                if (response.status.value == 401) invalidateSession()
                val text = response.bodyAsText()
                if (response.status.value !in 200..299) {
                    val detail = runCatching { CoreJson.decodeFromString<ErrorEnvelope>(text).error }
                        .getOrElse { ApiError("HTTP_${response.status.value}") }
                    throw CoreException(response.status.value, detail)
                }
                text
            }
        } catch (error: IOException) {
            logClientFailure("${method.value} ${path.substringBefore('?').split('/')[1]}", error)
            throw error
        }
    }

    suspend fun clearLocalSession(): RemoteLogout {
        val csrf = revokeLocalSession(clearAccount = true).identity?.csrfToken
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

    override fun close() { revokeLocalSession(clearAccount = true); client.close() }

    private companion object {
        val noteResource = Regex("^memos/[A-Za-z0-9_-]+$")
        val attachmentResourceName = Regex("^attachments/[A-Za-z0-9_-]+$")
        val imageMimeType = Regex("^image/[a-z0-9!#&^_.+-]+$")
    }
}

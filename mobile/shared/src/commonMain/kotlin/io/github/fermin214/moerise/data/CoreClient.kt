package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.*
import io.ktor.client.HttpClient
import io.ktor.client.engine.HttpClientEngine
import io.ktor.client.plugins.HttpTimeout
import io.ktor.client.plugins.cookies.HttpCookies
import io.ktor.client.request.*
import io.ktor.client.statement.bodyAsText
import io.ktor.http.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

expect fun platformHttpEngine(): HttpClientEngine

val CoreJson = Json { ignoreUnknownKeys = true; encodeDefaults = true }
data class RemoteLogout(val cookie: String?, val csrf: String?)

class CoreClient(
    val origin: String,
    private val cookies: SessionCookies,
    engine: HttpClientEngine = platformHttpEngine(),
    private val onUnauthorized: () -> Unit = {},
) : AutoCloseable {
    private val sessionLock = Mutex()
    private var identity: SessionIdentity? = null
    private var expectedAccount: String? = null
    private val client = HttpClient(engine) {
        followRedirects = false
        expectSuccess = false
        install(HttpCookies) { storage = cookies }
        install(HttpTimeout) {
            requestTimeoutMillis = 65_000
            connectTimeoutMillis = 10_000
            socketTimeoutMillis = 65_000
        }
    }

    fun bindAccount(accountKey: String) { expectedAccount = accountKey }

    suspend fun capabilities(): Capabilities = CoreJson.decodeFromString(send(HttpMethod.Get, "/capabilities"))

    suspend fun login(username: String, password: String): SessionIdentity = sessionLock.withLock {
        val body = buildJsonObject { put("username", username); put("password", password) }.toString()
        val next = CoreJson.decodeFromString<SessionIdentity>(send(HttpMethod.Post, "/session", body))
        require(next.csrfToken.isNotBlank() && next.instanceId.isNotBlank() && next.user.name.isNotBlank())
        expectedAccount = next.accountKey(origin)
        identity = next
        next
    }

    suspend fun session(): SessionIdentity = sessionLock.withLock { refreshSession() }

    private suspend fun refreshSession(): SessionIdentity {
        val next = CoreJson.decodeFromString<SessionIdentity>(send(HttpMethod.Get, "/session"))
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

    private suspend fun send(method: HttpMethod, path: String, body: String? = null, csrf: String? = null, idempotencyKey: String? = null, cookie: String? = null): String {
        require(path.startsWith("/") && !path.startsWith("//") && '#' !in path)
        val response = client.request("$origin/api/app/v1$path") {
            this.method = method
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

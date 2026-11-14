package io.github.fermin214.moerise.data

import io.ktor.client.plugins.cookies.CookiesStorage
import io.ktor.http.Cookie
import io.ktor.http.Url
import io.ktor.http.isSecure
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

interface SecretStore {
    suspend fun read(key: String): String?
    suspend fun write(key: String, value: String?)
}

expect fun epochMillis(): Long

@Serializable
private data class StoredCookie(val cookie: Cookie, val deadline: Long)

class SessionCookies(private val origin: String, private val store: SecretStore) : CookiesStorage {
    private val lock = Mutex()
    private var loaded = false
    private val detached = MutableStateFlow(false)
    private var retiredCookie: Cookie? = null
    private var saved: StoredCookie? = null
    private val key = "session:$origin"

    private suspend fun load() {
        if (!loaded) {
            saved = store.read(key)?.let { runCatching { Json.decodeFromString<StoredCookie>(it) }.getOrNull() }
            loaded = true
        }
    }

    override suspend fun get(requestUrl: Url): List<Cookie> = lock.withLock {
        if (!sameOrigin(requestUrl, origin)) return@withLock emptyList()
        if (detached.value) return@withLock if (requestUrl.encodedPath == "/api/app/v1/session") listOfNotNull(retiredCookie) else emptyList()
        load()
        val current = saved ?: return@withLock emptyList()
        if (current.deadline <= epochMillis()) {
            saved = null
            store.write(key, null)
            return@withLock emptyList()
        }
        if (current.cookie.secure && !requestUrl.protocol.isSecure()) return@withLock emptyList()
        listOf(current.cookie)
    }

    override suspend fun addCookie(requestUrl: Url, cookie: Cookie) = lock.withLock {
        if (detached.value || !sameOrigin(requestUrl, origin)) return@withLock
        val secure = requestUrl.protocol.isSecure()
        val expectedName = if (secure) "__Host-moerise_session" else "moerise_local_session"
        if (cookie.name != expectedName || cookie.path != "/" || cookie.domain != null ||
            secure && !cookie.secure || cookie.secure && !secure || !cookie.httpOnly) return@withLock
        val deadline = cookie.maxAge?.let { epochMillis() + it * 1000L } ?: cookie.expires?.timestamp ?: return@withLock
        saved = if (cookie.value.isEmpty() || deadline <= epochMillis()) null else StoredCookie(cookie, deadline)
        loaded = true
        store.write(key, saved?.let { Json.encodeToString(it) })
    }

    suspend fun clear() = lock.withLock {
        saved = null
        loaded = true
        store.write(key, null)
    }

    suspend fun detach(): Cookie? = lock.withLock {
        load()
        val previous = saved?.takeIf { it.deadline > epochMillis() }?.cookie
        retiredCookie = previous
        detached.value = true
        saved = null
        loaded = true
        store.write(key, null)
        previous
    }

    override fun close() { detached.value = true; retiredCookie = null }
}

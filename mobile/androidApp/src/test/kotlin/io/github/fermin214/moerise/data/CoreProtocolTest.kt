package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.domain.IdentityChangedException
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import kotlin.test.*
import kotlinx.coroutines.runBlocking

class MemorySecrets : SecretStore {
    val values = mutableMapOf<String, String>()
    override suspend fun read(key: String): String? = values[key]
    override suspend fun write(key: String, value: String?) {
        if (value == null) values.remove(key) else values[key] = value
    }
}

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class CoreProtocolTest {
    @Test
    fun originRejectsPathsCredentialsAndInsecureNonLoopback() {
        assertEquals("https://example.org", normalizeOrigin(" HTTPS://EXAMPLE.ORG:443/ "))
        assertEquals("http://127.0.0.1:8080", normalizeOrigin("http://127.0.0.1:8080", true))
        for (url in listOf("https://user:pass@example.org", "https://example.org/api", "https://example.org?key=x", "https://example.org#x", "http://example.org", "https://example.org\\@evil.test")) {
            assertFailsWith<IllegalArgumentException> { normalizeOrigin(url, true) }
        }
    }

    @Test
    fun cookiePersistsOnlyHostScopedSessionAndHonorsExpiry() = runBlocking<Unit> {
        val store = MemorySecrets()
        val origin = "https://notes.example"
        val url = Url("$origin/api/app/v1/session")
        val cookie = Cookie("__Host-moerise_session", "session", path = "/", maxAge = 300, secure = true, httpOnly = true)
        SessionCookies(origin, store).addCookie(url, cookie)
        val reopened = SessionCookies(origin, store)
        assertEquals("session", reopened.get(url).single().value)
        assertTrue(reopened.get(Url("https://other.example/")).isEmpty())
        assertTrue(reopened.get(Url("https://notes.example:8443/")).isEmpty())
        reopened.addCookie(url, cookie.copy(value = "foreign", domain = ".notes.example"))
        assertEquals("session", reopened.get(url).single().value)
        reopened.addCookie(url, cookie.copy(value = "", maxAge = 0))
        assertTrue(reopened.get(url).isEmpty())
        assertTrue(SessionCookies(origin, store).get(url).isEmpty())
    }

    @Test
    fun csrfRefreshKeepsWriteBodyAndKeyAndRejectsIdentitySwitch() = runBlocking<Unit> {
        val store = MemorySecrets()
        var sessions = 0
        var writes = 0
        val identities = { token: String, user: String ->
            """{"instance_id":"one","user":{"name":"$user"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"$token"}"""
        }
        val body = """{"content":"原始提交"}"""
        val engine = MockEngine { request ->
            when (request.url.encodedPath.substringAfter("/api/app/v1")) {
                "/session" -> {
                    sessions++
                    respond(identities("csrf-$sessions", if (sessions > 2) "users/other" else "users/owner"), headers = headersOf(HttpHeaders.ContentType, "application/json"))
                }
                "/notes" -> {
                    writes++
                    assertEquals("https://notes.example", request.headers[HttpHeaders.Origin])
                    assertEquals("fixed", request.headers["Idempotency-Key"])
                    assertEquals(body, request.body.toByteArray().decodeToString())
                    if (writes == 1) respond("""{"error":{"code":"CSRF_REJECTED"}}""", HttpStatusCode.Forbidden)
                    else { assertEquals("csrf-2", request.headers["X-CSRF-Token"]); respond("{}") }
                }
                else -> error("Unexpected route")
            }
        }
        CoreClient("https://notes.example", SessionCookies("https://notes.example", store), engine).use { core ->
            core.bindAccount("https://notes.example\u0000one\u0000users/owner")
            assertEquals("{}", core.api(HttpMethod.Post, "/notes", body, "fixed"))
            assertEquals(2, writes)
            assertFailsWith<IdentityChangedException> { core.session() }
        }
    }

    @Test
    fun lateLogoutCannotEraseNewLoginCookie() = runBlocking<Unit> {
        val secrets = MemorySecrets()
        val url = Url("https://notes.example/api/app/v1/session")
        val old = SessionCookies("https://notes.example", secrets)
        val cookie = Cookie("__Host-moerise_session", "old", path = "/", maxAge = 300, secure = true, httpOnly = true)
        old.addCookie(url, cookie)
        assertEquals("old", old.detach()?.value)
        val next = SessionCookies("https://notes.example", secrets)
        next.addCookie(url, cookie.copy(value = "new"))
        old.addCookie(url, cookie.copy(value = "", maxAge = 0))
        assertEquals("new", SessionCookies("https://notes.example", secrets).get(url).single().value)
        val retired = SessionCookies("https://notes.example", secrets)
        retired.get(url)
        retired.close()
        next.addCookie(url, cookie.copy(value = "latest"))
        retired.addCookie(url, cookie.copy(value = "late"))
        assertEquals("latest", SessionCookies("https://notes.example", secrets).get(url).single().value)
    }
}

package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.mockwebserver.SocketPolicy
import io.github.fermin214.moerise.domain.ChatGenerate
import io.github.fermin214.moerise.domain.CoreException
import io.ktor.client.engine.mock.MockEngine
import io.ktor.client.engine.mock.respond
import io.ktor.http.HttpMethod
import io.ktor.http.HttpStatusCode
import java.io.EOFException
import java.io.IOException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.*
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.runBlocking

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class SessionTransportRecoveryTest {
    private val identity = """{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"csrf"}"""

    @Test
    fun expiredPooledConnectionReconnectsWithCookieAndDoesNotRepeatWrites() = runBlocking<Unit> {
        val reads = AtomicInteger()
        val materialReads = AtomicInteger()
        val writes = AtomicInteger()
        val readSequences = CopyOnWriteArrayList<Int>()
        val materialSequences = CopyOnWriteArrayList<Int>()
        val messageSequences = CopyOnWriteArrayList<Int>()
        val cookies = CopyOnWriteArrayList<String>()
        val server = MockWebServer()
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                if (request.method == "GET") {
                    val materials = request.path.orEmpty().endsWith("/chat-materials")
                    if (materials) {
                        materialSequences += request.sequenceNumber
                        if (materialReads.incrementAndGet() == 1) return MockResponse().setSocketPolicy(SocketPolicy.DISCONNECT_AFTER_REQUEST)
                    } else {
                        readSequences += request.sequenceNumber
                        cookies += request.getHeader("Cookie").orEmpty()
                        if (reads.incrementAndGet() == 2) return MockResponse().setSocketPolicy(SocketPolicy.DISCONNECT_AFTER_REQUEST)
                    }
                    return MockResponse().setHeader("Content-Type", "application/json")
                        .setHeader("Set-Cookie", "moerise_local_session=fixture; Path=/; Max-Age=300; HttpOnly")
                        .setBody(if (materials) """{"notes":[]}""" else identity)
                }
                if (request.path.orEmpty().endsWith("/messages")) messageSequences += request.sequenceNumber else writes.incrementAndGet()
                return MockResponse().setSocketPolicy(SocketPolicy.DISCONNECT_AFTER_REQUEST)
            }
        }
        server.start()
        try {
            val origin = server.url("/").toString().removeSuffix("/")
            CoreClient(origin, SessionCookies(origin, MemorySecrets())).use { core ->
                core.session()
                core.session()
                assertEquals(3, reads.get())
                assertEquals(listOf(0, 1, 0), readSequences.toList())
                assertEquals(listOf("", "moerise_local_session=fixture", "moerise_local_session=fixture"), cookies.toList())
                assertEquals("""{"notes":[]}""", core.api(HttpMethod.Get, "/chat-materials"))
                assertEquals(2, materialReads.get())
                assertEquals(listOf(1, 0), materialSequences.toList())
                assertFailsWith<IOException> {
                    core.chatStream("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
                        ChatGenerate("insight-001", 1, "insight", "", "en")) {}
                }
                assertEquals(0, messageSequences.single())
                assertFailsWith<IOException> { core.api(HttpMethod.Post, "/notes", "{}", "fixed") }
                assertEquals(1, writes.get())
            }
        } finally { server.shutdown() }
    }

    @Test
    fun readsRecoverAndMutationsBypassExpiredConnectionsWithoutReplay() = runBlocking<Unit> {
        val warm = CountDownLatch(3)
        val recover = AtomicBoolean()
        val attempts = AtomicInteger()
        val mutations = AtomicInteger()
        val warmSequences = CopyOnWriteArrayList<Int>()
        val server = MockWebServer()
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                if (request.method == "POST") {
                    mutations.incrementAndGet()
                    if (request.sequenceNumber > 0) return MockResponse().setSocketPolicy(SocketPolicy.DISCONNECT_AFTER_REQUEST)
                }
                if (request.path.orEmpty().endsWith("/chat-materials")) {
                    if (!recover.get()) {
                        warmSequences += request.sequenceNumber
                        warm.countDown()
                        check(warm.await(5, TimeUnit.SECONDS))
                    } else {
                        attempts.incrementAndGet()
                        if (request.sequenceNumber > 0) return MockResponse().setSocketPolicy(SocketPolicy.DISCONNECT_AFTER_REQUEST)
                    }
                }
                return MockResponse().setBody(if (request.path.orEmpty().endsWith("/session")) identity else """{"notes":[]}""")
            }
        }
        server.start()
        try {
            val origin = server.url("/").toString().removeSuffix("/")
            CoreClient(origin, SessionCookies(origin, MemorySecrets())).use { core ->
                core.session()
                coroutineScope {
                    List(3) { async { core.api(HttpMethod.Get, "/chat-materials") } }.awaitAll()
                }
                assertEquals(listOf(0, 0, 1), warmSequences.sorted())
                recover.set(true)
                assertEquals("""{"notes":[]}""", core.api(HttpMethod.Get, "/chat-materials"))
                assertEquals(2, attempts.get())
                assertEquals("""{"notes":[]}""", core.api(HttpMethod.Post, "/chats", "{}"))
                assertEquals(1, mutations.get())
            }
        } finally {
            server.shutdown()
        }
    }

    @Test
    fun readReconnectionStopsAfterOneRetryAndDoesNotRetryHttpErrorsOrOtherIoFailures() = runBlocking<Unit> {
        val reads = listOf(HttpMethod.Get to "/session", HttpMethod.Get to "/chat-materials",
            HttpMethod.Post to "/search", HttpMethod.Post to "/chat-materials/resolve")
        for ((method, path) in reads) for (failure in listOf("eof", "io", "http")) {
            var requests = 0
            val engine = MockEngine { request ->
                if (path != "/session" && request.url.encodedPath.endsWith("/session"))
                    return@MockEngine respond(identity)
                requests++
                if (failure == "eof") throw IOException("closed connection", EOFException())
                if (failure == "io") throw IOException("unavailable")
                respond("""{"error":{"code":"SIGN_IN_REQUIRED"}}""", HttpStatusCode.Unauthorized)
            }
            val origin = "https://notes.example"
            CoreClient(origin, SessionCookies(origin, MemorySecrets()), engine).use { core ->
                suspend fun read() { if (path == "/session") core.session() else core.api(method, path, "{}") }
                if (failure == "http") assertFailsWith<CoreException> { read() }
                else assertFailsWith<IOException> { read() }
            }
            assertEquals(if (failure == "eof") 2 else 1, requests, "$path $failure")
        }
    }

    @Test
    fun eofDoesNotReplayMutationsOrBroadenTheReadOnlyPostPaths() = runBlocking<Unit> {
        for ((method, path) in listOf(HttpMethod.Post to "/notes", HttpMethod.Post to "/chats",
            HttpMethod.Patch to "/notes/one", HttpMethod.Delete to "/notes/one",
            HttpMethod.Post to "/search/other", HttpMethod.Post to "/chat-materials/resolve/other")) {
            var attempts = 0
            val engine = MockEngine { request ->
                if (request.method == HttpMethod.Get && request.url.encodedPath.endsWith("/session"))
                    respond(identity)
                else {
                    attempts++
                    throw IOException("closed connection", EOFException())
                }
            }
            val origin = "https://notes.example"
            CoreClient(origin, SessionCookies(origin, MemorySecrets()), engine).use { core ->
                assertFailsWith<IOException> { core.api(method, path, "{}") }
            }
            assertEquals(1, attempts, "$method $path")
        }
    }
}

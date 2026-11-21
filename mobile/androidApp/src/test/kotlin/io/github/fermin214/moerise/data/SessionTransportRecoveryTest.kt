package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import com.sun.net.httpserver.HttpServer
import io.github.fermin214.moerise.domain.ChatGenerate
import io.github.fermin214.moerise.domain.CoreException
import io.ktor.client.engine.mock.MockEngine
import io.ktor.client.engine.mock.respond
import io.ktor.http.HttpMethod
import io.ktor.http.HttpStatusCode
import java.io.EOFException
import java.io.IOException
import java.net.InetSocketAddress
import java.util.concurrent.CountDownLatch
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors
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
        val readPorts = CopyOnWriteArrayList<Int>()
        val materialPorts = CopyOnWriteArrayList<Int>()
        val messagePorts = CopyOnWriteArrayList<Int>()
        val cookies = CopyOnWriteArrayList<String>()
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.createContext("/api/app/v1") { exchange ->
            exchange.use {
                if (exchange.requestMethod == "GET") {
                    val materials = exchange.requestURI.path.endsWith("/chat-materials")
                    if (materials) {
                        materialPorts += exchange.remoteAddress.port
                        if (materialReads.incrementAndGet() == 1) return@createContext
                    } else {
                        readPorts += exchange.remoteAddress.port
                        cookies += exchange.requestHeaders.getFirst("Cookie").orEmpty()
                        if (reads.incrementAndGet() == 2) return@createContext
                    }
                    val body = (if (materials) """{"notes":[]}""" else identity).toByteArray()
                    exchange.responseHeaders.add("Content-Type", "application/json")
                    exchange.responseHeaders.add("Set-Cookie", "moerise_local_session=fixture; Path=/; Max-Age=300; HttpOnly")
                    exchange.sendResponseHeaders(200, body.size.toLong())
                    exchange.responseBody.write(body)
                } else {
                    exchange.requestBody.readBytes()
                    if (exchange.requestURI.path.endsWith("/messages")) messagePorts += exchange.remoteAddress.port
                    else writes.incrementAndGet()
                }
            }
        }
        server.start()
        try {
            val origin = "http://127.0.0.1:${server.address.port}"
            CoreClient(origin, SessionCookies(origin, MemorySecrets())).use { core ->
                core.session()
                core.session()
                assertEquals(3, reads.get())
                assertEquals(readPorts[0], readPorts[1])
                assertNotEquals(readPorts[1], readPorts[2])
                assertEquals(listOf("", "moerise_local_session=fixture", "moerise_local_session=fixture"), cookies.toList())
                assertEquals("""{"notes":[]}""", core.api(HttpMethod.Get, "/chat-materials"))
                assertEquals(2, materialReads.get())
                assertEquals(readPorts.last(), materialPorts.first())
                assertNotEquals(materialPorts[0], materialPorts[1])
                assertFailsWith<IOException> {
                    core.chatStream("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
                        ChatGenerate("insight-001", 1, "insight", "", "en")) {}
                }
                assertNotEquals(materialPorts.last(), messagePorts.single())
                assertFailsWith<IOException> { core.api(HttpMethod.Post, "/notes", "{}", "fixed") }
                assertEquals(1, writes.get())
            }
        } finally { server.stop(0) }
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
    fun readsRecoverAndMutationsBypassExpiredConnectionsWithoutReplay() = runBlocking<Unit> {
        val warm = CountDownLatch(3)
        val expired = CopyOnWriteArrayList<Int>()
        val recover = AtomicBoolean()
        val attempts = AtomicInteger()
        val mutations = AtomicInteger()
        val recoveredPorts = CopyOnWriteArrayList<Int>()
        val executor = Executors.newCachedThreadPool()
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.executor = executor
        server.createContext("/api/app/v1") { exchange ->
            exchange.use {
                if (exchange.requestMethod == "POST") {
                    exchange.requestBody.readBytes()
                    mutations.incrementAndGet()
                    if (exchange.remoteAddress.port in expired) return@createContext
                }
                if (exchange.requestURI.path.endsWith("/chat-materials")) {
                    if (!recover.get()) {
                        expired += exchange.remoteAddress.port
                        warm.countDown()
                        check(warm.await(5, TimeUnit.SECONDS))
                    } else {
                        attempts.incrementAndGet()
                        if (exchange.remoteAddress.port in expired) return@createContext
                        recoveredPorts += exchange.remoteAddress.port
                    }
                }
                val body = (if (exchange.requestURI.path.endsWith("/session")) identity else """{"notes":[]}""").toByteArray()
                exchange.sendResponseHeaders(200, body.size.toLong())
                exchange.responseBody.write(body)
            }
        }
        server.start()
        try {
            val origin = "http://127.0.0.1:${server.address.port}"
            CoreClient(origin, SessionCookies(origin, MemorySecrets())).use { core ->
                core.session()
                coroutineScope {
                    List(3) { async { core.api(HttpMethod.Get, "/chat-materials") } }.awaitAll()
                }
                assertEquals(3, expired.toSet().size)
                recover.set(true)
                assertEquals("""{"notes":[]}""", core.api(HttpMethod.Get, "/chat-materials"))
                assertEquals(2, attempts.get())
                expired += recoveredPorts
                assertEquals("""{"notes":[]}""", core.api(HttpMethod.Post, "/chats", "{}"))
                assertEquals(1, mutations.get())
            }
        } finally {
            server.stop(0)
            executor.shutdownNow()
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

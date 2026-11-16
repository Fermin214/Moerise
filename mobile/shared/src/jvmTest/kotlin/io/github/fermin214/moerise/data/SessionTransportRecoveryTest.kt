package io.github.fermin214.moerise.data

import com.sun.net.httpserver.HttpServer
import io.github.fermin214.moerise.domain.CoreException
import io.ktor.client.engine.mock.MockEngine
import io.ktor.client.engine.mock.respond
import io.ktor.http.HttpMethod
import io.ktor.http.HttpStatusCode
import java.io.EOFException
import java.io.IOException
import java.net.InetSocketAddress
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.*
import kotlinx.coroutines.runBlocking

class SessionTransportRecoveryTest {
    private val identity = """{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"csrf"}"""

    @Test
    fun expiredPooledConnectionReconnectsWithCookieAndDoesNotRepeatWrites() = runBlocking<Unit> {
        val reads = AtomicInteger()
        val writes = AtomicInteger()
        val readPorts = CopyOnWriteArrayList<Int>()
        val cookies = CopyOnWriteArrayList<String>()
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        server.createContext("/api/app/v1") { exchange ->
            exchange.use {
                if (exchange.requestMethod == "GET") {
                    readPorts += exchange.remoteAddress.port
                    cookies += exchange.requestHeaders.getFirst("Cookie").orEmpty()
                    if (reads.incrementAndGet() == 2) return@createContext
                    val body = identity.toByteArray()
                    exchange.responseHeaders.add("Content-Type", "application/json")
                    exchange.responseHeaders.add("Set-Cookie", "moerise_local_session=fixture; Path=/; Max-Age=300; HttpOnly")
                    exchange.sendResponseHeaders(200, body.size.toLong())
                    exchange.responseBody.write(body)
                } else {
                    exchange.requestBody.readBytes()
                    writes.incrementAndGet()
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
                assertFailsWith<IOException> { core.api(HttpMethod.Post, "/notes", "{}", "fixed") }
                assertEquals(1, writes.get())
            }
        } finally { server.stop(0) }
    }

    @Test
    fun sessionReconnectionStopsAfterOneRetryAndDoesNotRetryHttpErrors() = runBlocking<Unit> {
        for (httpError in listOf(false, true)) {
            var requests = 0
            val engine = MockEngine {
                requests++
                if (!httpError) throw IOException("closed connection", EOFException())
                respond("""{"error":{"code":"SIGN_IN_REQUIRED"}}""", HttpStatusCode.Unauthorized)
            }
            val origin = "https://notes.example"
            CoreClient(origin, SessionCookies(origin, MemorySecrets()), engine).use { core ->
                if (httpError) assertFailsWith<CoreException> { core.session() }
                else assertFailsWith<IOException> { core.session() }
            }
            assertEquals(if (httpError) 1 else 2, requests)
        }
    }
}

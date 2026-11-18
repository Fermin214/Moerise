package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.presentation.*
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.serialization.json.*

class DiscoveryRecoveryTest {
    private val identity = """{"instance_id":"one","user":{"name":"users/owner"},"timezone":"Pacific/Kiritimati","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}"""
    private val key = "https://notes.example\u0000one\u0000users/owner"
    private fun note(name: String) = """{"instance_id":"one","name":"memos/$name","content":"$name","state":"NORMAL","source_hash":"hash-$name"}"""

    @Test
    fun reviewUsesServiceDatePreservesPositionAndDiscardsAccountResponse() = runBlocking<Unit> {
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val arrived = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        val ended = CompletableDeferred<Unit>()
        val calls = AtomicInteger()
        val engine = MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(identity)
            else {
                assertEquals("/api/app/v1/reviews/daily", request.url.encodedPath)
                if (calls.incrementAndGet() == 3) {
                    arrived.complete(Unit)
                    withContext(NonCancellable) { release.await() }
                    ended.complete(Unit)
                }
                respond("""{"id":"batch","date":"2026-11-15","timezone":"Pacific/Kiritimati","items":[{"name":"memos/a","selected_source_hash":"old","selected_at":"2026-11-14T20:00:00Z","unavailable":false,"note":${note("a")}},{"name":"memos/deleted","selected_source_hash":"old","selected_at":"2026-11-14T20:00:00Z","unavailable":true}]}""")
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            val review = ReviewController({ core }, { key }, scope)
            withContext(dispatcher) { review.visible(true) }
            withTimeout(10_000) { review.state.first { !it.loading && it.batch != null } }
            assertEquals("2026-11-15", review.state.value.batch?.date)
            assertEquals("Pacific/Kiritimati", review.state.value.batch?.timezone)
            assertTrue(review.state.value.batch!!.items[1].unavailable)
            withContext(dispatcher) { review.select(1); review.visible(false); review.visible(true) }
            withTimeout(10_000) { review.state.first { !it.loading } }
            assertEquals(1, review.state.value.position)
            withContext(dispatcher) { review.refresh() }
            withTimeout(10_000) { arrived.await() }
            withContext(dispatcher) { review.clear() }
            release.complete(Unit)
            withTimeout(10_000) { ended.await() }
            assertNull(review.state.value.batch)
            assertFalse(review.state.value.loading)
        } finally { release.complete(Unit); scope.cancel(); core.close(); dispatcher.close() }
    }

    @Test
    fun walkUsesExactPathSerializesRequestsAndNeverRetriesOnReturn() = runBlocking<Unit> {
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val requests = CopyOnWriteArrayList<JsonObject>()
        val arrived = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        val endArrived = CompletableDeferred<Unit>()
        val endRelease = CompletableDeferred<Unit>()
        val endEnded = CompletableDeferred<Unit>()
        val calls = AtomicInteger()
        val engine = MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) respond(identity)
            else if (request.url.encodedPath.endsWith("/models")) {
                assertEquals(HttpMethod.Get, request.method)
                respond("""{"embedding":{"enabled":true,"configured":true}}""")
            } else if (request.url.encodedPath.endsWith("/notes")) {
                assertEquals(HttpMethod.Get, request.method)
                assertEquals("NORMAL", request.url.parameters["state"])
                respond("""{"notes":[],"next_page_token":""}""")
            }
            else {
                assertEquals("https://notes.example", request.headers[HttpHeaders.Origin])
                assertEquals("token", request.headers["X-CSRF-Token"])
                val body = CoreJson.parseToJsonElement(request.body.toByteArray().decodeToString()).jsonObject
                requests += body
                when (calls.incrementAndGet()) {
                    1 -> {
                        arrived.complete(Unit); release.await()
                        assertEquals(setOf("name"), body.keys)
                        respond("""{"mode":"continue","item":{"note":${note("a")}},"path":["memos/a"],"exhausted":false,"warnings":[]}""")
                    }
                    2 -> respond("""{"mode":"continue","item":{"note":${note("b")}},"path":["memos/a","memos/b"],"exhausted":false,"warnings":[]}""")
                    3 -> respond("""{"error":{"code":"INDEX_NOT_READY"}}""", HttpStatusCode.ServiceUnavailable)
                    4 -> respond("""{"mode":"continue","item":{"note":${note("b")}},"path":["memos/a","memos/b","memos/b"],"exhausted":false,"warnings":[]}""")
                    5 -> respond("""{"mode":"continue","item":null,"path":["memos/a","memos/b"],"exhausted":true,"warnings":[]}""")
                    else -> {
                        endArrived.complete(Unit)
                        withContext(NonCancellable) { endRelease.await() }
                        endEnded.complete(Unit)
                        respond("""{"mode":"random","item":{"note":${note("a")}},"path":["memos/a"],"exhausted":false,"warnings":[]}""")
                    }
                }
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            val walk = WalkController({ core }, { key }, scope)
            withContext(dispatcher) { walk.visible(true); walk.start("memos/a"); walk.start("memos/a") }
            withTimeout(10_000) { arrived.await() }
            assertEquals(1, calls.get())
            release.complete(Unit)
            withTimeout(10_000) { walk.state.first { !it.loading && it.result != null } }
            withContext(dispatcher) { walk.next() }
            withTimeout(10_000) { walk.state.first { !it.loading && it.result?.item?.note?.name == "memos/b" } }
            assertEquals("memos/a", walk.state.value.previous?.name)
            assertEquals(listOf("memos/a"), requests[1].getValue("path").jsonArray.map { it.jsonPrimitive.content })
            withContext(dispatcher) { walk.next() }
            withTimeout(10_000) { walk.state.first { !it.loading && it.error == "INDEX_NOT_READY" } }
            assertEquals(listOf("memos/a", "memos/b"), walk.state.value.result?.path)
            withContext(dispatcher) { walk.visible(false); walk.visible(true) }
            assertEquals(3, calls.get())
            withContext(dispatcher) { walk.retry() }
            withTimeout(10_000) { walk.state.first { !it.loading && it.error == "WALK_RESPONSE_INVALID" } }
            assertEquals(requests[2], requests[3])
            withContext(dispatcher) { walk.retry() }
            withTimeout(10_000) { walk.state.first { !it.loading && it.result?.exhausted == true } }
            assertEquals(requests[3], requests[4])
            assertNull(walk.state.value.retry)
            withContext(dispatcher) { walk.start() }
            withTimeout(10_000) { endArrived.await() }
            withContext(dispatcher) { walk.end() }
            endRelease.complete(Unit)
            withTimeout(10_000) { endEnded.await() }
            assertNull(walk.state.value.result)
            assertNull(walk.state.value.retry)
            assertFalse(walk.state.value.loading)
        } finally {
            release.complete(Unit); endRelease.complete(Unit)
            scope.cancel(); core.close(); dispatcher.close()
        }
    }
}

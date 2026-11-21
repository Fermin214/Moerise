package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.presentation.SearchController
import io.github.fermin214.moerise.presentation.SearchQuery
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class SearchRecoveryTest {
    @Test
    fun changedQueryDoesNotReuseCursorAndExpiredCursorKeepsResultsForRefresh() = runBlocking<Unit> {
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val key = "https://notes.example\u0000one\u0000users/owner"
        val requests = CopyOnWriteArrayList<Parameters>()
        val oldArrived = CompletableDeferred<Unit>()
        val releaseOld = CompletableDeferred<Unit>()
        val oldEnded = CompletableDeferred<Unit>()
        val engine = MockEngine { request ->
            when (request.url.encodedPath.substringAfter("/api/app/v1")) {
                "/session" -> respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""")
                "/tags" -> {
                    assertEquals("ARCHIVED", request.url.parameters["state"])
                    respond("""{"tags":[{"tag":"C++","count":1}]}""")
                }
                "/calendar" -> {
                    assertEquals("ARCHIVED", request.url.parameters["state"])
                    respond("""{"month":"2026-11","days":[{"date":"2026-11-14","count":2}]}""")
                }
                "/notes" -> {
                    assertEquals(HttpMethod.Get, request.method)
                    val parameters = request.url.parameters
                    requests += parameters
                    val query = parameters["query"]
                    if (query == "old") {
                        oldArrived.complete(Unit)
                        withContext(NonCancellable) { releaseOld.await() }
                        oldEnded.complete(Unit)
                    }
                    if (parameters["page_token"] == "cursor") {
                        respond("""{"error":{"code":"INVALID_PAGE_TOKEN"}}""", HttpStatusCode.UnprocessableEntity)
                    } else respond("""{"notes":[{"instance_id":"one","name":"memos/$query","content":"$query","state":"NORMAL","source_hash":"hash"}],"next_page_token":"cursor"}""")
                }
                else -> error("Unexpected request: ${request.url.encodedPath}")
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            val controller = SearchController({ core }, { key }, scope)
            withContext(dispatcher) { controller.change(SearchQuery(text = "old")); controller.search() }
            withTimeout(10_000) { oldArrived.await() }
            withContext(dispatcher) { controller.change(SearchQuery(text = "new", tags = setOf("C++"), date = "2026-11-14")); controller.search() }
            withTimeout(10_000) { controller.state.first { !it.loading && it.notes.firstOrNull()?.name == "memos/new" } }
            releaseOld.complete(Unit)
            withTimeout(10_000) { oldEnded.await() }
            assertEquals("memos/new", controller.state.value.notes.single().name)
            withContext(dispatcher) { controller.search(true) }
            withTimeout(10_000) { controller.state.first { !it.loading && it.error != null } }
            assertEquals("INVALID_PAGE_TOKEN", controller.state.value.error)
            assertEquals("memos/new", controller.state.value.notes.single().name)
            withContext(dispatcher) { controller.search() }
            withTimeout(10_000) { controller.state.first { !it.loading && it.error == null } }
            assertEquals("", requests.last()["page_token"].orEmpty())
            assertEquals(listOf("C++"), requests.last().getAll("tag"))
            assertEquals("2026-11-14", requests.last()["date"])
            withContext(dispatcher) { controller.change(controller.state.value.query.copy(state = "ARCHIVED")); controller.filters("2026-11") }
            withTimeout(10_000) { controller.state.first { !it.filtersLoading && it.calendar != null } }
            assertEquals("C++", controller.state.value.tags.single().tag)
            assertEquals(2, controller.state.value.calendar?.days?.single()?.count)
        } finally {
            releaseOld.complete(Unit)
            scope.cancel(); core.close(); dispatcher.close()
        }
    }
}

package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.*
import io.github.fermin214.moerise.presentation.*
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.nio.file.Files
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first

class StarMapRecoveryTest {
    @Test
    fun lostRebuildResponseIsObservedAfterReopenAndLateReaderCannotReplaceNewHash() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-stars-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        var scope = CoroutineScope(SupervisorJob() + dispatcher)
        var db = jvmDatabase(path)
        var repository = NoteRepository(db)
        val key = "https://notes.example\u0000one\u0000users/owner"
        val base = CoreJson.decodeFromString<StarMap>(javaClass.getResourceAsStream("/contracts/map/ready.json")!!.bufferedReader().use { it.readText() })
        val remote = AtomicReference(base)
        val posts = AtomicInteger()
        val noteCalls = AtomicInteger()
        val oldArrived = CompletableDeferred<Unit>()
        val releaseOld = CompletableDeferred<Unit>()
        val oldEnded = CompletableDeferred<Unit>()
        fun engine() = MockEngine { request ->
            when (request.url.encodedPath.removePrefix("/api/app/v1")) {
                "/session" -> respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""")
                "/map" -> respond(CoreJson.encodeToString(remote.get()))
                "/map/rebuild" -> { posts.incrementAndGet(); assertEquals("{}", request.body.toByteArray().decodeToString()); throw java.io.IOException("response lost") }
                "/notes/fixture" -> {
                    val first = noteCalls.incrementAndGet() == 1
                    val hash = remote.get().points.single().sourceHash
                    if (first) {
                        oldArrived.complete(Unit)
                        withContext(NonCancellable) { releaseOld.await() }
                        oldEnded.complete(Unit)
                    }
                    respond(CoreJson.encodeToString(Note("one", "memos/fixture", if (first) "old body" else "new body", "NORMAL", hash)))
                }
                else -> error("unexpected ${request.url}")
            }
        }
        var core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine())
        try {
            var controller = StarMapController({ repository }, { core }, { key }, scope)
            withContext(dispatcher) { controller.size(800.0, 600.0); controller.visible(true) }
            withTimeout(10_000) { controller.state.first { !it.loading && it.snapshot != null } }
            withContext(dispatcher) { controller.transform(2.0, panX = 35.0); controller.rebuild(); controller.rebuild() }
            withTimeout(10_000) { controller.state.first { !it.posting && it.error != null } }
            assertEquals(1, posts.get())
            assertEquals("unknown", CoreJson.decodeFromString<MapRebuild>(repository.feature(key, "map-rebuild")!!).state)
            assertEquals(base.layoutVersion, controller.state.value.snapshot?.layoutVersion)
            withContext(dispatcher) { controller.clear() }
            scope.coroutineContext[Job]!!.cancelAndJoin(); core.close(); db.close()
            db = jvmDatabase(path); repository = NoteRepository(db)
            scope = CoroutineScope(SupervisorJob() + dispatcher)
            core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine())
            controller = StarMapController({ repository }, { core }, { key }, scope)
            withContext(dispatcher) { controller.size(800.0, 600.0); controller.visible(true) }
            withTimeout(10_000) { controller.state.first { !it.loading && it.rebuild != null } }
            assertEquals(1, posts.get())
            assertEquals("unknown", controller.state.value.rebuild?.state)
            withContext(dispatcher) { controller.transform(2.0, panX = 35.0) }
            val view = controller.state.value.view
            remote.set(base.copy(updateState = "building"))
            withContext(dispatcher) { controller.refresh() }
            withTimeout(10_000) { controller.state.first { !it.loading && it.snapshot?.updateState == "building" } }
            assertEquals(view, controller.state.value.view)
            withContext(dispatcher) { controller.select("memos/fixture") }
            withTimeout(10_000) { oldArrived.await() }
            remote.set(base.copy(layoutVersion = "d".repeat(64), points = base.points.map { it.copy(sourceHash = "e".repeat(64)) }))
            withContext(dispatcher) { controller.refresh() }
            withTimeout(10_000) { controller.state.first { it.reader?.content == "new body" } }
            releaseOld.complete(Unit)
            withTimeout(10_000) { oldEnded.await() }
            assertEquals("new body", controller.state.value.reader?.content)
            assertEquals(view, controller.state.value.view)
            remote.set(remote.get().copy(layoutFrame = "f".repeat(64), layoutVersion = "1".repeat(64)))
            withContext(dispatcher) { controller.refresh() }
            withTimeout(10_000) { controller.state.first { !it.loading && it.rebuild?.state == "complete" } }
            assertNotEquals(view, controller.state.value.view)
            assertEquals("complete", CoreJson.decodeFromString<MapRebuild>(repository.feature(key, "map-rebuild")!!).state)
            assertEquals(1, posts.get())
            remote.set(remote.get().copy(algorithm = "unknown"))
            withContext(dispatcher) { controller.refresh() }
            withTimeout(10_000) { controller.state.first { !it.loading && !it.compatible } }
            assertEquals("MAP_INCOMPATIBLE", controller.state.value.error)
            withContext(dispatcher) { controller.rebuild(true) }
            assertEquals(1, posts.get())
        } finally {
            releaseOld.complete(Unit)
            scope.coroutineContext[Job]!!.cancelAndJoin(); core.close(); db.close(); dispatcher.close(); folder.deleteRecursively()
        }
    }
}

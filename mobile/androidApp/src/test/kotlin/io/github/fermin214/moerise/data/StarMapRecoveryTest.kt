package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

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

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class StarMapRecoveryTest {
    @Test
    fun offlineReopenRestoresRoomSnapshotAndReadsMatchingLocalNote() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("offline-stars-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        var db = testDatabase(path)
        var repo = NoteRepository(db)
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val key = "https://notes.example\u0000one\u0000users/owner"
        val body = javaClass.getResourceAsStream("/contracts/map/ready.json")!!.bufferedReader().use { it.readText() }
        val map = CoreJson.decodeFromString<StarMap>(body)
        try {
            repo.putCachedSnapshot(key, "map", CoreJson.encodeToString(map))
            repo.recordLocalNote(key, Note("one", "memos/fixture", "离线正文", "NORMAL", map.points.single().sourceHash))
            db.close()
            db = testDatabase(path); repo = NoteRepository(db)
            val stars = StarMapController({ repo }, { null }, { key }, scope, online = { false })
            withContext(dispatcher) { stars.size(800.0, 600.0); stars.visible(true) }
            withTimeout(10_000) { stars.state.first { it.snapshot != null && !it.loading } }
            assertEquals(map.points, stars.state.value.snapshot!!.points)
            assertNotNull(stars.state.value.view)
            withContext(dispatcher) { stars.select("memos/fixture") }
            withTimeout(10_000) { stars.state.first { !it.reading && it.reader != null } }
            assertEquals("离线正文", stars.state.value.reader!!.content)
            withContext(dispatcher) { stars.clear() }
            assertNull(stars.state.value.snapshot)
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin(); db.close(); dispatcher.close(); folder.deleteRecursively()
        }
    }

    @Test
    fun lostRebuildResponseIsObservedAfterReopenAndLateReaderCannotReplaceNewHash() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-stars-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        var scope = CoroutineScope(SupervisorJob() + dispatcher)
        var db = testDatabase(path)
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
            db = testDatabase(path); repository = NoteRepository(db)
            scope = CoroutineScope(SupervisorJob() + dispatcher)
            core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine())
            controller = StarMapController({ repository }, { core }, { key }, scope)
            withContext(dispatcher) { controller.size(800.0, 600.0); controller.visible(true) }
            withTimeout(10_000) { controller.state.first { !it.loading && it.rebuild != null } }
            assertEquals(1, posts.get())
            assertEquals("unknown", controller.state.value.rebuild?.state)
            remote.set(base.copy(status = "unavailable", layoutVersion = "", layoutFrame = "", vectorGeneration = "",
                points = emptyList(), topics = emptyList(), links = emptyList(), unavailableReason = "layout"))
            withContext(dispatcher) { controller.refresh() }
            withTimeout(10_000) { controller.state.first { !it.loading && it.unavailableReason == "layout" } }
            assertEquals("unknown", controller.state.value.rebuild?.state)
            withContext(dispatcher) { controller.transform(2.0, panX = 35.0) }
            val view = controller.state.value.view
            remote.set(base.copy(updateState = "unavailable", unavailableReason = "layout"))
            withContext(dispatcher) { controller.refresh() }
            withTimeout(10_000) { controller.state.first { !it.loading && it.snapshot?.updateState == "unavailable" } }
            assertEquals("unknown", controller.state.value.rebuild?.state)
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
            remote.set(remote.get().copy(layoutFrame = "f".repeat(64), layoutVersion = "1".repeat(64),
                updateState = "unavailable", unavailableReason = "layout"))
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

    @Test
    fun clearedExcerptInvalidatesReaderAndIgnoresStaleLocalData() = runBlocking<Unit> {
        val folder = Files.createTempDirectory("moerise-stars-cleared-").toFile()
        val path = folder.resolve("data.sqlite3").absolutePath
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val db = testDatabase(path)
        val repository = NoteRepository(db)
        val key = "https://notes.example\u0000one\u0000users/owner"
        val base = CoreJson.decodeFromString<StarMap>(javaClass.getResourceAsStream("/contracts/map/ready.json")!!.bufferedReader().use { it.readText() })
        val pointHash = base.points.single().sourceHash
        repository.recordLocalNote(key, Note("one", "memos/fixture", "old room body", "NORMAL", pointHash))
        val remote = AtomicReference(base.copy(points = base.points.map { it.copy(excerpt = "initial excerpt") }))
        val noteCalls = AtomicInteger()
        val engine = MockEngine { request ->
            when (request.url.encodedPath.removePrefix("/api/app/v1")) {
                "/session" -> respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""")
                "/map" -> respond(CoreJson.encodeToString(remote.get()))
                "/notes/fixture" -> {
                    noteCalls.incrementAndGet()
                    respond(CoreJson.encodeToString(Note("one", "memos/fixture", "old reader body", "NORMAL", pointHash)))
                }
                else -> error("unexpected ${request.url}")
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            val controller = StarMapController({ repository }, { core }, { key }, scope)
            withContext(dispatcher) { controller.size(800.0, 600.0); controller.visible(true) }
            withTimeout(10_000) { controller.state.first { !it.loading && it.snapshot != null } }
            withContext(dispatcher) { controller.select("memos/fixture") }
            withTimeout(10_000) { controller.state.first { it.reader?.content == "old reader body" } }
            withContext(dispatcher) { controller.excerpts(listOf("memos/fixture")) }
            assertEquals("initial excerpt", controller.state.value.excerpts["memos/fixture"])

            remote.set(base.copy(projectionRevision = base.projectionRevision + 1, points = base.points.map { it.copy(excerpt = "") }))
            withContext(dispatcher) { controller.refresh() }
            withTimeout(10_000) { controller.state.first { !it.loading && it.snapshot?.points?.single()?.excerpt == "" } }
            assertNull(controller.state.value.reader)
            assertFalse("memos/fixture" in controller.state.value.excerpts)

            withContext(dispatcher) { controller.excerpts(listOf("memos/fixture")) }
            assertFalse("memos/fixture" in controller.state.value.excerpts)
            assertEquals(1, noteCalls.get())
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin(); core.close(); db.close(); dispatcher.close(); folder.deleteRecursively()
        }
    }
}

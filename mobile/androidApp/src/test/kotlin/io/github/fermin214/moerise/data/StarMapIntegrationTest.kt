package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.domain.*
import io.ktor.http.HttpMethod
import java.io.File
import java.io.IOException
import java.util.UUID
import java.util.concurrent.TimeUnit
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.serialization.json.*

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class StarMapIntegrationTest {
    @Test
    fun realUmapFitTransformDeleteRebuildAndMissingModelUseTheSharedClient() = runBlocking<Unit> {
        val file = File(requireNotNull(System.getenv("MOERISE_ANDROID_FIXTURE")))
        val fixture = CoreJson.parseToJsonElement(file.readText()).jsonObject
        fun field(name: String) = fixture.getValue(name).jsonPrimitive.content
        require(fixture["stars"]?.jsonPrimitive?.boolean == true) { "使用 scripts/android/stars-check.sh" }
        val container = field("layoutContainer")
        require(Regex("^moerise-android-[a-f0-9]{12}-layout$").matches(container))
        val secrets = object : SecretStore {
            val values = mutableMapOf<String, String>()
            override suspend fun read(key: String) = values[key]
            override suspend fun write(key: String, value: String?) { if (value == null) values.remove(key) else values[key] = value; Unit }
        }
        fun worker(script: String): String {
            val process = ProcessBuilder("docker", "exec", container, "python", "-c", script)
                .redirectErrorStream(true).start()
            check(process.waitFor(60, TimeUnit.SECONDS)) { process.destroyForcibly(); "布局容器检查超时" }
            val text = process.inputStream.bufferedReader().readText()
            check(process.exitValue() == 0) { text }
            return text.trim()
        }
        fun model(): JsonObject = CoreJson.parseToJsonElement(worker("""
            import json, joblib
            from pathlib import Path
            paths = list(Path('/data/models').rglob('*.joblib'))
            state = joblib.load(max(paths, key=lambda p: p.stat().st_mtime))
            print(json.dumps(dict(model=type(state['model']).__name__, fit_count=state['fit_count'])))
        """.trimIndent())).jsonObject
        val core = CoreClient(field("origin"), SessionCookies(field("origin"), secrets))
        var transportRetries = 0
        suspend fun request(method: HttpMethod, path: String, body: String? = null, key: String? = null): String {
            for (attempt in 0..2) {
                try { return core.api(method, path, body, key) }
                catch (error: IOException) {
                    if (attempt == 2 || method != HttpMethod.Get && key == null) throw error
                    ++transportRetries
                    delay(500)
                }
            }
            error("Transport retry exhausted")
        }
        try {
            core.login(field("username"), field("password"))
            suspend fun ready(count: Int, accept: (StarMap) -> Boolean = { true }): StarMap = withTimeout(950_000) {
                while (true) {
                    val response = try { request(HttpMethod.Get, "/map") }
                    catch (error: CoreException) {
                        if (error.status !in setOf(429, 503)) throw error
                        delay(3000); continue
                    }
                    val map = CoreJson.decodeFromString<StarMap>(response).validate()
                    if (map.status == "ready" && map.points.size == count && map.updateState.isEmpty() && accept(map))
                        return@withTimeout map
                    delay(3000)
                }
                @Suppress("UNREACHABLE_CODE") error("布局未就绪")
            }
            fun stable(before: StarMap, after: StarMap, except: Set<String> = emptySet()) {
                assertEquals(before.layoutFrame, after.layoutFrame)
                val old = before.points.associateBy { it.name }
                for (point in after.points) if (point.name !in except && point.name in old) {
                    val prior = old.getValue(point.name)
                    assertEquals(listOf(prior.x, prior.y, prior.stellar), listOf(point.x, point.y, point.stellar))
                }
            }
            val initial = ready(32)
            assertEquals("umap-balanced-v1", initial.algorithm)
            assertTrue(initial.points.all { it.semantic && it.stellar != null })
            assertTrue(initial.topics.isNotEmpty())
            val firstModel = model()
            assertEquals("UMAP", firstModel.getValue("model").jsonPrimitive.content)
            assertEquals(32, firstModel.getValue("fit_count").jsonPrimitive.int)
            val added = CoreJson.decodeFromString<Note>(request(HttpMethod.Post, "/notes",
                """{"content":"Android star group 0 item 32 #stars"}""", UUID.randomUUID().toString()))
            val increment = ready(33) { it.layoutVersion != initial.layoutVersion }
            stable(initial, increment)
            assertTrue(increment.points.any { it.name == added.name })
            val transformedModel = model()
            assertEquals(32, transformedModel.getValue("fit_count").jsonPrimitive.int)

            val target = initial.points.first().name
            suspend fun change(suffix: String): Note {
                val path = "/notes/${target.removePrefix("memos/")}"
                val note = CoreJson.decodeFromString<Note>(request(HttpMethod.Get, path))
                return CoreJson.decodeFromString(request(HttpMethod.Patch, path, buildJsonObject {
                    put("source_hash", note.sourceHash); put("content", note.content + suffix)
                }.toString(), UUID.randomUUID().toString()))
            }
            val edited = change(" changed vector")
            val vectorUpdate = ready(33) { it.points.any { point -> point.name == target && point.sourceHash == edited.sourceHash } }
            stable(increment, vectorUpdate, setOf(target))
            request(HttpMethod.Delete, "/notes/${added.name.removePrefix("memos/")}",
                buildJsonObject { put("source_hash", added.sourceHash) }.toString())
            val deleted = ready(32) { it.layoutVersion != vectorUpdate.layoutVersion }
            stable(vectorUpdate, deleted)
            request(HttpMethod.Post, "/map/rebuild", "{}")
            val rebuilt = ready(32) { it.layoutFrame != deleted.layoutFrame }
            assertEquals("UMAP", model().getValue("model").jsonPrimitive.content)

            worker("from pathlib import Path\nfor p in Path('/data/models').rglob('*.joblib'): p.unlink()")
            val afterLoss = change(" missing model")
            val refitted = ready(32) { it.layoutFrame != rebuilt.layoutFrame &&
                it.points.any { point -> point.name == target && point.sourceHash == afterLoss.sourceHash } }
            val finalModel = model()
            assertEquals("UMAP", finalModel.getValue("model").jsonPrimitive.content)
            assertEquals(32, finalModel.getValue("fit_count").jsonPrimitive.int)
            File(file.parentFile, "stars-result.json").writeText(buildJsonObject {
                put("algorithm", initial.algorithm); put("initial_model", firstModel); put("transform_model", transformedModel)
                put("refit_model", finalModel); put("points", refitted.points.size)
                put("initial_frame", initial.layoutFrame); put("rebuilt_frame", rebuilt.layoutFrame)
                put("refitted_frame", refitted.layoutFrame); put("layout_image", field("layoutImage"))
                put("transport_retries", transportRetries)
                put("checks", JsonArray(listOf("fit", "insert-transform", "vector-transform", "delete", "rebuild", "missing-model-fit").map(::JsonPrimitive)))
            }.toString())
        } finally {
            runCatching { core.logout() }
            core.close()
        }
    }
}

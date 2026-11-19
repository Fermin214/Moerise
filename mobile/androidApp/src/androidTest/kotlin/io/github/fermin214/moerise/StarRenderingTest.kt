package io.github.fermin214.moerise

import android.graphics.Bitmap
import androidx.activity.compose.setContent
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.*
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.*
import io.github.fermin214.moerise.presentation.*
import java.io.File
import java.util.concurrent.atomic.AtomicLong
import io.ktor.client.engine.mock.*
import kotlinx.coroutines.*
import kotlin.math.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class StarRenderingTest {
    @get:Rule val compose = fixtureComposeRule()

    private class Scene(val map: StarMap, photo: Boolean) {
        val state = mutableStateOf(StarMapState(snapshot = map, photo = photo))
        val drawn = AtomicLong()
    }

    @Test
    fun mapReaderKeepsTheLastStarAndRestoresTheView() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val db = androidDatabase(context)
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
        val map = snapshot(16).let { source ->
            source.copy(points = source.points + StarPoint("memos/unplaced", "unplaced", 0.0, 0.0, false, emptyList()))
        }.validate()
        val origin = "https://stars.example"
        val slow = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        val unexpected = mutableListOf<String>()
        val core = CoreClient(origin, SessionCookies(origin, AndroidSecretStore(context)), MockEngine { request ->
            when (val path = request.url.encodedPath.removePrefix("/api/app/v1")) {
                "/session" -> respond("""{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2099-01-01T00:00:00Z","csrf_token":"token"}""")
                "/map" -> respond(CoreJson.encodeToString(StarMap.serializer(), map))
                "/notes/fixture0", "/notes/fixture1", "/notes/unplaced" -> {
                    val id = path.substringAfterLast('/')
                    if (id == "fixture0") { slow.complete(Unit); release.await() }
                    respond(CoreJson.encodeToString(Note.serializer(), Note("one", "memos/$id", "Original $id", "NORMAL", id)))
                }
                else -> { synchronized(unexpected) { unexpected += path }; error(path) }
            }
        })
        val repository = NoteRepository(db)
        val controller = StarMapController({ repository }, { core }, { "$origin\u0000one\u0000users/owner" }, scope)
        try {
            compose.activityRule.scenario.onActivity { activity ->
                activity.setContent { StarMapScreen(controller, core, true, Strings(UiLanguage.ENGLISH), rememberLazyListState(), {}, {}) }
                controller.visible(true)
            }
            compose.waitForTag("map_point_memos/fixture0")
            compose.clickTag("map_fullscreen")
            compose.runOnIdle { controller.transform(1.1, panX = 5.0) }
            val previousView = controller.state.value.view
            compose.onNodeWithTag("map_point_memos/fixture0").performClick()
            compose.waitUntil(10_000) { slow.isCompleted }
            compose.onNodeWithTag("map_point_memos/fixture1").performClick()
            release.complete(Unit)
            compose.waitForTag("map_reader")
            compose.waitUntil(10_000) { compose.onNode(readingText("Original fixture1")).isDisplayed() }
            compose.onNode(readingText("Original fixture0")).assertDoesNotExist()
            compose.activityRule.scenario.onActivity { it.onBackPressedDispatcher.onBackPressed() }
            compose.onNodeWithTag("map_reader").assertDoesNotExist()
            check(controller.state.value.fullScreen && controller.state.value.view == previousView)
            compose.clickTag("map_unplaced")
            compose.clickTag("map_unplaced_memos/unplaced")
            compose.waitUntil(10_000) { compose.onNode(readingText("Original unplaced")).isDisplayed() }
            compose.clickTag("map_close_reader")
            check(controller.state.value.view == previousView)
            check(synchronized(unexpected) { "/map/rebuild" !in unexpected })
        } finally {
            release.complete(Unit)
            compose.runOnIdle { controller.clear() }
            scope.cancel()
            core.close()
            db.close()
        }
    }

    @Test
    fun thousandAndFiveThousandStarsRenderSelectPanZoomAndRefresh() {
        val active = mutableStateOf<Scene?>(null)
        compose.activityRule.scenario.onActivity { activity ->
            activity.setContent {
                active.value?.let { scene ->
                    key(scene) {
                        val state = scene.state.value
                        StarCanvas(state, Strings(UiLanguage.CHINESE),
                            onSize = { width, height ->
                                if (scene.state.value.view == null) {
                                    val view = fitStars(scene.map.points, width, height)
                                    scene.state.value = scene.state.value.copy(view = view, fitScale = view.scale)
                                }
                            },
                            onTransform = { factor, x, y, dx, dy ->
                                scene.state.value = scene.state.value.copy(
                                    view = scene.state.value.view?.transform(factor, x, y, dx, dy))
                            },
                            onFocus = {}, onPick = { _, _, _ -> }, onExcerpts = {},
                            onDrawn = { scene.drawn.set(state.snapshot!!.projectionRevision) },
                            onSelect = { scene.state.value = scene.state.value.copy(selected = it) })
                    }
                }
            }
        }
        for (count in listOf(1000, 5000)) for (photo in listOf(false, true)) {
            val scene = Scene(snapshot(count), photo)
            compose.runOnIdle { active.value = scene }
            compose.waitUntil(20_000) { scene.drawn.get() == 1L }
            val canvas = compose.onNodeWithTag("star_canvas").assertIsDisplayed()
            screenshot("$count-$photo-fit", canvas)

            compose.onNodeWithTag("map_point_memos/fixture0").performClick().assertIsSelected()
            val fit = compose.runOnIdle { scene.state.value.view!! }
            canvas.performTouchInput {
                swipe(Offset(width * .4f, height * .5f), Offset(width * .6f, height * .5f), 300)
            }
            compose.runOnIdle { check(scene.state.value.view!!.x != fit.x) { "平移未改变视图" } }
            canvas.performTouchInput {
                pinch(start0 = Offset(width * .4f, height * .5f), end0 = Offset(width * .3f, height * .5f),
                    start1 = Offset(width * .6f, height * .5f), end1 = Offset(width * .7f, height * .5f),
                    durationMillis = 400)
            }
            compose.runOnIdle {
                check(scene.state.value.view!!.scale > fit.scale * 1.5) {
                    "双指缩放未改变视图：before=${fit.scale}，after=${scene.state.value.view!!.scale}"
                }
                val point = scene.map.points.first()
                scene.state.value = scene.state.value.copy(
                    view = fit.copy(x = point.x, y = point.y, scale = fit.scale * 3.4),
                    excerpts = mapOf(point.name to "固定摘要 ${point.name}"),
                    snapshot = scene.map.copy(layoutVersion = sha256("$count-$photo-refresh".encodeToByteArray()),
                        projectionRevision = 2))
            }
            compose.waitUntil(20_000) { scene.drawn.get() == 2L }
            screenshot("$count-$photo-closeup", canvas)
        }
    }

    private fun screenshot(name: String, canvas: SemanticsNodeInteraction) {
        val bitmap = canvas.captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(0, 0)
        var painted = 0
        for (y in 0 until bitmap.height step 2) for (x in 0 until bitmap.width step 2) {
            if (bitmap.getPixel(x, y) != background) painted++
        }
        check(painted > 100) { "星图画面为空：$name" }
        val directory = File(InstrumentationRegistry.getInstrumentation().targetContext.cacheDir,
            "acceptance-diagnostics").apply { mkdirs() }
        File(directory, "stars-$name.png").outputStream().use {
            check(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }

    private fun snapshot(count: Int): StarMap {
        val points = List(count) { index ->
            val group = index % 8
            val angle = index * 2.399963229728653
            val radius = sqrt((index + .5) / count)
            StarPoint("memos/fixture$index", "source$index", group % 4 * 2.8 + cos(angle) * radius,
                group / 4 * 3.5 + sin(angle) * radius, true, listOf("topic$group"),
                Stellar((index % 997) / 997.0,
                    if (index % 251 == 0) "core" else if (index % 137 == 0) "isolated" else "ordinary",
                    .4 + (index % 17) / 17.0, .25 + (index % 31) / 42.0))
        }
        val topics = List(8) { group ->
            StarTopic("topic-" + group.toString(16).padStart(16, '0'), "主题 $group",
                group % 4 * 2.8, group / 4 * 3.5,
                points.filterIndexed { index, _ -> index % 8 == group }.map { it.name })
        }
        return StarMap("ready", "umap-balanced-v1", sha256("version$count".encodeToByteArray()),
            sha256("frame$count".encodeToByteArray()), "fixture-v1", 1, points = points, topics = topics,
            links = points.drop(8).mapIndexed { index, point -> StarLink(points[index].name, point.name, .75) }).validate()
    }
}

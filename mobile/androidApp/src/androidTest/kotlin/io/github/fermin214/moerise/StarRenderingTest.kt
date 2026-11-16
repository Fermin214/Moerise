package io.github.fermin214.moerise

import android.graphics.Bitmap
import androidx.activity.compose.setContent
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.*
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.fermin214.moerise.data.sha256
import io.github.fermin214.moerise.domain.*
import io.github.fermin214.moerise.presentation.*
import java.io.File
import java.util.concurrent.atomic.AtomicLong
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

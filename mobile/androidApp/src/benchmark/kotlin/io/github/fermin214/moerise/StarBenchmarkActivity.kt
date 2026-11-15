package io.github.fermin214.moerise

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import androidx.tracing.Trace
import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.sha256
import io.github.fermin214.moerise.domain.*
import io.github.fermin214.moerise.presentation.*
import kotlinx.coroutines.*
import kotlin.math.*

private fun fixedSnapshot(count: Int, revision: Int): StarMap {
    val points = List(count) { index ->
        val group = index % 8
        val angle = index * 2.399963229728653
        val radius = sqrt((index + .5) / count)
        StarPoint("memos/fixture$index", "source$index", group % 4 * 2.8 + cos(angle) * radius,
            group / 4 * 3.5 + sin(angle) * radius, true, listOf("topic$group"),
            Stellar((index % 997) / 997.0, if (index % 251 == 0) "core" else if (index % 137 == 0) "isolated" else "ordinary",
                .4 + (index % 17) / 17.0, .25 + (index % 31) / 42.0))
    }
    val topics = List(8) { group ->
        val members = points.filterIndexed { index, _ -> index % 8 == group }.map { it.name }
        StarTopic("topic-" + group.toString(16).padStart(16, '0'), "主题 $group", group % 4 * 2.8, group / 4 * 3.5, members)
    }
    return StarMap("ready", "umap-balanced-v1", sha256("version$count-$revision".encodeToByteArray()),
        sha256("frame$count".encodeToByteArray()), "fixture-v1", revision.toLong(), points = points, topics = topics,
        links = points.drop(8).mapIndexed { index, point -> StarLink(points[index].name, point.name, .75) }).validate()
}

class StarBenchmarkActivity : ComponentActivity() {
    private var traceOpen = false
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val count = intent.getIntExtra("points", 1000).also { require(it in setOf(1000, 5000)) }
        val photo = intent.getBooleanExtra("photo", false)
        setContent {
            MaterialTheme {
                var revision by remember { mutableIntStateOf(1) }
                var state by remember { mutableStateOf(StarMapState(photo = photo)) }
                var drawnVersion by remember { mutableLongStateOf(0) }
                var summaries by remember { mutableStateOf(false) }
                var fitted by remember { mutableStateOf<StarView?>(null) }
                LaunchedEffect(revision) {
                    val body = withContext(Dispatchers.Default) { CoreJson.encodeToString(StarMap.serializer(), fixedSnapshot(count, revision)) }
                    if (traceOpen) Trace.endAsyncSection("star_scene_ready", hashCode())
                    Trace.beginAsyncSection("star_scene_ready", hashCode()); traceOpen = true
                    Trace.setCounter("moerise_map_body_bytes", body.encodeToByteArray().size)
                    val map = withContext(Dispatchers.Default) {
                        Trace.beginSection("star_scene_parse")
                        try { CoreJson.decodeFromString(StarMap.serializer(), body).validate() }
                        finally { Trace.endSection() }
                    }
                    state = state.copy(snapshot = map)
                }
                Column(Modifier.fillMaxSize().safeDrawingPadding()) {
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        TextButton(onClick = { state = state.copy(photo = !state.photo) },
                            modifier = Modifier.semantics { contentDescription = "scene_atmosphere" }) { Text("氛围") }
                        TextButton(onClick = {
                            val point = state.snapshot?.points?.firstOrNull()
                            val fit = fitted
                            if (point != null && fit != null) state = state.copy(view = fit.copy(x = point.x, y = point.y, scale = fit.scale * 3.4))
                        }, modifier = Modifier.semantics { contentDescription = "scene_closeup" }) { Text("近景") }
                        TextButton(onClick = {
                            summaries = !summaries
                            val view = state.view
                            val nearest = state.snapshot?.points.orEmpty().sortedBy { point ->
                                (point.x - (view?.x ?: 0.0)).pow(2) + (point.y - (view?.y ?: 0.0)).pow(2)
                            }.take(3)
                            state = state.copy(excerpts = if (summaries) nearest.associate { it.name to "固定摘要 ${it.name}" } else emptyMap())
                        },
                            modifier = Modifier.semantics { contentDescription = "scene_summaries" }) { Text("摘要") }
                        TextButton(onClick = { revision++ },
                            modifier = Modifier.semantics { contentDescription = "scene_refresh" }) { Text("刷新") }
                    }
                    Text("$count", modifier = Modifier.semantics { contentDescription = "scene_ready_$drawnVersion" })
                    Box(Modifier.weight(1f).fillMaxWidth().semantics { contentDescription = "scene_canvas" }) {
                        StarCanvas(state, Strings(UiLanguage.CHINESE),
                            onSize = { width, height ->
                                val map = state.snapshot
                                if (map != null && state.view == null) {
                                    val view = fitStars(map.points, width, height)
                                    fitted = view
                                    state = state.copy(view = view, fitScale = view.scale)
                                }
                            },
                            onTransform = { factor, x, y, dx, dy -> state = state.copy(view = state.view?.transform(factor, x, y, dx, dy)) },
                            onFocus = {}, onPick = { _, _, _ -> }, onExcerpts = {},
                            onDrawn = {
                                val version = state.snapshot?.projectionRevision ?: 0
                                if (drawnVersion != version) {
                                    drawnVersion = version
                                    if (traceOpen) {
                                        Trace.endAsyncSection("star_scene_ready", hashCode()); traceOpen = false
                                    }
                                    reportFullyDrawn()
                                }
                            })
                    }
                }
            }
        }
    }
    override fun onDestroy() {
        if (traceOpen) { Trace.endAsyncSection("star_scene_ready", hashCode()); traceOpen = false }
        super.onDestroy()
    }
}

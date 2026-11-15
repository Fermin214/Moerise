package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.*
import androidx.compose.ui.graphics.Canvas as GraphicsCanvas
import androidx.compose.ui.graphics.drawscope.*
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.*
import androidx.compose.ui.unit.*
import io.github.fermin214.moerise.data.epochMillis
import io.github.fermin214.moerise.domain.*
import kotlinx.coroutines.*
import kotlin.math.*

private val starTints = listOf(Color(0xFFC1D2E9), Color(0xFFD0C8DF), Color(0xFFBDDBD8), Color(0xFFD3D0DF))
private data class PaintedStar(val point: StarPoint, val color: Color, val radius: Float, val alpha: Float, val special: Boolean)
private data class PaintedLabel(val id: String, val members: List<String>, val x: Double, val y: Double, val small: Boolean, val layout: TextLayoutResult)
private data class LabelPlacement(val label: PaintedLabel, val bounds: androidx.compose.ui.geometry.Rect, val textOrigin: Offset)
private data class StarCloud(val bitmap: ImageBitmap, val x: Double, val y: Double, val extent: Double)

private suspend fun densityCloud(stars: List<PaintedStar>): StarCloud? = withContext(Dispatchers.Default) {
    if (stars.isEmpty()) return@withContext null
    val left = stars.minOf { it.point.x }; val right = stars.maxOf { it.point.x }
    val top = stars.minOf { it.point.y }; val bottom = stars.maxOf { it.point.y }
    val extent = max(.5, max(right - left, bottom - top)) * 1.22
    if (!extent.isFinite()) return@withContext null
    val x = (left + right - extent) / 2; val y = (top + bottom - extent) / 2
    val resolution = 128
    val mass = FloatArray(resolution * resolution)
    val red = FloatArray(mass.size); val green = FloatArray(mass.size); val blue = FloatArray(mass.size)
    for ((index, star) in stars.withIndex()) {
        if (index % 256 == 0) ensureActive()
        val px = ((star.point.x - x) / extent * resolution).roundToInt()
        val py = ((star.point.y - y) / extent * resolution).roundToInt()
        for (dy in -5..5) for (dx in -5..5) {
            if (dx * dx + dy * dy > 25 || px + dx !in 0 until resolution || py + dy !in 0 until resolution) continue
            val weight = exp(-(dx * dx + dy * dy) / 8.0).toFloat()
            val offset = (py + dy) * resolution + px + dx
            mass[offset] += weight
            red[offset] += star.color.red * weight; green[offset] += star.color.green * weight; blue[offset] += star.color.blue * weight
        }
    }
    val image = ImageBitmap(resolution, resolution)
    val canvas = GraphicsCanvas(image)
    val paint = Paint()
    for (index in mass.indices) {
        val excess = max(0f, mass[index] - 1.7f)
        if (excess == 0f) continue
        paint.color = Color(red[index] / mass[index], green[index] / mass[index], blue[index] / mass[index],
            .46f * (excess / (excess + 3.5f)).pow(1.3f))
        val px = (index % resolution).toFloat(); val py = (index / resolution).toFloat()
        canvas.drawRect(px, py, px + 1, py + 1, paint)
    }
    StarCloud(image, x, y, extent)
}

@Composable
fun StarCanvas(state: StarMapState, controller: StarMapController, strings: Strings) {
    StarCanvas(state, strings, controller::size, controller::transform, controller::focus, controller::pick,
        controller::excerpts, controller::drawn)
}

@Composable
fun StarCanvas(
    state: StarMapState, strings: Strings, onSize: (Double, Double) -> Unit,
    onTransform: (Double, Double, Double, Double, Double) -> Unit, onFocus: (List<String>) -> Unit,
    onPick: (Double, Double, Double) -> Unit, onExcerpts: (List<String>) -> Unit, onDrawn: (Long) -> Unit,
) {
    val map = state.snapshot ?: return
    val density = LocalDensity.current.density
    val measurer = rememberTextMeasurer(cacheSize = 128)
    val stars = remember(map.layoutVersion) {
        val groups = map.topics.flatMap { topic ->
            val tint = topic.id.takeLast(8).toLongOrNull(16)?.rem(starTints.size)?.toInt() ?: 0
            topic.members.map { it to tint }
        }.toMap()
        map.points.filter { it.semantic }.map { point ->
            val a = point.stellar ?: Stellar(.5, "ordinary", .7, .55)
            val tint = when {
                a.role == "core" -> if (a.seed < .4) Color(0xFFF0DFBD) else Color(0xFFD9E8FC)
                a.role == "isolated" -> Color(0xFFE2D0AD)
                a.seed < .09 && a.luminosity > .6 -> Color(0xFFE4D7BA)
                else -> groups[point.name]?.let { starTints[it] } ?: Color(0xFFC3D0E0)
            }
            PaintedStar(point, tint, (.56 + a.radius * .55 + a.luminosity * .22 + if (a.role == "core") .3 else .0).toFloat(),
                (if (a.role == "ordinary") .58 + a.luminosity * .39 else .97).toFloat(), a.role != "ordinary")
        }
    }
    val labels = remember(map.layoutVersion, density) {
        buildList {
            map.topics.sortedByDescending { it.members.size }.take(32).forEach { topic ->
                add(PaintedLabel(topic.id, topic.members, topic.x, topic.y, false, measurer.measure(
                    AnnotatedString(topic.label.take(19)), TextStyle(fontSize = 14.sp, color = Color(0xFFC1CFDF)))))
                topic.districts.take(6).forEach { district ->
                    add(PaintedLabel(district.id, district.members, district.x, district.y, true, measurer.measure(
                        AnnotatedString(district.label.take(19)), TextStyle(fontSize = 12.sp, color = Color(0xFFA5BAD1)))))
                }
            }
        }
    }
    val halos = remember(stars) { stars.map { it.color }.distinct().associateWith { color ->
        Brush.radialGradient(listOf(color.copy(alpha = .20f), color.copy(alpha = .035f), Color.Transparent), center = Offset.Zero, radius = 7f)
    } }
    val cloud by key(map.layoutVersion) {
        produceState<StarCloud?>(null) { value = densityCloud(stars) }
    }
    val excerptLayouts = remember(state.excerpts, density) { state.excerpts.mapValues { (_, text) ->
        measurer.measure(AnnotatedString(text), TextStyle(fontSize = 12.sp, color = Color(0xFFD5DFEA)),
            maxLines = 2, constraints = Constraints(maxWidth = (180 * density).toInt()))
    } }
    val adjacent = remember(map.layoutVersion) {
        val byName = stars.associateBy { it.point.name }
        map.links.groupBy { it.source }.mapValues { (_, links) -> links.mapNotNull { byName[it.target] } } to
            map.links.groupBy { it.target }.mapValues { (_, links) -> links.mapNotNull { byName[it.source] } }
    }
    var width by remember { mutableStateOf(0.0) }
    var height by remember { mutableStateOf(0.0) }
    val live by rememberUpdatedState(state)
    val placements = remember(labels, state.view, state.fitScale, width, height, density) {
        val view = state.view
        if (view == null) emptyList() else buildList<LabelPlacement> {
            val zoom = view.scale / state.fitScale
            var topics = 0
            for (label in labels) {
                if (label.small && zoom <= 1.35 || !label.small && topics >= 3 && zoom < 1.6) continue
                val p = view.screen(label.x, label.y, width, height)
                val x = p.x.toFloat() - label.layout.size.width / 2f
                val y = p.y.toFloat() - label.layout.size.height - 14 * density
                val rect = androidx.compose.ui.geometry.Rect(x - 8 * density, y - 4 * density,
                    x + label.layout.size.width + 8 * density, y + label.layout.size.height + 4 * density)
                if (rect.left < 16 * density || rect.right > width - 16 * density ||
                    rect.top < 16 * density || rect.bottom > height - 16 * density || any { it.bounds.overlaps(rect) }) continue
                add(LabelPlacement(label, rect, Offset(x, y)))
                if (!label.small) ++topics
            }
        }
    }
    val livePlacements by rememberUpdatedState(placements)
    LaunchedEffect(state.view, map.layoutVersion, width, height) {
        delay(180)
        val view = state.view ?: return@LaunchedEffect
        if (view.scale / state.fitScale > 2.7) {
            val names = stars.asSequence().filter {
                val position = view.screen(it.point.x, it.point.y, width, height)
                position.x in 20.0..(width - 20) && position.y in 60.0..(height - 60)
            }.sortedByDescending { it.point.stellar?.luminosity ?: .55 }.take(3).map { it.point.name }.toList()
            onExcerpts(names)
        } else onExcerpts(emptyList())
    }
    Canvas(Modifier.fillMaxSize().testTag("star_canvas").semantics { contentDescription = strings.text("观星，拖动平移，双指缩放", "Star map. Drag to pan; pinch to zoom.") }
        .onSizeChanged { size ->
            width = size.width.toDouble(); height = size.height.toDouble()
            onSize(width, height)
        }
        .pointerInput(map.layoutVersion, density) {
            detectTransformGestures { centroid, pan, zoom, _ ->
                onTransform(zoom.toDouble(), centroid.x - size.width / 2.0, centroid.y - size.height / 2.0,
                    pan.x.toDouble(), pan.y.toDouble())
            }
        }
        .pointerInput(map.layoutVersion, density) {
            detectTapGestures { at ->
                val view = live.view ?: return@detectTapGestures
                val label = livePlacements.firstOrNull { it.bounds.contains(at) }
                if (label != null) onFocus(label.label.members)
                else onPick(at.x.toDouble(), at.y.toDouble(), 24.0 * density)
            }
        },
    ) {
        val started = epochMillis()
        drawRect(Color(0xFF030A14))
        val view = state.view ?: return@Canvas
        val zoom = view.scale / state.fitScale
        val growth = (1 + .30 * log2(max(1.0, zoom))).toFloat()
        cloud?.takeIf { state.photo }?.let { cloud ->
            val origin = view.screen(cloud.x, cloud.y, size.width.toDouble(), size.height.toDouble())
            val extent = cloud.extent * view.scale
            if (origin.x.isFinite() && origin.y.isFinite() && extent in 1.0..1_000_000.0 &&
                abs(origin.x) < 1_000_000 && abs(origin.y) < 1_000_000) {
                drawImage(cloud.bitmap, srcSize = IntSize(128, 128), dstOffset = IntOffset(origin.x.roundToInt(), origin.y.roundToInt()),
                    dstSize = IntSize(extent.roundToInt(), extent.roundToInt()),
                    alpha = (.54 / (1 + max(0.0, zoom - 1) * .4)).toFloat(), blendMode = BlendMode.Screen,
                    filterQuality = FilterQuality.Medium)
            }
        }
        fun at(point: StarPoint): Offset {
            val p = view.screen(point.x, point.y, size.width.toDouble(), size.height.toDouble())
            return Offset(p.x.toFloat(), p.y.toFloat())
        }
        val selected = stars.firstOrNull { it.point.name == state.selected }
        if (selected != null) {
            val source = at(selected.point)
            (adjacent.first[selected.point.name].orEmpty() + adjacent.second[selected.point.name].orEmpty()).forEach {
                drawLine(Color(0x4799BCD9), source, at(it.point), strokeWidth = .65f * density)
            }
        }
        for (star in stars) {
            val position = at(star.point)
            if (position.x < -30 * density || position.y < -30 * density ||
                position.x > size.width + 30 * density || position.y > size.height + 30 * density) continue
            val active = star.point.name == state.selected
            val radius = (star.radius * growth + if (active) .45f else 0f) * density
            if (state.photo || star.special || active) {
                withTransform({
                    translate(position.x, position.y)
                    scale(radius, radius, pivot = Offset.Zero)
                }) {
                    drawCircle(halos.getValue(star.color), radius = 7f, center = Offset.Zero,
                        alpha = star.alpha * if (active) .95f else if (star.special) .85f else .55f)
                }
            }
            drawCircle(if (star.special) Color(0xFFFBF5E8) else star.color, radius * if (state.photo) .76f else .8f,
                center = position, alpha = star.alpha)
            if (state.photo && star.special) {
                val length = radius * if (star.point.stellar?.role == "core") 5 else 3
                drawLine(star.color.copy(alpha = .28f), position - Offset(length, 0f), position + Offset(length, 0f), .45f * density)
                drawLine(star.color.copy(alpha = .20f), position - Offset(0f, length), position + Offset(0f, length), .45f * density)
            }
            if (active) drawCircle(Color(0xCCCFDFEF), radius + 5 * density, position, style = Stroke(.7f * density))
        }
        val boxes = mutableListOf<androidx.compose.ui.geometry.Rect>()
        for (placement in placements) {
            val label = placement.label
            val rect = placement.bounds
            boxes += rect
            drawRoundRect(Color(0xB0030A14), rect.topLeft, rect.size, androidx.compose.ui.geometry.CornerRadius(6 * density))
            drawText(label.layout, topLeft = placement.textOrigin, alpha = if (label.small) .68f else .8f)
        }
        if (zoom > 2.7) for (star in stars) {
            val layout = excerptLayouts[star.point.name] ?: continue
            val position = at(star.point)
            val rect = androidx.compose.ui.geometry.Rect(position.x + 10 * density, position.y - 8 * density,
                position.x + layout.size.width + 22 * density, position.y + layout.size.height + 4 * density)
            if (rect.left < 0 || rect.right > size.width || rect.top < 0 || rect.bottom > size.height || boxes.any { it.overlaps(rect) }) continue
            boxes += rect
            drawRoundRect(Color(0xD9030A14), rect.topLeft, rect.size, androidx.compose.ui.geometry.CornerRadius(6 * density))
            drawText(layout, topLeft = rect.topLeft + Offset(6 * density, 4 * density))
        }
        if (!state.photo || cloud != null || stars.isEmpty()) onDrawn((epochMillis() - started).coerceAtLeast(0))
    }
}

package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.focusable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.calculateCentroid
import androidx.compose.foundation.gestures.calculatePan
import androidx.compose.foundation.gestures.calculateZoom
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.*
import androidx.compose.ui.graphics.Canvas as GraphicsCanvas
import androidx.compose.ui.graphics.drawscope.*
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.input.key.*
import androidx.compose.ui.input.pointer.*
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.selected
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
private data class PcaSpringBody(var x: Float = 0f, var y: Float = 0f, var vx: Float = 0f, var vy: Float = 0f)

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
fun StarCanvas(state: StarMapState, controller: StarMapController, strings: Strings,
    onSelect: (String) -> Unit = controller::select) {
    StarCanvas(state, strings, controller::size, controller::transform, controller::focus, controller::pick,
        controller::excerpts, controller::drawn, onSelect, controller::dismissReader)
}

@Composable
fun StarCanvas(
    state: StarMapState, strings: Strings, onSize: (Double, Double) -> Unit,
    onTransform: (Double, Double, Double, Double, Double) -> Unit, onFocus: (List<String>) -> Unit,
    onPick: (Double, Double, Double) -> Unit, onExcerpts: (List<String>) -> Unit, onDrawn: (Long) -> Unit,
    onSelect: ((String) -> Unit)? = null,
    onBlank: () -> Unit = {},
) {
    val map = state.snapshot ?: return
    val stellar = map.algorithm == "umap-balanced-v1"
    val pca by produceState<PcaGeometry?>(null, map.layoutVersion, stellar) {
        if (!stellar) value = withContext(Dispatchers.Default) { buildPcaRegions(map.points, map.topics) }
    }
    if (!stellar && pca == null) return
    val displayPoints = pca?.points ?: map.points
    val pcaRegions = remember(pca) {
        buildMap<String, PcaRegion> {
            fun register(node: PcaRegion) { put(node.id, node); node.children.forEach(::register) }
            pca?.roots?.forEach(::register)
        }
    }
    val density = LocalDensity.current.density
    val measurer = rememberTextMeasurer(cacheSize = 128)
    val stars = remember(map.layoutVersion, stellar, displayPoints) {
        val groups = map.topics.flatMap { topic ->
            val tint = topic.id.takeLast(8).toLongOrNull(16)?.rem(starTints.size)?.toInt() ?: 0
            topic.members.map { it to tint }
        }.toMap()
        val legacyColors = map.topics.flatMapIndexed { index, topic ->
            topic.members.map { it to pcaPalette[index % pcaPalette.size] }
        }.toMap()
        displayPoints.filter { !stellar || it.semantic }.map { point ->
            val a = point.stellar ?: Stellar(.5, "ordinary", .7, .55)
            val tint = when {
                !stellar -> legacyColors[point.name] ?: if (!point.semantic) Color(0xFFA9ADB4) else when (point.tags.firstOrNull()) {
                    "想法" -> Color(0xFF7FA6E8); "灵感" -> Color(0xFF7DBBA5); "摘录" -> Color(0xFFB09BDD); "教训" -> Color(0xFFE39A86)
                    else -> Color(0xFF9AA3AF)
                }
                a.role == "core" -> if (a.seed < .4) Color(0xFFF0DFBD) else Color(0xFFD9E8FC)
                a.role == "isolated" -> Color(0xFFE2D0AD)
                a.seed < .09 && a.luminosity > .6 -> Color(0xFFE4D7BA)
                else -> groups[point.name]?.let { starTints[it] } ?: Color(0xFFC3D0E0)
            }
            PaintedStar(point, tint, if (!stellar) 4f else (.56 + a.radius * .55 + a.luminosity * .22 + if (a.role == "core") .3 else .0).toFloat(),
                if (!stellar) .85f else (if (a.role == "ordinary") .58 + a.luminosity * .39 else .97).toFloat(), a.role != "ordinary")
        }
    }
    val labels = remember(map.layoutVersion, density, stellar, pcaRegions, strings) {
        buildList {
            if (!stellar) pcaRegions.values.filter { it.label.isNotBlank() || it.kind == "topic" }.forEach { region ->
                val text = region.label.ifBlank { strings.text("主题", "Topic") }
                add(PaintedLabel(region.id, region.members, region.x, region.y, region.kind == "district",
                    measurer.measure(AnnotatedString(text), TextStyle(fontSize = if (region.kind == "district") 13.sp else 15.sp,
                        color = androidx.compose.ui.graphics.lerp(Color(0xFF263343), region.color, .4f)),
                        maxLines = 1, constraints = Constraints(maxWidth = (240 * density).toInt()))))
            } else map.topics.sortedByDescending { it.members.size }.take(32).forEach { topic ->
                add(PaintedLabel(topic.id, topic.members, topic.x, topic.y, false, measurer.measure(
                    AnnotatedString(topic.label.take(19)), TextStyle(fontSize = 14.sp,
                        color = if (stellar) Color(0xFFC1CFDF) else MoeriseColors.Ink))))
                topic.districts.take(6).forEach { district ->
                    add(PaintedLabel(district.id, district.members, district.x, district.y, true, measurer.measure(
                        AnnotatedString(district.label.take(19)), TextStyle(fontSize = 12.sp,
                            color = if (stellar) Color(0xFFA5BAD1) else MoeriseColors.Muted))))
                }
            }
        }
    }
    val spriteColors = remember(stars, stellar) { if (stellar) stars.map { it.color }.distinct() else emptyList() }
    val sprites = remember(spriteColors) { spriteColors.takeIf { it.isNotEmpty() }?.let(::StarSprites) }
    val cloud by key(map.layoutVersion) {
        produceState<StarCloud?>(null) { if (stellar) value = densityCloud(stars) }
    }
    val excerptLayouts = remember(state.excerpts, density, stellar) { state.excerpts.mapValues { (_, text) ->
        measurer.measure(AnnotatedString(text), TextStyle(fontSize = 12.sp,
            color = if (stellar) Color(0xFFD5DFEA) else MoeriseColors.Ink),
            maxLines = 2, constraints = Constraints(maxWidth = (180 * density).toInt()))
    } }
    val adjacent = remember(map.layoutVersion) {
        val byName = stars.associateBy { it.point.name }
        map.links.groupBy { it.source }.mapValues { (_, links) -> links.mapNotNull { byName[it.target] } } to
            map.links.groupBy { it.target }.mapValues { (_, links) -> links.mapNotNull { byName[it.source] } }
    }
    var width by remember { mutableStateOf(0.0) }
    var height by remember { mutableStateOf(0.0) }
    var hovered by remember(map.layoutVersion) { mutableStateOf<String?>(null) }
    var keyboardFocused by remember(map.layoutVersion) { mutableStateOf<String?>(null) }
    val activePreview = keyboardFocused ?: hovered
    val displacements = remember(map.layoutVersion) { mutableStateMapOf<String, Offset>() }
    val springBodies = remember(map.layoutVersion) { mutableMapOf<String, PcaSpringBody>() }
    val springNeighbors = remember(map.layoutVersion) {
        val neighbors = mutableMapOf<String, MutableList<Pair<String, Double>>>()
        map.links.forEach { link ->
            neighbors.getOrPut(link.source) { mutableListOf() }.add(link.target to link.weight)
            neighbors.getOrPut(link.target) { mutableListOf() }.add(link.source to link.weight)
        }
        neighbors.mapValues { (_, edges) -> edges.sortedWith(compareByDescending<Pair<String, Double>> { it.second }.thenBy { it.first }).take(8) }
    }
    var draggingPoint by remember(map.layoutVersion) { mutableStateOf<String?>(null) }
    var springJob by remember(map.layoutVersion) { mutableStateOf<Job?>(null) }
    val scope = rememberCoroutineScope()
    DisposableEffect(map.layoutVersion) { onDispose { springJob?.cancel() } }
    val live by rememberUpdatedState(state)
    val pcaLayer = remember(pca, state.view, width, height, density) {
        state.view?.let { view -> pca?.let { visiblePcaRegions(it.roots, view, width, height, density) } }
    }
    val livePcaLayer by rememberUpdatedState(pcaLayer)
    val pcaPaths = remember(pca, pcaLayer, state.view, width, height) {
        state.view?.let { view ->
            (pca?.roots.orEmpty() + pcaLayer?.regions.orEmpty() + pcaLayer?.labels.orEmpty()).distinctBy { it.id }
                .associate { it.id to pcaRegionPath(it, view, width, height) }
        }.orEmpty()
    }
    val placements = remember(labels, state.view, state.fitScale, width, height, density, pcaLayer) {
        val view = state.view
        if (view == null) emptyList() else buildList<LabelPlacement> {
            if (!stellar) {
                val visibleLabels = pcaLayer?.labels.orEmpty()
                val activeIds = visibleLabels.map { it.id }.toSet()
                visibleLabels.asReversed().forEach { node ->
                    val label = labels.firstOrNull { it.id == node.id } ?: return@forEach
                    val center = view.screen(node.x, node.y, width, height)
                    val x = (center.x.toFloat() - label.layout.size.width / 2f)
                        .coerceIn(8 * density, max(8 * density, width.toFloat() - label.layout.size.width - 8 * density))
                    val expanded = node.children.any { it.id in activeIds } || pcaLayer?.regions.orEmpty().any { it.id.startsWith("${node.id}/") }
                    val y = (if (expanded) height / 2 + (node.top - view.y) * view.scale - 34 * density else center.y - 15 * density).toFloat()
                    val rect = androidx.compose.ui.geometry.Rect(x, max(10 * density, y), x + label.layout.size.width + 20 * density,
                        max(10 * density, y) + 30 * density)
                    if (center.x < -50 * density || center.x > width + 50 * density ||
                        center.y < -50 * density || center.y > height + 50 * density ||
                        node.kind == "district" && node.area * view.scale.pow(2) < min(14000.0 * density * density, width * height / 24) ||
                        any { it.bounds.inflate(8 * density).overlaps(rect) }) return@forEach
                    add(LabelPlacement(label, rect, rect.topLeft + Offset(10 * density, 5 * density)))
                }
                return@buildList
            }
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
    val pcaShown = remember(pcaLayer, displayPoints) {
        val names = pcaLayer?.regions?.mapNotNull { it.name }.orEmpty().toSet()
        displayPoints.filter { it.name in names || displayPoints.size <= 24 }.take(160)
    }
    val pcaCards = remember(pcaShown, pca, state.view, width, height, placements) {
        val view = state.view
        if (stellar || view == null) emptyList() else buildList {
            val rects = placements.map { it.bounds }.toMutableList()
            val cardWidth = (if (width < 600 * density) 132 else 164) * density
            val cardHeight = 72 * density
            pcaShown.sortedBy { hypot(it.x - view.x, it.y - view.y) }.forEach { point ->
                if (size >= (if (width < 600 * density) 4 else 10)) return@forEach
                val parcel = pca?.parcels?.get(point.name) ?: return@forEach
                val dx = (cardWidth / 2 + 8 * density) / view.scale; val dy = (cardHeight / 2 + 8 * density) / view.scale
                if (listOf(RegionPosition(point.x - dx, point.y - dy), RegionPosition(point.x + dx, point.y - dy),
                        RegionPosition(point.x - dx, point.y + dy), RegionPosition(point.x + dx, point.y + dy))
                        .any { !insideRegion(it, parcel.polygon) }) return@forEach
                val position = view.screen(point.x, point.y, width, height)
                val rect = androidx.compose.ui.geometry.Rect((position.x - cardWidth / 2 - 10 * density).toFloat(),
                    (position.y - cardHeight / 2 - 10 * density).toFloat(), (position.x + cardWidth / 2 + 10 * density).toFloat(),
                    (position.y + cardHeight / 2 + 10 * density).toFloat())
                if (rect.left < 10 * density || rect.right > width - 10 * density || rect.top < 60 * density ||
                    rect.bottom > height - 10 * density || rects.any { it.overlaps(rect) }) return@forEach
                rects += rect; add(point.name)
            }
        }
    }
    LaunchedEffect(state.view, map.layoutVersion, width, height, activePreview, pcaCards) {
        delay(180)
        val view = state.view ?: return@LaunchedEffect
        if (!stellar) onExcerpts((listOfNotNull(activePreview) + pcaCards).distinct().take(10))
        else if (view.scale / state.fitScale > 2.7) {
            val names = stars.asSequence().filter {
                val position = view.screen(it.point.x, it.point.y, width, height)
                position.x in 20.0..(width - 20) && position.y in 60.0..(height - 60)
            }.sortedByDescending { it.point.stellar?.luminosity ?: .55 }.take(if (width < 600 * density) 3 else 7).map { it.point.name }.toList()
            onExcerpts((listOfNotNull(activePreview) + names).distinct())
        } else onExcerpts(listOfNotNull(activePreview))
    }
    fun screen(point: StarPoint, view: StarView): Offset {
        val position = view.screen(point.x, point.y, width, height)
        return Offset(position.x.toFloat(), position.y.toFloat()) + (displacements[point.name] ?: Offset.Zero)
    }
    fun hit(position: Offset): PaintedStar? {
        val view = live.view ?: return null
        if (!stellar) {
            val world = RegionPosition(view.x + (position.x - width / 2) / view.scale,
                view.y + (position.y - height / 2) / view.scale)
            val parcel = livePcaLayer?.regions?.firstOrNull { it.name != null && insideRegion(world, it.polygon) }
            if (parcel != null) return stars.firstOrNull { it.point.name == parcel.name }
            if (stars.size > 24) return null
        }
        return stars.minByOrNull { (screen(it.point, view) - position).getDistanceSquared() }
            ?.takeIf { (screen(it.point, view) - position).getDistance() <= 24 * density }
    }
    fun focusRegion(id: String, members: List<String>) {
        val region = pcaRegions[id]
        val view = live.view
        if (!stellar && region != null && view != null) {
            val scale = max(view.scale * 1.4, min(max(1.0, width - 100 * density) / max(.01, region.right - region.left),
                max(1.0, height - 100 * density) / max(.01, region.bottom - region.top))).coerceAtMost(10000.0)
            onTransform(scale / view.scale, 0.0, 0.0, (view.x - region.x) * scale, (view.y - region.y) * scale)
        } else onFocus(members)
    }
    fun select(name: String) {
        if (onSelect != null) onSelect(name)
        else {
            val point = stars.firstOrNull { it.point.name == name }?.point ?: return
            val view = live.view ?: return
            val position = view.screen(point.x, point.y, width, height)
            onPick(position.x, position.y, 24.0 * density)
        }
    }
    val livePreview by rememberUpdatedState(activePreview)
    fun animateSprings() {
        if (springJob?.isActive == true || springBodies.isEmpty()) return
        springJob = scope.launch {
            var previous = withFrameNanos { it }
            while (true) {
                val frame = withFrameNanos { it }
                val delta = ((frame - previous) / 16_670_000f).coerceIn(.5f, 1.8f)
                previous = frame
                val forces = springBodies.mapValues { (name, body) ->
                    var fx = -body.x * .13f
                    var fy = ((if (name == livePreview) -6f * density else 0f) - body.y) * .13f
                    springNeighbors[name].orEmpty().forEach { (neighbor, weight) ->
                        springBodies[neighbor]?.let { other ->
                            fx += (other.x - body.x) * .045f * weight.toFloat()
                            fy += (other.y - body.y) * .045f * weight.toFloat()
                        }
                    }
                    fx to fy
                }
                var moving = false
                val resting = mutableListOf<String>()
                springBodies.forEach { (name, body) ->
                    if (name != draggingPoint) {
                        val (fx, fy) = forces.getValue(name)
                        body.vx = (body.vx + fx * delta) * .72f
                        body.vy = (body.vy + fy * delta) * .72f
                        body.x += body.vx * delta; body.y += body.vy * delta
                        if (abs(body.vx) + abs(body.vy) > .035f * density) moving = true
                        if (name != livePreview && draggingPoint == null && abs(body.x) + abs(body.y) < .08f * density) resting += name
                    }
                    displacements[name] = Offset(body.x, body.y)
                }
                resting.forEach { springBodies.remove(it); displacements.remove(it) }
                if (!moving && draggingPoint == null) break
            }
        }
    }
    fun wake(name: String) {
        val names = linkedSetOf(name)
        springNeighbors[name].orEmpty().forEach { (neighbor, _) ->
            names += neighbor
            springNeighbors[neighbor].orEmpty().take(2).forEach { (next, _) -> if (names.size < 28) names += next }
        }
        names.forEach { springBodies.getOrPut(it) { PcaSpringBody() } }
        animateSprings()
    }
    fun release(name: String) { draggingPoint = null; wake(name) }
    LaunchedEffect(activePreview, stellar) {
        if (!stellar) {
            if (activePreview != null) wake(activePreview) else animateSprings()
        }
    }
    Box(Modifier.fillMaxSize()
        .onSizeChanged { size ->
            width = size.width.toDouble(); height = size.height.toDouble()
            onSize(width, height)
        }) {
        Canvas(Modifier.fillMaxSize().testTag("star_canvas").semantics {
            customActions = listOf(
                CustomAccessibilityAction(strings.text("放大", "Zoom in")) { onTransform(1.35, 0.0, 0.0, 0.0, 0.0); true },
                CustomAccessibilityAction(strings.text("缩小", "Zoom out")) { onTransform(1 / 1.35, 0.0, 0.0, 0.0, 0.0); true })
            contentDescription = strings.text("观星，拖动平移，双指缩放。Tab 选星，Enter 阅读。",
                "Star map. Drag to pan; pinch to zoom. Tab to choose a star, Enter to read.")
        }
            .pointerInput(map.layoutVersion, density) {
                awaitEachGesture {
                    val down = awaitFirstDown(requireUnconsumed = false)
                    hovered = null
                    val candidate = hit(down.position)?.point?.name
                    var moved = false
                    var nodeDrag = !stellar && candidate != null && down.type == PointerType.Mouse
                    var draggingName: String? = null
                    try {
                        while (true) {
                            val event = awaitPointerEvent()
                            val change = event.changes.firstOrNull { it.id == down.id } ?: break
                            if (!change.pressed) {
                                if (!moved) {
                                    val label = livePlacements.firstOrNull { it.bounds.contains(down.position) }
                                    if (label != null) focusRegion(label.label.id, label.label.members)
                                    else if (candidate != null) select(candidate)
                                    else if (!stellar) live.view?.let { view ->
                                        val world = RegionPosition(view.x + (down.position.x - width / 2) / view.scale,
                                            view.y + (down.position.y - height / 2) / view.scale)
                                        livePcaLayer?.regions?.firstOrNull { it.name == null && insideRegion(world, it.polygon) }
                                            ?.let { focusRegion(it.id, it.members) } ?: onBlank()
                                    } else onBlank()
                                }
                                break
                            }
                            val multi = event.changes.count { it.pressed } > 1
                            val movement = change.position - down.position
                            if (!multi && !stellar && !moved && candidate != null &&
                                change.uptimeMillis - down.uptimeMillis >= 320) nodeDrag = true
                            if (multi) nodeDrag = false
                            val started = !moved
                            if (multi || movement.getDistance() > viewConfiguration.touchSlop) moved = true
                            if (!moved) continue
                            if (nodeDrag && candidate != null) {
                                if (draggingName == null) { draggingPoint = candidate; wake(candidate) }
                                draggingName = candidate
                                springBodies.getValue(candidate).apply { x = movement.x; y = movement.y; vx = 0f; vy = 0f }
                                displacements[candidate] = movement
                            } else {
                                draggingName?.let(::release)
                                draggingName = null
                                val centroid = event.calculateCentroid(useCurrent = false)
                                val pan = if (started && !multi) movement else event.calculatePan()
                                val zoom = event.calculateZoom()
                                onTransform(zoom.toDouble(), centroid.x - size.width / 2.0, centroid.y - size.height / 2.0,
                                    pan.x.toDouble(), pan.y.toDouble())
                            }
                            event.changes.forEach { if (it.positionChanged()) it.consume() }
                        }
                    } finally { draggingName?.let(::release) }
                }
            }.pointerInput(map.layoutVersion, density) {
                awaitPointerEventScope {
                    while (true) {
                        val event = awaitPointerEvent()
                        if (event.type == PointerEventType.Exit) hovered = null
                        else event.changes.firstOrNull { it.type == PointerType.Mouse && !it.pressed }?.let { hovered = hit(it.position)?.point?.name }
                        if (event.type == PointerEventType.Scroll) {
                            val change = event.changes.firstOrNull() ?: continue
                            val factor = exp((-change.scrollDelta.y * .12f).coerceIn(-1f, 1f).toDouble())
                            onTransform(factor, change.position.x - size.width / 2.0, change.position.y - size.height / 2.0, 0.0, 0.0)
                            change.consume()
                        }
                    }
                }
            },
        ) {
            val started = epochMillis()
            drawRect(if (stellar) Color(0xFF030A14) else Color(0xFFFBFCFE))
            val view = state.view ?: return@Canvas
            val zoom = view.scale / state.fitScale
            val growth = (1 + .30 * log2(max(1.0, zoom))).toFloat()
            if (!stellar) {
                pca?.roots?.forEach { root ->
                    pcaPaths[root.id]?.let { path ->
                        drawPath(path, pcaTint(root.color, .44))
                        drawPath(path, pcaTint(root.color, .3), style = Stroke(1.2f * density, join = StrokeJoin.Round))
                    }
                }
                pcaLayer?.regions?.forEach { node ->
                    pcaPaths[node.id]?.let { path ->
                        drawPath(path, pcaTint(node.color, max(.15, .43 + node.shade)))
                        drawPath(path, Color(0xFFFBFCFE), style = Stroke((if (node.kind == "note") 1.6f else 2.5f) * density, join = StrokeJoin.Round))
                    }
                }
                pcaLayer?.labels?.filter { it.kind == "district" }?.forEach { node ->
                    pcaPaths[node.id]?.let { drawPath(it, Color(0xFFFBFCFE), style = Stroke(3.5f * density, join = StrokeJoin.Round)) }
                }
                (listOfNotNull(activePreview, state.selected) + displacements.keys).distinct().forEach { name ->
                    val parcel = pca?.parcels?.get(name) ?: return@forEach
                    if (pcaLayer?.regions?.none { it.id == parcel.id } != false) return@forEach
                    val path = pcaRegionPath(parcel, view, width, height, displacements[name] ?: Offset.Zero)
                    drawPath(path, parcel.color.copy(alpha = .63f))
                    drawPath(path, if (name == activePreview || name == state.selected) parcel.color else Color(0xFFFBFCFE),
                        style = Stroke((if (name == activePreview || name == state.selected) 2.5f else 1.6f) * density, join = StrokeJoin.Round))
                }
            }
            cloud?.takeIf { stellar && state.photo }?.let { cloud ->
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
                return screen(point, view)
            }
            val selected = stars.firstOrNull { it.point.name == (activePreview ?: state.selected) }
            if (selected != null) {
                val source = at(selected.point)
                (adjacent.first[selected.point.name].orEmpty() + adjacent.second[selected.point.name].orEmpty()).forEach {
                    drawLine(if (stellar) Color(0x4799BCD9) else selected.color.copy(alpha = .35f), source, at(it.point), strokeWidth = .65f * density)
                }
            }
            for (star in stars) {
                if (!stellar) continue
                val position = at(star.point)
                if (position.x < -30 * density || position.y < -30 * density ||
                    position.x > size.width + 30 * density || position.y > size.height + 30 * density) continue
                val active = star.point.name == state.selected || star.point.name == activePreview
                val radius = (star.radius * growth + if (active) .45f else 0f) * density
                if (state.photo || star.special || active) {
                    sprites?.draw(drawContext.canvas, position, radius, star.color, star.alpha,
                        if (active) .95f else if (star.special) .85f else .55f, star.special, state.photo)
                } else drawCircle(star.color, radius * .8f, center = position, alpha = star.alpha)
                if (stellar && state.photo && star.special) {
                    val length = radius * if (star.point.stellar?.role == "core") 5 else 3
                    drawLine(star.color.copy(alpha = .28f), position - Offset(length, 0f), position + Offset(length, 0f), .45f * density)
                    drawLine(star.color.copy(alpha = .20f), position - Offset(0f, length), position + Offset(0f, length), .45f * density)
                }
                if (active) drawCircle(if (stellar) Color(0xCCCFDFEF) else star.color, radius + 5 * density, position, style = Stroke(.7f * density))
            }
            val boxes = mutableListOf<androidx.compose.ui.geometry.Rect>()
            for (placement in placements) {
                val label = placement.label
                val rect = placement.bounds
                boxes += rect
                if (stellar) drawRoundRect(Color(0xB0030A14), rect.topLeft, rect.size, androidx.compose.ui.geometry.CornerRadius(6 * density))
                drawText(label.layout, topLeft = placement.textOrigin, alpha = if (label.small) .68f else .8f)
            }
            if (stellar && zoom > 2.7) for (star in stars) {
                val layout = excerptLayouts[star.point.name] ?: continue
                val position = at(star.point)
                val rect = androidx.compose.ui.geometry.Rect(position.x + 10 * density, position.y - 8 * density,
                    position.x + layout.size.width + 22 * density, position.y + layout.size.height + 4 * density)
                if (rect.left < 0 || rect.right > size.width || rect.top < 0 || rect.bottom > size.height || boxes.any { it.overlaps(rect) }) continue
                boxes += rect
                drawRoundRect(if (stellar) Color(0xD9030A14) else Color(0xE6FFFFFF), rect.topLeft, rect.size, androidx.compose.ui.geometry.CornerRadius(6 * density))
                drawText(layout, topLeft = rect.topLeft + Offset(6 * density, 4 * density))
            }
            if (!stellar) pcaCards.forEach { name ->
                val point = stars.firstOrNull { it.point.name == name }?.point ?: return@forEach
                val layout = excerptLayouts[name] ?: return@forEach
                val position = at(point)
                val cardWidth = (if (size.width < 600 * density) 132 else 164) * density
                drawText(layout, topLeft = position - Offset(cardWidth / 2 - 12 * density, layout.size.height / 2f))
            }
            activePreview?.let { name ->
                val point = stars.firstOrNull { it.point.name == name }?.point ?: return@let
                val layout = excerptLayouts[name] ?: return@let
                val position = at(point)
                val boxSize = Size(layout.size.width + 24 * density, layout.size.height + 16 * density)
                val origin = Offset((position.x + 18 * density).coerceIn(12 * density, max(12 * density, size.width - boxSize.width - 12 * density)),
                    (position.y + 12 * density).coerceIn(12 * density, max(12 * density, size.height - boxSize.height - 12 * density)))
                drawRoundRect(if (stellar) Color(0xEE0B192A) else Color.White, origin, boxSize, androidx.compose.ui.geometry.CornerRadius(10 * density))
                drawText(layout, topLeft = origin + Offset(12 * density, 8 * density))
            }
            if (!stellar || !state.photo || cloud != null || stars.isEmpty()) onDrawn((epochMillis() - started).coerceAtLeast(0))
        }
        val view = state.view
        if (view != null) {
            val shownNames = pcaShown.map { it.name }.toSet()
            val visible = stars.filter {
                val position = screen(it.point, view)
                (stellar || it.point.name in shownNames) &&
                    position.x in 12 * density..(width.toFloat() - 12 * density) && position.y in 12 * density..(height.toFloat() - 12 * density)
            }.take(if (stellar) 120 else 160)
            val accessible = (visible + stars.filter { it.point.name == keyboardFocused || it.point.name == state.selected }).distinctBy { it.point.name }
            accessible.forEach { star ->
                key(star.point.name) {
                    val position = screen(star.point, view)
                    Box(Modifier.offset { IntOffset((position.x - 16 * density).roundToInt(), (position.y - 16 * density).roundToInt()) }
                        .size(32.dp).testTag("map_point_${star.point.name}")
                        .onFocusChanged {
                            if (it.isFocused) {
                                keyboardFocused = star.point.name
                                if (position.x < 20 * density || position.x > width - 20 * density ||
                                    position.y < 20 * density || position.y > height - 20 * density) {
                                    onTransform(1.0, 0.0, 0.0, (view.x - star.point.x) * view.scale, (view.y - star.point.y) * view.scale)
                                }
                            } else if (keyboardFocused == star.point.name) keyboardFocused = null
                        }
                        .onKeyEvent { event ->
                            if (event.type == KeyEventType.KeyDown && event.key in listOf(Key.Enter, Key.NumPadEnter, Key.Spacebar)) {
                                select(star.point.name); true
                            } else false
                        }.semantics {
                            role = Role.Button
                            contentDescription = state.excerpts[star.point.name]?.takeIf { it.isNotBlank() }
                                ?: strings.text("笔记 ${star.point.name.removePrefix("memos/")}", "Note ${star.point.name.removePrefix("memos/")}")
                            selected = star.point.name == state.selected
                            onClick { select(star.point.name); true }
                        }.focusable())
                }
            }
            placements.forEach { placement ->
                key(placement.label.id) {
                    Box(Modifier.offset { IntOffset(placement.bounds.left.roundToInt(), placement.bounds.top.roundToInt()) }
                        .size(with(LocalDensity.current) { placement.bounds.width.toDp() }, with(LocalDensity.current) { placement.bounds.height.toDp() })
                        .testTag("map_topic_${placement.label.id}")
                        .onKeyEvent { event ->
                            if (event.type == KeyEventType.KeyDown && event.key in listOf(Key.Enter, Key.NumPadEnter, Key.Spacebar)) {
                                focusRegion(placement.label.id, placement.label.members); true
                            } else false
                        }.semantics {
                            role = Role.Button
                            contentDescription = strings.text("聚焦主题：", "Focus topic: ") + placement.label.layout.layoutInput.text.text
                            onClick { focusRegion(placement.label.id, placement.label.members); true }
                        }.focusable())
                }
            }
        }
    }
}

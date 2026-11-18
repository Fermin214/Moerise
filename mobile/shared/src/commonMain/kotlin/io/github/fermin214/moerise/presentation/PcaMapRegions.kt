package io.github.fermin214.moerise.presentation

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.geometry.Offset
import io.github.fermin214.moerise.domain.StarPoint
import io.github.fermin214.moerise.domain.StarTopic
import io.github.fermin214.moerise.domain.StarView
import kotlin.math.*
import kotlinx.coroutines.ensureActive
import kotlin.coroutines.coroutineContext

private const val regionEpsilon = 1e-10
internal val pcaPalette = listOf(0xFF6D94CA, 0xFF67A997, 0xFFAA8EBB, 0xFFD39C69,
    0xFF7F9DA9, 0xFFBD8997, 0xFF94A66F, 0xFF898FBB).map { Color(it) }
internal data class RegionPosition(val x: Double, val y: Double)
internal data class PcaRegion(
    val id: String, val kind: String, val polygon: List<RegionPosition>, val color: Color,
    val label: String, val shade: Double, val members: List<String>,
    val x: Double, val y: Double, val area: Double, val left: Double, val right: Double, val top: Double, val bottom: Double,
    var name: String? = null, val children: MutableList<PcaRegion> = mutableListOf(),
)
internal data class PcaGeometry(val roots: List<PcaRegion>, val points: List<StarPoint>, val parcels: Map<String, PcaRegion>)
internal data class PcaLayer(val regions: List<PcaRegion>, val labels: List<PcaRegion>)

private fun area(polygon: List<RegionPosition>): Double = abs(polygon.indices.sumOf { index ->
    val p = polygon[index]; val q = polygon[(index + 1) % polygon.size]
    p.x * q.y - q.x * p.y
}) / 2

private fun center(polygon: List<RegionPosition>): RegionPosition {
    var area = 0.0; var x = 0.0; var y = 0.0
    polygon.indices.forEach { index ->
        val p = polygon[index]; val q = polygon[(index + 1) % polygon.size]
        val cross = p.x * q.y - q.x * p.y
        area += cross; x += (p.x + q.x) * cross; y += (p.y + q.y) * cross
    }
    return if (abs(area) > regionEpsilon) RegionPosition(x / (3 * area), y / (3 * area))
    else RegionPosition(polygon.sumOf { it.x } / polygon.size, polygon.sumOf { it.y } / polygon.size)
}

internal fun insideRegion(point: RegionPosition, polygon: List<RegionPosition>): Boolean = polygon.indices.all { index ->
    val p = polygon[index]; val q = polygon[(index + 1) % polygon.size]
    (q.x - p.x) * (point.y - p.y) - (q.y - p.y) * (point.x - p.x) >= -regionEpsilon
}

private fun landmass(points: List<StarPoint>): List<RegionPosition> {
    val x = points.sumOf { it.x } / points.size; val y = points.sumOf { it.y } / points.size
    val spread = sqrt(points.sumOf { (it.x - x).pow(2) + (it.y - y).pow(2) } / points.size)
    val padding = max(.055, spread * .16)
    val sorted = points.flatMap { p -> (0..7).map { index ->
        RegionPosition(p.x + cos(index * PI / 4) * padding, p.y + sin(index * PI / 4) * padding)
    } }.distinct().sortedWith(compareBy<RegionPosition> { it.x }.thenBy { it.y })
    fun half(source: List<RegionPosition>): List<RegionPosition> {
        val result = mutableListOf<RegionPosition>()
        for (p in source) {
            while (result.size > 1) {
                val a = result[result.lastIndex - 1]; val b = result.last()
                if ((b.x - a.x) * (p.y - a.y) - (b.y - a.y) * (p.x - a.x) > 0) break
                result.removeAt(result.lastIndex)
            }
            result += p
        }
        return result.dropLast(1)
    }
    return half(sorted) + half(sorted.asReversed())
}

private fun cell(boundary: List<RegionPosition>, site: RegionPosition, others: List<RegionPosition>): List<RegionPosition> {
    var polygon = boundary
    for (other in others) {
        val nx = other.x - site.x; val ny = other.y - site.y
        if (abs(nx) + abs(ny) < regionEpsilon) continue
        val limit = (other.x.pow(2) + other.y.pow(2) - site.x.pow(2) - site.y.pow(2)) / 2
        val next = mutableListOf<RegionPosition>()
        polygon.indices.forEach { index ->
            val a = polygon[index]; val b = polygon[(index + 1) % polygon.size]
            val da = a.x * nx + a.y * ny - limit; val db = b.x * nx + b.y * ny - limit
            if (da <= regionEpsilon) next += a
            if (da < 0 && db > 0 || da > 0 && db < 0) {
                val t = da / (da - db)
                next += RegionPosition(a.x + (b.x - a.x) * t, a.y + (b.y - a.y) * t)
            }
        }
        polygon = next
        if (polygon.isEmpty()) break
    }
    return polygon
}

private fun region(id: String, kind: String, polygon: List<RegionPosition>, points: List<StarPoint>,
    color: Color, label: String = "", shade: Double = 0.0): PcaRegion {
    val center = center(polygon)
    return PcaRegion(id, kind, polygon, color, label, shade, points.map { it.name }, center.x, center.y,
        area(polygon), polygon.minOf { it.x }, polygon.maxOf { it.x }, polygon.minOf { it.y }, polygon.maxOf { it.y })
}

private fun partition(points: List<StarPoint>, count: Int): Pair<List<StarPoint>, List<List<StarPoint>>> {
    val sites = mutableListOf(points.first())
    val nearest = DoubleArray(points.size) { Double.POSITIVE_INFINITY }
    while (sites.size < count) {
        val last = sites.last()
        var farthest = -1; var distance = 0.0
        points.forEachIndexed { index, point ->
            nearest[index] = min(nearest[index], (point.x - last.x).pow(2) + (point.y - last.y).pow(2))
            if (nearest[index] > distance) { distance = nearest[index]; farthest = index }
        }
        if (farthest < 0 || distance < regionEpsilon) break
        sites += points[farthest]
    }
    val groups = sites.map { mutableListOf<StarPoint>() }
    points.forEach { point ->
        val best = sites.indices.minBy { (point.x - sites[it].x).pow(2) + (point.y - sites[it].y).pow(2) }
        groups[best] += point
    }
    return sites to groups
}

private suspend fun parcels(parent: PcaRegion, points: List<StarPoint>, depth: Int = 0) {
    coroutineContext.ensureActive()
    if (points.size == 1) { parent.name = points.first().name; return }
    if (points.isEmpty()) return
    val leaves = points.size <= 40 || depth >= 6
    val (sites, groups) = if (leaves) points to points.map { listOf(it) }
    else partition(points, min(8, ceil(points.size / 24.0).toInt()))
    val positions = sites.map { RegionPosition(it.x, it.y) }
    sites.indices.forEach { index ->
        val polygon = cell(parent.polygon, positions[index], positions)
        if (polygon.size >= 3 && area(polygon) >= regionEpsilon && groups[index].isNotEmpty()) {
            val child = region("${parent.id}/$index", if (leaves) "note" else "block", polygon, groups[index],
                parent.color, shade = parent.shade + (index % 5 - 2) * .018)
            if (leaves) child.name = sites[index].name else parcels(child, groups[index], depth + 1)
            parent.children += child
        }
    }
}

private fun fitMembers(points: List<StarPoint>, polygon: List<RegionPosition>): List<StarPoint> {
    if (points.isEmpty()) return emptyList()
    val center = center(polygon)
    val source = RegionPosition(points.sumOf { it.x } / points.size, points.sumOf { it.y } / points.size)
    var factor = Double.POSITIVE_INFINITY
    for (point in points) {
        val dx = point.x - source.x; val dy = point.y - source.y
        polygon.indices.forEach { index ->
            val a = polygon[index]; val b = polygon[(index + 1) % polygon.size]
            val nx = b.y - a.y; val ny = a.x - b.x; val projected = dx * nx + dy * ny
            if (projected > regionEpsilon) factor = min(factor, ((a.x - center.x) * nx + (a.y - center.y) * ny) / projected)
        }
    }
    factor = if (factor.isFinite()) max(0.0, factor) * .82 else 1.0
    return points.map { it.copy(x = center.x + (it.x - source.x) * factor, y = center.y + (it.y - source.y) * factor) }
}

/** 与 Web 的地块层级共用投影、主题和分区，不产生新的布局存储值。 */
internal suspend fun buildPcaRegions(points: List<StarPoint>, topics: List<StarTopic>): PcaGeometry {
    val source = points.associateBy { it.name }; val display = source.toMutableMap()
    val roots = mutableListOf<PcaRegion>(); val assigned = mutableSetOf<String>()
    topics.forEachIndexed { index, topic ->
        coroutineContext.ensureActive()
        val members = topic.members.mapNotNull(source::get)
        if (members.isNotEmpty()) {
            assigned += members.map { it.name }
            val color = pcaPalette[index % pcaPalette.size]
            val root = region(topic.id, "topic", landmass(members), members, color, topic.label)
            roots += root
            if (topic.districts.isEmpty()) parcels(root, members)
            else {
                val radius = sqrt(root.area / PI)
                val spread = topic.districts.maxOf { hypot(it.x - root.x, it.y - root.y) }
                val sites = topic.districts.mapIndexed { i, district ->
                    val factor = min(1.0, radius * .6 / max(spread, regionEpsilon)); val angle = i * 2.3999632297
                    RegionPosition(root.x + (district.x - root.x) * factor + cos(angle) * radius * .035,
                        root.y + (district.y - root.y) * factor + sin(angle) * radius * .035)
                }
                topic.districts.forEachIndexed { i, district ->
                    val polygon = cell(root.polygon, sites[i], sites)
                    if (polygon.size >= 3 && area(polygon) >= regionEpsilon) {
                        val local = fitMembers(district.members.mapNotNull(source::get), polygon)
                        local.forEach { display[it.name] = it }
                        val child = region(district.id, "district", polygon, local, color, district.label, (i % 5 - 2) * .045)
                        parcels(child, local); root.children += child
                    }
                }
            }
        }
    }
    points.filter { it.name !in assigned }.forEach { point ->
        roots += region(point.name, "note", landmass(listOf(point)), listOf(point), Color(0xFFA9ADB4)).also { it.name = point.name }
    }
    val byName = mutableMapOf<String, PcaRegion>()
    fun register(node: PcaRegion) {
        node.name?.let { name ->
            byName[name] = node
            if (points.size > 24) display[name]?.let { display[name] = it.copy(x = node.x, y = node.y) }
        }
        node.children.forEach(::register)
    }
    roots.forEach(::register)
    return PcaGeometry(roots, points.mapNotNull { display[it.name] }, byName)
}

internal fun visiblePcaRegions(roots: List<PcaRegion>, view: StarView, width: Double, height: Double, density: Float = 1f): PcaLayer {
    fun visible(node: PcaRegion) = (node.right - view.x) * view.scale >= -width / 2 &&
        (node.left - view.x) * view.scale <= width / 2 && (node.bottom - view.y) * view.scale >= -height / 2 &&
        (node.top - view.y) * view.scale <= height / 2
    val regions = roots.filter(::visible).take(220).toMutableList()
    val labels = regions.filter { it.label.isNotEmpty() || it.kind == "topic" }.toMutableList()
    var index = 0
    while (index < regions.size) {
        val node = regions[index]; val children = node.children.filter(::visible)
        val minimum = when (node.kind) { "topic" -> min(18000.0 * density * density, width * height / 32)
            "district" -> 6500.0 * density * density; else -> 3000.0 * density * density }
        if (children.isNotEmpty() && node.area * view.scale.pow(2) / node.children.size > minimum &&
            regions.size + children.size - 1 <= 220) {
            regions.removeAt(index); regions.addAll(index, children)
            labels += children.filter { it.label.isNotEmpty() }
        } else index++
    }
    return PcaLayer(regions, labels)
}

internal fun pcaRegionPath(region: PcaRegion, view: StarView, width: Double, height: Double, offset: Offset = Offset.Zero): Path =
    Path().apply {
        region.polygon.forEachIndexed { index, point ->
            val position = view.screen(point.x, point.y, width, height)
            val x = position.x.toFloat() + offset.x; val y = position.y.toFloat() + offset.y
            if (index == 0) moveTo(x, y) else lineTo(x, y)
        }
        close()
    }

internal fun pcaTint(color: Color, amount: Double): Color =
    androidx.compose.ui.graphics.lerp(color, Color.White, amount.toFloat())

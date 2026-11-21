package io.github.fermin214.moerise.domain

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlin.math.*

@Serializable data class Stellar(val seed: Double, val role: String, val radius: Double, val luminosity: Double)
@Serializable data class StarPoint(
    val name: String, @SerialName("source_hash") val sourceHash: String, val x: Double, val y: Double,
    val semantic: Boolean, val tags: List<String>, val stellar: Stellar? = null,
    val excerpt: String = "", @SerialName("create_time") val createTime: String = "",
)
@Serializable data class StarDistrict(val id: String, val label: String, val x: Double, val y: Double, val members: List<String>)
@Serializable data class StarTopic(
    val id: String, val label: String, val x: Double, val y: Double, val members: List<String>,
    val districts: List<StarDistrict> = emptyList(),
)
@Serializable data class StarLink(val source: String, val target: String, val weight: Double)
@Serializable data class StarMap(
    val status: String, val algorithm: String, @SerialName("layout_version") val layoutVersion: String,
    @SerialName("layout_frame") val layoutFrame: String, @SerialName("vector_generation") val vectorGeneration: String,
    @SerialName("projection_revision") val projectionRevision: Long, @SerialName("generated_at") val generatedAt: String = "",
    val points: List<StarPoint>, val topics: List<StarTopic>, val links: List<StarLink>,
    @SerialName("update_state") val updateState: String = "",
    @SerialName("disabled_reason") val disabledReason: String = "",
) {
    val readable get() = status == "ready" || status == "empty"
    fun validate(): StarMap {
        require(algorithm == "umap-balanced-v1") { "MAP_INCOMPATIBLE" }
        require(status in setOf("disabled", "waiting", "building", "unavailable", "ready", "empty") &&
            projectionRevision >= 0 && updateState in setOf("", "building", "unavailable")) { "MAP_PROTOCOL" }
        require(disabledReason.isEmpty() || status == "disabled" &&
            disabledReason in setOf("stargazing", "embedding")) { "MAP_PROTOCOL" }
        val digest = Regex("^[a-f0-9]{64}$")
        require(if (readable) digest.matches(layoutVersion) && digest.matches(layoutFrame) && vectorGeneration.isNotBlank()
            else layoutVersion.isEmpty() && layoutFrame.isEmpty() && vectorGeneration.isEmpty() && points.isEmpty()) { "MAP_PROTOCOL" }
        require((status != "empty" || points.isEmpty()) && (status != "ready" || points.isNotEmpty()) &&
            (readable || topics.isEmpty() && links.isEmpty())) { "MAP_PROTOCOL" }
        val names = mutableSetOf<String>()
        val semantic = mutableSetOf<String>()
        for (point in points) {
            require(Regex("^memos/[A-Za-z0-9_-]+$").matches(point.name) && names.add(point.name) &&
                point.sourceHash.isNotBlank() && point.x.isFinite() && point.y.isFinite()) { "MAP_PROTOCOL" }
            if (point.semantic) semantic += point.name
            require(!point.semantic || point.stellar != null) { "MAP_PROTOCOL" }
            point.stellar?.let {
                require(it.seed.isFinite() && it.seed in 0.0..1.0 && it.radius.isFinite() && it.radius > 0 && it.radius <= 4 &&
                    it.luminosity.isFinite() && it.luminosity in 0.0..1.0 && it.role in setOf("ordinary", "core", "isolated")) { "MAP_PROTOCOL" }
            }
        }
        require(topics.size <= points.size && links.size <= points.size * 3) { "MAP_PROTOCOL" }
        val topicIds = mutableSetOf<String>(); val assigned = mutableSetOf<String>(); val districtIds = mutableSetOf<String>()
        for (topic in topics) {
            require(Regex("^topic-[a-f0-9]{16}$").matches(topic.id) && topicIds.add(topic.id) && topic.label.length <= 200 &&
                topic.x.isFinite() && topic.y.isFinite() && topic.members.isNotEmpty() &&
                topic.members.all { it in semantic && assigned.add(it) } && topic.districts.size <= 6) { "MAP_PROTOCOL" }
            val local = mutableSetOf<String>(); val members = topic.members.toSet()
            for (district in topic.districts) {
                require(Regex("^district-[a-f0-9]{16}$").matches(district.id) && districtIds.add(district.id) &&
                    district.label.length <= 200 && district.x.isFinite() && district.y.isFinite() && district.members.isNotEmpty() &&
                    district.members.all { it in members && local.add(it) }) { "MAP_PROTOCOL" }
            }
        }
        val pairs = mutableSetOf<Pair<String, String>>()
        for (link in links) {
            require(link.source in semantic && link.target in semantic && link.source != link.target &&
                link.weight.isFinite() && link.weight in 0.55..1.0 &&
                pairs.add(if (link.source < link.target) link.source to link.target else link.target to link.source)) { "MAP_PROTOCOL" }
        }
        return this
    }
}

data class StarPosition(val x: Double, val y: Double)
data class StarView(val x: Double = 0.0, val y: Double = 0.0, val scale: Double = 100.0) {
    fun screen(x: Double, y: Double, width: Double, height: Double) = StarPosition(width / 2 + (x - this.x) * scale, height / 2 + (y - this.y) * scale)
    fun transform(factor: Double, beforeX: Double, beforeY: Double, panX: Double = 0.0, panY: Double = 0.0): StarView {
        if (!factor.isFinite() || factor <= 0) return this
        val next = (scale * factor).coerceIn(0.01, 10000.0)
        return StarView(x + beforeX / scale - (beforeX + panX) / next, y + beforeY / scale - (beforeY + panY) / next, next)
            .takeIf { it.x.isFinite() && it.y.isFinite() && abs(it.x) < 1e6 && abs(it.y) < 1e6 } ?: this
    }
}

fun fitStars(points: List<StarPoint>, width: Double, height: Double, all: Boolean = true, topics: List<StarTopic> = emptyList()): StarView {
    val source = points.filter { it.semantic }
    fun bounds(left: Double, right: Double, top: Double, bottom: Double, single: Boolean = false) =
        StarView(left / 2 + right / 2, top / 2 + bottom / 2,
            min(if (single) 100.0 else 1000.0, min(max(1.0, width - 80) / max(.01, right - left),
                max(1.0, height - 96) / max(.01, bottom - top))).coerceAtLeast(.01))
    if (source.isEmpty()) return StarView()
    val full = bounds(source.minOf { it.x }, source.maxOf { it.x }, source.minOf { it.y }, source.maxOf { it.y }, source.size == 1)
    if (all || source.size < 100) return full
    val xs = source.map { it.x }.sorted(); val ys = source.map { it.y }.sorted()
    fun quantile(values: List<Double>, fraction: Double) = values[((values.size - 1) * fraction).roundToInt()]
    val cropped = bounds(quantile(xs, .015), quantile(xs, .985), quantile(ys, .015), quantile(ys, .985))
    val subject = cropped.copy(scale = min(cropped.scale, full.scale * 1.12))
    val visible = source.filter {
        abs(it.x - subject.x) * subject.scale < width / 2 - 20 && abs(it.y - subject.y) * subject.scale < height / 2 - 45
    }.map { it.name }.toSet()
    return if (visible.size.toDouble() / source.size >= .97 && topics.all { topic ->
            topic.members.count { it in visible }.toDouble() / topic.members.size >= .8
        }) subject else full
}

fun hitStar(points: List<StarPoint>, view: StarView, width: Double, height: Double, x: Double, y: Double, radius: Double): String? {
    var closest: String? = null
    var distance = radius * radius
    for (point in points) {
        if (!point.semantic) continue
        val screen = view.screen(point.x, point.y, width, height)
        val next = (screen.x - x).pow(2) + (screen.y - y).pow(2)
        if (next < distance) { distance = next; closest = point.name }
    }
    return closest
}

@Serializable data class MapRebuild(val frame: String, val startedAt: Long, val state: String = "unknown", val retryAfter: Long = 0)

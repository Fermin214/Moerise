package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.domain.*
import kotlin.test.*

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class StarMapContractTest {
    private fun fixture(name: String) = CoreJson.decodeFromString<StarMap>(
        javaClass.getResourceAsStream("/contracts/map/$name.json")!!.bufferedReader().use { it.readText() })

    @Test
    fun publishedFixturesValidateAndMalformedGeometryCannotBecomeStars() {
        val names = listOf("disabled", "waiting", "building", "unavailable", "ready", "empty", "updating", "update-unavailable")
        assertEquals(6, names.map { fixture(it).validate().status }.distinct().size)
        val ready = fixture("ready")
        val point = ready.points.single()
        assertFailsWith<IllegalArgumentException> { ready.copy(points = listOf(point, point)).validate() }
        assertFailsWith<IllegalArgumentException> { ready.copy(points = listOf(point.copy(x = Double.NaN))).validate() }
        assertFailsWith<IllegalArgumentException> { ready.copy(points = listOf(point.copy(stellar = null))).validate() }
        assertFailsWith<IllegalArgumentException> { ready.copy(links = listOf(StarLink(point.name, "memos/missing", .8))).validate() }
        assertFailsWith<IllegalArgumentException> { ready.copy(topics = listOf(
            StarTopic("topic-0000000000000001", "topic", 0.0, 0.0, listOf("memos/missing")))).validate() }
        assertEquals("MAP_INCOMPATIBLE", assertFailsWith<IllegalArgumentException> { ready.copy(algorithm = "pca-topics-v3", points = listOf(point.copy(stellar = null))).validate() }.message)
        assertEquals("MAP_INCOMPATIBLE", assertFailsWith<IllegalArgumentException> { ready.copy(algorithm = "unknown").validate() }.message)
    }

    @Test
    fun drawingAndHitTestingShareTheTransformAndOnlySemanticNotesAreFitted() {
        val p = fixture("ready").points.single()
        val points = listOf(p.copy(name = "memos/a", x = -1.0), p.copy(name = "memos/b", x = 1.0),
            p.copy(name = "memos/unplaced", semantic = false, x = 10000.0))
        val view = fitStars(points, 800.0, 600.0)
        assertEquals(0.0, view.x)
        val at = view.screen(1.0, 0.0, 800.0, 600.0)
        assertEquals("memos/b", hitStar(points, view, 800.0, 600.0, at.x, at.y, 24.0))
        val transformed = view.transform(2.0, at.x - 400, at.y - 300, 17.0, -12.0)
        val moved = transformed.screen(1.0, 0.0, 800.0, 600.0)
        assertEquals(at.x + 17, moved.x, .00001)
        assertEquals(at.y - 12, moved.y, .00001)
        assertEquals("memos/b", hitStar(points, transformed, 800.0, 600.0, moved.x, moved.y, 24.0))
        assertNull(hitStar(points, transformed, 800.0, 600.0, 0.0, 0.0, 24.0))
        assertEquals(view, view.transform(Double.NaN, 0.0, 0.0))
    }
}

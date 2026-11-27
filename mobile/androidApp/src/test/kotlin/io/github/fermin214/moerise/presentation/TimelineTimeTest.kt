package io.github.fermin214.moerise.presentation

import java.time.Instant
import java.time.ZoneId
import kotlin.test.Test
import kotlin.test.assertEquals

class TimelineTimeTest {
    @Test
    fun formatsOnlyTheTimelineColophon() {
        val zone = "Pacific/Kiritimati"
        val instant = "2026-11-23T10:57:00Z"
        val today = "2026-11-24"
        assertEquals("00:57", displayTimelineTime(instant, zone, today, english = false))
        assertEquals("00:57", displayLocalTimelineTime(Instant.parse(instant).toEpochMilli(), zone, today, english = true))
        assertEquals("Nov 23", displayTimelineTime(instant, "UTC", today, english = true))
        assertEquals("11月24日", displayTimelineTime(instant, zone, "2026-11-25", english = false))
        assertEquals("Nov 24", displayTimelineTime(instant, zone, "2026-11-25", english = true))
        assertEquals("2025年12月18日", displayTimelineTime("2025-12-18T00:00:00Z", zone, today, english = false))
        assertEquals("2025-12-18", displayTimelineTime("2025-12-18T00:00:00Z", zone, today, english = true))
        assertEquals("", displayTimelineTime("invalid", zone, today, english = true))
        assertEquals("2026-11-24 00:57", displayNoteDate(instant, zone, english = true))
    }

    @Test
    fun schedulesTheNextServiceDayAcrossDstAndMidnightGaps() {
        val cases = listOf(
            Triple("Pacific/Kiritimati", "2026-11-24T09:59:59.999Z", "2026-11-24T10:00:00Z"),
            Triple("America/New_York", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"),
            Triple("America/New_York", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"),
            Triple("America/Santiago", "2026-09-05T04:00:00Z", "2026-09-06T04:00:00Z"),
        )
        for ((zone, now, boundary) in cases) {
            assertEquals(Instant.parse(boundary), nextServiceDay(Instant.parse(now), ZoneId.of(zone)))
        }
    }
}

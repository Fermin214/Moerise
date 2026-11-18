package io.github.fermin214.moerise.presentation

import java.time.Instant
import java.time.YearMonth
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

actual fun monthGrid(month: String): MonthGrid {
    val parsed = YearMonth.parse(month)
    return MonthGrid(parsed.year, parsed.monthValue, parsed.lengthOfMonth(), parsed.atDay(1).dayOfWeek.value - 1)
}
actual fun shiftMonth(month: String, delta: Int): String = YearMonth.parse(month).plusMonths(delta.toLong()).toString()
actual fun serviceMonth(timezone: String): String = YearMonth.now(ZoneId.of(timezone)).toString()
actual fun serviceToday(timezone: String): String = java.time.LocalDate.now(ZoneId.of(timezone)).toString()
actual fun displayServiceTime(instant: String, timezone: String): String = runCatching {
    Instant.parse(instant).atZone(ZoneId.of(timezone)).format(DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm", Locale.ROOT))
}.getOrDefault(instant)

actual fun displayNoteDate(instant: String, timezone: String, english: Boolean): String = runCatching {
    Instant.parse(instant).atZone(ZoneId.of(timezone)).format(DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm", Locale.ROOT))
}.getOrDefault(instant)

package io.github.fermin214.moerise.presentation

import java.time.Instant
import java.time.YearMonth
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

fun monthGrid(month: String): MonthGrid {
    val parsed = YearMonth.parse(month)
    return MonthGrid(parsed.year, parsed.monthValue, parsed.lengthOfMonth(), parsed.atDay(1).dayOfWeek.value - 1)
}
fun shiftMonth(month: String, delta: Int): String = YearMonth.parse(month).plusMonths(delta.toLong()).toString()
fun serviceMonth(timezone: String): String = YearMonth.now(ZoneId.of(timezone)).toString()
fun serviceToday(timezone: String): String = java.time.LocalDate.now(ZoneId.of(timezone)).toString()
fun displayServiceTime(instant: String, timezone: String): String = runCatching {
    Instant.parse(instant).atZone(ZoneId.of(timezone)).format(DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm", Locale.ROOT))
}.getOrDefault(instant)

fun displayNoteDate(instant: String, timezone: String, english: Boolean): String = displayServiceTime(instant, timezone)

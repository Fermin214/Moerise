package io.github.fermin214.moerise.presentation

data class MonthGrid(val year: Int, val month: Int, val days: Int, val offset: Int)

expect fun monthGrid(month: String): MonthGrid
expect fun shiftMonth(month: String, delta: Int): String
expect fun serviceMonth(timezone: String): String
expect fun displayServiceTime(instant: String, timezone: String): String

@OptIn(kotlin.time.ExperimentalTime::class)
fun displaySnapshotTime(epochMillis: Long, timezone: String): String =
    if (epochMillis > 0) displayServiceTime(kotlin.time.Instant.fromEpochMilliseconds(epochMillis).toString(), timezone) else ""

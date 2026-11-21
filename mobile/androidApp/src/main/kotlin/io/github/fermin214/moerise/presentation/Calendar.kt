package io.github.fermin214.moerise.presentation

data class MonthGrid(val year: Int, val month: Int, val days: Int, val offset: Int)


@OptIn(kotlin.time.ExperimentalTime::class)
fun displayLocalNoteDate(epochMillis: Long, timezone: String, english: Boolean): String =
    if (epochMillis > 0) displayNoteDate(kotlin.time.Instant.fromEpochMilliseconds(epochMillis).toString(), timezone, english) else ""

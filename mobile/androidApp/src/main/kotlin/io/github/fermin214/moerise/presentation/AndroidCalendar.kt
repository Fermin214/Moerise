package io.github.fermin214.moerise.presentation

import androidx.compose.runtime.*
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import java.time.Clock
import java.time.Duration
import java.time.Instant
import java.time.LocalDate
import java.time.YearMonth
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale
import kotlinx.coroutines.delay

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

fun displayNoteDay(instant: String, timezone: String, english: Boolean): String = runCatching {
    val date = Instant.parse(instant).atZone(ZoneId.of(timezone)).toLocalDate()
    if (english) date.toString() else "${date.year}年${date.monthValue}月${date.dayOfMonth}日"
}.getOrDefault("")

private val timelineClockFormat = DateTimeFormatter.ofPattern("HH:mm", Locale.ROOT)
private val timelineMonthFormat = DateTimeFormatter.ofPattern("MMM d", Locale.US)

fun displayTimelineTime(instant: String, timezone: String, today: String = serviceToday(timezone), english: Boolean): String = runCatching {
    val time = Instant.parse(instant).atZone(ZoneId.of(timezone))
    val date = time.toLocalDate()
    val current = LocalDate.parse(today)
    when {
        date == current -> time.format(timelineClockFormat)
        date.year == current.year -> if (english) date.format(timelineMonthFormat) else "${date.monthValue}月${date.dayOfMonth}日"
        english -> date.toString()
        else -> "${date.year}年${date.monthValue}月${date.dayOfMonth}日"
    }
}.getOrDefault("")

internal fun nextServiceDay(now: Instant, zone: ZoneId): Instant =
    now.atZone(zone).toLocalDate().plusDays(1).atStartOfDay(zone).toInstant()

internal val LocalServiceClock = staticCompositionLocalOf<Clock> { Clock.systemUTC() }

@Composable
internal fun rememberServiceDay(timezone: String): String {
    val clock = LocalServiceClock.current
    val zone = remember(timezone) { ZoneId.of(timezone) }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    fun date() = LocalDate.now(clock.withZone(zone)).toString()
    var serviceDay by remember(zone, clock) { mutableStateOf(date()) }
    var resumed by remember(lifecycle) { mutableStateOf(lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED)) }
    DisposableEffect(lifecycle, zone, clock) {
        val observer = LifecycleEventObserver { _, event ->
            when (event) {
                Lifecycle.Event.ON_RESUME -> { serviceDay = date(); resumed = true }
                Lifecycle.Event.ON_PAUSE -> resumed = false
                else -> Unit
            }
        }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer) }
    }
    LaunchedEffect(zone, clock, serviceDay, resumed) {
        if (!resumed) return@LaunchedEffect
        while (true) {
            val now = clock.instant()
            val day = now.atZone(zone).toLocalDate().toString()
            if (day != serviceDay) { serviceDay = day; break }
            delay(Duration.between(now, nextServiceDay(now, zone)).toMillis().coerceAtLeast(1))
        }
    }
    return serviceDay
}

internal fun elapsedNoteDays(instant: String, nowMillis: Long): Long? = runCatching {
    ((nowMillis - Instant.parse(instant).toEpochMilli()) / 86_400_000).coerceAtLeast(0)
}.getOrNull()

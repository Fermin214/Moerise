package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.DraftEntity
import io.github.fermin214.moerise.domain.Note
import kotlin.time.ExperimentalTime
import kotlin.time.Instant

sealed interface TimelineEntry {
    val key: String
    data class Remote(val note: Note) : TimelineEntry { override val key = note.name }
    data class Local(val draft: DraftEntity) : TimelineEntry { override val key = "queued:${draft.id}" }
}

/** 本机编辑替换同一资源的远端卡片；新建记录按保存时间并入时间线。 */
@OptIn(ExperimentalTime::class)
fun timelineEntries(notes: List<Note>, local: List<DraftEntity>): List<TimelineEntry> {
    val replaced = local.mapNotNull { it.targetName }.toSet()
    val entries = notes.filter { it.name !in replaced }.map { TimelineEntry.Remote(it) } +
        local.map { TimelineEntry.Local(it) }
    fun instant(value: String?): Long? = value?.let {
        runCatching { Instant.parse(it).toEpochMilliseconds() }.getOrNull()
    }
    fun time(entry: TimelineEntry): Long = when (entry) {
        is TimelineEntry.Remote -> instant(entry.note.createTime)
        is TimelineEntry.Local -> {
            val baseline = entry.draft.baselineJson?.let {
                runCatching { CoreJson.decodeFromString<Note>(it) }.getOrNull()
            }
            instant(baseline?.createTime) ?: entry.draft.createdAt.takeIf { it > 0 }
        }
    } ?: Long.MIN_VALUE
    return entries.sortedWith(compareByDescending<TimelineEntry> { time(it) }.thenBy { it.key })
}

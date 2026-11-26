package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.DraftEntity
import io.github.fermin214.moerise.domain.Note
import kotlin.time.ExperimentalTime
import kotlin.time.Instant

sealed interface TimelineEntry {
    val key: String
    data class Remote(val note: Note, override val key: String = note.name) : TimelineEntry
    data class Local(val draft: DraftEntity, override val key: String = draft.targetName ?: "queued:${draft.id}") : TimelineEntry
}

internal fun filteredTimelineEntries(notes: List<Note>, queued: List<DraftEntity>, displayKeys: Map<String, String>): List<TimelineEntry> {
    val byTarget = queued.filter { it.targetName != null }.distinctBy { it.targetName }.associateBy { it.targetName }
    return notes.map { note ->
        val key = displayKeys[note.name] ?: note.name
        byTarget[note.name]?.let { TimelineEntry.Local(it, key) } ?: TimelineEntry.Remote(note, key)
    }
}

/** 本机编辑替换同一资源的远端卡片；新建记录按保存时间并入时间线。 */
@OptIn(ExperimentalTime::class)
fun timelineEntries(notes: List<Note>, local: List<DraftEntity>, displayKeys: Map<String, String> = emptyMap()): List<TimelineEntry> {
    val replaced = local.mapNotNull { it.targetName }.toSet()
    val entries = local.map { TimelineEntry.Local(it, displayKeys[it.targetName] ?: it.targetName ?: "queued:${it.id}") } +
        notes.filter { it.name !in replaced }.map { TimelineEntry.Remote(it, displayKeys[it.name] ?: it.name) }
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
    return entries.distinctBy { it.key }.sortedWith(compareByDescending<TimelineEntry> { time(it) }.thenBy { it.key })
}

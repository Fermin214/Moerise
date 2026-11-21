package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.domain.*

data class InsightPickerState(
    val visible: Boolean = false,
    val tab: String = "date",
    val selected: List<MaterialCandidate> = emptyList(),
    val characters: Int = 0,
    val from: String = "",
    val through: String = "",
    val month: String = "",
    val calendarDays: Map<String, Int>? = null,
    val dateGroup: MaterialDateGroup = MaterialDateGroup(),
    val tags: List<TagCount> = emptyList(),
    val chosenTags: Set<String> = emptySet(),
    val candidates: List<MaterialCandidate> = emptyList(),
    val previousCandidates: List<MaterialCandidate> = emptyList(),
    val next: String = "",
    val busy: Boolean = false,
    val batch: String = "",
    val compact: Boolean = false,
    val exhausted: Boolean = false,
    val manualOnly: Boolean = false,
    val changed: List<String> = emptyList(),
    val limit: Int = 20_000,
    val error: String? = null,
)

internal fun materialCharacters(notes: List<Note>): Int = notes.sumOf { codePoints(it.content) }

internal fun codePoints(value: String): Int {
    var count = 0
    var index = 0
    while (index < value.length) {
        val first = value[index++]
        if (first.isHighSurrogate() && index < value.length && value[index].isLowSurrogate()) index++
        count++
    }
    return count
}

internal fun Note.candidate() = MaterialCandidate(name, sourceHash, codePoints(content), content.take(160), createTime, state)

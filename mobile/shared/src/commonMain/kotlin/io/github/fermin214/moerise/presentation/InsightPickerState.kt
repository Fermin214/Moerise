package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.domain.Note

data class InsightPickerState(
    val visible: Boolean = false,
    val tab: String = "date",
    val selected: List<Note> = emptyList(),
    val characters: Int = 0,
    val from: String = "",
    val through: String = "",
    val month: String = "",
    val years: List<Int> = emptyList(),
    val calendarDays: Set<String> = emptySet(),
    val tags: List<TagCount> = emptyList(),
    val chosenTags: Set<String> = emptySet(),
    val candidates: List<Note> = emptyList(),
    val next: String = "",
    val query: String = "",
    val filter: String = "ALL",
    val busy: Boolean = false,
    val error: String? = null,
)

internal fun materialCharacters(notes: List<Note>): Int = notes.sumOf { note ->
    var count = 0
    var index = 0
    while (index < note.content.length) {
        val first = note.content[index++]
        if (first.isHighSurrogate() && index < note.content.length && note.content[index].isLowSurrogate()) index++
        count++
    }
    count
}

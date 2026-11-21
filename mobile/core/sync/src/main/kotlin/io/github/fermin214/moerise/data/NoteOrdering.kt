package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.Note
import kotlin.time.Instant
import kotlin.time.ExperimentalTime

@OptIn(ExperimentalTime::class)
fun orderedNotes(notes: List<Note>): List<Note> = notes.sortedByDescending { Instant.parse(it.createTime) }

@OptIn(ExperimentalTime::class)
fun sortableTime(value: String): String = runCatching {
    val instant = Instant.parse(value)
    val sec = instant.epochSeconds.toString().padStart(20, '0')
    val nano = instant.nanosecondsOfSecond.toString().padStart(9, '0')
    "$sec.$nano"
}.getOrDefault(value)


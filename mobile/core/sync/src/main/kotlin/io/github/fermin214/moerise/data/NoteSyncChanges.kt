package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.SyncNotesResponse

class NoteSyncChanges(private val instance: String, initialized: Boolean, initialGeneration: String) {
    val upserts = linkedMapOf<String, Note>()
    val deletions = linkedSetOf<String>()
    var reset = !initialized
        private set
    var generation = initialGeneration
        private set
    private var resetGeneration: String? = null
    private val visited = mutableSetOf<String>()
    private val noteName = Regex("^memos/[A-Za-z0-9_-]+$")

    fun accept(page: SyncNotesResponse) {
        require(page.mode in setOf("full", "delta", "reset")) { "SYNC_PROTOCOL" }
        require(page.items.all { it.instanceId == instance && noteName.matches(it.name) &&
            it.state in setOf("NORMAL", "ARCHIVED") && it.sourceHash.isNotBlank() }) { "ACCOUNT_CHANGED" }
        require(page.deleted.all(noteName::matches)) { "SYNC_PROTOCOL" }
        if (page.mode == "reset" && (!reset || resetGeneration != page.syncGeneration)) {
            reset = true
            resetGeneration = page.syncGeneration
            upserts.clear()
            deletions.clear()
        }
        page.items.forEach { note -> deletions.remove(note.name); upserts[note.name] = note }
        page.deleted.forEach { name -> upserts.remove(name); deletions.add(name) }
        generation = page.syncGeneration
        if (page.hasMore) require(page.nextCursor.isNotBlank() && visited.add(page.nextCursor)) { "SYNC_PROTOCOL" }
    }
}

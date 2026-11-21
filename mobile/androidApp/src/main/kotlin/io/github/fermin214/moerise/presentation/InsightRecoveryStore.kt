package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.domain.InsightRecovery
import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.NoteRepository
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

internal class InsightRecoveryStore(private val repository: suspend () -> NoteRepository) {
    private val lock = Mutex()
    private val written = mutableMapOf<String, Long>()

    suspend fun read(account: String): InsightRecovery = lock.withLock {
        repository().feature(account, "insights")?.let { CoreJson.decodeFromString<InsightRecovery>(it) } ?: InsightRecovery()
    }

    fun isWritten(account: String, revision: Long): Boolean = written[account] == revision

    suspend fun write(account: String, recovery: InsightRecovery, revision: Long) = lock.withLock {
        if (revision < (written[account] ?: 0L)) return@withLock
        repository().putFeature(account, "insights", CoreJson.encodeToString(recovery))
        written[account] = revision
    }
}

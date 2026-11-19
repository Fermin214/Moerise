package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

class FilterReads(
    private val repository: suspend () -> NoteRepository,
    private val core: () -> CoreClient?,
    private val account: () -> String?,
    private val timezone: () -> String,
    private val scope: CoroutineScope,
) {
    private val locks = mutableMapOf<String, Mutex>()
    private var generation = 0L

    fun clear() { generation++; locks.clear() }

    suspend fun tags(state: String): TagPage =
        CoreJson.decodeFromString(read("tags:$state", "/tags?state=$state"))

    suspend fun calendar(state: String, month: String): CalendarPage {
        require(month.matches(Regex("\\d{4}-(0[1-9]|1[0-2])")))
        return CoreJson.decodeFromString<CalendarPage>(read("calendar:$state:$month", "/calendar?state=$state&month=$month"))
            .also { require(it.month == month) }
    }

    private suspend fun read(name: String, path: String): String {
        val key = account() ?: throw CancellationException()
        val zone = timezone()
        val filter = "$zone:$name"
        val stamp = generation
        val lock = locks.getOrPut("$key:$filter") { Mutex() }
        return lock.withLock {
            val repo = repository()
            val cached = repo.readSnapshot(key, filter)
            val active = core()
            fun checkOwner() {
                if (key != account() || zone != timezone() || stamp != generation) throw CancellationException()
            }
            checkOwner()
            if (cached != null && (epochMillis() - cached.fetchedAt in 0..59_999 || active == null)) return@withLock cached.body
            val body = (active ?: error("CONNECTION_FAILED")).api(HttpMethod.Get, path)
            checkOwner()
            repo.putSnapshot(SnapshotEntity(key, filter, body, epochMillis()))
            checkOwner()
            body
        }
    }

    fun changed(before: Note?, after: Note?) {
        val key = account() ?: return
        val zone = timezone()
        val statesChanged = before?.state != after?.state
        val day = { note: Note? -> note?.let { displayServiceTime(it.createTime, zone).take(10) } }
        val names = buildSet {
            for (note in listOfNotNull(before, after)) {
                if (statesChanged && note.tags.isNotEmpty() || before?.tags.orEmpty().toSet() != after?.tags.orEmpty().toSet())
                    add("$zone:tags:${note.state}")
                if (statesChanged || day(before) != day(after))
                    add("$zone:calendar:${note.state}:${day(note)?.take(7)}")
            }
        }
        for (name in names) {
            val lock = locks.getOrPut("$key:$name") { Mutex() }
            scope.launch(start = CoroutineStart.UNDISPATCHED) { lock.withLock { repository().dropSnapshot(key, name) } }
        }
    }
}

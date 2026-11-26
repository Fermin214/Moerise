package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

internal data class FilterPage(val tags: TagPage, val calendar: CalendarPage)

class FilterReads(
    private val repository: suspend () -> NoteRepository,
    private val core: () -> CoreClient?,
    private val account: () -> String?,
    private val timezone: () -> String,
    private val scope: CoroutineScope,
) {
    private data class Key(val account: String, val state: String, val month: String, val zone: String)
    private data class Cached(val page: FilterPage, val fetchedAt: Long?, val offline: Boolean, val projection: Long)
    private val lock = Mutex()
    private val pages = linkedMapOf<Key, Cached>()
    private var generation = 0L
    private var observedRepository: NoteRepository? = null
    private var observation: Job? = null
    private val mutableRevision = MutableStateFlow(0L)
    val revision = mutableRevision.asStateFlow()

    fun clear() {
        generation++
        pages.clear()
        mutableRevision.value = generation
    }

    private fun cacheKey(state: String, month: String) = account()?.let { Key(it, state, month, timezone()) }

    internal fun cached(state: String, month: String): FilterPage? = cacheKey(state, month)?.let(::cached)

    private fun cached(key: Key): FilterPage? = pages[key]?.takeIf {
        val online = core() != null
        it.projection == observedRepository?.projection?.value &&
            (it.fetchedAt == null || !online || !it.offline && epochMillis() - it.fetchedAt in 0..59_999)
    }?.page

    private fun observe(repo: NoteRepository) {
        if (observedRepository === repo) return
        observation?.cancel()
        observedRepository = repo
        observation = scope.launch(start = CoroutineStart.UNDISPATCHED) {
            repo.projection.drop(1).collect { clear() }
        }
    }

    internal suspend fun filters(state: String, month: String): FilterPage {
        require(month.matches(Regex("\\d{4}-(0[1-9]|1[0-2])")))
        val key = cacheKey(state, month) ?: throw CancellationException()
        val stamp = generation
        fun checkOwner() {
            if (key != cacheKey(state, month) || stamp != generation) throw CancellationException()
        }
        return lock.withLock {
            checkOwner()
            cached(key)?.let { return@withLock it }
            val repo = repository()
            observe(repo)
            val projection = repo.projection.value
            val local = repo.isNotesSynced(key.account)
            val active = core()
            suspend fun read(name: String, path: String): SnapshotEntity {
                val filter = "${key.zone}:$name"
                val cached = repo.readSnapshot(key.account, filter)
                checkOwner()
                if (cached != null && (epochMillis() - cached.fetchedAt in 0..59_999 || active == null)) return cached
                val body = (active ?: error("CONNECTION_FAILED")).api(HttpMethod.Get, path)
                checkOwner()
                val snapshot = SnapshotEntity(key.account, filter, body, epochMillis())
                repo.putFilterSnapshot(snapshot, projection)
                return snapshot
            }
            val result = if (local) Cached(FilterPage(
                repo.localTagPage(key.account, state), repo.localCalendarPage(key.account, state, month, key.zone),
            ), null, false, projection) else {
                val tags = read("tags:$state", "/tags?state=$state")
                val calendar = read("calendar:$state:$month", "/calendar?state=$state&month=$month")
                Cached(FilterPage(CoreJson.decodeFromString(tags.body), CoreJson.decodeFromString(calendar.body)),
                    minOf(tags.fetchedAt, calendar.fetchedAt), active == null, projection)
            }
            require(result.page.calendar.month == month)
            checkOwner()
            if (projection != repo.projection.value) throw CancellationException()
            if (pages.size >= 24) pages.remove(pages.keys.first())
            pages[key] = result
            result.page
        }
    }

    fun changed(before: Note?, after: Note?) {
        if (before?.state == after?.state && before?.createTime == after?.createTime &&
            before?.tags.orEmpty().toSet() == after?.tags.orEmpty().toSet()) return
        clear()
    }
}

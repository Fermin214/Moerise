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
    private data class Key(val account: String, val zone: String, val filter: String)
    private data class Cached<T>(val page: T, val fetchedAt: Long?, val offline: Boolean, val projection: Long)
    private val lock = Mutex()
    private val tags = linkedMapOf<Key, Cached<TagPage>>()
    private val calendars = linkedMapOf<Key, Cached<CalendarPage>>()
    private var generation = 0L
    private var observedRepository: NoteRepository? = null
    private var observation: Job? = null
    private val mutableRevision = MutableStateFlow(0L)
    val revision = mutableRevision.asStateFlow()

    fun clear() {
        generation++
        tags.clear()
        calendars.clear()
        mutableRevision.value = generation
    }

    private fun cacheKey(filter: String) = account()?.let { Key(it, timezone(), filter) }

    internal fun cached(state: String, month: String): FilterPage? {
        val tags = cacheKey("tags:$state")?.let(tags::get).fresh() ?: return null
        val calendar = cacheKey("calendar:NORMAL:$month")?.let(calendars::get).fresh() ?: return null
        return FilterPage(tags, calendar)
    }

    internal suspend fun writingTags(owner: String): List<TagCount> {
        if (account() != owner) return emptyList()
        val key = cacheKey("tags:NORMAL") ?: return emptyList()
        tags[key].fresh()?.let { return it.tags.sortedByDescending { tag -> tag.count } }
        val repo = repository()
        observe(repo)
        if (!repo.isNotesSynced(owner) || account() != owner) return emptyList()
        while (account() == owner) {
            val projection = repo.projection.value
            val result = repo.localTagPage(owner, "NORMAL").tags
            if (account() == owner && projection == repo.projection.value) return result
        }
        return emptyList()
    }

    private fun <T> Cached<T>?.fresh(): T? = this?.takeIf {
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
        val tagKey = cacheKey("tags:$state") ?: throw CancellationException()
        val calendarKey = tagKey.copy(filter = "calendar:NORMAL:$month")
        val stamp = generation
        fun checkOwner() {
            if (tagKey != cacheKey(tagKey.filter) || stamp != generation) throw CancellationException()
        }
        return lock.withLock {
            checkOwner()
            cached(state, month)?.let { return@withLock it }
            val repo = repository()
            observe(repo)
            val projection = repo.projection.value
            val local = repo.isNotesSynced(tagKey.account)
            val active = core()
            suspend fun read(key: Key, path: String): SnapshotEntity {
                val filter = "${key.zone}:${key.filter}"
                val cached = repo.readSnapshot(key.account, filter)
                checkOwner()
                if (cached != null && (epochMillis() - cached.fetchedAt in 0..59_999 || active == null)) return cached
                val body = (active ?: error("CONNECTION_FAILED")).api(HttpMethod.Get, path)
                checkOwner()
                val snapshot = SnapshotEntity(key.account, filter, body, epochMillis())
                repo.putFilterSnapshot(snapshot, projection)
                return snapshot
            }
            suspend fun <T> load(
                key: Key, path: String, cache: MutableMap<Key, Cached<T>>,
                localPage: suspend () -> T, decode: (String) -> T,
            ): Cached<T> {
                val existing = cache[key]
                if (existing.fresh() != null) return requireNotNull(existing)
                if (local) return Cached(localPage(), null, false, projection)
                val snapshot = read(key, path)
                return Cached(decode(snapshot.body), snapshot.fetchedAt, active == null, projection)
            }
            val tagPage = load(tagKey, "/tags?state=$state", tags,
                { repo.localTagPage(tagKey.account, state) }, { CoreJson.decodeFromString<TagPage>(it) })
            val calendarPage = load(calendarKey, "/calendar?state=NORMAL&month=$month", calendars,
                { repo.localCalendarPage(calendarKey.account, "NORMAL", month, calendarKey.zone) },
                { CoreJson.decodeFromString<CalendarPage>(it) })
            require(calendarPage.page.month == month)
            checkOwner()
            if (projection != repo.projection.value) throw CancellationException()
            tags.store(tagKey, tagPage)
            calendars.store(calendarKey, calendarPage)
            FilterPage(tagPage.page, calendarPage.page)
        }
    }

    private fun <T> MutableMap<Key, Cached<T>>.store(key: Key, value: Cached<T>) {
        if (key !in this && size >= 24) remove(keys.first())
        this[key] = value
    }

    fun changed(before: Note?, after: Note?) {
        if (before?.state == after?.state && before?.createTime == after?.createTime &&
            before?.tags.orEmpty().toSet() == after?.tags.orEmpty().toSet()) return
        clear()
    }
}

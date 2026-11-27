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
    private val tagLock = Mutex()
    private val calendarLock = Mutex()
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

    internal fun cachedTags(state: String): TagPage? = cacheKey("tags:$state")?.let(tags::get).fresh()
    internal fun cachedCalendar(month: String): CalendarPage? = cacheKey("calendar:NORMAL:$month")?.let(calendars::get).fresh()
    internal fun cached(state: String, month: String): FilterPage? =
        cachedTags(state)?.let { tags -> cachedCalendar(month)?.let { FilterPage(tags, it) } }

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
        val filter = "tags:$state"
        val owner = cacheKey(filter) ?: throw CancellationException()
        val stamp = generation
        fun checkOwner() {
            if (owner != cacheKey(filter) || stamp != generation) throw CancellationException()
        }
        var sharedRepo: NoteRepository? = null
        suspend fun sharedRepository() = sharedRepo ?: repository().also { sharedRepo = it }
        val tagPage = tags(state, ::sharedRepository)
        checkOwner()
        val calendarPage = calendar(month, ::sharedRepository)
        checkOwner()
        return FilterPage(tagPage, calendarPage)
    }

    internal suspend fun tags(state: String, provider: suspend () -> NoteRepository = repository): TagPage = load(
        "tags:$state", "/tags?state=$state", tags, tagLock, provider,
        { key -> localTagPage(key.account, state) }, { CoreJson.decodeFromString(it) },
    )

    internal suspend fun calendar(month: String, provider: suspend () -> NoteRepository = repository): CalendarPage {
        require(month.matches(Regex("\\d{4}-(0[1-9]|1[0-2])")))
        return load("calendar:NORMAL:$month", "/calendar?state=NORMAL&month=$month", calendars, calendarLock, provider,
            { key -> localCalendarPage(key.account, "NORMAL", month, key.zone) },
            { CoreJson.decodeFromString<CalendarPage>(it).also { page -> require(page.month == month) } },
        )
    }

    private suspend fun <T> load(
        filter: String, path: String, cache: MutableMap<Key, Cached<T>>, lock: Mutex, provider: suspend () -> NoteRepository,
        localPage: suspend NoteRepository.(Key) -> T, decode: (String) -> T,
    ): T {
        val key = cacheKey(filter) ?: throw CancellationException()
        val stamp = generation
        fun checkOwner() {
            if (key != cacheKey(filter) || stamp != generation) throw CancellationException()
        }
        return lock.withLock {
            checkOwner()
            cache[key].fresh()?.let { return@withLock it }
            val repo = provider()
            checkOwner()
            observe(repo)
            val projection = repo.projection.value
            val result = if (repo.isNotesSynced(key.account)) Cached(repo.localPage(key), null, false, projection)
            else {
                val active = core()
                val snapshotKey = "${key.zone}:${key.filter}"
                val cached = repo.readSnapshot(key.account, snapshotKey)
                checkOwner()
                if (cached != null && (epochMillis() - cached.fetchedAt in 0..59_999 || active == null)) {
                    Cached(decode(cached.body), cached.fetchedAt, active == null, projection)
                } else {
                    val body = (active ?: error("CONNECTION_FAILED")).api(HttpMethod.Get, path)
                    checkOwner()
                    val page = decode(body)
                    val fetchedAt = epochMillis()
                    repo.putFilterSnapshot(SnapshotEntity(key.account, snapshotKey, body, fetchedAt), projection)
                    Cached(page, fetchedAt, false, projection)
                }
            }
            checkOwner()
            if (projection != repo.projection.value) throw CancellationException()
            cache.store(key, result)
            result.page
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

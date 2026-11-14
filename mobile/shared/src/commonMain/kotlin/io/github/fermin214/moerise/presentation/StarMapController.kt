package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.*
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.serialization.json.*

data class StarMapState(
    val snapshot: StarMap? = null, val status: String = "", val view: StarView? = null, val fitScale: Double = 100.0,
    val selected: String? = null, val reader: Note? = null, val reading: Boolean = false, val readerError: String? = null,
    val loading: Boolean = false, val posting: Boolean = false, val error: String? = null, val rebuild: MapRebuild? = null,
    val excerpts: Map<String, String> = emptyMap(), val photo: Boolean = true, val fullScreen: Boolean = false,
    val allFraming: Boolean = false, val tab: Int = 0, val compatible: Boolean = true, val unplacedOnly: Boolean = false,
)
data class MapMetric(val kind: String, val value: Long, val version: String)

class StarMapController(
    private val repository: suspend () -> NoteRepository, private val core: () -> CoreClient?,
    private val account: () -> String?, private val scope: CoroutineScope,
) {
    private val mutable = MutableStateFlow(StarMapState())
    val state = mutable.asStateFlow()
    private val mutableMetrics = MutableSharedFlow<MapMetric>(extraBufferCapacity = 128, onBufferOverflow = BufferOverflow.DROP_OLDEST)
    val metrics = mutableMetrics.asSharedFlow()
    private var key: String? = null
    private var visible = false
    private var epoch = 0L
    private var readJob: Job? = null
    private var postJob: Job? = null
    private var polling: Job? = null
    private var readerJob: Job? = null
    private var readerGeneration = 0L
    private var excerptJob: Job? = null
    private var excerptGeneration = 0L
    private var desired = emptyList<String>()
    private val excerptCache = linkedMapOf<String, Pair<String, String>>()
    private var width = 0.0
    private var height = 0.0
    private var firstDrawVersion = ""
    private var receivedAt = 0L

    fun visible(value: Boolean) {
        if (value == visible) return
        visible = value
        if (value) {
            refresh()
            if (key == account()) state.value.selected?.let(::select)
        } else {
            ++epoch; readJob?.cancel(); postJob?.cancel(); polling?.cancel()
            ++readerGeneration; readerJob?.cancel(); cancelExcerpts()
            mutable.update { it.copy(loading = false, posting = false, reading = false) }
        }
    }
    fun clear() { visible(false); key = null; excerptCache.clear(); mutable.value = StarMapState(); firstDrawVersion = "" }
    fun tab(value: Int) { if (value != 0) cancelExcerpts(); mutable.update { it.copy(tab = value) } }
    fun unplaced() { mutable.update { it.copy(unplacedOnly = !it.unplacedOnly) } }
    fun atmosphere() { mutable.update { it.copy(photo = !it.photo) } }
    fun fullScreen() { mutable.update { it.copy(fullScreen = !it.fullScreen) } }
    fun back(): Boolean = when {
        state.value.fullScreen -> { fullScreen(); true }
        state.value.selected != null -> { closeReader(); true }
        else -> false
    }
    fun size(width: Double, height: Double) {
        this.width = width; this.height = height
        if (state.value.view == null) fit()
    }
    fun fit(all: Boolean = state.value.allFraming) {
        val map = state.value.snapshot ?: return
        if (width <= 0 || height <= 0) return
        cancelExcerpts()
        val view = fitStars(map.points, width, height, all, map.topics)
        mutable.update { it.copy(view = view, fitScale = view.scale, allFraming = all) }
    }
    fun focus(names: List<String>) {
        val members = names.toSet()
        val points = state.value.snapshot?.points?.filter { it.name in members } ?: return
        if (points.isEmpty()) return
        cancelExcerpts()
        mutable.update { it.copy(view = fitStars(points, width, height), tab = 0) }
    }
    fun transform(factor: Double, beforeX: Double = 0.0, beforeY: Double = 0.0, panX: Double = 0.0, panY: Double = 0.0) {
        val view = state.value.view ?: return
        cancelExcerpts()
        mutable.update { it.copy(view = view.transform(factor, beforeX, beforeY, panX, panY)) }
    }
    fun pick(x: Double, y: Double, radius: Double) {
        val map = state.value.snapshot ?: return
        val view = state.value.view ?: return
        hitStar(map.points, view, width, height, x, y, radius)?.let(::select)
    }

    private fun current(stamp: Long, account: String, active: CoreClient) =
        stamp == epoch && visible && account == this.account() && active === core()

    fun refresh() {
        if (!visible || state.value.loading || state.value.posting) return
        val active = core() ?: return
        val account = account() ?: return
        val stamp = epoch
        val previous = readJob
        polling?.cancel()
        mutable.update { it.copy(loading = true, error = null) }
        readJob = scope.launch {
            try {
                previous?.join(); postJob?.join()
                if (key != account) {
                    val rebuild = repository().feature(account, "map-rebuild")?.let { CoreJson.decodeFromString<MapRebuild>(it) }
                    if (!current(stamp, account, active)) return@launch
                    key = account
                    mutable.update { it.copy(rebuild = rebuild) }
                }
                val response = active.api(HttpMethod.Get, "/map")
                receivedAt = epochMillis()
                metric("response_bytes", response.encodeToByteArray().size.toLong())
                val map = withContext(Dispatchers.Default) {
                    try { CoreJson.decodeFromString<StarMap>(response).validate() }
                    catch (_: kotlinx.serialization.SerializationException) { throw IllegalArgumentException("MAP_PROTOCOL") }
                }
                if (!current(stamp, account, active)) return@launch
                metric("parse_ms", (epochMillis() - receivedAt).coerceAtLeast(0))
                val previousMap = state.value.snapshot
                val frameChanged = previousMap?.layoutFrame != map.layoutFrame
                val selected = state.value.selected
                val point = map.points.firstOrNull { it.name == selected }
                val oldPoint = previousMap?.points?.firstOrNull { it.name == selected }
                val snapshot = when {
                    map.readable && map.layoutVersion == previousMap?.layoutVersion -> previousMap.copy(
                        status = map.status, updateState = map.updateState, generatedAt = map.generatedAt)
                    map.readable -> map
                    map.status == "disabled" -> null
                    else -> previousMap
                }
                if (snapshot?.layoutVersion != previousMap?.layoutVersion) cancelExcerpts()
                val hashes = snapshot?.points?.associate { it.name to it.sourceHash }.orEmpty()
                excerptCache.keys.toList().filter { excerptCache[it]?.first != hashes[it] }.forEach { excerptCache.remove(it) }
                mutable.update { it.copy(snapshot = snapshot, status = map.status, loading = false, compatible = true,
                    view = if (map.readable && frameChanged) null else it.view,
                    selected = if (map.readable && point == null || map.status == "disabled") null else it.selected,
                    reader = if (map.readable && point?.sourceHash != oldPoint?.sourceHash || map.status == "disabled") null else it.reader,
                    excerpts = if (snapshot?.layoutVersion != previousMap?.layoutVersion) emptyMap() else it.excerpts) }
                if (state.value.selected == null) closeReader()
                else if (map.readable && point?.sourceHash != oldPoint?.sourceHash) select(state.value.selected!!)
                if (state.value.view == null) fit()
                val rebuild = state.value.rebuild
                if (rebuild != null && rebuild.state in setOf("unknown", "observing")) {
                    val next = when {
                        map.status == "unavailable" || map.updateState == "unavailable" || map.status == "disabled" ->
                            rebuild.copy(state = "failed", retryAfter = epochMillis() + 10_000)
                        map.readable && map.layoutFrame != rebuild.frame -> rebuild.copy(state = "complete")
                        else -> rebuild
                    }
                    if (next != rebuild) recordRebuild(account, next)
                }
                if (state.value.selected != null && state.value.reader == null && !state.value.reading) select(state.value.selected!!)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (current(stamp, account, active)) mutable.update { it.copy(loading = false, error = errorCode(error),
                    compatible = errorCode(error) !in setOf("MAP_INCOMPATIBLE", "MAP_PROTOCOL")) }
            } finally { if (current(stamp, account, active)) schedulePoll() }
        }
    }

    private fun schedulePoll() {
        polling?.cancel()
        if (!visible || !state.value.compatible) return
        val state = state.value
        val waiting = state.status in setOf("waiting", "building") || state.snapshot?.updateState == "building" ||
            state.rebuild?.state in setOf("unknown", "observing")
        val failed = state.error != null || state.status == "unavailable" || state.snapshot?.updateState == "unavailable" ||
            state.rebuild?.state == "failed"
        val delayMs = if (failed) 10_000L else if (waiting) 3000L else 60_000L
        polling = scope.launch { delay(delayMs); refresh() }
    }
    private suspend fun recordRebuild(account: String, record: MapRebuild) {
        repository().putFeature(account, "map-rebuild", CoreJson.encodeToString(record))
        if (account == this.account() && account == key) mutable.update { it.copy(rebuild = record) }
    }
    fun rebuild(explicitAgain: Boolean = false) {
        if (!visible || state.value.posting || state.value.loading || !state.value.compatible) return
        val old = state.value.rebuild
        if (!explicitAgain && old != null && old.state in setOf("unknown", "observing")) return
        if (old?.state == "failed" && epochMillis() < old.retryAfter) return
        val active = core() ?: return
        val account = account() ?: return
        if (key != account || state.value.status.isEmpty() || state.value.status == "disabled") return
        val stamp = epoch
        val record = MapRebuild(state.value.snapshot?.layoutFrame ?: "", epochMillis())
        val previous = postJob
        mutable.update { it.copy(posting = true, error = null) }
        polling?.cancel()
        postJob = scope.launch {
            try {
                previous?.join(); readJob?.join()
                if (!current(stamp, account, active)) return@launch
                recordRebuild(account, record)
                if (!current(stamp, account, active)) return@launch
                val result = CoreJson.parseToJsonElement(active.api(HttpMethod.Post, "/map/rebuild", "{}")).jsonObject
                require(result["status"]?.jsonPrimitive?.content == "queued") { "MAP_PROTOCOL" }
                if (current(stamp, account, active)) recordRebuild(account, record.copy(state = "observing"))
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (current(stamp, account, active)) {
                    if (error is CoreException && error.status in 400..599)
                        recordRebuild(account, record.copy(state = "failed", retryAfter = epochMillis() + 10_000))
                    mutable.update { it.copy(error = errorCode(error)) }
                }
            } finally {
                if (current(stamp, account, active)) { mutable.update { it.copy(posting = false) }; schedulePoll() }
            }
        }
    }
    fun closeReader() {
        ++readerGeneration; readerJob?.cancel()
        mutable.update { it.copy(selected = null, reader = null, reading = false, readerError = null) }
    }
    fun select(name: String) {
        val point = state.value.snapshot?.points?.firstOrNull { it.name == name } ?: return
        val active = core() ?: return
        val account = account() ?: return
        if (!visible) return
        val stamp = epoch
        val ticket = ++readerGeneration
        readerJob?.cancel()
        mutable.update { it.copy(selected = name, reader = null, reading = true, readerError = null) }
        readerJob = scope.launch {
            try {
                val note = CoreJson.decodeFromString<Note>(active.api(HttpMethod.Get, "/notes/${name.removePrefix("memos/")}"))
                require(note.instanceId == account.split('\u0000')[1] && note.name == name) { "ACCOUNT_CHANGED" }
                if (ticket == readerGeneration && current(stamp, account, active) &&
                    state.value.snapshot?.points?.firstOrNull { it.name == name }?.sourceHash == point.sourceHash)
                    mutable.update { it.copy(reader = note, reading = false) }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (ticket == readerGeneration && current(stamp, account, active))
                    mutable.update { it.copy(reading = false, readerError = errorCode(error)) }
            }
        }
    }
    fun cancelExcerpts() { ++excerptGeneration; excerptJob?.cancel(); desired = emptyList() }
    fun excerpts(names: List<String>) {
        val requested = names.distinct().take(3)
        if (!visible || requested == desired) return
        cancelExcerpts(); desired = requested
        val active = core() ?: return
        val account = account() ?: return
        val stamp = epoch
        val ticket = excerptGeneration
        val points = state.value.snapshot?.points?.associateBy { it.name } ?: return
        excerptJob = scope.launch {
            val result = mutableMapOf<String, String>()
            for (name in requested) {
                val point = points[name] ?: continue
                excerptCache[name]?.takeIf { it.first == point.sourceHash }?.let { result[name] = it.second }
                if (name in result) continue
                state.value.reader?.takeIf { it.name == name && it.sourceHash == point.sourceHash }?.let {
                    result[name] = it.content.lineSequence().firstOrNull { it.isNotBlank() }?.take(100).orEmpty()
                }
                if (name in result) continue
                try {
                    metric("excerpt_request", 1)
                    val note = CoreJson.decodeFromString<Note>(active.api(HttpMethod.Get, "/notes/${name.removePrefix("memos/")}"))
                    require(note.instanceId == account.split('\u0000')[1] && note.name == name) { "ACCOUNT_CHANGED" }
                    if (ticket != excerptGeneration || !current(stamp, account, active)) return@launch
                    if (note.sourceHash == point.sourceHash && state.value.snapshot?.points?.firstOrNull { it.name == name }?.sourceHash == point.sourceHash) {
                        result[name] = note.content.lineSequence().firstOrNull { it.isNotBlank() }?.take(100).orEmpty()
                        excerptCache[name] = point.sourceHash to result.getValue(name)
                        while (excerptCache.size > 32) excerptCache.remove(excerptCache.keys.first())
                    }
                } catch (error: Exception) { if (error is CancellationException) throw error }
            }
            if (ticket == excerptGeneration && current(stamp, account, active)) mutable.update { it.copy(excerpts = result) }
        }
    }
    fun drawn(duration: Long) {
        metric("draw_ms", duration)
        val version = state.value.snapshot?.layoutVersion ?: return
        if (firstDrawVersion != version) {
            firstDrawVersion = version
            metric("first_draw_ms", (epochMillis() - receivedAt).coerceAtLeast(0))
        }
    }
    private fun metric(kind: String, value: Long) { mutableMetrics.tryEmit(MapMetric(kind, value, state.value.snapshot?.layoutVersion.orEmpty())) }
}

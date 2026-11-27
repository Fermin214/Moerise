package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.*

@Serializable data class ReviewItem(
    val name: String, @SerialName("selected_source_hash") val selectedSourceHash: String,
    @SerialName("selected_at") val selectedAt: String, val unavailable: Boolean, val note: Note? = null,
)
@Serializable data class ReviewBatch(
    val id: String, val date: String, val timezone: String, val items: List<ReviewItem>,
)
data class ReviewState(
    val batch: ReviewBatch? = null, val position: Int = 0, val loading: Boolean = false, val error: String? = null,
)

private val noteResource = Regex("^memos/[A-Za-z0-9_-]+$")
private fun Note.belongsTo(account: String) = instanceId == account.split('\u0000')[1] &&
    noteResource.matches(name) && sourceHash.isNotBlank() && state in setOf("NORMAL", "ARCHIVED")

data class WalkStartNote(val name: String, val createTime: String, val summary: String)

internal fun walkSummaryPreview(note: Note, body: String = walkSummaryBody(note.content, note.tags)): String =
    limitWalkSummary(body.ifBlank {
        (note.attachments.firstOrNull()?.get("filename") as? JsonPrimitive)?.contentOrNull.orEmpty()
    })

private fun walkStartingNotes(notes: List<Note>): List<WalkStartNote> = notes.take(20)
    .mapNotNull { note ->
        val body = walkSummaryBody(note.content, note.tags)
        walkStartPriority(body, note.attachments.isNotEmpty())?.let {
            it to WalkStartNote(note.name, note.createTime, walkSummaryPreview(note, body))
        }
    }
    .sortedBy { it.first }.take(6).map { it.second }

class ReviewController(
    private val core: () -> CoreClient?, private val account: () -> String?, private val scope: CoroutineScope,
    private val privacy: PrivacyController? = null,
    private val repository: (suspend () -> NoteRepository)? = null,
) {
    private val mutable = MutableStateFlow(ReviewState())
    val state = mutable.asStateFlow()
    private var visible = false
    private var generation = 0L
    private var request: Job? = null

    fun visible(value: Boolean) {
        if (value == visible) return
        visible = value
        if (value) refresh() else leave()
    }

    fun clear() { visible = false; leave(); mutable.value = ReviewState() }
    private fun leave() { ++generation; request?.cancel(); mutable.update { it.copy(loading = false) } }

    fun select(position: Int) {
        val count = state.value.batch?.items?.size ?: return
        if (position in 0 until count) mutable.update { it.copy(position = position) }
    }
    fun next() {
        val count = state.value.batch?.items?.size ?: return
        if (count > 0) select((state.value.position + 1) % count)
    }

    private fun validateBatch(batch: ReviewBatch, key: String): ReviewBatch {
        require(batch.id.isNotBlank() && Regex("^\\d{4}-\\d{2}-\\d{2}$").matches(batch.date) && batch.timezone.isNotBlank() &&
            batch.items.map { it.name }.distinct().size == batch.items.size && batch.items.all {
                noteResource.matches(it.name) && (it.unavailable || (it.note?.belongsTo(key) == true && it.note.name == it.name))
            }) { "REVIEW_RESPONSE_INVALID" }
        return batch
    }

    suspend fun prefetch() {
        val key = account() ?: return
        val repo = repository?.invoke() ?: return
        val cached = repo.readCachedSnapshot(key, "review")
        if (cached != null && state.value.batch == null) {
            runCatching { validateBatch(CoreJson.decodeFromString(cached.body), key) }.getOrNull()?.let { batch ->
                if (key == account()) mutable.update { it.copy(batch = it.batch ?: batch) }
            }
        }
        val active = core() ?: return
        if (visible || state.value.loading) return
        val response = runCatching { active.api(HttpMethod.Get, "/reviews/daily") }.getOrNull() ?: return
        val batch = runCatching {
            withContext(Dispatchers.Default) { validateBatch(CoreJson.decodeFromString(response), key) }
        }.getOrNull() ?: return
        if (key == account() && active === core()) {
            repo.putCachedSnapshot(key, "review", response)
            mutable.update { it.copy(batch = batch, position =
                if (it.batch?.id == batch.id) it.position.coerceAtMost((batch.items.size - 1).coerceAtLeast(0)) else 0) }
        }
    }

    fun refresh() {
        if (!visible || state.value.loading) return
        val key = account() ?: return
        val active = core()
        val stamp = ++generation
        val started = epochMillis()
        mutable.update { it.copy(loading = state.value.batch == null, error = null) }
        request = scope.launch {
            try {
                val repo = repository?.invoke()
                if (state.value.batch == null && repo != null) {
                    repo.readCachedSnapshot(key, "review")?.let { cached ->
                        runCatching { validateBatch(CoreJson.decodeFromString(cached.body), key) }.getOrNull()?.let { batch ->
                            if (stamp == generation && key == account()) mutable.update { it.copy(batch = batch, loading = active != null) }
                        }
                    }
                }
                val client = active ?: return@launch
                val response = client.api(HttpMethod.Get, "/reviews/daily")
                val batch = withContext(Dispatchers.Default) { validateBatch(CoreJson.decodeFromString(response), key) }
                if (stamp != generation || key != account() || client !== core()) return@launch
                repo?.putCachedSnapshot(key, "review", response)
                mutable.update { it.copy(batch = batch, position =
                    if (it.batch?.id == batch.id) it.position.coerceAtMost((batch.items.size - 1).coerceAtLeast(0)) else 0,
                    loading = false) }
                privacy?.record("review", started = started)
            } catch (error: Exception) {
                if (key == account()) privacy?.record("review", usageResult(error), started, error = error)
                if (error is CancellationException) throw error
                if (stamp == generation && key == account() && active === core())
                    mutable.update { it.copy(loading = false, error = errorCode(error)) }
            }
        }
    }
}

@Serializable data class WalkHit(val note: Note)
@Serializable data class WalkResult(
    val mode: String, val item: WalkHit? = null, val path: List<String>, val exhausted: Boolean, val warnings: List<String>,
)
data class WalkRequest(val start: Boolean, val name: String = "", val path: List<String> = emptyList()) {
    fun body() = buildJsonObject {
        if (name.isNotBlank()) put("name", name)
        if (!start) put("path", JsonArray(path.map(::JsonPrimitive)))
    }.toString()
}
data class WalkState(
    val result: WalkResult? = null, val previous: Note? = null, val loading: Boolean = false,
    val error: String? = null, val retry: WalkRequest? = null,
    val embeddingEnabled: Boolean? = null, val embeddingConfigured: Boolean? = null,
    val configurationLoading: Boolean = false, val configurationError: String? = null,
    val offline: Boolean = false, val hasLocalNotes: Boolean = false,
    val floaters: List<WalkStartNote> = emptyList(), val floatersLoading: Boolean = false, val floaterError: String? = null,
) {
    val initialConfigurationLoading: Boolean get() = configurationLoading && embeddingEnabled == null
}

class WalkController(
    private val core: () -> CoreClient?, private val account: () -> String?, private val scope: CoroutineScope,
    private val privacy: PrivacyController? = null,
    private val repository: (suspend () -> NoteRepository)? = null,
    private val enablePrefetch: Boolean = false,
    private val online: () -> Boolean = { true },
) {
    private val mutable = MutableStateFlow(WalkState())
    val state = mutable.asStateFlow()
    private var visible = false
    private var generation = 0L
    private var request: Job? = null
    private var prefetchGeneration = 0L
    private var prefetchJob: Job? = null
    private var prefetched: Pair<List<String>, WalkResult>? = null
    private var configurationJob: Job? = null
    private var floaterJob: Job? = null
    private var configurationGeneration = 0L
    private var floaterGeneration = 0L
    private var pendingStart: WalkRequest? = null

    fun visible(value: Boolean) {
        if (value == visible) return
        visible = value
        if (value) {
            loadConfiguration()
            loadFloaters()
        } else {
            ++generation; request?.cancel(); cancelPrefetch()
            ++configurationGeneration; configurationJob?.cancel()
            ++floaterGeneration; floaterJob?.cancel()
            mutable.update { it.copy(loading = false, configurationLoading = false, floatersLoading = false,
                error = if (it.loading) "WALK_INTERRUPTED" else it.error) }
        }
    }
    fun clear() { visible(false); pendingStart = null; cancelPrefetch(); mutable.value = WalkState() }
    fun end() {
        ++generation; request?.cancel(); pendingStart = null; cancelPrefetch()
        mutable.update { it.copy(result = null, previous = null, loading = false, error = null, retry = null) }
    }
    fun start(name: String = "") {
        val input = WalkRequest(start = true, name = name)
        cancelPrefetch()
        if (!online()) send(input)
        else if (state.value.embeddingEnabled == null) { pendingStart = input; loadConfiguration() } else send(input)
    }
    fun next() {
        val result = state.value.result ?: return
        if (result.exhausted || result.item == null) return
        val buffered = prefetched?.takeIf { it.first == result.path }?.second
        if (buffered != null) {
            prefetched = null
            if (buffered.item != null || buffered.exhausted) {
                val displayed = if (buffered.item == null) buffered.copy(item = result.item) else buffered
                mutable.update {
                    it.copy(
                        result = displayed,
                        previous = if (buffered.item == null) it.previous else result.item.note,
                        loading = false,
                        error = null,
                        retry = null,
                    )
                }
                if (displayed.item != null && !displayed.exhausted) schedulePrefetch(displayed, account() ?: return, core() ?: return, generation)
                return
            }
        }
        send(WalkRequest(false, result.item.note.name, result.path))
    }
    fun retry() { cancelPrefetch(); state.value.retry?.let(::send) }
    fun retryConfiguration() = loadConfiguration()
    fun retryFloaters() = loadFloaters()
    fun connectivityChanged() {
        if (state.value.offline == !online()) return
        mutable.update { it.copy(offline = !online()) }
        cancelPrefetch()
        if (visible && online()) loadConfiguration()
    }

    fun configurationChanged() {
        mutable.update { it.copy(embeddingEnabled = null, embeddingConfigured = null) }
        if (visible) loadConfiguration()
    }

    private fun cancelPrefetch() {
        ++prefetchGeneration
        prefetchJob?.cancel()
        prefetchJob = null
        prefetched = null
    }

    private fun loadConfiguration() {
        if (!visible || !online()) return
        val active = core() ?: return
        val key = account() ?: return
        val stamp = ++configurationGeneration
        configurationJob?.cancel()
        mutable.update { it.copy(configurationLoading = true, configurationError = null) }
        configurationJob = scope.launch {
            try {
                val models = CoreJson.parseToJsonElement(active.api(HttpMethod.Get, "/models")).jsonObject
                val embedding = models.getValue("embedding").jsonObject
                val enabled = requireNotNull(embedding["enabled"]?.jsonPrimitive?.booleanOrNull) { "WALK_RESPONSE_INVALID" }
                val configured = embedding["configured"]?.jsonPrimitive?.booleanOrNull
                if (stamp != configurationGeneration || key != account() || active !== core()) return@launch
                mutable.update { it.copy(embeddingEnabled = enabled, embeddingConfigured = configured, configurationLoading = false) }
                if (enabled) pendingStart?.let { pendingStart = null; send(it) }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == configurationGeneration && key == account() && active === core())
                    mutable.update { it.copy(configurationLoading = false, configurationError = errorCode(error)) }
            }
        }
    }

    private fun loadFloaters() {
        if (!visible) return
        val key = account() ?: return
        val stamp = ++floaterGeneration
        floaterJob?.cancel()
        mutable.update { it.copy(floatersLoading = repository == null, floaterError = null, offline = !online()) }
        floaterJob = scope.launch {
            try {
                val repo = repository?.invoke()
                if (repo != null) {
                    val notes = repo.localNotesPage(key, "NORMAL", 20, 0).first
                    val local = withContext(Dispatchers.Default) { walkStartingNotes(notes) }
                    if (stamp == floaterGeneration && key == account())
                        mutable.update { it.copy(floaters = local, floatersLoading = false, hasLocalNotes = notes.isNotEmpty()) }
                    if (notes.isNotEmpty() || repo.isNotesSynced(key) || !online()) return@launch
                }
                if (!online()) return@launch
                val active = core() ?: return@launch
                mutable.update { it.copy(floatersLoading = true) }
                val page = CoreJson.decodeFromString<io.github.fermin214.moerise.domain.NotePage>(
                    active.api(HttpMethod.Get, "/notes?state=NORMAL&page_size=20"))
                require(page.notes.all { it.belongsTo(key) && it.state == "NORMAL" } &&
                    page.notes.map { it.name }.distinct().size == page.notes.size) { "NOTE_LIST_INVALID" }
                val selected = withContext(Dispatchers.Default) { walkStartingNotes(page.notes) }
                if (stamp == floaterGeneration && key == account() && active === core())
                    mutable.update { it.copy(floaters = selected, floatersLoading = false) }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == floaterGeneration && key == account())
                    mutable.update { it.copy(floatersLoading = false, floaterError = errorCode(error)) }
            }
        }
    }

    private fun validateWalkResult(result: WalkResult, prior: List<String>, input: WalkRequest, key: String): WalkResult {
        require(result.mode in setOf("random", "continue") && result.path.all(noteResource::matches) &&
            result.path.distinct().size == result.path.size && result.path.take(prior.size) == prior &&
            if (result.item != null) {
                result.item.note.belongsTo(key) && result.item.note.state == "NORMAL" &&
                    result.path.lastOrNull() == result.item.note.name && result.path.size == prior.size + 1 &&
                    (!input.start || input.name.isEmpty() || input.name == result.item.note.name)
            } else result.path.size == prior.size
        ) { "WALK_RESPONSE_INVALID" }
        return result
    }

    private fun schedulePrefetch(current: WalkResult, key: String, active: CoreClient, stamp: Long) {
        if (!online() || !enablePrefetch || current.exhausted || current.item == null || !visible) return
        val input = WalkRequest(false, current.item.note.name, current.path)
        val pStamp = ++prefetchGeneration
        prefetchJob?.cancel()
        prefetched = null
        prefetchJob = scope.launch {
            try {
                require(noteResource.matches(input.name)) { "INVALID_ARGUMENT" }
                val response = active.api(HttpMethod.Post, "/walks/next", input.body())
                val nextResult = validateWalkResult(
                    withContext(Dispatchers.Default) { CoreJson.decodeFromString<WalkResult>(response) },
                    input.path, input, key,
                )
                if (pStamp == prefetchGeneration && visible && key == account() && active === core() &&
                    state.value.result?.path == current.path) {
                    prefetched = current.path to nextResult
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
            }
        }
    }

    private fun send(input: WalkRequest) {
        if (!visible || state.value.loading || online() && state.value.embeddingEnabled != true) return
        val active = core()
        val key = account() ?: return
        val stamp = ++generation
        val started = epochMillis()
        mutable.update { it.copy(loading = true, error = null, retry = input) }
        request = scope.launch {
            try {
                require(input.name.isEmpty() || noteResource.matches(input.name)) { "INVALID_ARGUMENT" }
                if (!online()) {
                    val repo = repository?.invoke() ?: throw IllegalArgumentException("NOTE_NOT_FOUND")
                    val prior = if (input.start) emptyList() else input.path
                    val note = if (input.start && input.name.isNotEmpty()) repo.localNote(key, input.name)?.takeIf { it.state == "NORMAL" }
                        else repo.localWalkNote(key, prior)
                    if (stamp != generation || key != account()) return@launch
                    val result = WalkResult("random", note?.let(::WalkHit), prior + listOfNotNull(note?.name), note == null, emptyList())
                    mutable.update { it.copy(result = if (note == null && !input.start) result.copy(item = it.result?.item) else result,
                        previous = if (input.start) null else if (note == null) it.previous else it.result?.item?.note,
                        loading = false, retry = null) }
                    return@launch
                }
                val client = active ?: throw IllegalArgumentException("SESSION_REQUIRED")
                val pendingPrefetch = if (!input.start) prefetchJob else null
                if (pendingPrefetch?.isActive == true) {
                    pendingPrefetch.join()
                    val buffered = prefetched?.takeIf { it.first == input.path }?.second
                    if (buffered != null && (buffered.item != null || buffered.exhausted)) {
                        prefetched = null
                        if (stamp != generation || key != account() || active !== core()) return@launch
                        privacy?.record("walk", started = started)
                        var currentResult: WalkResult? = null
                        mutable.update { previous ->
                            val displayed = if (buffered.item == null) buffered.copy(item = previous.result?.item) else buffered
                            currentResult = displayed
                            previous.copy(result = displayed, previous = if (buffered.item == null) previous.previous
                                else previous.result?.item?.note, loading = false, retry = null)
                        }
                        currentResult?.let { schedulePrefetch(it, key, client, stamp) }
                        return@launch
                    }
                }
                cancelPrefetch()
                val response = client.api(HttpMethod.Post, if (input.start) "/walks/start" else "/walks/next", input.body())
                val result = validateWalkResult(
                    withContext(Dispatchers.Default) { CoreJson.decodeFromString<WalkResult>(response) },
                    if (input.start) emptyList() else input.path, input, key,
                )
                if (stamp != generation || key != account() || active !== core()) return@launch
                if (result.item == null && !result.exhausted) {
                    privacy?.record("walk", "over_limit", started)
                    mutable.update { it.copy(loading = false, error = "WALK_RESOURCE_LIMIT",
                        result = it.result?.copy(warnings = result.warnings), retry = input) }
                } else {
                    privacy?.record("walk", started = started)
                    var currentResult: WalkResult? = null
                    mutable.update { previous ->
                        val displayed = if (result.item == null && !input.start) result.copy(item = previous.result?.item) else result
                        currentResult = displayed
                        previous.copy(result = displayed, previous = if (result.item == null) previous.previous
                            else if (input.start) null else previous.result?.item?.note, loading = false, retry = null)
                    }
                    currentResult?.let { schedulePrefetch(it, key, client, stamp) }
                }
            } catch (error: Exception) {
                if (key == account()) privacy?.record("walk", usageResult(error), started, error = error)
                if (error is CancellationException) throw error
                if (stamp == generation && key == account() && active === core())
                    mutable.update { it.copy(loading = false, error = errorCode(error)) }
            }
        }
    }
}

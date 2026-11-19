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

class ReviewController(
    private val core: () -> CoreClient?, private val account: () -> String?, private val scope: CoroutineScope,
    private val privacy: PrivacyController? = null,
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

    fun refresh() {
        if (!visible || state.value.loading) return
        val active = core() ?: return
        val key = account() ?: return
        val stamp = ++generation
        val started = epochMillis()
        mutable.update { it.copy(loading = true, error = null) }
        request = scope.launch {
            try {
                val response = active.api(HttpMethod.Get, "/reviews/daily")
                val batch = withContext(Dispatchers.Default) { CoreJson.decodeFromString<ReviewBatch>(response) }
                require(batch.id.isNotBlank() && Regex("^\\d{4}-\\d{2}-\\d{2}$").matches(batch.date) && batch.timezone.isNotBlank() &&
                    batch.items.map { it.name }.distinct().size == batch.items.size && batch.items.all {
                        noteResource.matches(it.name) && (it.unavailable || (it.note?.belongsTo(key) == true && it.note.name == it.name))
                    }) { "REVIEW_RESPONSE_INVALID" }
                if (stamp != generation || key != account() || active !== core()) return@launch
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
    val floaters: List<Note> = emptyList(), val floatersLoading: Boolean = false, val floaterError: String? = null,
)

class WalkController(
    private val core: () -> CoreClient?, private val account: () -> String?, private val scope: CoroutineScope,
    private val privacy: PrivacyController? = null,
) {
    private val mutable = MutableStateFlow(WalkState())
    val state = mutable.asStateFlow()
    private var visible = false
    private var generation = 0L
    private var request: Job? = null
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
            ++generation; request?.cancel()
            ++configurationGeneration; configurationJob?.cancel()
            ++floaterGeneration; floaterJob?.cancel()
            mutable.update { it.copy(loading = false, configurationLoading = false, floatersLoading = false,
                error = if (it.loading) "WALK_INTERRUPTED" else it.error) }
        }
    }
    fun clear() { visible(false); pendingStart = null; mutable.value = WalkState() }
    fun end() {
        ++generation; request?.cancel(); pendingStart = null
        mutable.update { it.copy(result = null, previous = null, loading = false, error = null, retry = null) }
    }
    fun start(name: String = "") {
        val input = WalkRequest(start = true, name = name)
        if (state.value.embeddingEnabled == null) { pendingStart = input; loadConfiguration() } else send(input)
    }
    fun next() {
        val result = state.value.result ?: return
        if (!result.exhausted && result.item != null) send(WalkRequest(false, result.item.note.name, result.path))
    }
    fun retry() { state.value.retry?.let(::send) }
    fun retryConfiguration() = loadConfiguration()
    fun retryFloaters() = loadFloaters()
    fun configurationChanged() {
        mutable.update { it.copy(embeddingEnabled = null, embeddingConfigured = null) }
        if (visible) loadConfiguration()
    }

    private fun loadConfiguration() {
        if (!visible) return
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
        val active = core() ?: return
        val key = account() ?: return
        val stamp = ++floaterGeneration
        floaterJob?.cancel()
        mutable.update { it.copy(floatersLoading = true, floaterError = null) }
        floaterJob = scope.launch {
            try {
                val page = CoreJson.decodeFromString<io.github.fermin214.moerise.domain.NotePage>(
                    active.api(HttpMethod.Get, "/notes?state=NORMAL&page_size=6"))
                require(page.notes.all { it.belongsTo(key) && it.state == "NORMAL" } &&
                    page.notes.map { it.name }.distinct().size == page.notes.size) { "NOTE_LIST_INVALID" }
                if (stamp == floaterGeneration && key == account() && active === core())
                    mutable.update { it.copy(floaters = page.notes.take(6), floatersLoading = false) }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == floaterGeneration && key == account() && active === core())
                    mutable.update { it.copy(floatersLoading = false, floaterError = errorCode(error)) }
            }
        }
    }

    private fun send(input: WalkRequest) {
        if (!visible || state.value.loading || state.value.embeddingEnabled != true) return
        val active = core() ?: return
        val key = account() ?: return
        val stamp = ++generation
        val started = epochMillis()
        mutable.update { it.copy(loading = true, error = null, retry = input) }
        request = scope.launch {
            try {
                require(input.name.isEmpty() || noteResource.matches(input.name)) { "INVALID_ARGUMENT" }
                val response = active.api(HttpMethod.Post, if (input.start) "/walks/start" else "/walks/next", input.body())
                val result = withContext(Dispatchers.Default) { CoreJson.decodeFromString<WalkResult>(response) }
                val prior = if (input.start) emptyList() else input.path
                require(result.mode in setOf("random", "continue") && result.path.all(noteResource::matches) &&
                    result.path.distinct().size == result.path.size && result.path.take(prior.size) == prior &&
                    if (result.item != null) {
                        result.item.note.belongsTo(key) && result.item.note.state == "NORMAL" &&
                            result.path.lastOrNull() == result.item.note.name && result.path.size == prior.size + 1 &&
                            (!input.start || input.name.isEmpty() || input.name == result.item.note.name)
                    } else result.path.size == prior.size
                ) { "WALK_RESPONSE_INVALID" }
                if (stamp != generation || key != account() || active !== core()) return@launch
                if (result.item == null && !result.exhausted) {
                    privacy?.record("walk", "over_limit", started)
                    mutable.update { it.copy(loading = false, error = "WALK_RESOURCE_LIMIT",
                        result = it.result?.copy(warnings = result.warnings), retry = input) }
                } else {
                    privacy?.record("walk", started = started)
                    mutable.update { previous ->
                        val displayed = if (result.item == null && !input.start) result.copy(item = previous.result?.item) else result
                        previous.copy(result = displayed, previous = if (result.item == null) previous.previous
                            else if (input.start) null else previous.result?.item?.note, loading = false, retry = null)
                    }
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

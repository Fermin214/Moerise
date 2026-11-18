package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.domain.CoreException
import io.ktor.http.HttpMethod
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.serialization.json.*

class SettingsController(
    private val core: () -> CoreClient?,
    private val account: () -> String?,
    private val scope: CoroutineScope,
) {
    private val mutable = MutableStateFlow(SettingsUiState())
    val state = mutable.asStateFlow()
    private var visible = false
    private var generation = 0L
    private var owner: String? = null
    private var client: CoreClient? = null
    private var operation: Job? = null
    private var polling: Job? = null
    private val checkJobs = mutableMapOf<String, Job>()
    private var editRevision = 0L
    private val fieldRevisions = mutableMapOf<String, Long>()
    private val secretRevisions = mutableMapOf<String, Long>()
    private var timesRevision = 0L

    fun load() {
        val stamp = ++generation
        cancelWork()
        visible = true
        owner = account()
        client = core()
        resetRevisions()
        mutable.value = SettingsUiState(loading = true)
        val active = client
        val key = owner
        if (active == null || key == null || !key.startsWith("${active.origin}\u0000")) {
            mutable.value = SettingsUiState(error = "ACCOUNT_CHANGED")
            return
        }
        operation = scope.launch(start = CoroutineStart.LAZY) {
            try {
                val response = read(active.api(HttpMethod.Get, "/settings"))
                if (!owns(stamp, active, key)) return@launch
                val snapshot = SettingsForm.readSnapshot(response)
                mutable.value = SettingsUiState(snapshot = snapshot, values = SettingsForm.values(snapshot),
                    secrets = SettingsForm.secretNames.associateWith { SecretEdit() }, times = SettingsForm.times(snapshot))
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (owns(stamp, active, key)) mutable.update { it.copy(error = errorCode(error)) }
            } finally {
                if (owns(stamp, active, key)) {
                    operation = null
                    mutable.update { it.copy(loading = false) }
                    if (state.value.snapshot != null) poll(stamp, active, key)
                }
            }
        }
        operation?.start()
    }

    fun leave() {
        visible = false
        ++generation
        cancelWork()
        mutable.update { it.copy(loading = false, busy = false,
            secrets = it.secrets.mapValues { SecretEdit() }, checking = emptySet(),
            checks = emptyMap(), checkErrors = emptyMap(), status = null) }
    }

    fun clear() {
        leave()
        owner = null
        client = null
        resetRevisions()
        mutable.value = SettingsUiState()
    }

    fun change(path: String, value: String) {
        if (!editable() || path !in SettingsForm.paths) return
        val changes = linkedMapOf(path to value)
        when (path) {
            "embedding.provider" -> changes["embedding.protocol"] =
                if (value == "bailian") "dashscope" else "openai-compatible"
            "embedding.protocol" -> changes["embedding.provider"] =
                if (value == "dashscope") "bailian" else "openai-compatible"
        }
        val next = state.value.values + changes
        if (path.startsWith("embedding.")) {
            if (next["embedding.protocol"] == "openai-compatible") changes["embedding.query_instruction"] = ""
            else changes["embedding.send_dimensions"] = "false"
        }
        if (path == "channel.kind" && value == "none") changes["notifications.enabled"] = "false"
        val changed = changes.filter { (name, text) -> state.value.values[name] != text }
        if (changed.isEmpty()) return
        for (name in changed.keys) fieldRevisions[name] = ++editRevision
        mutable.update { it.copy(values = it.values + changed,
            error = if (it.conflict) "SETTINGS_CONFLICT" else null, status = null) }
        SettingsForm.modelGroups.filter { path.startsWith("$it.") }.forEach(::cancelCheck)
    }

    fun secret(name: String, action: String, value: String = "") {
        if (!editable() || name !in SettingsForm.secretNames) return
        if (action !in setOf("keep", "replace", "clear") || action != "replace" && value.isNotEmpty()) {
            mutable.update { it.copy(error = "SETTINGS_INPUT_INVALID", status = null) }
            return
        }
        val next = SecretEdit(action, value)
        if (state.value.secrets[name] == next) return
        secretRevisions[name] = ++editRevision
        mutable.update { it.copy(secrets = it.secrets + (name to next),
            error = if (it.conflict) "SETTINGS_CONFLICT" else null, status = null) }
        SettingsForm.modelGroups.firstOrNull { name == "${it}_api_key" }?.let(::cancelCheck)
    }

    fun addTime() {
        if (!editable() || state.value.times.size >= 24) return
        timesRevision = ++editRevision
        mutable.update { it.copy(times = it.times + "",
            error = if (it.conflict) "SETTINGS_CONFLICT" else null, status = null) }
    }

    fun removeTime(index: Int) {
        if (!editable() || state.value.times.size <= 1 || index !in state.value.times.indices) return
        timesRevision = ++editRevision
        mutable.update { it.copy(times = it.times.filterIndexed { position, _ -> position != index },
            error = if (it.conflict) "SETTINGS_CONFLICT" else null, status = null) }
    }

    fun changeTime(index: Int, value: String) {
        if (!editable() || index !in state.value.times.indices || state.value.times[index] == value) return
        timesRevision = ++editRevision
        mutable.update { it.copy(times = it.times.mapIndexed { position, text -> if (position == index) value else text },
            error = if (it.conflict) "SETTINGS_CONFLICT" else null, status = null) }
    }

    fun restorePrompt() {
        if (!editable()) return
        val prompt = (state.value.snapshot?.get("default_insight_prompt") as? JsonPrimitive)?.takeIf { it.isString } ?: return
        change("insights.default_prompt", prompt.content)
        mutable.update { it.copy(status = "SETTINGS_PROMPT_RESTORED") }
    }

    fun save() {
        if (!editable() || state.value.busy || state.value.conflict) return
        val active = client ?: return
        val key = owner ?: return
        val submitted = state.value
        val snapshot = submitted.snapshot ?: return
        val patch = try { SettingsForm.patch(snapshot, submitted) }
        catch (error: Exception) {
            mutable.update { it.copy(error = errorCode(error), status = null) }
            return
        }
        val secretOperations = patch.getValue("secrets").jsonObject
        if (patch.getValue("settings").jsonObject.isEmpty() &&
            secretOperations.values.all { it.jsonObject.getValue("action").jsonPrimitive.content == "keep" }) {
            mutable.update { it.copy(error = null, status = "SETTINGS_UNCHANGED") }
            return
        }
        val fieldsAtSubmit = fieldRevisions.toMap()
        val secretsAtSubmit = secretRevisions.toMap()
        val timesAtSubmit = timesRevision
        val stamp = ++generation
        cancelModels()
        mutable.update { it.copy(busy = true, error = null, status = null, checks = emptyMap(), checkErrors = emptyMap()) }
        operation = scope.launch(start = CoroutineStart.LAZY) {
            try {
                val response = read(active.api(HttpMethod.Patch, "/settings", patch.toString()))
                if (!owns(stamp, active, key)) return@launch
                val saved = SettingsForm.readSnapshot(response)
                require(SettingsForm.version(saved) > SettingsForm.version(snapshot)) { "SETTINGS_RESPONSE_INVALID" }
                val savedValues = SettingsForm.values(saved)
                // 只回填提交后未再编辑的字段，修订号也区分改回相同文字的操作。
                mutable.update { live ->
                    live.copy(snapshot = saved,
                        values = savedValues.mapValues { (path, value) ->
                            if (fieldRevisions[path] == fieldsAtSubmit[path]) value else live.values[path] ?: value
                        },
                        times = if (timesRevision == timesAtSubmit) SettingsForm.times(saved) else live.times,
                        secrets = live.secrets.mapValues { (name, edit) ->
                            if (name in secretOperations && secretRevisions[name] == secretsAtSubmit[name]) SecretEdit() else edit
                        },
                        models = null, conflict = false, error = null, status = "SETTINGS_SAVED")
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (owns(stamp, active, key)) {
                    if (error is CoreException && error.detail.code == "SETTINGS_CONFLICT") conflict()
                    else mutable.update { it.copy(error = errorCode(error)) }
                }
            } finally {
                if (owns(stamp, active, key)) {
                    operation = null
                    mutable.update { live ->
                        live.copy(busy = false, secrets = live.secrets.mapValues { (name, edit) ->
                            if (name in secretOperations && secretRevisions[name] == secretsAtSubmit[name]) SecretEdit() else edit
                        })
                    }
                    if (!state.value.conflict) poll(stamp, active, key)
                }
            }
        }
        operation?.start()
    }

    fun modelDirty(group: String): Boolean {
        if (group !in SettingsForm.modelGroups) return false
        val current = state.value
        val snapshot = current.snapshot ?: return false
        for (name in SettingsForm.groups.getValue(group)) {
            val path = "$group.$name"
            val text = current.values[path] ?: return true
            val original = SettingsForm.original(snapshot, path)
            if (text.isEmpty() && (original == null || original == JsonNull)) continue
            if (SettingsForm.same(JsonPrimitive(text), original)) continue
            val value = try { SettingsForm.value(path, text) } catch (_: IllegalArgumentException) { return true }
            if (!SettingsForm.same(value, original)) return true
        }
        return current.secrets["${group}_api_key"]?.action?.let { it != "keep" } ?: false
    }

    fun canCheckModel(group: String): Boolean {
        val current = state.value
        return visible && owner != null && owner == account() && client === core() &&
            group in SettingsForm.modelGroups && current.snapshot != null &&
            (current.snapshot["version"] as? JsonPrimitive)?.longOrNull?.let { it >= 1 } == true &&
            !current.loading && !current.busy && !current.conflict && group !in current.checking &&
            (SettingsForm.original(current.snapshot, "$group.enabled") as? JsonPrimitive)?.booleanOrNull == true &&
            !modelDirty(group)
    }

    fun checkModel(group: String) {
        if (!editable() || !canCheckModel(group)) return
        val active = client ?: return
        val key = owner ?: return
        val snapshot = state.value.snapshot ?: return
        val version = SettingsForm.version(snapshot)
        val stamp = generation
        mutable.update { it.copy(checking = it.checking + group, checks = it.checks - group, checkErrors = it.checkErrors - group) }
        val job = scope.launch(start = CoroutineStart.LAZY) {
            try {
                val result = withTimeout(60_000) {
                    read(active.api(HttpMethod.Post, "/models/$group/check", buildJsonObject { put("version", version) }.toString()))
                }
                if (!owns(stamp, active, key) || checkJobs[group] !== currentCoroutineContext()[Job]) return@launch
                if (!versionMatches(result, version)) { conflict(); return@launch }
                require((result["status"] as? JsonPrimitive)?.content == "ok" &&
                    (result["duration_ms"] as? JsonPrimitive)?.takeUnless { it.isString }
                        ?.longOrNull?.let { it >= 0 } == true) { "MODEL_CHECK_PROTOCOL" }
                mutable.update { it.copy(checks = it.checks + (group to result), checkErrors = it.checkErrors - group) }
            } catch (error: Exception) {
                if (error is CancellationException && error !is TimeoutCancellationException) throw error
                if (owns(stamp, active, key) && checkJobs[group] === currentCoroutineContext()[Job]) {
                    if (error is CoreException && error.detail.code == "SETTINGS_CONFLICT") conflict()
                    else mutable.update { it.copy(checkErrors = it.checkErrors +
                        (group to if (error is TimeoutCancellationException) "MODEL_CHECK_TIMEOUT" else errorCode(error))) }
                }
            } finally {
                if (owns(stamp, active, key) && checkJobs[group] === currentCoroutineContext()[Job]) {
                    checkJobs.remove(group)
                    mutable.update { it.copy(checking = it.checking - group) }
                }
            }
        }
        checkJobs[group] = job
        job.start()
    }

    private fun poll(stamp: Long, active: CoreClient, key: String) {
        polling?.cancel()
        polling = scope.launch(start = CoroutineStart.LAZY) {
            while (owns(stamp, active, key) && !state.value.conflict) {
                try {
                    val models = read(active.api(HttpMethod.Get, "/models"))
                    if (!owns(stamp, active, key)) return@launch
                    val version = state.value.snapshot?.let { SettingsForm.version(it, positive = false) } ?: return@launch
                    if (!versionMatches(models, version)) { conflict(); return@launch }
                    require(SettingsForm.modelGroups.all { models[it] is JsonObject }) { "MODEL_STATUS_PROTOCOL" }
                    mutable.update { it.copy(models = models, checkErrors = it.checkErrors - "models") }
                } catch (error: Exception) {
                    if (error is CancellationException) throw error
                    if (!owns(stamp, active, key)) return@launch
                    if (error is CoreException && error.detail.code == "SETTINGS_CONFLICT") { conflict(); return@launch }
                    mutable.update { it.copy(checkErrors = it.checkErrors + ("models" to errorCode(error))) }
                }
                delay(5000)
            }
        }
        polling?.start()
    }

    private fun conflict() {
        cancelModels()
        mutable.update { it.copy(conflict = true, error = "SETTINGS_CONFLICT", status = null,
            models = null, checks = emptyMap(), checkErrors = emptyMap()) }
    }

    private fun editable(): Boolean {
        if (!visible) return false
        if (owner != account() || client !== core()) { clear(); return false }
        return state.value.snapshot != null && !state.value.loading
    }

    private fun owns(stamp: Long, active: CoreClient, key: String): Boolean {
        if (!visible || stamp != generation) return false
        if (key != account() || active !== core()) { clear(); return false }
        return owner == key && client === active
    }

    private fun cancelCheck(group: String) {
        checkJobs.remove(group)?.cancel()
        mutable.update { it.copy(checking = it.checking - group, checks = it.checks - group, checkErrors = it.checkErrors - group) }
    }

    private fun cancelModels() {
        polling?.cancel()
        polling = null
        val pendingChecks = checkJobs.values.toList()
        checkJobs.clear()
        pendingChecks.forEach { it.cancel() }
        mutable.update { it.copy(checking = emptySet()) }
    }

    private fun cancelWork() {
        operation?.cancel()
        operation = null
        cancelModels()
    }

    private fun resetRevisions() {
        editRevision = 0
        fieldRevisions.clear()
        secretRevisions.clear()
        timesRevision = 0
    }

    private fun versionMatches(response: JsonObject, version: Long): Boolean =
        (response["version"] as? JsonPrimitive)?.let { !it.isString && it.longOrNull == version } == true

    private fun read(body: String): JsonObject = try {
        CoreJson.parseToJsonElement(body) as? JsonObject ?: throw IllegalArgumentException("SETTINGS_RESPONSE_INVALID")
    } catch (_: IllegalArgumentException) {
        throw IllegalArgumentException("SETTINGS_RESPONSE_INVALID")
    }
}

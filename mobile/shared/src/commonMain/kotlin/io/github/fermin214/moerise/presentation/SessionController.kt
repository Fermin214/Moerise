package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.IdentityChangedException
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update

data class SessionUiState(
    val loading: Boolean = true,
    val profile: ConnectionProfile? = null,
    val connected: Boolean = false,
    val needsLogin: Boolean = false,
    val busy: Boolean = false,
    val error: String? = null,
)

class SessionController(
    private val secrets: SecretStore,
    private val allowLoopbackHttp: Boolean,
    private val scope: CoroutineScope,
) : AutoCloseable {
    private val repository = ConnectionRepository(secrets)
    private val mutableState = MutableStateFlow(SessionUiState())
    val state = mutableState.asStateFlow()
    private val mutableLanguage = MutableStateFlow(UiLanguage.SYSTEM)
    val language = mutableLanguage.asStateFlow()
    private var generation = 0L
    private var operation: Job? = null
    var core: CoreClient? = null
        private set

    init {
        scope.launch {
            mutableLanguage.value = secrets.read("ui-language")?.let { saved ->
                UiLanguage.entries.firstOrNull { it.name == saved }
            } ?: UiLanguage.SYSTEM
        }
        operation = scope.launch { restore() }
    }

    fun language(value: UiLanguage) {
        mutableLanguage.value = value
        scope.launch { secrets.write("ui-language", value.name) }
    }

    private fun client(origin: String, stamp: Long): CoreClient =
        CoreClient(origin, SessionCookies(origin, secrets), onUnauthorized = {
            scope.launch { if (generation == stamp) mutableState.update { it.copy(connected = false, needsLogin = true) } }
        })

    private suspend fun restore() {
        val stamp = ++generation
        core?.close()
        core = null
        try {
            val profile = repository.selected()
            if (stamp != generation) return
            mutableState.value = SessionUiState(loading = false, profile = profile)
            if (profile == null) return
            val active = client(profile.origin, stamp).also { it.bindAccount(profile.accountKey); core = it }
            active.session()
            if (stamp == generation) mutableState.update { it.copy(connected = true, needsLogin = false) }
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            if (stamp == generation) mutableState.update { it.copy(loading = false, error = errorCode(error)) }
        }
    }

    fun login(originText: String, username: String, password: String) {
        if (state.value.busy) return
        val stamp = ++generation
        core?.close()
        core = null
        mutableState.update { it.copy(busy = true, error = null, connected = false) }
        operation?.cancel()
        operation = scope.launch {
            var active: CoreClient? = null
            try {
                val origin = normalizeOrigin(originText, allowLoopbackHttp)
                active = client(origin, stamp)
                val capabilities = active.capabilities()
                require("password" in capabilities.loginMethods && capabilities.notes == "ready") { "INCOMPATIBLE_CORE" }
                val identity = active.login(username, password)
                if (stamp != generation) return@launch
                val profile = repository.select(origin, identity)
                core = active
                active = null
                mutableState.value = SessionUiState(loading = false, profile = profile, connected = true)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == generation) mutableState.update { it.copy(busy = false, error = errorCode(error)) }
            } finally { active?.close() }
        }
    }

    fun retrySession() {
        if (state.value.busy) return
        operation?.cancel()
        operation = scope.launch { restore() }
    }

    fun logout() {
        val active = core
        val stamp = ++generation
        operation?.cancel()
        core = null
        mutableState.value = SessionUiState(loading = false, busy = true)
        scope.launch {
            try {
                val logout = active?.clearLocalSession()
                repository.clear()
                mutableState.value = SessionUiState(loading = false)
                if (logout != null) active.revokeSession(logout)
            }
            catch (error: Exception) {
                if (error is CancellationException) throw error
                runCatching { repository.recordLogoutFailure(errorCode(error)) }
                if (generation == stamp && state.value.busy) mutableState.value = SessionUiState(loading = false)
            }
            finally { active?.close() }
        }
    }

    override fun close() { ++generation; core?.close() }
}

fun errorCode(error: Exception): String = when (error) {
    is CoreException -> error.detail.code
    is IdentityChangedException -> "ACCOUNT_CHANGED"
    is IllegalArgumentException -> error.message ?: "INVALID_ARGUMENT"
    else -> "CONNECTION_FAILED"
}

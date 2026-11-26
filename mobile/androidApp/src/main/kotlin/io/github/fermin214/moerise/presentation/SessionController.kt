package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.IdentityChangedException
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

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
    private val onConnectionFailure: (Exception) -> Unit = {},
    private val removalRepository: (suspend () -> NoteRepository)? = null,
    private val clearProtectedDownloads: suspend () -> Unit = {},
    private val onNetworkRequest: () -> Unit = {},
) : AutoCloseable {
    private val repository = ConnectionRepository(secrets)
    private val mutableState = MutableStateFlow(SessionUiState())
    val state = mutableState.asStateFlow()
    private val mutableLanguage = MutableStateFlow(UiLanguage.SYSTEM)
    val language = mutableLanguage.asStateFlow()
    private val mutablePreferencesError = MutableStateFlow<String?>(null)
    val preferencesError = mutablePreferencesError.asStateFlow()
    private val languageLock = Mutex()
    private var languageRevision = 0L
    private var generation = 0L
    private var settingsRevision = 0L
    private var operation: Job? = null
    internal var onRequestFailure: (Long, Exception) -> Unit = { _, _ -> }
    var core: CoreClient? = null
        private set

    init {
        scope.launch {
            try {
                val saved = secrets.read("ui-language")?.let { value -> UiLanguage.entries.firstOrNull { it.name == value } } ?: UiLanguage.SYSTEM
                if (languageRevision == 0L) mutableLanguage.value = saved
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (languageRevision == 0L) mutablePreferencesError.value = "LOCAL_PREFERENCES_FAILED"
            }
        }
        operation = scope.launch { restore() }
    }

    fun language(value: UiLanguage) {
        val revision = ++languageRevision
        mutableLanguage.value = value
        scope.launch {
            try {
                languageLock.withLock {
                    if (revision != languageRevision) return@withLock
                    secrets.write("ui-language", value.name)
                    if (revision == languageRevision) mutablePreferencesError.value = null
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (revision == languageRevision) mutablePreferencesError.value = "LOCAL_PREFERENCES_FAILED"
            }
        }
    }

    private fun client(origin: String, stamp: Long): CoreClient =
        CoreClient(origin, SessionCookies(origin, secrets), onUnauthorized = {
            scope.launch { if (generation == stamp) mutableState.update { it.copy(connected = false, needsLogin = true) } }
        }, onRequestFailure = { started, error ->
            scope.launch { if (generation == stamp) onRequestFailure(started, error) }
        }, onRequest = onNetworkRequest)

    private suspend fun restore() {
        val stamp = ++generation
        val timeStamp = settingsRevision
        core?.close()
        core = null
        try {
            val profile = restoreSelection()
            if (stamp != generation) return
            mutableState.value = SessionUiState(loading = false, profile = profile)
            if (profile == null) return
            val active = client(profile.origin, stamp).also { it.bindAccount(profile.accountKey); core = it }
            val identity = withTimeout(CORE_REQUEST_TIMEOUT_MILLIS) { active.session() }
            if (stamp != generation) return
            val refreshed = repository.select(profile.origin, identity) { selected ->
                if (stamp != generation) throw CancellationException("Account changed")
                if (timeStamp == settingsRevision) selected else selected.copy(
                    timezone = state.value.profile?.timezone ?: selected.timezone,
                    stargazingEnabled = state.value.profile?.stargazingEnabled ?: selected.stargazingEnabled)
            }
            if (stamp == generation) mutableState.update { it.copy(profile = if (timeStamp == settingsRevision) refreshed
                else refreshed.copy(timezone = it.profile?.timezone ?: refreshed.timezone,
                    stargazingEnabled = it.profile?.stargazingEnabled ?: refreshed.stargazingEnabled),
                connected = true, needsLogin = false) }
        } catch (error: Exception) {
            if (error is CancellationException && error !is TimeoutCancellationException) throw error
            onConnectionFailure(error)
            if (stamp == generation) mutableState.update { it.copy(loading = false, error = errorCode(error)) }
        }
    }

    private suspend fun restoreSelection(): ConnectionProfile? {
        val profile = repository.selected() ?: return null
        val notes = removalRepository?.invoke() ?: return profile
        if (!notes.hasPendingLogout(profile.accountKey)) return profile
        try {
            clearProtectedDownloads()
            SessionCookies(profile.origin, secrets).clear()
            repository.clear()
            notes.completePendingLogout(profile.accountKey)
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            throw IllegalArgumentException("LOCAL_REMOVE_FAILED", error)
        }
        return null
    }

    fun login(originText: String, username: String, password: String) {
        if (state.value.busy) return
        val stamp = ++generation
        core?.close()
        core = null
        mutableState.update { it.copy(busy = true, error = null, connected = false) }
        val previous = operation
        previous?.cancel()
        operation = scope.launch {
            var active: CoreClient? = null
            try {
                previous?.join()
                if (restoreSelection() == null && state.value.profile != null)
                    mutableState.update { it.copy(profile = null, connected = false) }
                val origin = normalizeOrigin(originText, allowLoopbackHttp)
                active = client(origin, stamp)
                val capabilities = active.capabilities()
                require("password" in capabilities.loginMethods && capabilities.notes == "ready") { "INCOMPATIBLE_CORE" }
                val identity = active.login(username, password)
                if (stamp != generation) return@launch
                removalRepository?.invoke()?.completePendingLogout(identity.accountKey(origin))
                val profile = repository.select(origin, identity) {
                    if (stamp != generation) throw CancellationException("Account changed")
                    it
                }
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

    suspend fun checkConnection(): Boolean {
        if (state.value.busy) return false
        val stamp = generation
        val timeStamp = settingsRevision
        val profile = state.value.profile ?: return false
        val active = core ?: client(profile.origin, stamp).also {
            it.bindAccount(profile.accountKey)
            core = it
        }
        return try {
            val identity = withTimeout(CORE_REQUEST_TIMEOUT_MILLIS) { active.session() }
            if (stamp != generation) return false
            val refreshed = repository.select(profile.origin, identity) { selected ->
                if (stamp != generation) throw CancellationException("Account changed")
                if (timeStamp == settingsRevision) selected else selected.copy(
                    timezone = state.value.profile?.timezone ?: selected.timezone,
                    stargazingEnabled = state.value.profile?.stargazingEnabled ?: selected.stargazingEnabled)
            }
            if (stamp != generation) return false
            mutableState.update { it.copy(profile = if (timeStamp == settingsRevision) refreshed
                else refreshed.copy(timezone = it.profile?.timezone ?: refreshed.timezone,
                    stargazingEnabled = it.profile?.stargazingEnabled ?: refreshed.stargazingEnabled),
                connected = true, needsLogin = false, error = null) }
            true
        } catch (error: Exception) {
            if (error is CancellationException && error !is TimeoutCancellationException) throw error
            onConnectionFailure(error)
            if (stamp == generation) mutableState.update {
                it.copy(connected = false,
                    needsLogin = it.needsLogin || error is IdentityChangedException ||
                        error is CoreException && error.status == 401,
                    error = errorCode(error))
            }
            false
        }
    }

    fun acceptSettings(accountKey: String, timezone: String, stargazingEnabled: Boolean) {
        if (state.value.profile?.accountKey != accountKey) return
        serviceMonth(timezone)
        ++settingsRevision
        mutableState.update { if (it.profile?.accountKey == accountKey)
            it.copy(profile = it.profile.copy(timezone = timezone, stargazingEnabled = stargazingEnabled)) else it }
        scope.launch {
            try {
                repository.updateSettings(accountKey) {
                    state.value.profile?.takeIf { it.accountKey == accountKey }
                }
            }
            catch (error: Exception) {
                if (error is CancellationException) throw error
                if (state.value.profile?.accountKey == accountKey) mutablePreferencesError.value = "LOCAL_PREFERENCES_FAILED"
            }
        }
    }

    fun connectionFailed(account: String) {
        if (account == state.value.profile?.accountKey) mutableState.update { it.copy(connected = false) }
    }

    suspend fun stopForRemoval() {
        ++generation
        mutableState.update { it.copy(connected = false, busy = true) }
        operation?.cancelAndJoin()
        operation = null
    }

    fun recoverAfterRemovalFailure() {
        mutableState.update { it.copy(busy = false) }
        retrySession()
    }

    suspend fun logout() {
        stopForRemoval()
        finishLogout()
    }

    internal suspend fun logoutAfterRemoval(account: String, notes: NoteRepository, clearDownloads: suspend () -> Unit) {
        try {
            clearDownloads()
            finishLogout()
            notes.completePendingLogout(account)
        } catch (error: Exception) {
            core?.close()
            core = null
            mutableState.value = SessionUiState(loading = false)
            reportRemovalFailure()
            throw error
        }
    }

    internal fun reportRemovalFailure() {
        mutableState.update { if (it.profile == null) it.copy(error = "LOCAL_REMOVE_FAILED") else it }
    }

    private suspend fun finishLogout() {
        val active = core
        val logout = active?.clearLocalSession()
        if (active == null) state.value.profile?.let { profile ->
            SessionCookies(profile.origin, secrets).clear()
        }
        repository.clear()
        core = null
        mutableState.value = SessionUiState(loading = false)
        scope.launch {
            try { if (logout != null) active?.revokeSession(logout) }
            catch (error: Exception) {
                if (error is CancellationException) throw error
                runCatching { repository.recordLogoutFailure(errorCode(error)) }
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
    else -> { logClientFailure("client", error); "CONNECTION_FAILED" }
}

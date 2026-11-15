package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.domain.SessionIdentity
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.Serializable

@Serializable
data class ConnectionProfile(
    val origin: String,
    val accountKey: String,
    val instanceId: String,
    val userName: String,
    val displayName: String,
    val timezone: String,
    val stargazingEnabled: Boolean = true,
)

class ConnectionRepository(private val secrets: SecretStore) {
    private val lock = Mutex()
    suspend fun selected(): ConnectionProfile? = lock.withLock {
        secrets.read("selected-profile")?.let { CoreJson.decodeFromString<ConnectionProfile>(it) }
    }
    suspend fun select(
        origin: String,
        identity: SessionIdentity,
        accept: (ConnectionProfile) -> ConnectionProfile = { it },
    ): ConnectionProfile = lock.withLock {
        val profile = accept(ConnectionProfile(origin, identity.accountKey(origin), identity.instanceId,
            identity.user.name, identity.user.displayName, identity.timezone, identity.stargazingEnabled))
        secrets.write("selected-profile", CoreJson.encodeToString(profile))
        profile
    }
    suspend fun clear() = lock.withLock { secrets.write("selected-profile", null) }
    suspend fun updateSettings(accountKey: String, profile: () -> ConnectionProfile?): ConnectionProfile? = lock.withLock {
        val selected = secrets.read("selected-profile")?.let { CoreJson.decodeFromString<ConnectionProfile>(it) }
            ?.takeIf { it.accountKey == accountKey } ?: return@withLock null
        val value = profile()?.takeIf { it.accountKey == accountKey } ?: return@withLock null
        selected.copy(timezone = value.timezone, stargazingEnabled = value.stargazingEnabled)
            .also { secrets.write("selected-profile", CoreJson.encodeToString(it)) }
    }
    suspend fun recordLogoutFailure(code: String) { secrets.write("logout-error", "${epochMillis()}:$code") }
}

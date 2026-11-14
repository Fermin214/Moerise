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
)

class ConnectionRepository(private val secrets: SecretStore) {
    private val lock = Mutex()
    suspend fun selected(): ConnectionProfile? = lock.withLock {
        secrets.read("selected-profile")?.let { CoreJson.decodeFromString<ConnectionProfile>(it) }
    }
    suspend fun select(origin: String, identity: SessionIdentity): ConnectionProfile = lock.withLock {
        val profile = ConnectionProfile(origin, identity.accountKey(origin), identity.instanceId, identity.user.name, identity.user.displayName, identity.timezone)
        secrets.write("selected-profile", CoreJson.encodeToString(profile))
        profile
    }
    suspend fun clear() = lock.withLock { secrets.write("selected-profile", null) }
    suspend fun recordLogoutFailure(code: String) { secrets.write("logout-error", "${epochMillis()}:$code") }
}

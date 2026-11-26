package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.ApiError
import io.github.fermin214.moerise.domain.CoreException
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.first
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.mockwebserver.SocketPolicy
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class PrivacyControllerTest {
    private data class Attempt(val at: Long?, val frozen: String?, val batch: UsageBatch)

    @Test
    fun withdrawalStopsCollectionAndSurvivesAFailedPreferenceCommit() = runBlocking<Unit> {
        val backing = MemoryInstallPreferences()
        val preferences = object : InstallPreferences by backing {
            var fail = true
            var marker = false
            override fun consentWithdrawn() = marker
            override fun markConsentWithdrawn(withdrawn: Boolean) { marker = withdrawn }
            override fun persist(vararg entries: Pair<String, String?>) {
                if (fail && ("choice-v1" to "off") in entries) throw java.io.IOException("disk unavailable")
                backing.writeBatch(*entries)
            }
        }
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val session = SessionController(MemorySecrets(), true, scope)
        try {
            preferences.write("choice-v1", "on")
            val window = UsageWindow(epochMillis(),
                UsageBatch(clientVersion = "0.3.0", coreVersion = "0.3.0", systemMajor = "29",
                    rows = listOf(UsageRow("sync", "success", "unknown", "user", 1))))
            preferences.write("inflight-batch", CoreJson.encodeToString(listOf(window)))
            val privacy = PrivacyController(preferences, session, scope)
            assertTrue(privacy.state.value.enabled)
            privacy.choose(false)
            assertTrue(preferences.marker)
            assertFalse(privacy.state.value.enabled)
            assertTrue(privacy.state.value.choiceSaveFailed)
            assertEquals("on", preferences.read("choice-v1"))
            assertNotNull(preferences.read("inflight-batch"))
            privacy.record("sync")
            assertNull(preferences.read("aggregate"))

            val reopened = PrivacyController(preferences, session, scope)
            assertFalse(reopened.state.value.enabled)
            assertTrue(reopened.state.value.choiceSaveFailed)
            preferences.fail = false
            reopened.retryChoice()
            assertFalse(reopened.state.value.choiceSaveFailed)
            assertEquals("off", preferences.read("choice-v1"))
            assertNull(preferences.read("inflight-batch"))
        } finally { session.close(); scope.coroutineContext[Job]!!.cancelAndJoin() }
    }

    @Test
    fun startupAndForegroundExpireWindowsWithoutAnOnlineCore() = runBlocking<Unit> {
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val session = SessionController(MemorySecrets(), true, scope)
        val preferences = MemoryInstallPreferences()
        val day = 86_400_000L
        try {
            val old = UsageWindow(epochMillis() - 2 * day,
                UsageBatch(clientVersion = "0.2.0", coreVersion = "0.2.0", systemMajor = "28",
                    rows = listOf(UsageRow("sync", "success", "unknown", "user", 1))))
            val recent = old.copy(start = epochMillis() - day)
            preferences.write("aggregate", CoreJson.encodeToString(old))
            preferences.write("inflight-batch", CoreJson.encodeToString(listOf(old, recent)))
            val privacy = PrivacyController(preferences, session, scope)
            assertNull(preferences.read("aggregate"))
            assertEquals(listOf(recent), CoreJson.decodeFromString<List<UsageWindow>>(assertNotNull(preferences.read("inflight-batch"))))
            preferences.write("inflight-batch", CoreJson.encodeToString(listOf(old)))
            privacy.foreground(true)
            assertNull(preferences.read("inflight-batch"))
            assertFalse(session.state.value.connected)
        } finally { session.close(); scope.coroutineContext[Job]!!.cancelAndJoin() }
    }

    @Test
    fun timedOutPostIsDurableAndNewEventsCannotBypassTheLimitEvenAfterRestart() = runBlocking<Unit> {
        val preferences = MemoryInstallPreferences()
        val requests = Channel<Attempt>(Channel.UNLIMITED)
        val timeout = AtomicBoolean(true)
        val server = MockWebServer()
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when (request.path) {
                "/api/app/v1/session" -> MockResponse().setBody(
                    """{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}""",
                )
                "/api/app/v1/capabilities" -> MockResponse().setBody(
                    """{"login_methods":[],"notes":"ready","version":"0.3.0","telemetry":{"schema":1,"allowed":true}}""",
                )
                "/api/app/v1/telemetry" -> {
                    requests.trySend(Attempt(preferences.read("last-attempt")?.toLongOrNull(), preferences.read("inflight-batch"),
                        CoreJson.decodeFromString(request.body.readUtf8())))
                    if (timeout.get()) MockResponse().setSocketPolicy(SocketPolicy.NO_RESPONSE)
                    else MockResponse().setResponseCode(204)
                }
                else -> MockResponse().setResponseCode(404)
            }
        }
        server.start()
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val secrets = MemorySecrets()
        val origin = server.url("/").toString().removeSuffix("/")
        val key = "$origin\u0000one\u0000users/owner"
        val profile = ConnectionProfile(origin, key, "one", "users/owner", "", "UTC")
        val day = 86_400_000L
        var running: CoroutineScope? = null
        var connection: SessionController? = null
        suspend fun stop() {
            withContext(dispatcher) { connection?.close() }
            running?.coroutineContext?.get(Job)?.cancelAndJoin()
        }
        suspend fun start(): PrivacyController {
            val scope = CoroutineScope(SupervisorJob() + dispatcher)
            running = scope
            val privacy = withContext(dispatcher) {
                val session = SessionController(secrets, true, scope)
                connection = session
                PrivacyController(preferences, session, scope).also { it.foreground(true); it.editorDisplayed(null) }
            }
            withTimeout(10_000) { privacy.state.first { it.allowed == true } }
            return privacy
        }
        suspend fun expectNoPost() { assertNull(withTimeoutOrNull(900) { requests.receive() }) }
        try {
            secrets.write("selected-profile", CoreJson.encodeToString(profile))
            var privacy = start()
            withContext(dispatcher) { privacy.choose(true); privacy.record("sync") }
            val initial = assertNotNull(preferences.read("last-attempt")?.toLongOrNull())
            assertTrue(epochMillis() - initial in 0 until day)
            expectNoPost()

            withContext(dispatcher) {
                preferences.write("last-attempt", (initial - day).toString())
                privacy.record("edit")
            }
            val first = withTimeout(10_000) { requests.receive() }
            val attempted = assertNotNull(first.at)
            assertTrue(attempted >= initial)
            assertNotNull(first.frozen)
            assertEquals(listOf("sync", "edit"), first.batch.rows.map { it.operation })
            withContext(dispatcher) { privacy.record("create") }
            delay(5_100)
            withContext(dispatcher) { privacy.record("review") }
            expectNoPost()
            assertEquals(attempted.toString(), preferences.read("last-attempt"))
            assertEquals(first.frozen, preferences.read("inflight-batch"))

            stop()
            privacy = start()
            withContext(dispatcher) { privacy.record("walk") }
            expectNoPost()
            assertEquals(attempted.toString(), preferences.read("last-attempt"))
            assertEquals(first.frozen, preferences.read("inflight-batch"))

            timeout.set(false)
            withContext(dispatcher) {
                preferences.write("last-attempt", (epochMillis() - day).toString())
                privacy.record("stars")
            }
            val retry = withTimeout(10_000) { requests.receive() }
            assertEquals(listOf("sync", "edit", "create", "review", "walk", "stars"), retry.batch.rows.map { it.operation })
            withTimeout(5_000) { while (preferences.read("inflight-batch") != null) delay(10) }
            withContext(dispatcher) {
                privacy.record("sync")
                privacy.beginWrite("local", epochMillis())
                privacy.sendingWrite("local")
                privacy.choose(false)
            }
            for (name in listOf("aggregate", "inflight-batch", "pending", "in-flight", "last-attempt")) {
                assertNull(preferences.read(name), name)
            }
            assertFalse(privacy.state.value.enabled)
        } finally {
            stop()
            dispatcher.close()
            server.shutdown()
        }
    }

    @Test
    fun diagnosticSummaryIsAvailableWithoutErrorsAndOmitsRawErrorMessages() = runBlocking<Unit> {
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val session = SessionController(MemorySecrets(), true, scope)
        try {
            val controller = PrivacyController(MemoryInstallPreferences(), session, scope)
            val strings = Strings(UiLanguage.ENGLISH)
            val summary = controller.diagnosticSummary(strings)
            assertTrue(summary.contains("Moerise 0.3.0"))
            assertTrue(summary.contains("Core unknown"))
            assertTrue(summary.contains("Android SDK 29"))
            assertTrue(summary.contains("Not connected"))
            assertTrue(summary.contains("No recent errors"))
            controller.record("sync", "failure", error = CoreException(500, ApiError("CONNECTION_FAILED", "private note")))
            val diagnostic = controller.diagnosticSummary(strings)
            assertTrue(diagnostic.contains("code=CONNECTION_FAILED"))
            assertFalse(diagnostic.contains("private note"))
            assertFalse(diagnostic.contains("No recent errors"))
        } finally { session.close(); scope.coroutineContext[Job]!!.cancelAndJoin() }
    }
}

package io.github.fermin214.moerise.presentation

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.MemorySecrets
import io.github.fermin214.moerise.data.SessionCookies
import io.ktor.client.engine.mock.*
import io.ktor.http.*
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import kotlinx.serialization.json.*

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class SettingsControllerTest {
    private val identity = """{"instance_id":"one","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2027-01-01T00:00:00Z","csrf_token":"token"}"""
    private val key = "https://notes.example\u0000one\u0000users/owner"

    private fun settingsPayload(): String = buildJsonObject {
        put("version", 2)
        put("settings", buildJsonObject {
            put("timezone", "Asia/Shanghai")
            put("review", buildJsonObject {
                put("count", 5); put("min_age_hours", 12); put("recent_days", 14); put("include_archived", false)
            })
            put("notifications", buildJsonObject {
                put("enabled", true)
                put("times", buildJsonArray { add("09:00") })
            })
            put("stargazing", buildJsonObject {
                put("enabled", true)
            })
            put("embedding", buildJsonObject {
                put("enabled", false); put("provider", "openai-compatible"); put("protocol", "openai-compatible")
                put("base_url", ""); put("model", ""); put("dimensions", 1536); put("send_dimensions", false)
                put("batch_size", 8); put("timeout_seconds", 30); put("max_concurrency", 2); put("max_retries", 1)
                put("query_instruction", "")
            })
            put("channel", buildJsonObject {
                put("kind", "none"); put("app_id", ""); put("user_open_id", ""); put("destination", "")
                put("qq_app_id", ""); put("feishu_mode", "websocket"); put("webhook_images", false); put("webhook_app_id", "")
            })
        })
        put("channel_status", buildJsonObject {})
        put("secrets", buildJsonObject {})
        put("channel_capabilities", buildJsonObject {})
    }.toString()

    @Test
    fun settingsFormProducesMinimalStargazingPatch() {
        val raw = CoreJson.parseToJsonElement(settingsPayload()).jsonObject
        val snapshot = SettingsForm.readSnapshot(raw)
        val values = SettingsForm.values(snapshot)
        assertEquals("true", values["stargazing.enabled"])
        val state = SettingsUiState(
            snapshot = snapshot,
            values = values + ("stargazing.enabled" to "false"),
            times = SettingsForm.times(snapshot),
        )
        val patch = SettingsForm.patch(snapshot, state)
        assertEquals(
            false,
            patch.getValue("settings").jsonObject.getValue("stargazing").jsonObject.getValue("enabled").jsonPrimitive.boolean,
        )
    }

    @Test
    fun fourTabPollingTargetsRelevantEndpointsAndRebuildQueuesMap() = runBlocking<Unit> {
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val modelsCalls = AtomicInteger()
        val historyCalls = AtomicInteger()
        val rebuildCalls = AtomicInteger()
        val engine = MockEngine { request ->
            when {
                request.url.encodedPath.endsWith("/session") -> respond(identity)
                request.url.encodedPath.endsWith("/settings") -> respond(settingsPayload())
                request.url.encodedPath.endsWith("/models") -> {
                    modelsCalls.incrementAndGet()
                    respond("""{"version":2,"embedding":{"enabled":false},"index_status":"disabled"}""")
                }
                request.url.encodedPath.endsWith("/admin/notifications") -> {
                    historyCalls.incrementAndGet()
                    respond("""{"notifications":[]}""")
                }
                request.url.encodedPath.endsWith("/map/rebuild") && request.method == HttpMethod.Post -> {
                    rebuildCalls.incrementAndGet()
                    respond("""{"rebuild":{"state":"queued"}}""")
                }
                else -> respond("""{}""", HttpStatusCode.NotFound)
            }
        }
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), engine)
        try {
            val controller = SettingsController({ core }, { key }, scope)
            withContext(dispatcher) { controller.load() }
            withTimeout(10_000) { controller.state.first { !it.loading && it.models != null } }
            assertEquals(1, modelsCalls.get())
            assertEquals(0, historyCalls.get())

            withContext(dispatcher) { controller.selectTab(2) }
            withTimeout(10_000) { controller.state.first { it.deliveries != null } }
            assertEquals(1, historyCalls.get())

            val modelsBeforePrivacy = modelsCalls.get()
            val historyBeforePrivacy = historyCalls.get()
            withContext(dispatcher) { controller.selectTab(3) }
            delay(50)
            assertEquals(modelsBeforePrivacy, modelsCalls.get())
            assertEquals(historyBeforePrivacy, historyCalls.get())

            withContext(dispatcher) { controller.rebuildMap() }
            withTimeout(10_000) { controller.state.first { it.status == "SETTINGS_MAP_REBUILT" } }
            assertEquals(1, rebuildCalls.get())
        } finally {
            scope.cancel()
            core.close()
            dispatcher.close()
        }
    }
}

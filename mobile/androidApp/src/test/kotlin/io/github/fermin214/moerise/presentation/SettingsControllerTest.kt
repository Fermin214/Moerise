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
import io.ktor.http.content.OutgoingContent
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
    fun disablingCredentialsBuildsOneLegalPatchWithoutAProbe() {
        val snapshot = SettingsForm.readSnapshot(CoreJson.parseToJsonElement(settingsPayload()).jsonObject)
        val settings = snapshot.getValue("settings").jsonObject
        val configured = JsonObject(snapshot + ("settings" to JsonObject(settings + mapOf(
            "channel" to JsonObject(settings.getValue("channel").jsonObject + ("kind" to JsonPrimitive("telegram"))),
            "notifications" to JsonObject(settings.getValue("notifications").jsonObject + ("enabled" to JsonPrimitive(true))),
        ))))
        val clearing = buildJsonObject {
            put("settings", buildJsonObject {})
            put("secrets", buildJsonObject { put("bot_token", buildJsonObject { put("action", "clear") }) })
        }
        val patch = SettingsForm.linkedPatch(configured, clearing, "telegram")
        assertEquals("""{"notifications":{"enabled":false}}""", patch.getValue("settings").toString())
        assertNull(SettingsForm.candidate(configured, patch, "telegram"))
        val embedding = SettingsForm.linkedPatch(configured, buildJsonObject {
            put("settings", buildJsonObject {})
            put("secrets", buildJsonObject { put("embedding_api_key", buildJsonObject { put("action", "clear") }) })
        }, "embedding")
        assertEquals("""{"embedding":{"enabled":false}}""", embedding.getValue("settings").toString())
        assertNull(SettingsForm.candidate(configured, embedding, "embedding"))
    }

    @Test
    fun clearingFeishuCallbackTokenRequiresExplicitAppSecretClear() {
        val snapshot = SettingsForm.readSnapshot(CoreJson.parseToJsonElement(settingsPayload()).jsonObject)
        val settings = snapshot.getValue("settings").jsonObject
        val configured = JsonObject(snapshot + mapOf(
            "settings" to JsonObject(settings + ("channel" to JsonObject(settings.getValue("channel").jsonObject + mapOf(
                "kind" to JsonPrimitive("feishu-app"), "feishu_mode" to JsonPrimitive("callback"),
            )))),
            "secrets" to CoreJson.parseToJsonElement("""{"app_secret":{"configured":true}}"""),
        ))
        val patch = CoreJson.parseToJsonElement("""{"settings":{},"secrets":{
            "verification_token":{"action":"clear"},"app_secret":{"action":"replace","value":"replacement-secret"}
        }}""").jsonObject
        val rejected = assertFailsWith<IllegalArgumentException> { SettingsForm.linkedPatch(configured, patch, "feishu-app") }
        assertEquals("FEISHU_CALLBACK_CLEAR_REQUIRES_APP_SECRET", rejected.message)
        assertEquals("replacement-secret", patch.getValue("secrets").jsonObject.getValue("app_secret")
            .jsonObject.getValue("value").jsonPrimitive.content)
        val clear = CoreJson.parseToJsonElement("""{"action":"clear"}""")
        val confirmed = JsonObject(patch + ("secrets" to JsonObject(patch.getValue("secrets").jsonObject + ("app_secret" to clear))))
        val linked = SettingsForm.linkedPatch(configured, confirmed, "feishu-app")
        assertEquals("""{"notifications":{"enabled":false}}""", linked.getValue("settings").toString())
        assertEquals(confirmed["secrets"], linked["secrets"])
        assertNull(SettingsForm.candidate(configured, linked, "feishu-app"))
    }

    @Test
    fun atomicDisableUsesTheCommittedVersionEvenAfterLeavingSettings() = runBlocking<Unit> {
        val scope = CoroutineScope(coroutineContext + SupervisorJob())
        val first = CompletableDeferred<JsonObject>()
        val resume = CompletableDeferred<Unit>()
        val disabled = CompletableDeferred<JsonObject>()
        var snapshot = settingsPayload().replace("\"kind\":\"none\"", "\"kind\":\"telegram\"")
        val core = CoreClient("https://notes.example", SessionCookies("https://notes.example", MemorySecrets()), MockEngine { request ->
            when {
                request.url.encodedPath.endsWith("/session") -> respond(identity)
                request.method == HttpMethod.Get -> respond(snapshot)
                request.method == HttpMethod.Patch -> {
                    val patch = CoreJson.parseToJsonElement((request.body as OutgoingContent.ByteArrayContent).bytes().decodeToString()).jsonObject
                    val version = patch.getValue("version").jsonPrimitive.int
                    if (version == 2) {
                        first.complete(patch)
                        resume.await()
                        snapshot = snapshot.replace("\"version\":2", "\"version\":3")
                            .replace("\"stargazing\":{\"enabled\":true}", "\"stargazing\":{\"enabled\":false}")
                    } else {
                        snapshot = snapshot.replace("\"version\":3", "\"version\":4")
                            .replace("\"notifications\":{\"enabled\":true", "\"notifications\":{\"enabled\":false")
                        disabled.complete(patch)
                    }
                    respond(snapshot)
                }
                else -> error(request.url.encodedPath)
            }
        })
        try {
            val committed = CompletableDeferred<Unit>()
            val controller = SettingsController({ core }, { key }, scope, { _, after, _ ->
                if (SettingsForm.version(after) == 4L) committed.complete(Unit)
            })
            controller.load()
            withTimeout(5_000) { controller.state.first { it.snapshot != null && !it.loading } }
            controller.saveAtomic("stargazing.enabled", "false")
            withTimeout(5_000) { first.await() }
            controller.saveAtomic("notifications.enabled", "false")
            controller.leave()
            assertFalse(disabled.isCompleted)
            resume.complete(Unit)
            val patch = withTimeout(5_000) { disabled.await().also { committed.await() } }
            assertEquals("""{"version":3,"settings":{"notifications":{"enabled":false}},"secrets":{}}""", patch.toString())
            controller.load()
            val reopened = withTimeout(5_000) { controller.state.first { !it.loading && it.snapshot != null } }
            assertEquals("false", reopened.values["notifications.enabled"])
        } finally {
            scope.coroutineContext[Job]!!.cancelAndJoin()
            core.close()
        }
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
            withContext(dispatcher) { controller.selectTab(1); controller.load() }
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

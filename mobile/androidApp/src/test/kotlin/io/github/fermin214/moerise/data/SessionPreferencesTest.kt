package io.github.fermin214.moerise.data

import android.app.UiModeManager
import android.content.Context
import android.content.res.Configuration
import android.graphics.drawable.ColorDrawable
import androidx.compose.ui.graphics.toArgb
import androidx.core.view.WindowCompat
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RuntimeEnvironment
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

import io.github.fermin214.moerise.presentation.*
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first

@RunWith(RobolectricTestRunner::class)
@Config(manifest = Config.NONE, sdk = [29])
class SessionPreferencesTest {
    @Test
    @Config(sdk = [31], qualifiers = "notnight")
    fun applicationNightModeIsCalibratedFromSynchronousPreferences() {
        val context = RuntimeEnvironment.getApplication()
        val preferences = context.getSharedPreferences("ui-preferences", Context.MODE_PRIVATE)
        assertTrue(preferences.edit().putString("ui-theme", "DARK").commit())
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
        val store = AndroidUiThemeStore(context)
        val manager = shadowOf(context.getSystemService(UiModeManager::class.java))
        val session = SessionController(MemorySecrets(), false, scope, themeStore = store,
            onThemeChange = { applyApplicationNightMode(context, it) })
        try {
            assertTrue(session.state.value.loading)
            assertEquals(UiTheme.DARK, session.theme.value)
            assertTrue(session.theme.value.isDark(context.resources.configuration.isDarkMode()))
            applyApplicationNightMode(context, session.theme.value)
            assertEquals(UiModeManager.MODE_NIGHT_YES, manager.applicationNightMode)
            session.theme(UiTheme.LIGHT)
            assertEquals(UiModeManager.MODE_NIGHT_NO, manager.applicationNightMode)
            session.theme(UiTheme.SYSTEM)
            assertEquals(UiModeManager.MODE_NIGHT_AUTO, manager.applicationNightMode)
            SessionController(MemorySecrets(), false, scope, themeStore = AndroidUiThemeStore(context)).use { restart ->
                assertEquals(UiTheme.SYSTEM, restart.theme.value)
            }
        } finally {
            session.close()
            scope.cancel()
            preferences.edit().clear().commit()
        }
    }

    @Test
    @Config(sdk = [31], qualifiers = "notnight")
    fun failedThemeSaveKeepsThePersistedApplicationNightMode() {
        val context = RuntimeEnvironment.getApplication()
        val uiModeManager = context.getSystemService(UiModeManager::class.java)
        val manager = shadowOf(uiModeManager)
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
        val store = object : UiThemeStore {
            override fun read() = UiTheme.DARK
            override fun write(theme: UiTheme) { error("storage unavailable") }
        }
        try {
            uiModeManager.setNightMode(UiModeManager.MODE_NIGHT_NO)
            applyApplicationNightMode(context, store.read())
            val configuration = Configuration(context.resources.configuration).apply { uiMode = Configuration.UI_MODE_NIGHT_YES }
            SessionController(MemorySecrets(), false, scope, themeStore = store,
                onThemeChange = { applyApplicationNightMode(context, it) }).use { session ->
                session.theme(UiTheme.LIGHT)
                assertEquals(UiTheme.LIGHT, session.theme.value)
                assertEquals("LOCAL_PREFERENCES_FAILED", session.themeError.value)
                assertEquals(UiModeManager.MODE_NIGHT_YES, manager.applicationNightMode)
                session.theme(UiTheme.SYSTEM)
                assertEquals(UiTheme.SYSTEM, session.theme.value)
                assertTrue(configuration.isDarkMode())
                assertFalse(session.theme.value.resolveDark(context, configuration, useSystemSetting = session.themeError.value != null))
                assertEquals("LOCAL_PREFERENCES_FAILED", session.themeError.value)
                assertEquals(UiModeManager.MODE_NIGHT_YES, manager.applicationNightMode)
            }
            SessionController(MemorySecrets(), false, scope, themeStore = store).use {
                assertEquals(UiTheme.DARK, it.theme.value)
            }
        } finally { scope.cancel() }
    }

    @Test
    @Config(qualifiers = "notnight")
    fun legacyWindowUsesSavedPaletteBeforeCompositionAndKeepsThemeOnWriteFailure() {
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
        val host = Robolectric.buildActivity(android.app.Activity::class.java).setup()
        val store = object : UiThemeStore {
            override fun read() = UiTheme.DARK
            override fun write(theme: UiTheme) { error("storage unavailable") }
        }
        try {
            SessionController(MemorySecrets(), false, scope, themeStore = store).use { session ->
                val activity = host.get()
                val window = activity.window
                window.applyMoeriseTheme(session.theme.value.isDark(activity.resources.configuration.isDarkMode()))
                assertEquals(DarkMoerisePalette.Paper.toArgb(), (window.decorView.background as ColorDrawable).color)
                val controller = WindowCompat.getInsetsController(window, window.decorView)
                assertFalse(controller.isAppearanceLightStatusBars)
                assertFalse(controller.isAppearanceLightNavigationBars)
                session.theme(UiTheme.LIGHT)
                assertEquals(UiTheme.LIGHT, session.theme.value)
                assertEquals("LOCAL_PREFERENCES_FAILED", session.themeError.value)
                window.applyMoeriseTheme(session.theme.value.isDark(true))
                assertEquals(LightMoerisePalette.Paper.toArgb(), (window.decorView.background as ColorDrawable).color)
                assertTrue(controller.isAppearanceLightStatusBars)
                assertTrue(controller.isAppearanceLightNavigationBars)
            }
        } finally {
            scope.cancel()
            host.pause().stop().destroy()
        }
    }

    @Test
    fun lateInitialPreferenceCannotReplaceSelectionAndIoFailureDoesNotEscapeScope() = runBlocking<Unit> {
        val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val uncaught = mutableListOf<Throwable>()
        val scope = CoroutineScope(SupervisorJob() + dispatcher + CoroutineExceptionHandler { _, error -> uncaught += error })
        val arrived = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        val failWrite = AtomicBoolean(true)
        var saved: String? = null
        val secrets = object : SecretStore {
            override suspend fun read(key: String): String? {
                if (key != "ui-language") return null
                arrived.complete(Unit); release.await()
                return "CHINESE"
            }
            override suspend fun write(key: String, value: String?) {
                if (key == "ui-language" && failWrite.get()) throw java.io.IOException("storage unavailable")
                if (key == "ui-language") saved = value
            }
        }
        val session = SessionController(secrets, false, scope)
        try {
            withTimeout(10_000) { arrived.await() }
            withContext(dispatcher) { session.language(UiLanguage.ENGLISH) }
            withTimeout(10_000) { session.preferencesError.first { it != null } }
            release.complete(Unit)
            withContext(dispatcher) { yield() }
            assertEquals(UiLanguage.ENGLISH, session.language.value)
            assertEquals("LOCAL_PREFERENCES_FAILED", session.preferencesError.value)
            assertTrue(uncaught.isEmpty())
            failWrite.set(false)
            withContext(dispatcher) { session.language(UiLanguage.SYSTEM); session.language(UiLanguage.CHINESE) }
            withTimeout(10_000) { session.preferencesError.first { it == null } }
            assertEquals("CHINESE", saved)
            assertEquals(UiLanguage.CHINESE, session.language.value)
            assertTrue(uncaught.isEmpty())
        } finally { release.complete(Unit); session.close(); scope.coroutineContext[Job]!!.cancelAndJoin(); dispatcher.close() }
    }
}

package io.github.fermin214.moerise.data

import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
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

package io.github.fermin214.moerise

import android.os.Bundle
import android.app.Application
import android.os.Process
import android.os.SystemClock
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.tracing.Trace
import androidx.core.view.doOnPreDraw
import io.github.fermin214.moerise.presentation.*

class AppModel(application: Application) : AndroidViewModel(application) {
    private val runtime = (application as MoeriseApplication).runtime
    val session = runtime.session
    val mobile = runtime.mobile
}

class MainActivity : ComponentActivity() {
    private lateinit var model: AppModel
    private var homeReady = false
    private var foregroundTrace = false
    private var foregroundGeneration = 0
    private fun foregroundReady() {
        if (foregroundTrace) {
            Trace.endAsyncSection("note_foreground_ready", hashCode())
            foregroundTrace = false
        }
    }
    override fun onCreate(savedInstanceState: Bundle?) {
        Trace.beginAsyncSection("note_editor_ready", hashCode())
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        model = ViewModelProvider(this)[AppModel::class.java]
        window.applyMoeriseTheme(model.session.theme.value.isDark(resources.configuration.isDarkMode()))
        setContent {
            MoeriseApp(model.mobile) {
                if (!homeReady) {
                    homeReady = true
                    val started = if (firstActivity) System.currentTimeMillis() -
                        (SystemClock.elapsedRealtime() - Process.getStartElapsedRealtime()) else null
                    firstActivity = false
                    model.mobile.privacy.editorDisplayed(started)
                    Trace.endAsyncSection("note_editor_ready", hashCode())
                    reportFullyDrawn()
                }
                foregroundReady()
            }
        }
    }

    override fun onRestart() {
        super.onRestart()
        // HOT 启动从重新进入 Activity 开始，每次恢复对应一个首绘回执。
        beginForeground()
    }

    private fun beginForeground() {
        if (foregroundTrace) return
        foregroundTrace = true
        val generation = ++foregroundGeneration
        Trace.beginAsyncSection("note_foreground_ready", hashCode())
        window.decorView.doOnPreDraw {
            window.decorView.post {
                if (generation == foregroundGeneration && homeReady) foregroundReady()
            }
        }
    }

    override fun onStart() {
        super.onStart()
        beginForeground()
    }

    override fun onStop() {
        model.mobile.flush()
        ++foregroundGeneration
        foregroundReady()
        super.onStop()
    }

    override fun onDestroy() {
        if (!homeReady) Trace.endAsyncSection("note_editor_ready", hashCode())
        super.onDestroy()
    }

    private companion object { var firstActivity = true }
}

package io.github.fermin214.moerise

import android.os.Bundle
import android.app.Application
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
    private var editorReady = false
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
        setContent {
            MoeriseApp(model.mobile) {
                if (!editorReady) {
                    editorReady = true
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
                if (generation == foregroundGeneration && editorReady) foregroundReady()
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
        if (!editorReady) Trace.endAsyncSection("note_editor_ready", hashCode())
        super.onDestroy()
    }
}

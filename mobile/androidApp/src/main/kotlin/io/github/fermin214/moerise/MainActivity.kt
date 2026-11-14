package io.github.fermin214.moerise

import android.os.Bundle
import android.app.Application
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewModelScope
import androidx.tracing.Trace
import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.presentation.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.collect

class AppModel(application: Application) : AndroidViewModel(application) {
    private val database = viewModelScope.async(Dispatchers.IO) { androidDatabase(application) }
    val session = SessionController(AndroidSecretStore(application), BuildConfig.DEBUG, viewModelScope)
    private val repository = viewModelScope.async(Dispatchers.IO) { NoteRepository(database.await(), androidAttachmentFiles(application)) }
    val mobile = MobileController(session, { repository.await() }, viewModelScope) {
        clearAttachmentCache(application)
    }
    init {
        viewModelScope.launch {
            mobile.stars.metrics.collect { metric ->
                android.util.Log.i("MoeriseMap", "${metric.kind}=${metric.value} version=${metric.version}")
                Trace.setCounter("moerise_map_${metric.kind}", metric.value.toInt())
            }
        }
    }
    override fun onCleared() {
        session.close()
        if (database.isCompleted && !database.isCancelled) {
            runBlocking(Dispatchers.IO) { database.await().close() }
        }
    }
}

class MainActivity : ComponentActivity() {
    private lateinit var model: AppModel
    private var editorReady = false
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
            }
        }
    }

    override fun onStart() {
        super.onStart()
        model.mobile.foreground(true)
    }

    override fun onStop() {
        model.mobile.foreground(false)
        model.mobile.flush()
        super.onStop()
    }

    override fun onDestroy() {
        if (!editorReady) Trace.endAsyncSection("note_editor_ready", hashCode())
        super.onDestroy()
    }
}

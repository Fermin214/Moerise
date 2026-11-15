package io.github.fermin214.moerise

import android.app.Activity
import android.os.Bundle
import android.util.Log
import android.widget.TextView
import io.github.fermin214.moerise.data.NoteRepository
import io.github.fermin214.moerise.data.androidDatabase
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout

/** 在计时区间外检查主页实际写入的首屏缓存。 */
class PrepareBenchmarkActivity : Activity() {
    private var preparation: Job? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val marker = TextView(this)
        setContentView(marker)
        val runtime = (application as MoeriseApplication).runtime
        preparation = runtime.scope.launch {
            val timeline = runtime.mobile.timeline
            var retried = false
            try {
                withTimeout(70_000) {
                    timeline.state.first { state ->
                        if (!retried && !state.loading && (!state.fresh || state.error != null)) {
                            retried = true
                            timeline.refresh()
                        }
                        state.fresh && !state.loading && state.error == null && state.notes.size == 20
                    }
                    val key = checkNotNull(runtime.session.state.value.profile).accountKey
                    withContext(Dispatchers.IO) {
                        val database = androidDatabase(applicationContext)
                        try {
                            val cached = checkNotNull(NoteRepository(database).snapshot(key))
                            check(cached.notes.size == 20 && cached.nextPageToken.isNotBlank())
                            val long = cached.notes.single { it.content.startsWith("Long Markdown") }
                            check(long.content.encodeToByteArray().size == 65536 && long.attachments.size == 10)
                        } finally {
                            database.close()
                        }
                    }
                }
                marker.contentDescription = "benchmark_cache_ready_20_65536_10"
            } catch (error: Exception) {
                if (error is CancellationException && error !is TimeoutCancellationException) throw error
                val state = timeline.state.value
                val code = state.error?.let {
                    if (it.matches(Regex("[A-Z][A-Z0-9_]{0,79}"))) it else "UNKNOWN"
                } ?: "NONE"
                val reason = if (error is TimeoutCancellationException) "TIMEOUT" else error.javaClass.simpleName
                Log.e("MoeriseBenchmark",
                    "cache_prepare_failed reason=$reason fresh=${state.fresh} loading=${state.loading} " +
                        "notes=${state.notes.size} error=$code retried=$retried")
                marker.contentDescription = "benchmark_cache_failed"
            }
        }
    }

    override fun onDestroy() {
        preparation?.cancel()
        super.onDestroy()
    }
}

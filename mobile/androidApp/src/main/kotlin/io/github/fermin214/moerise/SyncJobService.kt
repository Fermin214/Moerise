package io.github.fermin214.moerise

import android.app.job.JobParameters
import android.app.job.JobService
import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.launch

class SyncJobService : JobService() {
    private val runtime: AndroidRuntime
        get() = (application as MoeriseApplication).runtime
    private var parameters: JobParameters? = null
    private var work: Job? = null

    override fun onStartJob(params: JobParameters): Boolean {
        parameters = params
        runtime.jobStarted(params)
        work = runtime.scope.launch(Dispatchers.Main) {
            val retry = try {
                runtime.synchronizeInBackground()
            } catch (error: Exception) {
                currentCoroutineContext().ensureActive()
                Log.w("MoeriseSync", "background_sync_failed=${error.javaClass.simpleName}")
                true
            }
            if (parameters === params) {
                parameters = null
                work = null
                jobFinished(params, retry)
                runtime.jobFinished(params)
            }
        }
        return true
    }

    override fun onStopJob(params: JobParameters): Boolean {
        val active = parameters ?: return false
        if (active.jobId != params.jobId) return false
        parameters = null
        work?.cancel()
        work = null
        runtime.jobFinished(active)
        return true
    }

    override fun onDestroy() {
        val active = parameters
        if (active != null) {
            parameters = null
            work?.cancel()
            work = null
            runtime.jobFinished(active)
        }
        super.onDestroy()
    }
}

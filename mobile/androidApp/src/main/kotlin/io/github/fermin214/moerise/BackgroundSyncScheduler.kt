package io.github.fermin214.moerise

import android.app.job.JobInfo
import android.app.job.JobScheduler
import android.content.ComponentName
import android.content.Context
import android.util.Log

internal class BackgroundSyncScheduler(context: Context) {
    private val scheduler = requireNotNull(context.getSystemService(JobScheduler::class.java))
    private val service = ComponentName(context, SyncJobService::class.java)
    private var running: Any? = null

    fun ensureScheduled(): Boolean {
        if (running != null) return true
        return try {
            if (scheduler.getPendingJob(JOB_ID) != null) return true
            val job = JobInfo.Builder(JOB_ID, service)
                .setRequiredNetworkType(JobInfo.NETWORK_TYPE_ANY)
                .setPersisted(true)
                .setBackoffCriteria(30_000L, JobInfo.BACKOFF_POLICY_EXPONENTIAL)
                .build()
            val accepted = scheduler.schedule(job) == JobScheduler.RESULT_SUCCESS
            if (!accepted) {
                Log.w("MoeriseSync", "background_schedule_failed")
            }
            accepted
        } catch (error: RuntimeException) {
            Log.w("MoeriseSync", "background_schedule_failed=${error.javaClass.simpleName}")
            false
        }
    }

    fun cancelIfIdle() {
        if (running == null) scheduler.cancel(JOB_ID)
    }

    fun started(token: Any) { running = token }
    fun finished(token: Any) {
        if (running === token) running = null
    }

    private companion object {
        const val JOB_ID = 0x4D4F4552
    }
}

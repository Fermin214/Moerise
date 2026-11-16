package io.github.fermin214.moerise

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.os.Handler
import android.os.Looper
import android.util.Log
import androidx.tracing.Trace
import io.github.fermin214.moerise.data.AndroidSecretStore
import io.github.fermin214.moerise.data.NoteRepository
import io.github.fermin214.moerise.data.androidDatabase
import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.presentation.MobileController
import io.github.fermin214.moerise.presentation.SessionController
import io.github.fermin214.moerise.presentation.androidAttachmentFiles
import io.github.fermin214.moerise.presentation.clearAttachmentCache
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineExceptionHandler
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import java.util.concurrent.atomic.AtomicInteger

class AndroidRuntime(application: MoeriseApplication) {
    internal val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate +
        CoroutineExceptionHandler { _, error ->
            Log.e("MoeriseSync", "background_operation_failed=${error.javaClass.simpleName}")
        })
    private val scheduler = BackgroundSyncScheduler(application)
    private val mainHandler = Handler(Looper.getMainLooper())
    private val queueChecks = Channel<Unit>(Channel.CONFLATED)
    private var scheduleRetry: Job? = null
    private var scheduleRetryDelay = 30_000L
    private val requests = AtomicInteger()
    private val database = scope.async(Dispatchers.IO) { androidDatabase(application) }
    private val repository = scope.async(Dispatchers.IO) {
        NoteRepository(database.await(), androidAttachmentFiles(application))
    }
    val session = SessionController(AndroidSecretStore(application), BuildConfig.ALLOW_LOOPBACK_HTTP, scope,
        onConnectionFailure = { error ->
            val chain = generateSequence<Throwable>(error) { it.cause }.take(5)
                .joinToString("->") { it.javaClass.simpleName }
            val http = (error as? CoreException)?.status
            Log.w("MoeriseSync", "connection_failed=$chain http_status=$http")
        }) {
        val count = requests.incrementAndGet()
        Trace.setCounter("moerise_network_requests", count)
        Log.i("MoeriseStartup", "network_requests=$count")
    }
    val mobile = MobileController(
        session = session,
        repository = { repository.await() },
        scope = scope,
        clearFiles = { clearAttachmentCache(application) },
        onQueueChanged = {
            ensureJobScheduled()
            queueChecks.trySend(Unit)
        },
        initialForeground = false,
    )
    private var inForeground = false

    init {
        scope.launch {
            mobile.stars.metrics.collect { metric ->
                Log.i("MoeriseMap", "${metric.kind}=${metric.value} version=${metric.version}")
                Trace.setCounter("moerise_map_${metric.kind}", metric.value.toInt())
            }
        }
        scope.launch {
            combine(session.state, mobile.sync.state, mobile.clearing) { connection, sync, clearing ->
                Triple(connection, sync, clearing)
            }.distinctUntilChanged().collect { queueChecks.trySend(Unit) }
        }
        scope.launch {
            for (ignored in queueChecks) reconcileJob()
        }
        observeNetwork(application)
    }

    fun foreground(value: Boolean) {
        inForeground = value
        mobile.foreground(value)
        queueChecks.trySend(Unit)
        if (value) mobile.sync.request()
    }

    private suspend fun reconcileJob() {
        val connection = session.state.value
        if (connection.loading || connection.busy || mobile.clearing.value) return
        val key = connection.profile?.accountKey
        if (key == null || connection.needsLogin) {
            cancelJobIfIdle()
            return
        }
        try {
            val pending = repository.await().automaticWritesPending(key)
            val latest = session.state.value
            if (latest.profile?.accountKey != key || latest.loading || latest.busy || mobile.clearing.value) return
            if (!latest.needsLogin && pending) ensureJobScheduled()
            else cancelJobIfIdle()
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            Log.w("MoeriseSync", "queue_check_failed=${error.javaClass.simpleName}")
        }
    }

    private fun ensureJobScheduled() {
        if (scheduleRetry?.isActive == true) return
        if (scheduler.ensureScheduled()) {
            scheduleRetry?.cancel()
            scheduleRetry = null
            scheduleRetryDelay = 30_000L
        } else {
            val wait = scheduleRetryDelay
            scheduleRetryDelay = (wait * 2).coerceAtMost(300_000L)
            scheduleRetry = scope.launch {
                delay(wait)
                scheduleRetry = null
                queueChecks.trySend(Unit)
            }
        }
    }

    private fun cancelJobIfIdle() {
        scheduleRetry?.cancel()
        scheduleRetry = null
        scheduleRetryDelay = 30_000L
        scheduler.cancelIfIdle()
    }

    internal fun jobStarted(token: Any) { scheduler.started(token) }

    internal fun jobFinished(token: Any) {
        // 等系统主线程队列处理完成回执后，再重新检查队列，避免漏掉任务结束时的新保存。
        mainHandler.post {
            scheduler.finished(token)
            queueChecks.trySend(Unit)
        }
    }

    internal suspend fun synchronizeInBackground(): Boolean = withTimeoutOrNull(120_000L) sync@{
        val ready = withTimeoutOrNull(30_000L) {
            combine(session.state, mobile.editor.state, mobile.clearing) { connection, editor, clearing ->
                val key = connection.profile?.accountKey
                !connection.loading && !connection.busy && !clearing &&
                    (key == null || connection.needsLogin || editor.accountKey == key && editor.draft != null)
            }.first { it }
        }
        if (ready == null) return@sync true
        val connection = session.state.value
        val key = connection.profile?.accountKey ?: return@sync false
        if (connection.needsLogin) return@sync false
        val repo = repository.await()
        if (!repo.automaticWritesPending(key)) return@sync false
        mobile.sync.synchronizeOnce()
        val latest = session.state.value
        val activeKey = latest.profile?.accountKey
        activeKey != null && !latest.needsLogin && repo.automaticWritesPending(activeKey)
    } ?: true

    private fun observeNetwork(context: Context) {
        val connectivity = requireNotNull(context.getSystemService(ConnectivityManager::class.java))
        val callback = object : ConnectivityManager.NetworkCallback() {
            private var current: Network? = null
            private var validated = false

            override fun onAvailable(network: Network) {
                current = network
                validated = false
                networkChanged()
            }

            override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) {
                if (network != current) return
                val ready = capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
                if (ready && !validated) networkChanged()
                validated = ready
            }

            override fun onLost(network: Network) {
                if (network != current) return
                current = null
                validated = false
                networkChanged()
            }
        }
        try {
            connectivity.registerDefaultNetworkCallback(callback)
        } catch (error: Exception) {
            Log.w("MoeriseSync", "network_callback_failed=${error.javaClass.simpleName}")
        }
    }

    private fun networkChanged() {
        scope.launch {
            queueChecks.trySend(Unit)
            if (inForeground) mobile.sync.request()
        }
    }
}

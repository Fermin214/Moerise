package io.github.fermin214.moerise.data

import android.util.Log
import io.ktor.client.engine.HttpClientEngine
import io.ktor.client.engine.okhttp.OkHttp
import io.ktor.client.engine.okhttp.OkHttpEngine
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import java.util.concurrent.TimeUnit

fun epochMillis(): Long = System.currentTimeMillis()

fun platformHttpEngine(): HttpClientEngine = OkHttp.create {
    preconfigured = OkHttpClient()
    config {
        followRedirects(false)
        followSslRedirects(false)
        retryOnConnectionFailure(false)
        callTimeout(0, TimeUnit.MILLISECONDS)
        connectTimeout(10, TimeUnit.SECONDS)
        readTimeout(135, TimeUnit.SECONDS)
        writeTimeout(135, TimeUnit.SECONDS)
    }
}

internal suspend fun discardIdleHttpConnections(engine: HttpClientEngine) {
    withContext(Dispatchers.IO) {
        (engine as? OkHttpEngine)?.config?.preconfigured?.connectionPool?.evictAll()
    }
}

internal fun logClientFailure(operation: String, error: Exception) {
    val chain = generateSequence<Throwable>(error) { it.cause }.take(5).joinToString("->") { it.javaClass.simpleName }
    Log.w("MoeriseClient", "operation=$operation failure=$chain")
}

fun newLocalId(): String = java.util.UUID.randomUUID().toString()

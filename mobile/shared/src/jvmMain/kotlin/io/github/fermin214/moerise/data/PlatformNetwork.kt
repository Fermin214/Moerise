package io.github.fermin214.moerise.data

import io.ktor.client.engine.HttpClientEngine
import io.ktor.client.engine.okhttp.OkHttp
import java.util.concurrent.TimeUnit

actual fun epochMillis(): Long = System.currentTimeMillis()

actual fun platformHttpEngine(): HttpClientEngine = OkHttp.create {
    config {
        followRedirects(false)
        followSslRedirects(false)
        retryOnConnectionFailure(false)
        callTimeout(0, TimeUnit.MILLISECONDS)
        connectTimeout(10, TimeUnit.SECONDS)
        readTimeout(65, TimeUnit.SECONDS)
        writeTimeout(65, TimeUnit.SECONDS)
    }
}

actual fun newLocalId(): String = java.util.UUID.randomUUID().toString()

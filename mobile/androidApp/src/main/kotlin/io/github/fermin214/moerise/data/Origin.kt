package io.github.fermin214.moerise.data

import io.ktor.http.URLProtocol
import io.ktor.http.Url

fun normalizeOrigin(input: String, allowLoopbackHttp: Boolean = false): String {
    val raw = input.trim()
    require(Regex("^https?://", RegexOption.IGNORE_CASE).containsMatchIn(raw) &&
        raw.none { it.isWhitespace() || it == '\\' }) { "INVALID_ORIGIN" }
    val url = Url(raw)
    require(url.host.isNotBlank() && url.port in 1..65535 && url.user == null && url.password == null &&
        url.encodedPath in listOf("", "/") && url.parameters.isEmpty() &&
        url.fragment.isEmpty() && !raw.contains('?') && !raw.contains('#')) { "INVALID_ORIGIN" }
    val loopback = url.host.lowercase().removeSurrounding("[", "]") in setOf("localhost", "127.0.0.1", "::1")
    require(url.protocol == URLProtocol.HTTPS ||
        allowLoopbackHttp && loopback && url.protocol == URLProtocol.HTTP) { "HTTPS_REQUIRED" }
    val host = url.host.lowercase().let { if (':' in it && !it.startsWith('[')) "[$it]" else it }
    return "${url.protocol.name}://$host" + if (url.port == url.protocol.defaultPort) "" else ":${url.port}"
}

fun sameOrigin(url: Url, origin: String): Boolean {
    val base = Url(origin)
    return url.protocol == base.protocol && url.host.equals(base.host, true) && url.port == base.port
}

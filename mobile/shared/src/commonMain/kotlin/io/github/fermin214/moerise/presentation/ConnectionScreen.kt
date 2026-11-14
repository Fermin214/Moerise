package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp

@Composable
fun ConnectionScreen(controller: SessionController, strings: Strings, onConnected: () -> Unit = {}) {
    val state by controller.state.collectAsState()
    var origin by remember(state.profile?.origin) { mutableStateOf(state.profile?.origin ?: "") }
    var username by remember { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    var submitted by remember { mutableStateOf(false) }
    LaunchedEffect(state.connected, state.busy) { if (submitted && state.connected && !state.busy) onConnected() }
    Column(
        Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text(strings.text("连接 Moerise", "Connect to Moerise"), style = MaterialTheme.typography.headlineSmall)
        OutlinedTextField(origin, { origin = it }, label = { Text(strings.text("服务地址", "Server address")) },
            placeholder = { Text("https://notes.example.org") }, singleLine = true, enabled = !state.busy,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri), modifier = Modifier.fillMaxWidth().testTag("server_origin"))
        OutlinedTextField(username, { username = it }, label = { Text(strings.text("用户名", "Username")) },
            singleLine = true, enabled = !state.busy, modifier = Modifier.fillMaxWidth().testTag("server_username"))
        OutlinedTextField(password, { password = it }, label = { Text(strings.text("密码", "Password")) },
            singleLine = true, enabled = !state.busy, modifier = Modifier.fillMaxWidth().testTag("server_password"),
            visualTransformation = PasswordVisualTransformation(),
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password))
        state.error?.let { Text(connectionError(it, strings), color = MaterialTheme.colorScheme.error) }
        Button(
            onClick = { submitted = true; controller.login(origin, username, password); password = "" },
            enabled = !state.busy && origin.isNotBlank() && username.isNotBlank() && password.isNotBlank(),
            modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp).testTag("server_login"),
        ) { Text(if (state.busy) strings.text("连接中…", "Connecting…") else strings.text("登录", "Sign in")) }
    }
}

fun connectionError(code: String, strings: Strings): String = when (code) {
    "HTTPS_REQUIRED" -> strings.text("服务地址需要使用 HTTPS。", "Use an HTTPS server address.")
    "INVALID_ORIGIN" -> strings.text("请填写不含路径的服务地址。", "Enter a server address without a path.")
    "SESSION_UNAUTHORIZED", "INVALID_CREDENTIALS", "AUTHENTICATION_FAILED" -> strings.text("请检查用户名和密码后重新登录。", "Check your username and password, then sign in again.")
    "ACCOUNT_CHANGED" -> strings.text("服务账号已变化，请重新登录。", "The server account changed. Sign in again.")
    "RATE_LIMITED" -> strings.text("请求过于频繁，请稍后重试。", "Too many requests. Try again later.")
    "CSRF_REJECTED", "ORIGIN_REJECTED" -> strings.text("服务连接校验失败，请检查服务地址。", "Connection validation failed. Check the server address.")
    "INCOMPATIBLE_CORE" -> strings.text("此服务未提供兼容的笔记接口。", "This server does not provide a compatible notes API.")
    "INVALID_PAGE_TOKEN", "SOURCE_CHANGED" -> strings.text("笔记列表已变化，请刷新后重试。", "Notes changed. Refresh and try again.")
    else -> strings.text("无法连接服务，请检查地址和网络后重试。", "Could not connect. Check the address and network, then retry.")
}

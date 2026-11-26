package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.focus.FocusDirection
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

@Composable
fun ConnectionScreen(controller: SessionController, strings: Strings, onConnected: () -> Unit = {}) {
    val focusManager = LocalFocusManager.current
    val keyboard = LocalSoftwareKeyboardController.current
    val state by controller.state.collectAsState()
    val language by controller.language.collectAsState()
    var origin by rememberSaveable(state.profile?.origin) { mutableStateOf(state.profile?.origin ?: "") }
    var username by rememberSaveable { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    var submitted by remember { mutableStateOf(false) }
    var changingServer by remember(state.profile?.origin) { mutableStateOf(state.profile == null) }
    var languageMenu by remember { mutableStateOf(false) }
    val canLogin = !state.busy && origin.isNotBlank() && username.isNotBlank() && password.isNotBlank()
    fun submitLogin() {
        if (canLogin) {
            focusManager.clearFocus()
            keyboard?.hide()
            submitted = true
            controller.login(origin, username, password)
            password = ""
        }
    }
    LaunchedEffect(state.connected, state.busy) { if (submitted && state.connected && !state.busy) onConnected() }
    BoxWithConstraints(Modifier.fillMaxSize().imePadding()) {
        Column(
            Modifier.fillMaxWidth().verticalScroll(rememberScrollState())
                .heightIn(min = maxHeight).padding(16.dp),
            verticalArrangement = Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Surface(
                modifier = Modifier.widthIn(max = 360.dp).fillMaxWidth(),
                shape = RoundedCornerShape(12.dp), color = Color.White,
                border = BorderStroke(1.dp, MoeriseColors.Line), shadowElevation = 1.dp,
            ) {
                Column(Modifier.padding(28.dp)) {
                    Box(Modifier.align(Alignment.End)) {
                        TextButton(onClick = { languageMenu = true }, enabled = !state.busy, modifier = Modifier.testTag("login_language")) {
                            Text(when (language) {
                                UiLanguage.SYSTEM -> strings.text("跟随系统", "System")
                                UiLanguage.CHINESE -> "简体中文"
                                UiLanguage.ENGLISH -> "English"
                            })
                        }
                        DropdownMenu(languageMenu, onDismissRequest = { languageMenu = false },
                            modifier = Modifier.platformTestTags()) {
                            UiLanguage.entries.forEach { choice -> DropdownMenuItem(text = { Text(when (choice) {
                                UiLanguage.SYSTEM -> strings.text("跟随系统", "System")
                                UiLanguage.CHINESE -> "简体中文"
                                UiLanguage.ENGLISH -> "English"
                            }) }, onClick = { languageMenu = false; controller.language(choice) }) }
                        }
                    }
                    Text(
                        strings.text("登录", "Sign in"),
                        style = MaterialTheme.typography.titleMedium.copy(
                            fontSize = 18.sp, lineHeight = 28.sp, fontWeight = FontWeight.SemiBold,
                        ),
                    )
                    Text(strings.text("欢迎回来。", "Welcome back."), color = MoeriseColors.Muted,
                        style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(top = 4.dp))
                    Spacer(Modifier.height(24.dp))
                    Column(verticalArrangement = Arrangement.spacedBy(16.dp)) {
                        if (changingServer) ConnectionField(
                            origin, { if (origin != it) { origin = it; password = ""; submitted = false } }, strings.text("服务地址", "Server address"),
                            "server_origin", !state.busy, placeholder = "https://notes.example.org",
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                        ) else TextButton(onClick = { changingServer = true }, enabled = !state.busy, modifier = Modifier.testTag("server_change")) {
                            Text(strings.text("更换服务器", "Change server"))
                        }
                        ConnectionField(
                            username, { username = it }, strings.text("用户名", "Username"),
                            "server_username", !state.busy,
                        )
                        ConnectionField(
                            password, { password = it }, strings.text("密码", "Password"),
                            "server_password", !state.busy,
                            visualTransformation = PasswordVisualTransformation(),
                            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                            onSubmit = ::submitLogin,
                        )
                        state.error?.let {
                            Text(connectionError(it, strings), color = MaterialTheme.colorScheme.error,
                                style = MaterialTheme.typography.bodyMedium)
                        }
                        Button(
                            onClick = ::submitLogin,
                            enabled = canLogin,
                            modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp).testTag("server_login"),
                            shape = RoundedCornerShape(6.dp),
                            contentPadding = PaddingValues(horizontal = 12.dp, vertical = 12.dp),
                            colors = ButtonDefaults.buttonColors(
                                containerColor = MaterialTheme.colorScheme.primary,
                                contentColor = Color.White,
                                disabledContainerColor = MaterialTheme.colorScheme.primary.copy(alpha = .5f),
                                disabledContentColor = Color.White.copy(alpha = .5f),
                            ),
                        ) {
                            Text(
                                if (state.busy) strings.text("连接中…", "Connecting…") else strings.text("登录", "Sign in"),
                                fontSize = 14.sp, lineHeight = 20.sp, fontWeight = FontWeight.Medium,
                            )
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun ConnectionField(
    value: String, onValueChange: (String) -> Unit, label: String, tag: String, enabled: Boolean,
    placeholder: String? = null, keyboardOptions: KeyboardOptions = KeyboardOptions.Default,
    visualTransformation: VisualTransformation = VisualTransformation.None,
    onSubmit: (() -> Unit)? = null,
) {
    val focus = LocalFocusManager.current
    val textStyle = MaterialTheme.typography.bodyLarge.copy(fontSize = 16.sp, lineHeight = 20.sp)
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(label, fontSize = 14.sp, lineHeight = 14.sp, fontWeight = FontWeight.Medium)
        BasicTextField(
            value = value, onValueChange = onValueChange, singleLine = true, enabled = enabled,
            keyboardOptions = keyboardOptions.copy(imeAction = if (onSubmit != null) ImeAction.Done else ImeAction.Next),
            keyboardActions = KeyboardActions(onNext = { focus.moveFocus(FocusDirection.Next) }, onDone = { onSubmit?.invoke() }),
            visualTransformation = visualTransformation,
            textStyle = textStyle.copy(color = MoeriseColors.Ink.copy(alpha = if (enabled) 1f else .5f)),
            cursorBrush = SolidColor(MaterialTheme.colorScheme.primary),
            modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp)
                .border(1.dp, MoeriseColors.Line, RoundedCornerShape(6.dp))
                .testTag(tag).semantics { contentDescription = label },
            decorationBox = { innerTextField ->
                Box(
                    Modifier.fillMaxWidth().heightIn(min = 44.dp).padding(horizontal = 12.dp, vertical = 10.dp)
                        .testTag(if (value.isEmpty()) "${tag}_empty" else "${tag}_filled"),
                    contentAlignment = Alignment.CenterStart,
                ) {
                    if (value.isEmpty() && placeholder != null) {
                        Text(placeholder, style = textStyle, color = MoeriseColors.Muted)
                    }
                    innerTextField()
                }
            },
        )
    }
}

fun connectionError(code: String, strings: Strings): String = when (code) {
    "LOCAL_REMOVE_FAILED" -> strings.text("本机数据清理未完成，请重试。", "Local cleanup is incomplete. Try again.")
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

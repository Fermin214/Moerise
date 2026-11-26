package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.unit.dp

@Composable
private fun ShareChoice(checked: Boolean, enabled: Boolean, strings: Strings, change: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().toggleable(checked, enabled = enabled, role = Role.Switch, onValueChange = change)
        .testTag("privacy_share"), verticalAlignment = Alignment.CenterVertically) {
        Text(strings.text("分享使用与故障统计", "Share usage and error statistics"), Modifier.weight(1f))
        Switch(checked, onCheckedChange = null, enabled = enabled)
    }
    Text(if (enabled) strings.text(
        "开启后，发送功能使用次数、等待时间和错误类型等汇总，帮助改进 Moerise。不含笔记、搜索词、提问和回答正文。",
        "Share feature counts, waiting-time ranges and error categories to improve Moerise. Notes, searches, questions and answers are excluded.")
        else strings.text("此实例禁止发送统计。", "This instance disables statistics sharing."))
}

@Composable
private fun Feedback(strings: Strings) {
    val uri = LocalUriHandler.current
    TextButton({ uri.openUri("https://github.com/Fermin214/Moerise/issues") }) {
        Text(strings.text("反馈问题", "Report an issue"))
    }
}

@Composable
fun WelcomeDialog(controller: PrivacyController, strings: Strings) {
    val state by controller.state.collectAsState()
    if (!state.welcome) return
    var checked by remember { mutableStateOf(false) }
    var details by remember { mutableStateOf(false) }
    if (details) {
        InformationOverlay(strings.text("统计说明", "About statistics"), strings, { details = false }, "privacy_details") {
            Column(Modifier.verticalScroll(rememberScrollState())) { PrivacyDetails(strings); Feedback(strings) }
        }
        return
    }
    AlertDialog(onDismissRequest = { controller.choose(false) }, modifier = Modifier.platformTestTags(),
        title = { Text(strings.text("欢迎使用 Moerise", "Welcome to Moerise")) },
        text = { Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Text(strings.text("Moerise 还在持续完善中，欢迎反馈问题和建议。", "Moerise is still improving. Your feedback and suggestions are welcome."))
            ShareChoice(checked && state.allowed != false, state.allowed != false, strings) { checked = it }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Feedback(strings)
                Text("·")
                TextButton({ details = true }) { Text(strings.text("查看统计说明", "About statistics")) }
            }
        } }, confirmButton = { Button({ controller.choose(checked) }, modifier = Modifier.testTag("welcome_enter")) {
            Text(strings.text("进入 Moerise", "Enter Moerise"))
        } })
}

@Composable
private fun PrivacyDetails(strings: Strings) {
    Text(strings.text(
        "Moerise 项目维护者使用每日汇总比较功能使用次数、等待时间和故障。字段包括客户端和 Core 版本、Android 大版本、操作、结果及耗时档，例如「关键词搜索 · 成功 · 1 秒以内」。",
        "Moerise maintainers use daily totals to compare feature use, waits and failures. Fields include client and Core versions, Android API level, operation, result and duration range, such as “keyword search · success · under 1 s”."))
    Text(strings.text(
        "本机汇总最多保留 48 小时，每 24 小时最多经本人 Core 发送一次。项目接收端保留每日汇总 30 天。关闭开关会丢弃未发批次。",
        "This device keeps totals for up to 48 hours and sends at most one batch per 24 hours through your Core. Project daily totals expire after 30 days. Turning sharing off discards unsent counts."))
}

@Composable
fun PrivacySettings(controller: PrivacyController, strings: Strings) {
    val state by controller.state.collectAsState()
    var preview by remember { mutableStateOf(false) }
    var text by remember { mutableStateOf("") }
    val clipboard = LocalClipboardManager.current
    Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
        ShareChoice(state.enabled && state.allowed != false, state.allowed != false, strings, controller::choose)
        PrivacyDetails(strings)
        Feedback(strings)
        TextButton({ text = state.diagnostic; preview = true }, enabled = state.diagnostic.isNotEmpty()) {
            Text(strings.text("复制诊断摘要", "Copy diagnostic summary"))
        }
    }
    if (preview) AlertDialog(onDismissRequest = { preview = false },
        title = { Text(strings.text("诊断摘要", "Diagnostic summary")) },
        text = { Column(Modifier.verticalScroll(rememberScrollState())) {
            Text(strings.text("公开 GitHub Issue 的内容会被其他人看到。请检查并删减后再复制。", "Other people can see public GitHub Issues. Review and edit this text before copying it."))
            OutlinedTextField(text, { text = it }, modifier = Modifier.fillMaxWidth(), minLines = 3)
        } }, dismissButton = { TextButton({ preview = false }) { Text(strings.text("取消", "Cancel")) } },
        confirmButton = { TextButton({ clipboard.setText(AnnotatedString(text)); preview = false }) { Text(strings.text("复制", "Copy")) } })
}

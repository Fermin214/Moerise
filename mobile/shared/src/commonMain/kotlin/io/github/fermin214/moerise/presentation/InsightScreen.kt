package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import io.github.fermin214.moerise.domain.ChatTurn

@Composable
fun InsightScreen(mobile: MobileController, strings: Strings, scroll: List<ScrollState>, onMenu: () -> Unit) {
    val controller = mobile.insights
    val state by controller.state.collectAsState()
    val connection by mobile.session.state.collectAsState()
    var deleting by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxSize().safeDrawingPadding().imePadding().testTag("insights_page")) {
        Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp), horizontalArrangement = Arrangement.SpaceBetween) {
            TextButton(onClick = onMenu, modifier = Modifier.testTag("open_menu")) { Text(strings.text("导航", "Menu")) }
            Text(strings.text("洞察", "Insights"), style = MaterialTheme.typography.titleLarge, modifier = Modifier.padding(top = 12.dp))
            TextButton(onClick = controller::newChat, enabled = !state.busy && !state.running && state.recovery.mutation == null && state.recovery.creation == null,
                modifier = Modifier.testTag("chat_new")) { Text(strings.text("新对话", "New chat")) }
        }
        PrimaryTabRow(selectedTabIndex = state.tab) {
            listOf(strings.text("对话", "Chat"), strings.text("材料", "Materials"), strings.text("历史", "History")).forEachIndexed { index, label ->
                Tab(state.tab == index, onClick = { controller.tab(index) }, text = { Text(label) }, modifier = Modifier.testTag("chat_tab_$index"))
            }
        }
        if (state.busy) LinearProgressIndicator(Modifier.fillMaxWidth())
        Column(Modifier.weight(1f).verticalScroll(scroll[state.tab]).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            state.error?.let { code ->
                Text(insightError(code, strings), color = MaterialTheme.colorScheme.error, modifier = Modifier.testTag("chat_error"))
                if (code == "LLM_DISABLED") {
                    val uri = LocalUriHandler.current
                    TextButton(onClick = { connection.profile?.let { runCatching { uri.openUri("${it.origin}/settings") } } }) {
                        Text(strings.text("打开 Web 设置", "Open web settings"))
                    }
                }
            }
            if (state.recovery.creation != null) OutlinedButton(onClick = controller::applyMaterials, enabled = !state.busy && connection.connected,
                modifier = Modifier.testTag("chat_retry_create")) { Text(strings.text("恢复新建对话", "Recover chat creation")) }
            if (state.recovery.mutation != null) {
                OutlinedButton(onClick = controller::retryMutation, enabled = !state.busy && connection.connected,
                    modifier = Modifier.testTag("chat_retry_write")) { Text(strings.text("检查操作结果", "Check operation result")) }
                if (state.error == "CHAT_DELETE_REQUIRES_CONFIRMATION") OutlinedButton(onClick = { deleting = true }, enabled = !state.busy) {
                    Text(strings.text("删除对话", "Delete chat"))
                }
            }
            if (state.recovery.mutation != null || state.recovery.generation != null) {
                TextButton(onClick = controller::adoptServer, enabled = !state.busy && !state.running && connection.connected) {
                    Text(strings.text("读取并采用服务器会话", "Read and use server session"))
                }
            }
            when (state.tab) {
                0 -> {
                    state.chat?.let { chat ->
                        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                            Text(chat.title, style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                            TextButton(onClick = controller::refresh, enabled = !state.busy && connection.connected,
                                modifier = Modifier.testTag("chat_refresh")) { Text(strings.text("重新读取", "Read again")) }
                            TextButton(onClick = { deleting = true }, enabled = !state.busy && !state.running && state.recovery.mutation == null,
                                modifier = Modifier.testTag("chat_delete")) { Text(strings.text("删除", "Delete")) }
                        }
                        val turns = chat.turns
                        turns.forEach { turn -> InsightTurn(turn, mobile, strings, !state.busy && connection.connected) }
                        val partial = state.recovery.partial
                        if (partial != null && turns.none { it.id == partial.id }) {
                            Text(strings.text("保留的部分回复", "Saved partial reply"), style = MaterialTheme.typography.labelMedium)
                            Text(partial.assistant.content)
                        }
                        if (turns.isEmpty() && state.recovery.generation == null) Button(onClick = { controller.generate("insight") },
                            enabled = !state.locked && connection.connected, modifier = Modifier.testTag("chat_insight")) {
                            Text(strings.text("一键洞察", "Generate insights"))
                        }
                    } ?: Text(strings.text("选择笔记材料，开始一次对话。", "Select notes to start a conversation."))
                    if (state.recovery.generation != null && !state.busy) {
                        OutlinedButton(onClick = controller::refresh, enabled = connection.connected) { Text(strings.text("检查生成结果", "Check generation result")) }
                        if (!state.running) Button(onClick = controller::retryGeneration, enabled = connection.connected,
                            modifier = Modifier.testTag("chat_retry_generation")) { Text(strings.text("恢复或重新生成", "Recover or generate again")) }
                    }
                }
                1 -> {
                    Text(strings.text("已选 ${state.recovery.selected.size} 条", "${state.recovery.selected.size} notes selected"),
                        style = MaterialTheme.typography.titleMedium)
                    state.materials?.let { materials ->
                        Text("${materials.characters} / ${materials.limit}", color =
                            if (materials.characters > materials.limit) MaterialTheme.colorScheme.error else MoeriseColors.Muted,
                            modifier = Modifier.testTag("chat_budget"))
                    }
                    state.recovery.selected.forEach { name ->
                        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                            TextButton(onClick = { mobile.openNote(name) }) { Text(name) }
                            TextButton(onClick = { controller.toggle(name) }, enabled = !state.locked) { Text(strings.text("移除", "Remove")) }
                        }
                    }
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        OutlinedButton(onClick = controller::resolve, enabled = !state.busy && connection.connected && state.recovery.selected.isNotEmpty()) {
                            Text(strings.text("检查预算", "Check budget"))
                        }
                        Button(onClick = controller::applyMaterials,
                            enabled = !state.locked && connection.connected && state.recovery.selected.isNotEmpty(),
                            modifier = Modifier.testTag("chat_apply_materials")) { Text(strings.text("用于对话", "Use in chat")) }
                    }
                    HorizontalDivider()
                    OutlinedTextField(state.query, controller::query, label = { Text(strings.text("查找笔记", "Find notes")) },
                        enabled = !state.busy, singleLine = true, modifier = Modifier.fillMaxWidth().testTag("chat_material_query"))
                    Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        listOf("ALL" to strings.text("全部", "All"), "NORMAL" to strings.text("正常", "Active"), "ARCHIVED" to strings.text("归档", "Archived"))
                            .forEach { (value, label) -> FilterChip(state.filter == value, onClick = { controller.filter(value) }, label = { Text(label) }, enabled = !state.busy) }
                    }
                    Button(onClick = { controller.pick() }, enabled = !state.busy && connection.connected, modifier = Modifier.testTag("chat_find_materials")) {
                        Text(strings.text("查找", "Find"))
                    }
                    state.candidates.forEach { note ->
                        Surface(shape = RoundedCornerShape(12.dp), border = BorderStroke(1.dp, MoeriseColors.Line)) {
                            Row(Modifier.fillMaxWidth().clickable(enabled = !state.locked) { controller.toggle(note.name) }.padding(12.dp)) {
                                Checkbox(note.name in state.recovery.selected, onCheckedChange = { controller.toggle(note.name) }, enabled = !state.locked)
                                Column(Modifier.weight(1f)) {
                                    Text(note.content, maxLines = 4)
                                    Text(note.name, style = MaterialTheme.typography.labelSmall, color = MoeriseColors.Muted)
                                }
                            }
                        }
                    }
                    if (state.pickerNext.isNotBlank()) TextButton(onClick = { controller.pick(true) }, enabled = !state.busy && connection.connected) {
                        Text(strings.text("加载更多", "Load more"))
                    }
                }
                2 -> {
                    TextButton(onClick = { controller.history() }, enabled = !state.busy && connection.connected) { Text(strings.text("刷新历史", "Refresh history")) }
                    state.history.forEach { chat ->
                        Surface(shape = RoundedCornerShape(12.dp), border = BorderStroke(1.dp, MoeriseColors.Line),
                            modifier = Modifier.fillMaxWidth().clickable(enabled = !state.busy && !state.running && state.recovery.mutation == null && state.recovery.creation == null) { controller.open(chat.id) }) {
                            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                Text(chat.title, style = MaterialTheme.typography.titleMedium)
                                Text(chat.preview, maxLines = 3)
                                Text(displayServiceTime(chat.updatedAt, connection.profile?.timezone ?: "UTC"), color = MoeriseColors.Muted, style = MaterialTheme.typography.labelSmall)
                            }
                        }
                    }
                    if (state.historyNext.isNotBlank()) TextButton(onClick = { controller.history(true) }, enabled = !state.busy && connection.connected) {
                        Text(strings.text("加载更多", "Load more"))
                    }
                }
            }
        }
        if (state.tab == 0) {
            HorizontalDivider()
            Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(state.recovery.question, controller::question, label = { Text(strings.text("继续提问", "Ask a question")) },
                    enabled = !state.locked, modifier = Modifier.fillMaxWidth().heightIn(max = 160.dp).testTag("chat_question"))
                if (state.canStop) Button(onClick = controller::stop, enabled = connection.connected,
                    modifier = Modifier.fillMaxWidth().testTag("chat_stop")) { Text(strings.text("停止", "Stop")) }
                else Button(onClick = { controller.generate("question") }, enabled = !state.locked && connection.connected &&
                    state.chat != null && state.recovery.question.isNotBlank(), modifier = Modifier.fillMaxWidth().testTag("chat_send")) { Text(strings.text("发送", "Send")) }
            }
        }
    }
    if (deleting) AlertDialog(onDismissRequest = { deleting = false },
        title = { Text(strings.text("删除这段对话？", "Delete this conversation?")) },
        confirmButton = { TextButton(onClick = {
            deleting = false
            if (state.recovery.mutation?.method == "DELETE") controller.confirmDeleteAgain() else controller.delete()
        }, modifier = Modifier.testTag("chat_delete_confirm")) { Text(strings.text("删除", "Delete")) } },
        dismissButton = { TextButton(onClick = { deleting = false }) { Text(strings.text("取消", "Cancel")) } })
}

@Composable
private fun InsightTurn(turn: ChatTurn, mobile: MobileController, strings: Strings, enabled: Boolean) {
    Text(if (turn.kind == "insight") strings.text("一键洞察", "Generate insights") else turn.user.content,
        style = MaterialTheme.typography.titleSmall)
    Surface(shape = RoundedCornerShape(16.dp), color = MoeriseColors.BlueSurface) {
        Column(Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            if (turn.assistant.status == "running") Text(turn.assistant.content)
            else MarkdownBody(turn.assistant.content)
            Text(when (turn.assistant.status) {
                "running" -> strings.text("生成中…", "Generating…")
                "completed" -> strings.text("已完成", "Completed")
                "canceled" -> strings.text("已停止", "Stopped")
                "interrupted" -> strings.text("已中断", "Interrupted")
                else -> insightError(turn.assistant.errorCode.ifBlank { "CHAT_FAILED" }, strings)
            }, color = MoeriseColors.Muted, style = MaterialTheme.typography.labelSmall)
            if (turn.assistant.savedName.isNotBlank()) TextButton(onClick = { mobile.openNote(turn.assistant.savedName) }) {
                Text(strings.text("打开已存笔记", "Open saved note"))
            } else if (turn.assistant.status == "completed") TextButton(onClick = { mobile.insights.save(turn) }, enabled = enabled,
                modifier = Modifier.testTag("chat_save_${turn.requestId}")) { Text(strings.text("保存为笔记", "Save as note")) }
        }
    }
}

fun insightError(code: String, strings: Strings): String = when (code) {
    "CHAT_MATERIAL_REQUIRED" -> strings.text("请先选择笔记材料并用于对话。", "Select notes and apply them to the conversation.")
    "CHAT_MATERIAL_LIMIT" -> strings.text("材料超过字数上限，请减少所选笔记。", "The materials exceed the character limit. Select fewer notes.")
    "SOURCE_CHANGED" -> strings.text("材料已修改，请重新选择并用于对话。", "The materials changed. Select and apply them again.")
    "SOURCE_NOT_FOUND" -> strings.text("材料已删除或无法访问，请调整选择。", "Some materials are missing or unavailable. Change the selection.")
    "CHAT_CONFLICT" -> strings.text("会话已在其他页面更改，请重新读取。", "This conversation changed elsewhere. Read it again.")
    "CHAT_BUSY" -> strings.text("会话正在生成，请等待或停止后继续。", "This conversation is generating. Wait or stop it to continue.")
    "CHAT_NOT_FOUND" -> strings.text("这段对话已删除或无法访问。", "This conversation is missing or unavailable.")
    "CHAT_PROTOCOL", "CHAT_INTERRUPTED", "LLM_PROTOCOL" -> strings.text("回复已中断，已保留收到的内容。请重新读取会话。", "The reply was interrupted. Received content is saved. Read the conversation again.")
    "CHAT_CANCELED" -> strings.text("已停止生成。", "Generation stopped.")
    "LLM_DISABLED" -> strings.text("请在 Web 设置中启用 LLM。", "Enable the LLM in web settings.")
    "CHAT_OUTCOME_UNKNOWN" -> strings.text("请先检查操作结果。", "Check the operation result first.")
    "CHAT_DELETE_REQUIRES_CONFIRMATION" -> strings.text("对话仍然存在，可以再次确认删除。", "This conversation still exists. You can confirm deletion again.")
    "CONTENT_TOO_LARGE" -> strings.text("回复超过笔记正文上限，请让 AI 总结后再保存。", "The reply exceeds the note limit. Ask for a summary before saving.")
    "LOCAL_SAVE_FAILED" -> strings.text("本机保存失败，请释放存储空间后重试。", "Could not save on this device. Free up storage and retry.")
    else -> connectionError(code, strings)
}

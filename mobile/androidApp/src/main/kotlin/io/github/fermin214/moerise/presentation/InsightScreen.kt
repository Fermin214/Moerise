package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.input.key.*
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.fermin214.moerise.domain.*
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun InsightScreen(mobile: MobileController, strings: Strings, scroll: List<ScrollState>,
    onMenu: () -> Unit, onSearch: () -> Unit, onSettings: () -> Unit) {
    val controller = mobile.insights
    val state by controller.state.collectAsState()
    val picker by controller.picker.state.collectAsState()
    val connection by mobile.session.state.collectAsState()
    val sessionFocus = remember { FocusRequester() }
    var menu by remember { mutableStateOf(false) }
    var dialog by remember { mutableStateOf("") }
    val chat = state.chat
    val recovery = state.recovery
    val turns = chat?.turns.orEmpty() + listOfNotNull(recovery.partial?.takeIf { local -> chat?.turns.orEmpty().none { it.id == local.id } })
    val pending = recovery.generation
    val waiting = state.starting.isNotBlank() || pending != null && turns.none { it.requestId == pending.input.requestId }
    val first = turns.isEmpty() && !waiting
    val online = connection.connected
    val edit = online && !state.locked
    val canSend = edit && recovery.selected.isNotEmpty()
    val timezone = connection.profile?.timezone ?: "UTC"
    val closeDialog = { dialog = ""; sessionFocus.requestFocus(); Unit }
    Column(Modifier.fillMaxSize().safeDrawingPadding().testTag("insights_page")) {
        WebToolbar(strings, onMenu, onSearch)
        Column(Modifier.weight(1f).widthIn(max = 928.dp).fillMaxWidth().align(Alignment.CenterHorizontally)
            .padding(horizontal = 16.dp)) {
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                Text(strings.text("洞察", "Insights"), modifier = Modifier.weight(1f), fontSize = 22.sp, fontWeight = FontWeight.SemiBold)
                Box {
                    IconButton(onClick = { menu = true }, modifier = Modifier.focusRequester(sessionFocus).testTag("chat_session_menu")) {
                        WebIcon(WebIcon.More, strings.text("更多", "More"))
                    }
                    DropdownMenu(menu, onDismissRequest = { menu = false }, modifier = Modifier.platformTestTags()) {
                        DropdownMenuItem(text = { Text(strings.text("新建会话", "New conversation")) }, enabled = edit,
                            onClick = { menu = false; controller.newChat() }, modifier = Modifier.testTag("chat_new"))
                        DropdownMenuItem(text = { Text(strings.text("历史会话", "Conversation history")) },
                            onClick = { menu = false; dialog = "history"; controller.history() }, modifier = Modifier.testTag("chat_history"))
                        if (!first) DropdownMenuItem(text = { Text(strings.text("修改材料", "Change materials")) },
                            enabled = edit, onClick = { menu = false; controller.openPicker() })
                        DropdownMenuItem(text = { Text(strings.text("保存为笔记", "Save as note")) },
                            enabled = online && (turns.isNotEmpty() || state.pendingSave != null),
                            onClick = { menu = false; dialog = "save" }, modifier = Modifier.testTag("chat_save_selection"))
                        DropdownMenuItem(text = { Text(strings.text("删除对话", "Delete conversation"), color = MaterialTheme.colorScheme.error) },
                            enabled = online && state.canDelete && !state.saving, onClick = { menu = false; dialog = "delete" },
                            modifier = Modifier.testTag("chat_delete"))
                    }
                }
            }
            if (first) FlowRow(verticalArrangement = Arrangement.Center, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (recovery.selected.isNotEmpty()) Text(strings.text(
                    "已选 ${recovery.selected.size} 条 · ${recovery.candidates.sumOf { it.characters }} 字",
                    "${recovery.selected.size} selected · ${recovery.candidates.sumOf { it.characters }} chars"),
                    modifier = Modifier.padding(vertical = 12.dp).testTag("chat_material_summary"), color = MoeriseColors.Muted)
                TextButton(onClick = controller::openPicker, enabled = edit, modifier = Modifier.testTag("chat_add_materials")) {
                    Text(strings.text("添加笔记", "Add notes"))
                }
            }
            var following by remember(chat?.id) { mutableStateOf(true) }
            val scope = rememberCoroutineScope()
            val messages = scroll[0]
            LaunchedEffect(messages, chat?.id) {
                snapshotFlow { messages.isScrollInProgress to messages.canScrollForward }.distinctUntilChanged()
                    .collect { (moving, canScroll) -> if (moving) following = !canScroll }
            }
            LaunchedEffect(following, chat?.id) {
                if (following) snapshotFlow { messages.maxValue }.collect { end ->
                    if (following && !messages.isScrollInProgress && end != Int.MAX_VALUE) messages.scrollTo(end)
                }
            }
            Box(Modifier.weight(1f).fillMaxWidth()) {
                Column(Modifier.fillMaxWidth().verticalScroll(messages).padding(vertical = 8.dp),
                    verticalArrangement = Arrangement.spacedBy(14.dp)) {
                    turns.forEachIndexed { index, turn -> key(turn.id) {
                        if (index > 0 && turn.materialVersion != turns[index - 1].materialVersion) MaterialUpdate(strings)
                        InsightTurn(turn, strings)
                    } }
                    if (turns.isNotEmpty() && chat != null && turns.last().materialVersion < chat.materialVersion)
                        MaterialUpdate(strings)
                    if (waiting) {
                        val question = pending?.input?.content ?: recovery.question
                        if ((pending?.input?.kind ?: state.starting) == "question") UserMessage(question)
                        ReplyWaiting(strings)
                    }
                }
                if (!following && messages.canScrollForward) Surface(
                    modifier = Modifier.align(Alignment.BottomCenter).padding(bottom = 10.dp).size(44.dp),
                    shape = CircleShape,
                    color = Color.White,
                    border = BorderStroke(1.dp, MoeriseColors.Line),
                    shadowElevation = 4.dp,
                ) {
                    IconButton(
                        onClick = { scope.launch { messages.animateScrollTo(messages.maxValue); following = true } },
                        modifier = Modifier.fillMaxSize().testTag("chat_latest"),
                    ) {
                        WebIcon(WebIcon.ArrowDown, strings.text("回到最新", "Jump to latest"), tint = MoeriseColors.Ink)
                    }
                }
            }
            if (!picker.visible && dialog.isEmpty()) {
                state.error?.let { InsightError(it, strings) }
                if (state.error == "LLM_DISABLED") TextButton(onSettings) { Text(strings.text("打开设置", "Open settings")) }
                FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    if (recovery.creation != null) TextButton(controller::applyMaterials, enabled = online && !state.busy) {
                        Text(strings.text("恢复新建会话", "Recover conversation"))
                    }
                    if (recovery.mutation != null) TextButton({ if (recovery.mutation?.method == "DELETE") dialog = "delete" else controller.retryMutation() },
                        enabled = online && !state.busy) { Text(strings.text("检查操作结果", "Check operation result")) }
                    if (pending != null && !state.streaming) TextButton(controller::refresh, enabled = online && !state.busy) {
                        Text(strings.text("检查回复", "Check reply"))
                    }
                    if (state.failedGeneration && pending != null && !state.conflicted) TextButton(controller::retryGeneration, enabled = online && !state.busy) {
                        Text(strings.text("重新生成", "Generate again"))
                    }
                    if (state.conflicted) TextButton({ dialog = "adopt" }, enabled = online && !state.busy) {
                        Text(strings.text("使用服务器版本", "Use server version"))
                    }
                }
            }
            Surface(Modifier.fillMaxWidth().padding(vertical = 8.dp), color = Color.White, shape = RoundedCornerShape(14.dp),
                border = BorderStroke(1.dp, MoeriseColors.Line)) {
                Column(Modifier.padding(8.dp)) {
                    BasicTextField(recovery.question, controller::question, readOnly = state.locked,
                        minLines = 1, maxLines = 4, cursorBrush = SolidColor(MoeriseColors.Blue),
                        textStyle = MaterialTheme.typography.bodyMedium.copy(color = MoeriseColors.Ink, fontSize = 16.sp),
                        modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp).testTag("chat_question").semantics {
                            contentDescription = strings.text("向 AI 提问", "Ask AI")
                        }.onPreviewKeyEvent {
                            if (it.type == KeyEventType.KeyDown && it.key == Key.Enter && it.isCtrlPressed && canSend && recovery.question.isNotBlank()) {
                                controller.generate("question"); true
                            } else false
                        }, decorationBox = { input ->
                            Box(contentAlignment = Alignment.CenterStart) {
                                if (recovery.question.isEmpty()) Text(strings.text("围绕所选笔记提问或追问…", "Ask about the selected notes…"),
                                    color = MoeriseColors.Muted, fontSize = 15.sp)
                                input()
                            }
                        })
                    FlowRow(Modifier.align(Alignment.End), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        if (first) TextButton({ controller.generate("insight") }, enabled = canSend, modifier = Modifier.testTag("chat_insight")) {
                            Text(strings.text("一键洞察", "Generate insights"))
                        }
                        if (state.canStop) WebAction(strings.text("停止", "Stop"), controller::stop,
                            enabled = online && !state.stopping && !state.deleting, modifier = Modifier.testTag("chat_stop"))
                        else WebAction(strings.text("发送", "Send"), { controller.generate("question") },
                            enabled = canSend && recovery.question.isNotBlank(), modifier = Modifier.testTag("chat_send"))
                    }
                }
            }
        }
    }
    if (picker.visible) InsightPickerDialog(controller, state, picker, timezone, strings, online) { name ->
        controller.closePicker()
        mobile.openNote(name)
    }
    when (dialog) {
        "history" -> InsightOverlay(strings.text("历史会话", "Conversation history"), strings, closeDialog, "chat_history_dialog") {
            val list = rememberLazyListState()
            LazyColumn(Modifier.weight(1f), state = list) {
                if (state.history.isEmpty()) item { Text(if (state.busy) strings.text("正在读取…", "Loading…")
                    else strings.text("还没有会话", "No conversations yet")) }
                items(state.history, key = { it.id }) { summary ->
                    Column(Modifier.fillMaxWidth().clickable(enabled = edit) { controller.open(summary.id); closeDialog() }
                        .padding(vertical = 10.dp).testTag("chat_history_${summary.id}")) {
                        Text(if (summary.previewKind == "insight") strings.text("一键洞察", "Generate insights") else summary.preview.ifBlank { summary.title })
                        Text(displayServiceTime(summary.updatedAt, timezone), fontSize = 12.sp, color = MoeriseColors.Muted)
                    }
                }
                if (state.error != null) item {
                    InsightError(state.error!!, strings)
                    TextButton({ controller.history(state.historyNext.isNotBlank()) }) { Text(strings.text("重试", "Retry")) }
                }
                if (state.historyNext.isNotBlank() && state.error == null) item {
                    LaunchedEffect(state.historyNext, state.busy) { if (!state.busy) controller.history(true) }
                    Text(strings.text("正在读取…", "Loading…"))
                }
            }
        }
        "save" -> InsightSaveDialog(controller, state, strings, closeDialog)
        "delete" -> ConfirmInsight(strings.text("删除对话", "Delete conversation"), strings.text("已保存的笔记会保留。", "Saved notes will remain."),
            strings, closeDialog) { closeDialog(); controller.delete() }
        "adopt" -> ConfirmInsight(strings.text("使用服务器版本", "Use server version"),
            strings.text("将舍弃本机未发送的提问和未提交的材料修改。", "Your unsent question and pending material changes will be discarded."),
            strings, closeDialog) { closeDialog(); controller.adoptServer() }
    }
}

@Composable
private fun UserMessage(text: String) {
    BoxWithConstraints(Modifier.fillMaxWidth()) {
        Surface(Modifier.align(Alignment.CenterEnd).widthIn(max = maxWidth * .83f), shape = RoundedCornerShape(14.dp),
            color = Color(0xFFE3ECFA)) { SelectionContainer { Text(text, Modifier.padding(horizontal = 14.dp, vertical = 9.dp)) } }
    }
}

@Composable
private fun MaterialUpdate(strings: Strings) = Text(strings.text("材料已更新", "Materials updated"),
    modifier = Modifier.fillMaxWidth().wrapContentWidth(), fontSize = 12.sp, color = MoeriseColors.Muted)

@Composable
private fun ReplyWaiting(strings: Strings) = Text(strings.text("正在洞察……", "Generating insights…"),
    modifier = Modifier.testTag("chat_reply_waiting").semantics { liveRegion = LiveRegionMode.Polite }, color = MoeriseColors.Muted)

@Composable
private fun InsightTurn(turn: ChatTurn, strings: Strings) {
    UserMessage(if (turn.kind == "question") turn.user.content else strings.text("一键洞察", "Generate insights"))
    if (turn.assistant.content.isEmpty() && turn.assistant.status == "running") ReplyWaiting(strings)
    else SelectionContainer { MarkdownBody(turn.assistant.content, Modifier.testTag("chat_reply_${turn.assistant.status}")) }
    if (turn.assistant.status in setOf("failed", "canceled", "interrupted") && turn.assistant.content.isNotEmpty()) {
        val clipboard = LocalClipboardManager.current
        TextButton({ clipboard.setText(AnnotatedString(turn.assistant.content)) }) {
            Text(strings.text("复制已收到的内容", "Copy received text"))
        }
    }
}

@Composable
internal fun InsightOverlay(title: String, strings: Strings, onDismiss: () -> Unit, tag: String,
    content: @Composable ColumnScope.() -> Unit) {
    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        BoxWithConstraints(Modifier.fillMaxSize().safeDrawingPadding().padding(8.dp), contentAlignment = Alignment.Center) {
            Surface(Modifier.widthIn(max = 560.dp).fillMaxWidth().height(minOf(700.dp, maxHeight)).platformTestTags().testTag(tag),
                shape = RoundedCornerShape(14.dp), color = Color.White) {
                Column(Modifier.padding(horizontal = 12.dp, vertical = 6.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(title, Modifier.weight(1f), fontSize = 18.sp, fontWeight = FontWeight.SemiBold)
                        IconButton(onDismiss, modifier = Modifier.testTag("${tag}_close")) { WebIcon(WebIcon.Close, strings.text("关闭", "Close")) }
                    }
                    content()
                }
            }
        }
    }
}

@Composable
private fun ConfirmInsight(action: String, message: String, strings: Strings, close: () -> Unit, confirm: () -> Unit) {
    val cancel = remember { FocusRequester() }
    AlertDialog(onDismissRequest = close, title = { Text(action) }, text = { Text(message) },
        dismissButton = { TextButton(close, modifier = Modifier.focusRequester(cancel)) { Text(strings.text("取消", "Cancel")) } },
        confirmButton = { TextButton(confirm, modifier = Modifier.testTag("chat_confirm")) { Text(action) } })
    LaunchedEffect(Unit) { cancel.requestFocus() }
}

@Composable
private fun InsightSaveDialog(controller: InsightController, state: InsightState, strings: Strings, close: () -> Unit) {
    val chat = state.chat ?: return
    val durable = selectableMessages(state.confirmed).associateBy { it.ref }
    val choices = selectableMessages(chat.turns).map { preview ->
        durable[preview.ref] ?: preview.copy(eligible = false)
    }
    val savedBefore = remember { state.savedOperation }
    val chosen = remember(chat.id) { mutableStateListOf<ChatMessageRef>().apply {
        val request = state.pendingSave
        if (request != null) addAll(request.input.messages)
        else choices.lastOrNull { it.eligible && it.ref.role == "assistant" && it.status == "completed" }?.let { add(it.ref) }
    } }
    val selected = choices.filter { it.ref in chosen && it.eligible }
    val incomplete = selected.any { it.status == "canceled" }
    val bytes = selectionBody(selected, strings.text("zh", "en") == "en").encodeToByteArray().size
    var confirming by remember { mutableStateOf(false) }
    LaunchedEffect(state.savedOperation) { if (savedBefore != state.savedOperation) close() }
    InsightOverlay(strings.text("保存为笔记", "Save as note"), strings, close, "chat_save_dialog") {
        LazyColumn(Modifier.weight(1f)) {
            items(choices, key = { "${it.ref.requestId}/${it.ref.role}" }) { item ->
                Row(Modifier.fillMaxWidth().clickable(enabled = item.eligible && !state.saving && state.pendingSave == null) {
                    if (!chosen.remove(item.ref)) chosen.add(item.ref)
                }.padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(checked = item.ref in chosen, onCheckedChange = null, enabled = item.eligible)
                    Column(Modifier.padding(start = 8.dp)) {
                        val role = if (item.ref.role == "assistant") "Moerise" else strings.text("我", "Me")
                        val status = when (item.status) {
                            "completed" -> strings.text("已完成", "Complete")
                            "canceled" -> strings.text("已停止（内容不完整）", "Stopped (incomplete)")
                            "running" -> strings.text("正在生成", "Generating")
                            else -> strings.text("未完成", "Incomplete")
                        }
                        Text("$role · $status", fontSize = 13.sp, color = MoeriseColors.Muted)
                        Text(item.content.take(180), maxLines = 3)
                    }
                }
            }
        }
        Text(strings.text("已选 ${selected.size} 条 · $bytes / 65,536 字节", "${selected.size} selected · $bytes / 65,536 bytes"))
        if (bytes > 65_536) Text(strings.text("请减少所选消息。", "Select fewer messages."), color = MaterialTheme.colorScheme.error)
        state.saveError?.let { InsightError(it, strings) }
        WebAction(if (state.pendingSave != null) strings.text("重试保存", "Retry save") else strings.text("保存为笔记", "Save as note"), {
            if (state.pendingSave != null) controller.retrySave()
            else if (incomplete) confirming = true
            else controller.saveSelection(selected.map { it.ref }, false)
        }, enabled = !state.saving && (state.pendingSave != null || selected.isNotEmpty() && bytes <= 65_536),
            modifier = Modifier.align(Alignment.End).testTag("chat_save_confirm"))
    }
    if (confirming) ConfirmInsight(strings.text("保存为笔记", "Save as note"),
        strings.text("所选回答含已停止的内容，保存的笔记将包含不完整的回答。", "The saved note will include an incomplete, stopped reply."),
        strings, { confirming = false }) { confirming = false; controller.saveSelection(selected.map { it.ref }, true) }
}

@Composable
internal fun InsightError(code: String, strings: Strings) {
    Text(insightError(code, strings), color = MaterialTheme.colorScheme.error, fontSize = 13.sp,
        modifier = Modifier.testTag("chat_error").semantics { liveRegion = LiveRegionMode.Polite })
}

fun insightError(code: String, strings: Strings): String = when (code) {
    "CHAT_MATERIAL_REQUIRED" -> strings.text("请先选择笔记材料。", "Select note materials first.")
    "CHAT_MATERIAL_LIMIT" -> strings.text("超过字数上限，请减少所选笔记。", "Select fewer notes to fit the character limit.")
    "CHAT_RESOURCE_LIMIT" -> strings.text("来源超过上限，请缩小范围或逐条选择。", "Narrow the filter or select notes individually.")
    "SOURCE_CHANGED" -> strings.text("所选材料有修改，请重新选择变化的笔记。", "Reselect the notes that changed.")
    "SOURCE_NOT_FOUND" -> strings.text("所选材料已删除或无法访问。", "Selected notes were deleted or are unavailable.")
    "CHAT_CONFLICT" -> strings.text("会话已在其他设备更改。", "The conversation changed on another device.")
    "CHAT_BUSY" -> strings.text("会话正在生成。", "This conversation is generating.")
    "CHAT_NOT_FOUND" -> strings.text("会话已删除或无法访问。", "This conversation was deleted or is unavailable.")
    "CHAT_PROTOCOL", "CHAT_INTERRUPTED", "LLM_PROTOCOL" -> strings.text("回复已中断。", "The reply was interrupted.")
    "CHAT_CANCELED" -> strings.text("已停止生成。", "Generation stopped.")
    "LLM_DISABLED" -> strings.text("请在设置中启用 LLM。", "Enable the LLM in settings.")
    "CHAT_OUTCOME_UNKNOWN" -> strings.text("请先检查操作结果。", "Check the operation result first.")
    "CHAT_DELETE_REQUIRES_CONFIRMATION" -> strings.text("会话仍然存在，可以再次确认删除。", "The conversation still exists. Confirm deletion again.")
    "CONTENT_TOO_LARGE" -> strings.text("请减少所选消息。", "Select fewer messages.")
    "LOCAL_SAVE_FAILED" -> strings.text("本机保存失败，请释放存储空间后重试。", "Free up device storage and try again.")
    else -> connectionError(code, strings)
}

package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.fermin214.moerise.domain.ChatTurn

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun InsightScreen(mobile: MobileController, strings: Strings, scroll: List<ScrollState>,
    onMenu: () -> Unit, onSearch: () -> Unit, onSettings: () -> Unit) {
    val controller = mobile.insights
    val state by controller.state.collectAsState()
    val connection by mobile.session.state.collectAsState()
    val sessionFocus = remember { FocusRequester() }
    val addFocus = remember { FocusRequester() }
    var materialOpen by remember { mutableStateOf(true) }
    var sessionMenu by remember { mutableStateOf(false) }
    var deleting by remember { mutableStateOf(false) }
    var historyOpen by remember { mutableStateOf(false) }
    val picker by controller.picker.state.collectAsState()
    val pickerOpen = picker.visible
    var pickerWasOpen by remember { mutableStateOf(false) }
    val timezone = connection.profile?.timezone ?: "UTC"
    val selected = state.recovery.selected
    val notes = state.materials?.notes.orEmpty().associateBy { it.name }
    val characters = state.materials?.characters ?: if (selected.all { it in notes })
        selected.sumOf { name -> insightCharacters(notes.getValue(name).content) } else null
    val limit = state.materials?.limit ?: 20_000
    val over = characters != null && characters > limit
    val canGenerate = !state.locked && connection.connected && selected.isNotEmpty() && !over && !pickerOpen
    val canChangeMaterials = !state.locked && connection.connected && !deleting && !pickerOpen

    LaunchedEffect(state.chat?.id) { deleting = false }
    LaunchedEffect(pickerOpen, state.busy) {
        if (pickerOpen) pickerWasOpen = true
        else if (pickerWasOpen && !state.busy) { addFocus.requestFocus(); pickerWasOpen = false }
    }

    Column(Modifier.fillMaxSize().safeDrawingPadding().imePadding().testTag("insights_page")) {
        WebToolbar(strings, onMenu, onSearch)
        Column(Modifier.weight(1f).widthIn(max = 928.dp).fillMaxWidth()
            .align(Alignment.CenterHorizontally).padding(horizontal = 16.dp)) {
            Column(Modifier.fillMaxWidth().padding(top = 4.dp, bottom = 10.dp),
                verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.SpaceBetween) {
                    Text(strings.text("洞察", "Insights"), fontSize = 22.sp, fontWeight = FontWeight.SemiBold)
                    Box {
                        IconButton(onClick = { sessionMenu = !sessionMenu },
                            modifier = Modifier.focusRequester(sessionFocus).testTag("chat_session_menu")) {
                            WebIcon(WebIcon.Menu, strings.text("会话", "Conversation"))
                        }
                        DropdownMenu(expanded = sessionMenu, onDismissRequest = { sessionMenu = false },
                            modifier = Modifier.platformTestTags()) {
                            DropdownMenuItem(text = { Text(strings.text("新建会话", "New conversation")) },
                                enabled = !state.locked && connection.connected,
                                onClick = { sessionMenu = false; controller.newChat() },
                                modifier = Modifier.testTag("chat_new"))
                            DropdownMenuItem(text = { Text(strings.text("历史会话", "Conversation history")) },
                                onClick = { sessionMenu = false; historyOpen = true; controller.history() },
                                modifier = Modifier.testTag("chat_history"))
                            DropdownMenuItem(text = {
                                Text(strings.text("删除当前会话", "Delete current conversation"), color = MaterialTheme.colorScheme.error)
                            }, enabled = state.canDelete && connection.connected,
                                onClick = { sessionMenu = false; deleting = true },
                                modifier = Modifier.testTag("chat_delete"))
                        }
                    }
                }
                Surface(color = Color.White, shape = RoundedCornerShape(14.dp),
                    border = BorderStroke(1.dp, MoeriseColors.Line)) {
                    Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 8.dp)) {
                        FlowRow(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween,
                            verticalArrangement = Arrangement.spacedBy(2.dp)) {
                            TextButton(onClick = { materialOpen = !materialOpen },
                                contentPadding = PaddingValues(horizontal = 6.dp),
                                modifier = Modifier.testTag("chat_material_toggle").semantics {
                                    stateDescription = if (materialOpen) strings.text("展开", "Expanded") else strings.text("收起", "Collapsed")
                                }) {
                                Text(strings.text(
                                    "材料 · ${selected.size} 条 · ${characters ?: "—"} / $limit 字 · ${if (materialOpen) "收起" else "展开"}",
                                    "Materials · ${selected.size} notes · ${characters ?: "—"} / $limit chars · ${if (materialOpen) "Collapse" else "Expand"}"),
                                    color = Color(0xFF3A4049), fontSize = 14.sp)
                            }
                            InsightPlainAction(strings.text("添加笔记", "Add notes"), onClick = controller::openPicker, enabled = canChangeMaterials,
                                modifier = Modifier.focusRequester(addFocus).testTag("chat_add_materials"))
                        }
                        if (materialOpen && selected.isNotEmpty()) {
                            FlowRow(Modifier.fillMaxWidth().heightIn(max = 176.dp).verticalScroll(scroll[1]).padding(top = 6.dp),
                                horizontalArrangement = Arrangement.spacedBy(6.dp),
                                verticalArrangement = Arrangement.spacedBy(6.dp)) {
                                selected.forEach { name ->
                                    key(name) {
                                        Surface(color = MoeriseColors.TagSurface, shape = RoundedCornerShape(50),
                                            border = BorderStroke(1.dp, MoeriseColors.TagLine)) {
                                            Row(verticalAlignment = Alignment.CenterVertically) {
                                                TextButton(onClick = { mobile.openNote(name) },
                                                    contentPadding = PaddingValues(start = 9.dp, end = 2.dp),
                                                    modifier = Modifier.widthIn(max = 240.dp).semantics {
                                                        contentDescription = strings.text("在主页查看", "View on home")
                                                    }) {
                                                    Text(insightPreview(notes[name]?.content?.ifBlank { name } ?: name, 12) + "…",
                                                        color = Color(0xFF2A5BB5), fontSize = 13.sp, maxLines = 2)
                                                }
                                                IconButton(onClick = { controller.removeMaterial(name) }, enabled = canChangeMaterials,
                                                    modifier = Modifier.size(44.dp).testTag("chat_remove_$name")) {
                                                    WebIcon(WebIcon.Close, strings.text("移除这条笔记", "Remove this note"), Modifier.size(14.dp))
                                                }
                                            }
                                        }
                                    }
                                }
                            }
                        }
                        if (over) Text(strings.text("超过字数上限，请减少所选笔记。", "Over the character limit. Select fewer notes."),
                            color = MaterialTheme.colorScheme.error, fontSize = 13.sp,
                            modifier = Modifier.padding(top = 4.dp).testTag("chat_budget"))
                    }
                }
                if (deleting) {
                    Surface(color = Color.White, shape = RoundedCornerShape(10.dp),
                        border = BorderStroke(1.dp, MoeriseColors.Line),
                        modifier = Modifier.onPreviewKeyEvent {
                            if (it.type == KeyEventType.KeyDown && it.key == Key.Escape && !state.deleting && !state.stopping) {
                                deleting = false
                                sessionFocus.requestFocus()
                                true
                            } else false
                        }) {
                        Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                            Text(strings.text("删除这个会话？已保存的笔记会保留。", "Delete this conversation? Saved notes will remain."),
                                style = MaterialTheme.typography.bodyMedium)
                            Row(Modifier.align(Alignment.End), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                TextButton(onClick = { deleting = false; sessionFocus.requestFocus() }, enabled = !state.deleting && !state.stopping) {
                                    Text(strings.text("取消", "Cancel"))
                                }
                                TextButton(onClick = {
                                    deleting = false
                                    if (state.recovery.mutation?.method == "DELETE") controller.confirmDeleteAgain() else controller.delete()
                                }, enabled = state.canDelete && connection.connected,
                                    modifier = Modifier.testTag("chat_delete_confirm")) {
                                    Text(strings.text("删除", "Delete"), color = MaterialTheme.colorScheme.error)
                                }
                            }
                        }
                    }
                }
            }
            BoxWithConstraints(Modifier.weight(1f).fillMaxWidth()) {
                val contentHeight = maxHeight
                Column(Modifier.fillMaxWidth().heightIn(min = contentHeight).verticalScroll(scroll[0]),
                    verticalArrangement = Arrangement.spacedBy(14.dp)) {
                    val turns = state.chat?.turns.orEmpty()
                    val partial = state.recovery.partial?.takeIf { pending -> turns.none { it.id == pending.id } }
                    if (turns.isEmpty() && partial == null) {
                        Box(Modifier.fillMaxWidth().heightIn(min = contentHeight).padding(vertical = 40.dp),
                            contentAlignment = Alignment.Center) {
                            WebAction(strings.text("一键洞察", "Generate insights"), onClick = { controller.generate("insight") },
                                enabled = canGenerate && !deleting, modifier = Modifier.testTag("chat_insight"))
                        }
                    } else {
                        turns.forEach { turn ->
                            key(turn.id) { InsightTurn(turn, mobile, strings, !state.busy && !deleting && connection.connected) }
                        }
                        partial?.let { InsightTurn(it, mobile, strings, !state.busy && !deleting && connection.connected) }
                    }
                }
            }
            Column(Modifier.fillMaxWidth().padding(top = 10.dp, bottom = 16.dp),
                verticalArrangement = Arrangement.spacedBy(6.dp)) {
                if (state.error != null && !pickerOpen && !historyOpen) {
                    Text(insightError(state.error!!, strings), color = MaterialTheme.colorScheme.error,
                        fontSize = 13.sp, modifier = Modifier.testTag("chat_error").semantics { liveRegion = LiveRegionMode.Polite })
                    if (state.error == "LLM_DISABLED") {
                        TextButton(onClick = onSettings) {
                            Text(strings.text("打开设置", "Open Settings"))
                        }
                    }
                }
                if (state.recovery.creation != null || state.recovery.mutation != null || state.recovery.generation != null ||
                    state.conflicted || state.error in setOf("CHAT_CONFLICT", "CHAT_PROTOCOL", "CHAT_INTERRUPTED", "LLM_PROTOCOL", "CHAT_DELETE_REQUIRES_CONFIRMATION")) {
                    FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        if (state.recovery.creation != null) InsightPlainAction(
                            strings.text("恢复新建会话", "Recover conversation"), controller::applyMaterials,
                            enabled = !state.busy && connection.connected, modifier = Modifier.testTag("chat_retry_create"))
                        if (state.recovery.mutation != null) InsightPlainAction(
                            strings.text("检查操作结果", "Check operation result"), controller::retryMutation,
                            enabled = !state.busy && connection.connected, modifier = Modifier.testTag("chat_retry_write"))
                        if (state.error == "CHAT_DELETE_REQUIRES_CONFIRMATION") InsightPlainAction(
                            strings.text("删除当前会话", "Delete current conversation"), { deleting = true }, enabled = !state.busy)
                        if (state.conflicted || state.recovery.generation != null ||
                            state.error in setOf("CHAT_CONFLICT", "CHAT_PROTOCOL", "CHAT_INTERRUPTED", "LLM_PROTOCOL")) {
                            InsightPlainAction(strings.text("重新读取会话", "Reload conversation"), controller::refresh,
                                enabled = !state.busy && connection.connected, modifier = Modifier.testTag("chat_refresh"))
                        }
                        if (state.recovery.generation != null && !state.running) InsightPlainAction(
                            strings.text("恢复或重新生成", "Recover or generate again"), controller::retryGeneration,
                            enabled = !state.busy && connection.connected, modifier = Modifier.testTag("chat_retry_generation"))
                        if (state.recovery.mutation != null || state.recovery.generation != null) TextButton(
                            onClick = controller::adoptServer, enabled = !state.busy && !state.running && connection.connected) {
                            Text(strings.text("读取并采用服务器会话", "Read and use server conversation"))
                        }
                    }
                }
                Surface(color = Color.White, shape = RoundedCornerShape(14.dp), border = BorderStroke(1.dp, MoeriseColors.Line)) {
                    BoxWithConstraints {
                        val wrapped = strings.text("zh", "en") == "en" && maxWidth <= 640.dp
                        val question: @Composable (Modifier) -> Unit = { modifier ->
                            BasicTextField(state.recovery.question, controller::question,
                                enabled = !state.locked && !deleting && !pickerOpen,
                                readOnly = state.recovery.generation != null,
                                singleLine = true, cursorBrush = SolidColor(MoeriseColors.Blue),
                                textStyle = MaterialTheme.typography.bodyMedium.copy(color = MoeriseColors.Ink, fontSize = 16.sp),
                                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Send),
                                keyboardActions = KeyboardActions(onSend = {
                                    if (canGenerate && !deleting && state.recovery.question.isNotBlank()) controller.generate("question")
                                }),
                                modifier = modifier.heightIn(min = 44.dp).padding(start = 8.dp)
                                    .testTag("chat_question").semantics { contentDescription = strings.text("向 AI 提问", "Ask AI") },
                                decorationBox = { input ->
                                    Box(contentAlignment = Alignment.CenterStart) {
                                        if (state.recovery.question.isEmpty()) Text(
                                            strings.text("围绕所选笔记提问或追问…", "Ask about the selected notes…"),
                                            color = MoeriseColors.Muted, fontSize = 15.sp, maxLines = 1)
                                        input()
                                    }
                                })
                        }
                        val actions: @Composable () -> Unit = {
                            InsightPlainAction(strings.text("一键洞察", "Generate insights"), { controller.generate("insight") },
                                enabled = canGenerate && !deleting, modifier = Modifier.testTag("chat_insight_bottom"))
                            if (state.canStop) WebAction(strings.text("停止", "Stop"), controller::stop,
                                enabled = connection.connected && !state.stopping && !state.deleting && !deleting, modifier = Modifier.testTag("chat_stop"))
                            else WebAction(strings.text("发送", "Send"), { controller.generate("question") },
                                enabled = canGenerate && !deleting && state.recovery.question.isNotBlank(),
                                modifier = Modifier.testTag("chat_send"))
                        }
                        if (wrapped) Column(Modifier.padding(6.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                            question(Modifier.fillMaxWidth())
                            Row(Modifier.align(Alignment.End), horizontalArrangement = Arrangement.spacedBy(8.dp),
                                verticalAlignment = Alignment.CenterVertically) { actions() }
                        } else Row(Modifier.padding(6.dp), horizontalArrangement = Arrangement.spacedBy(8.dp),
                            verticalAlignment = Alignment.CenterVertically) {
                            question(Modifier.weight(1f))
                            actions()
                        }
                    }
                }
            }
        }
    }
    if (historyOpen) InsightOverlay(strings.text("历史会话", "Conversation history"), strings,
        onDismiss = { historyOpen = false; sessionFocus.requestFocus() }, tag = "chat_history_dialog") {
        Column(Modifier.weight(1f).fillMaxWidth().verticalScroll(scroll[2])) {
            if (state.history.isEmpty() && !state.busy && state.error == null)
                Text(strings.text("还没有会话", "No conversations yet"), color = MoeriseColors.Muted,
                    modifier = Modifier.padding(vertical = 12.dp))
            state.history.forEach { chat ->
                Surface(color = if (chat.id == state.chat?.id) MoeriseColors.BlueSurface else Color.Transparent,
                    shape = RoundedCornerShape(8.dp),
                    modifier = Modifier.fillMaxWidth().clickable(enabled = !state.locked && connection.connected) {
                        historyOpen = false
                        controller.open(chat.id)
                        sessionFocus.requestFocus()
                    }.testTag("chat_history_${chat.id}")) {
                    Column(Modifier.padding(horizontal = 8.dp, vertical = 12.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                        Text(if (chat.previewKind == "insight") strings.text("一键洞察", "Generate insights")
                            else chat.preview.ifBlank { chat.title.ifBlank { strings.text("洞察", "Insights") } },
                            fontSize = 14.sp, color = MoeriseColors.Ink)
                        Text(strings.text("${displayServiceTime(chat.updatedAt, timezone)} · ${chat.materialCount} 条材料",
                            "${displayServiceTime(chat.updatedAt, timezone)} · ${chat.materialCount} materials"),
                            fontSize = 12.sp, color = MoeriseColors.Muted)
                    }
                }
            }
        }
        if (state.busy) LinearProgressIndicator(Modifier.fillMaxWidth())
        state.error?.let { Text(insightError(it, strings), color = MaterialTheme.colorScheme.error,
            fontSize = 13.sp, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite }) }
        if (state.historyNext.isNotBlank() || state.error != null) InsightPlainAction(
            if (state.historyNext.isNotBlank()) strings.text("加载更多", "Load more") else strings.text("重试", "Retry"),
            { controller.history(state.historyNext.isNotBlank()) }, enabled = !state.busy && connection.connected,
            modifier = Modifier.align(Alignment.End))
    }
    if (pickerOpen) InsightOverlay(strings.text("选择笔记", "Select notes"), strings,
        onDismiss = controller::closePicker,
        canDismiss = !state.busy, tag = "chat_material_dialog") {
        val pickerOver = picker.characters > limit
        val enabled = !state.locked && connection.connected
        Text(strings.text("已选 ${picker.selected.size} 条 · ${picker.characters} / $limit 字",
            "${picker.selected.size} selected · ${picker.characters} / $limit chars"), fontSize = 13.sp,
            color = if (pickerOver) MaterialTheme.colorScheme.error else MoeriseColors.Muted,
            modifier = Modifier.testTag("chat_picker_budget"))
        WebTabs(listOf(strings.text("按日期", "By date"), strings.text("按标签 / 逐条", "By tag / individual")),
            if (picker.tab == "date") 0 else 1,
            { if (enabled) controller.picker.tab(if (it == 0) "date" else "tag") }, tagPrefix = "chat_picker_tab", enabled = enabled)
        val candidateList = if (picker.tab == "tag" && !picker.busy && picker.candidates.isNotEmpty())
            Modifier.testTag("chat_picker_candidates_ready") else Modifier
        Column(Modifier.weight(1f).fillMaxWidth().then(candidateList).verticalScroll(rememberScrollState()),
            verticalArrangement = Arrangement.spacedBy(8.dp)) {
            if (picker.tab == "date") InsightPickerCalendar(picker, controller.picker, strings, enabled)
            else FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                FilterChip(selected = picker.chosenTags.isEmpty(), onClick = { controller.picker.tag("") }, enabled = enabled,
                    label = { Text(strings.text("全部", "All")) }, modifier = Modifier.testTag("chat_picker_all_tags"))
                picker.tags.forEach { tag ->
                    FilterChip(selected = tag.tag in picker.chosenTags, onClick = { controller.picker.tag(tag.tag) }, enabled = enabled,
                        label = { Text(tag.tag) }, modifier = Modifier.testTag("chat_picker_tag_${tag.tag}"))
                }
            }
            if (!picker.busy && (picker.tab == "tag" || picker.from.isNotBlank() && picker.candidates.isNotEmpty())) {
                FlowRow(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween,
                    verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    Text(if (picker.tab == "date") strings.text("这段时间的笔记，勾选要用的", "Notes in this period, select to use")
                        else strings.text("共 ${picker.candidates.size} 条，勾选要用的", "${picker.candidates.size} notes, select to use"),
                        color = MoeriseColors.Muted, fontSize = 13.sp)
                    TextButton(onClick = controller.picker::selectAll, enabled = enabled && picker.candidates.isNotEmpty(),
                        modifier = Modifier.testTag("chat_picker_select_all")) {
                        Text(strings.text("全选这 ${picker.candidates.size} 条", "Select all ${picker.candidates.size}"))
                    }
                }
                picker.candidates.forEach { note ->
                    val chosen = picker.selected.any { it.name == note.name }
                    Row(Modifier.fillMaxWidth().testTag("chat_picker_note_${note.name}")
                        .clickable(enabled = enabled) { controller.picker.toggle(note) }.padding(vertical = 6.dp),
                        verticalAlignment = Alignment.CenterVertically) {
                        Checkbox(chosen, onCheckedChange = null, enabled = enabled)
                        Column(Modifier.weight(1f).padding(start = 8.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                            Text(insightPreview(note.content.ifBlank { note.name }, 24), fontSize = 14.sp)
                            Text(listOf(displayNoteDate(note.createTime, timezone, strings.text("zh", "en") == "en"),
                                note.tags.joinToString(" ") { "#$it" }).filter { it.isNotBlank() }.joinToString(" · "),
                                fontSize = 12.sp, color = MoeriseColors.Muted)
                        }
                    }
                }
            }
            if (picker.tab == "date" && picker.from.isNotBlank() && picker.candidates.isEmpty() && !picker.busy && picker.error == null)
                Text(strings.text("这段时间没有笔记。", "No notes in this period."), fontSize = 13.sp, color = MoeriseColors.Muted)
            if (picker.next.isNotBlank()) TextButton(onClick = controller.picker::loadMore,
                enabled = enabled && !picker.busy, modifier = Modifier.align(Alignment.End).testTag("chat_picker_more")) {
                Text(strings.text("加载更多", "Load more"))
            }
        }
        if (picker.busy || state.busy) LinearProgressIndicator(Modifier.fillMaxWidth())
        picker.error?.let { Text(insightError(it, strings), color = MaterialTheme.colorScheme.error,
            fontSize = 13.sp, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite }) }
        WebAction(strings.text("完成", "Done"), onClick = controller::completePicker,
            enabled = enabled && !picker.busy && !pickerOver,
            modifier = Modifier.align(Alignment.End).testTag("chat_apply_materials"))
    }
}

@Composable
private fun InsightPickerCalendar(picker: InsightPickerState, controller: InsightMaterialPicker, strings: Strings, enabled: Boolean) {
    if (picker.month.isBlank()) return
    val grid = monthGrid(picker.month)
    var choosingMonth by remember { mutableStateOf(false) }
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = { controller.month(shiftMonth(picker.month, -1)) },
                enabled = enabled && picker.month > "1000-01", modifier = Modifier.testTag("chat_picker_previous_month")) {
                WebIcon(WebIcon.ChevronLeft, strings.text("上个月", "Previous month"))
            }
            TextButton(onClick = { choosingMonth = !choosingMonth }, enabled = enabled,
                modifier = Modifier.weight(1f).testTag("chat_picker_month")) {
                Text(strings.text("${grid.year}年${grid.month}月", "${grid.year}-${grid.month.toString().padStart(2, '0')}"))
            }
            IconButton(onClick = { controller.month(shiftMonth(picker.month, 1)) },
                enabled = enabled && picker.month < "9999-12", modifier = Modifier.testTag("chat_picker_next_month")) {
                WebIcon(WebIcon.ChevronRight, strings.text("下个月", "Next month"))
            }
        }
        if (choosingMonth) {
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = { controller.year(grid.year - 1) }, enabled = enabled && grid.year > 1000,
                    modifier = Modifier.testTag("chat_picker_previous_year")) {
                    WebIcon(WebIcon.ChevronLeft, strings.text("上一年", "Previous year"))
                }
                Text(strings.text("${grid.year} 年", grid.year.toString()), modifier = Modifier.weight(1f).wrapContentWidth())
                IconButton(onClick = { controller.year(grid.year + 1) }, enabled = enabled && grid.year < 9999,
                    modifier = Modifier.testTag("chat_picker_next_year")) {
                    WebIcon(WebIcon.ChevronRight, strings.text("下一年", "Next year"))
                }
            }
            val months = strings.text("1月,2月,3月,4月,5月,6月,7月,8月,9月,10月,11月,12月",
                "Jan,Feb,Mar,Apr,May,Jun,Jul,Aug,Sep,Oct,Nov,Dec").split(',')
            repeat(4) { row ->
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                    repeat(3) { column ->
                        val month = row * 3 + column + 1
                        Surface(Modifier.weight(1f).testTag("chat_picker_month_$month"),
                            color = if (grid.month == month) MoeriseColors.BlueSurface else Color.Transparent,
                            shape = RoundedCornerShape(6.dp)) {
                            TextButton(onClick = {
                                controller.month("${grid.year}-${month.toString().padStart(2, '0')}")
                                choosingMonth = false
                            }, enabled = enabled) { Text(months[month - 1]) }
                        }
                    }
                }
            }
        } else {
            Row(Modifier.fillMaxWidth()) {
                strings.text("一,二,三,四,五,六,日", "M,T,W,T,F,S,S").split(',').forEach { day ->
                    Text(day, fontSize = 12.sp, color = MoeriseColors.Muted, modifier = Modifier.weight(1f).wrapContentWidth())
                }
            }
            val rows = (grid.offset + grid.days + 6) / 7
            repeat(rows) { row ->
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(2.dp)) {
                    repeat(7) { column ->
                        val day = row * 7 + column - grid.offset + 1
                        val date = "${picker.month}-${day.toString().padStart(2, '0')}"
                        val edge = date == picker.from || date == picker.through
                        val range = picker.from.isNotBlank() && picker.through.isNotBlank() && date > picker.from && date < picker.through
                        Surface(Modifier.weight(1f).heightIn(min = 44.dp), shape = RoundedCornerShape(6.dp),
                            color = if (edge) MoeriseColors.Blue else if (range) MoeriseColors.BlueSurface else Color.Transparent) {
                            if (day in 1..grid.days) TextButton(onClick = {
                                if (picker.from.isBlank() || picker.through.isNotBlank()) controller.date(date, "")
                                else controller.date(picker.from, date)
                            }, enabled = enabled, contentPadding = PaddingValues(0.dp),
                                modifier = Modifier.testTag("chat_picker_date_$date").semantics { contentDescription = date }) {
                                Text(day.toString(), fontSize = 13.sp,
                                    fontWeight = if (date in picker.calendarDays) FontWeight.Bold else FontWeight.Normal,
                                    color = if (edge) Color.White else MoeriseColors.Ink)
                            }
                        }
                    }
                }
            }
        }
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Text(when {
                picker.from.isBlank() -> strings.text("点选起止日期，筛出这段时间的笔记", "Select start and end dates to filter notes")
                picker.through.isBlank() -> strings.text("${picker.from} – 请选择结束日", "${picker.from} – Select an end date")
                else -> strings.text("${picker.from} – ${picker.through} · ${picker.candidates.size} 条",
                    "${picker.from} – ${picker.through} · ${picker.candidates.size} notes")
            }, fontSize = 13.sp, color = MoeriseColors.Muted, modifier = Modifier.weight(1f))
            if (picker.from.isNotBlank()) TextButton(onClick = controller::clearRange, enabled = enabled,
                modifier = Modifier.testTag("chat_picker_clear_range")) { Text(strings.text("清除", "Clear")) }
        }
    }
}

@Composable
private fun InsightOverlay(
    title: String, strings: Strings, onDismiss: () -> Unit, tag: String,
    canDismiss: Boolean = true, content: @Composable ColumnScope.() -> Unit,
) {
    val closeFocus = remember { FocusRequester() }
    Dialog(onDismissRequest = { if (canDismiss) onDismiss() },
        properties = DialogProperties(usePlatformDefaultWidth = false, dismissOnClickOutside = false, dismissOnBackPress = canDismiss)) {
        BoxWithConstraints(Modifier.fillMaxSize().safeDrawingPadding().imePadding().padding(16.dp),
            contentAlignment = Alignment.Center) {
            Surface(Modifier.widthIn(max = 520.dp).fillMaxWidth().height(minOf(640.dp, maxHeight * .86f))
                .platformTestTags().testTag(tag),
                shape = RoundedCornerShape(14.dp), color = Color.White, border = BorderStroke(1.dp, MoeriseColors.Line)) {
                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.SpaceBetween) {
                        Text(title, modifier = Modifier.weight(1f), fontSize = 16.sp, fontWeight = FontWeight.SemiBold)
                        IconButton(onClick = onDismiss, enabled = canDismiss, modifier = Modifier.focusRequester(closeFocus).testTag("${tag}_close")) {
                            WebIcon(WebIcon.Close, strings.text("关闭", "Close"))
                        }
                    }
                    content()
                }
            }
        }
        LaunchedEffect(Unit) { closeFocus.requestFocus() }
    }
}

@Composable
private fun InsightPlainAction(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true) {
    OutlinedButton(onClick = onClick, modifier = modifier.heightIn(min = 44.dp),
        enabled = enabled, shape = RoundedCornerShape(10.dp), border = BorderStroke(1.dp, MoeriseColors.Line),
        contentPadding = PaddingValues(horizontal = 12.dp, vertical = 8.dp),
        colors = ButtonDefaults.outlinedButtonColors(contentColor = MoeriseColors.Ink)) {
        Text(text, fontSize = 13.sp)
    }
}

@Composable
private fun InsightTurn(turn: ChatTurn, mobile: MobileController, strings: Strings, enabled: Boolean) {
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(14.dp)) {
        BoxWithConstraints(Modifier.fillMaxWidth()) {
            Surface(modifier = Modifier.align(Alignment.CenterEnd).widthIn(max = maxWidth * .8f),
                shape = RoundedCornerShape(14.dp), color = Color(0xFFE3ECFA)) {
                Text(if (turn.kind == "insight") strings.text("一键洞察", "Generate insights") else turn.user.content,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp), fontSize = 15.sp,
                    color = Color(0xFF1E4A9A))
            }
        }
        BoxWithConstraints(Modifier.fillMaxWidth()) {
            Column(Modifier.widthIn(max = maxWidth * .92f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                Surface(shape = RoundedCornerShape(14.dp), color = Color.White, border = BorderStroke(1.dp, MoeriseColors.Line)) {
                    Box(Modifier.padding(horizontal = 18.dp, vertical = 14.dp)) { MarkdownBody(turn.assistant.content) }
                }
                TextButton(onClick = { mobile.insights.save(turn) },
                    enabled = enabled && turn.assistant.status == "completed" && turn.assistant.savedName.isBlank(),
                    contentPadding = PaddingValues(horizontal = 8.dp),
                    modifier = Modifier.testTag("chat_save_${turn.requestId}")) {
                    Text(if (turn.assistant.savedName.isNotBlank()) strings.text("已保存为笔记", "Saved as note")
                        else strings.text("保存为笔记", "Save as note"), fontSize = 12.sp, color = MoeriseColors.Muted)
                }
            }
        }
    }
}

private fun insightCharacters(text: String): Int =
    text.indices.count { index -> !text[index].isLowSurrogate() || index == 0 || !text[index - 1].isHighSurrogate() }

private fun insightPreview(text: String, count: Int): String {
    var index = 0
    var characters = 0
    while (index < text.length && characters < count) {
        index += if (text[index].isHighSurrogate() && index + 1 < text.length && text[index + 1].isLowSurrogate()) 2 else 1
        characters++
    }
    return text.substring(0, index)
}

fun insightError(code: String, strings: Strings): String = when (code) {
    "CHAT_MATERIAL_REQUIRED" -> strings.text("请先选择笔记材料。", "Select note materials first.")
    "CHAT_MATERIAL_LIMIT" -> strings.text("超过字数上限，请减少所选笔记。", "Over the character limit. Select fewer notes.")
    "CHAT_RESOURCE_LIMIT" -> strings.text("材料请求超过资源限制，请减少选择范围。", "The material request exceeds resource limits. Select a smaller range.")
    "SOURCE_CHANGED" -> strings.text("所选材料已修改，请重新选择这些笔记。", "The selected notes changed. Select them again.")
    "SOURCE_NOT_FOUND" -> strings.text("所选材料已删除或无法访问，请调整材料。", "Selected notes were deleted or are unavailable. Adjust the materials.")
    "CHAT_CONFLICT" -> strings.text("会话已在其他页面更改，请重新读取。", "This conversation changed elsewhere. Reload it.")
    "CHAT_BUSY" -> strings.text("该会话正在生成，请等待或停止后继续。", "This conversation is generating. Wait or stop it to continue.")
    "CHAT_NOT_FOUND" -> strings.text("该会话已删除或无法访问。", "This conversation was deleted or is unavailable.")
    "CHAT_PROTOCOL", "CHAT_INTERRUPTED", "LLM_PROTOCOL" -> strings.text("回复已中断，已保留收到的内容。请重新读取会话后继续。", "The reply was interrupted. Received content is saved. Reload the conversation to continue.")
    "CHAT_CANCELED" -> strings.text("已停止生成，已保留收到的内容。", "Generation stopped. Received content is saved.")
    "LLM_DISABLED" -> strings.text("请在设置中启用 LLM。", "Enable the LLM in settings.")
    "CHAT_OUTCOME_UNKNOWN" -> strings.text("请先检查操作结果。", "Check the operation result first.")
    "CHAT_DELETE_REQUIRES_CONFIRMATION" -> strings.text("会话仍然存在，可以再次确认删除。", "This conversation still exists. You can confirm deletion again.")
    "CONTENT_TOO_LARGE" -> strings.text("回复超过笔记正文上限，无法完整保存。请让 AI 总结后保存总结。", "The reply exceeds the note limit. Ask AI for a summary and save it.")
    "LOCAL_SAVE_FAILED" -> strings.text("本机保存失败，请释放存储空间后重试。", "Could not save on this device. Free up storage and retry.")
    else -> connectionError(code, strings)
}

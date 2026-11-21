package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.*
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.fermin214.moerise.domain.MaterialCandidate

@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun InsightPickerDialog(insights: InsightController, state: InsightState, picker: InsightPickerState,
    timezone: String, strings: Strings, online: Boolean, read: (String) -> Unit) {
    val controller = insights.picker
    var page by remember { mutableStateOf("list") }
    var draftFrom by remember { mutableStateOf("") }
    var draftThrough by remember { mutableStateOf("") }
    val list = rememberLazyListState()
    val selectedList = rememberLazyListState()
    val selected = picker.selected.mapTo(mutableSetOf()) { it.name }
    val enabled = online && !state.locked
    val back = { if (page == "list") insights.closePicker() else page = "list" }
    val title = when (page) {
        "calendar" -> strings.text("选择日期", "Choose dates")
        "selected" -> strings.text("查看已选", "Selected notes")
        else -> strings.text("选择笔记", "Select notes")
    }
    InsightOverlay(title, strings, back, "chat_material_dialog") {
        if (page == "calendar") {
            Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
                PickerCalendar(picker, draftFrom, draftThrough, controller, strings) { date ->
                    if (draftFrom.isEmpty() || draftThrough.isNotEmpty()) { draftFrom = date; draftThrough = "" }
                    else draftThrough = date
                }
            }
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                TextButton({ draftFrom = ""; draftThrough = "" }) { Text(strings.text("清除", "Clear")) }
                WebAction(strings.text("应用日期", "Apply dates"), {
                    controller.date(draftFrom, draftThrough); page = "list"
                }, enabled = enabled, modifier = Modifier.testTag("chat_picker_apply_dates"))
            }
        } else {
            if (page == "list") {
                WebTabs(listOf(strings.text("日期", "Date"), strings.text("标签", "Tags")),
                    if (picker.tab == "date") 0 else 1, { controller.tab(if (it == 0) "date" else "tag") },
                    tagPrefix = "chat_picker_tab", enabled = enabled)
                if (picker.tab == "date") TextButton({
                    draftFrom = picker.from; draftThrough = picker.through; page = "calendar"
                }, modifier = Modifier.testTag("chat_picker_open_dates")) {
                    Text(if (picker.from.isEmpty()) strings.text("选择日期", "Choose dates") else
                        listOf(picker.from, picker.through).filter { it.isNotEmpty() }.distinct().joinToString(" – "))
                } else Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    FilterChip(selected = picker.chosenTags.isEmpty(), onClick = { controller.tag("") },
                        label = { Text(strings.text("全部", "All")) }, modifier = Modifier.testTag("chat_picker_all_tags"))
                    picker.tags.forEach { tag ->
                        FilterChip(selected = tag.tag in picker.chosenTags, onClick = { controller.tag(tag.tag) },
                            label = { Text("#${tag.tag}") }, modifier = Modifier.testTag("chat_picker_tag_${tag.tag}"))
                    }
                }
                if (picker.candidates.isNotEmpty() && picker.batch.isEmpty()) {
                    val matching = picker.candidates.count { it.name in selected }
                    FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        if (!picker.exhausted || matching < picker.candidates.size)
                            TextButton({ controller.bulk(true) }, modifier = Modifier.testTag("chat_picker_select_all")) {
                                Text(strings.text("全选", "Select all"))
                            }
                        if (matching > 0 || !picker.exhausted && selected.isNotEmpty())
                            TextButton({ controller.bulk(false) }) { Text(strings.text("取消全选", "Deselect all")) }
                    }
                }
            }
            val refreshing = page == "list" && picker.candidates.isEmpty() && picker.previousCandidates.isNotEmpty()
            val rows = if (page == "list") picker.candidates.ifEmpty { picker.previousCandidates } else picker.selected
            val listState = if (page == "list") list else selectedList
            LaunchedEffect(picker.tab, picker.from, picker.through, picker.chosenTags) {
                if (list.firstVisibleItemIndex != 0) list.scrollToItem(0)
            }
            LazyColumn(Modifier.weight(1f).fillMaxWidth().then(
                if (rows.isNotEmpty()) Modifier.testTag("chat_picker_candidates_ready") else Modifier), state = listState) {
                if (picker.changed.isNotEmpty()) {
                    item { Text(strings.text("请重选已变化的笔记", "Reselect changed notes"),
                        fontSize = 13.sp, color = MaterialTheme.colorScheme.error) }
                    items(picker.changed, key = { "changed/$it" }) { name ->
                        TextButton({ controller.reselect(name) }, enabled = enabled) { Text(name) }
                    }
                }
                items(rows, key = { it.name }) { note ->
                    PickerRow(note, note.name in selected, enabled && !refreshing, timezone, strings, { controller.toggle(note) },
                        if (page == "selected") { { read(note.name) } } else null)
                }
                if (page == "list") {
                    if (picker.error != null) item {
                        InsightError(picker.error, strings)
                        if (!picker.exhausted && picker.error != "CHAT_PROTOCOL") TextButton(controller::loadMore) {
                            Text(strings.text("重试", "Retry"))
                        }
                    } else if (picker.candidates.isEmpty() && picker.exhausted) item {
                        Text(strings.text("没有匹配的笔记。", "No matching notes."), color = MoeriseColors.Muted)
                    } else if (!picker.exhausted && picker.batch.isEmpty()) item {
                        LaunchedEffect(picker.next, picker.busy, picker.manualOnly) {
                            if (picker.next.isNotBlank() && !picker.busy) controller.loadMore()
                        }
                        if (picker.busy) Text(strings.text("正在读取…", "Loading…"), color = MoeriseColors.Muted)
                    }
                }
            }
            if (picker.batch.isNotEmpty()) {
                if (picker.error == null) Text(strings.text("正在统计 · ${picker.candidates.size} 条", "Counting · ${picker.candidates.size} notes"),
                    modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
                TextButton(controller::stopBatch, modifier = Modifier.testTag("chat_picker_stop_batch")) {
                    Text(strings.text("停止批量选择，改为逐条勾选", "Stop bulk selection and select individually"))
                }
            }
            Text(strings.text("已选 ${selected.size} 条 · ${picker.characters} / ${picker.limit} 字",
                "${selected.size} selected · ${picker.characters} / ${picker.limit} chars"),
                fontSize = 13.sp, color = if (picker.characters > picker.limit) MaterialTheme.colorScheme.error else MoeriseColors.Muted,
                modifier = Modifier.testTag("chat_picker_budget"))
            val clear = selected.isEmpty() && state.chat?.materials?.isNotEmpty() == true
            FlowRow(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                TextButton({ page = if (page == "list") "selected" else "list" }, modifier = Modifier.testTag("chat_picker_selected")) {
                    Text(if (page == "list") strings.text("查看已选", "View selected") else strings.text("返回清单", "Back to list"))
                }
                WebAction(if (clear) strings.text("清空材料", "Clear materials") else strings.text("应用所选 ${selected.size} 条", "Apply ${selected.size} selected"),
                    insights::completePicker, enabled = enabled && picker.batch.isEmpty() && (clear || selected.isNotEmpty()) &&
                        selected.size <= 1000 && picker.characters <= picker.limit,
                    modifier = Modifier.testTag("chat_apply_materials"))
            }
        }
    }
}

@Composable
private fun PickerRow(note: MaterialCandidate, checked: Boolean, enabled: Boolean, timezone: String, strings: Strings,
    toggle: () -> Unit, read: (() -> Unit)?) {
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        Row(Modifier.weight(1f).testTag("chat_picker_note_${note.name}").toggleable(checked, enabled, role = Role.Checkbox) { toggle() }
            .padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            Checkbox(checked, onCheckedChange = null)
            Column(Modifier.padding(start = 8.dp)) {
                Text(note.preview.ifBlank { note.name }, maxLines = 2, fontSize = 14.sp)
                Text(displayNoteDate(note.createTime, timezone, strings.text("zh", "en") == "en"), fontSize = 12.sp, color = MoeriseColors.Muted)
            }
        }
        if (read != null) TextButton(read, modifier = Modifier.testTag("chat_picker_read_${note.name}")) { Text(strings.text("查看原文", "Read note")) }
    }
}

@Composable
private fun PickerCalendar(picker: InsightPickerState, from: String, through: String, controller: InsightMaterialPicker,
    strings: Strings, select: (String) -> Unit) {
    val grid = monthGrid(picker.month)
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        IconButton({ controller.month(shiftMonth(picker.month, -1)) }, enabled = picker.month > "1000-01",
            modifier = Modifier.testTag("chat_picker_previous_month")) { WebIcon(WebIcon.ChevronLeft, strings.text("上个月", "Previous month")) }
        Text(picker.month, modifier = Modifier.weight(1f).wrapContentWidth())
        IconButton({ controller.month(shiftMonth(picker.month, 1)) }, enabled = picker.month < "9999-12",
            modifier = Modifier.testTag("chat_picker_next_month")) { WebIcon(WebIcon.ChevronRight, strings.text("下个月", "Next month")) }
    }
    Row(Modifier.fillMaxWidth()) {
        strings.text("一,二,三,四,五,六,日", "M,T,W,T,F,S,S").split(',').forEach {
            Text(it, fontSize = 12.sp, color = MoeriseColors.Muted, modifier = Modifier.weight(1f).wrapContentWidth())
        }
    }
    repeat((grid.offset + grid.days + 6) / 7) { row ->
        Row(Modifier.fillMaxWidth()) {
            repeat(7) { column ->
                val day = row * 7 + column - grid.offset + 1
                val date = "${picker.month}-${day.toString().padStart(2, '0')}"
                if (day !in 1..grid.days) Spacer(Modifier.weight(1f))
                else {
                    val count = picker.calendarDays?.get(date) ?: picker.calendarDays?.let { 0 }
                    val edge = date == from || date == through
                    val within = through.isNotEmpty() && date in minOf(from, through)..maxOf(from, through)
                    val surface = when (count) {
                        null -> MoeriseColors.Paper
                        0 -> Color.White
                        1 -> MoeriseColors.BlueSurface
                        else -> Color(0xFFB3D1FC)
                    }
                    val shape = RoundedCornerShape(6.dp)
                    Box(Modifier.weight(1f).heightIn(min = 44.dp).background(surface, shape)
                        .then(if (edge) Modifier.border(BorderStroke(2.dp, MoeriseColors.Blue), shape) else Modifier)
                        .testTag("chat_picker_date_$date").clickable { select(date) }.semantics {
                            contentDescription = "$date · " + (count?.let { strings.text("$it 条笔记", "$it notes") }
                                ?: strings.text("数量未知", "Count unknown"))
                            selected = edge || within
                        }, contentAlignment = Alignment.Center) {
                        Text(day.toString(), fontSize = 13.sp)
                    }
                }
            }
        }
    }
    if (from.isNotEmpty()) Text(listOf(from, through).filter { it.isNotBlank() }.sorted().distinct().joinToString(" – "),
        color = MoeriseColors.Muted)
}

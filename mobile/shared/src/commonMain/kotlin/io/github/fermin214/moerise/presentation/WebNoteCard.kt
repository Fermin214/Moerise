package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.domain.Note

@Composable
fun WebNoteCard(
    note: Note, timezone: String, strings: Strings, core: CoreClient?,
    onTag: (String) -> Unit, modifier: Modifier = Modifier,
    onEdit: (() -> Unit)? = null, onState: (() -> Unit)? = null, onDelete: (() -> Unit)? = null,
    onWalk: (() -> Unit)? = null, onInsight: (() -> Unit)? = null,
    online: Boolean = true, busy: Boolean = false, error: String? = null,
    pendingAction: Boolean = false, deleteUnknown: Boolean = false, onCheckDeletion: (() -> Unit)? = null,
) {
    val presentation = remember(note.content, note.tags) { notePresentation(note.content, note.tags) }
    var confirmDelete by remember(note.name) { mutableStateOf(false) }
    val menuActions = buildList {
        onEdit?.let { add(NoteMenuAction(strings.text("编辑", "Edit"), it, !busy && !pendingAction, tag = "edit_note_${note.name}")) }
        onState?.let { add(NoteMenuAction(
            if (pendingAction && !deleteUnknown) strings.text("重试操作", "Retry action")
            else if (note.state == "ARCHIVED") strings.text("恢复到主页", "Restore to home") else strings.text("归档", "Archive"),
            it, online && !busy && !deleteUnknown, tag = "archive_note_${note.name}")) }
        if (note.state == "NORMAL") onWalk?.let { add(NoteMenuAction(strings.text("从这条漫步", "Walk from this note"), it, online && !busy, tag = "walk_note_${note.name}")) }
        onInsight?.let { add(NoteMenuAction(strings.text("洞察这条", "Explore this note"), it, online && !busy, tag = "insight_note_${note.name}")) }
        if (deleteUnknown) onCheckDeletion?.let { add(NoteMenuAction(strings.text("检查删除结果", "Check deletion"), it, online && !busy, tag = "check_delete_${note.name}")) }
        onDelete?.let { add(NoteMenuAction(strings.text("删除", "Delete"), { confirmDelete = true }, online && !busy && !pendingAction, danger = true, tag = "delete_note_${note.name}")) }
    }
    Surface(modifier.fillMaxWidth().testTag("note_card_${note.name}"),
        shape = RoundedCornerShape(14.dp), color = Color.White,
        border = BorderStroke(1.dp, MoeriseColors.Line)) {
        Column(Modifier.padding(start = 18.dp, top = 14.dp, end = 18.dp, bottom = 16.dp)) {
            Row(Modifier.fillMaxWidth().heightIn(min = 32.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(displayNoteDate(note.createTime, timezone, strings.text("zh", "en") == "en"),
                    fontSize = 13.sp, color = MoeriseColors.Muted,
                    modifier = Modifier.weight(1f).heightIn(min = 44.dp).wrapContentHeight())
                WebNoteMenu(strings, menuActions, tag = "note_more_${note.name}")
            }
            if (presentation.body.isNotBlank()) Box(Modifier.fillMaxWidth().padding(top = 4.dp, bottom = 10.dp)) {
                MarkdownBody(presentation.body, note = note, core = core)
            }
            NoteAttachments(note, core, strings)
            NoteTags(presentation.tags, onTag = onTag)
            error?.let { Text(editorError(it, strings), color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(top = 8.dp)) }
            if (busy) LinearProgressIndicator(Modifier.fillMaxWidth().padding(top = 8.dp))
            if (confirmDelete) {
                HorizontalDivider(Modifier.padding(top = 10.dp), color = MoeriseColors.Line)
                Text(strings.text("删除这条笔记？删除后无法恢复。", "Delete this note? This cannot be undone."),
                    style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 8.dp))
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
                    TextButton(onClick = { confirmDelete = false }, enabled = !busy,
                        modifier = Modifier.testTag("delete_cancel_${note.name}")) { Text(strings.text("取消", "Cancel")) }
                    TextButton(onClick = { confirmDelete = false; onDelete?.invoke() }, enabled = online && !busy && !pendingAction,
                        modifier = Modifier.testTag("delete_confirm_${note.name}")) { Text(strings.text("删除", "Delete"), color = MaterialTheme.colorScheme.error) }
                }
            }
        }
    }
}

data class NoteMenuAction(val label: String, val onClick: () -> Unit, val enabled: Boolean = true, val danger: Boolean = false,
    val tag: String? = null)

@Composable
fun WebNoteMenu(strings: Strings, actions: List<NoteMenuAction>, tag: String = "note_more") {
    var open by remember { mutableStateOf(false) }
    Box {
        IconButton(onClick = { open = !open }, modifier = Modifier.size(44.dp).testTag(tag).semantics {
            stateDescription = if (open) strings.text("已展开", "Expanded") else strings.text("已收起", "Closed")
        }) {
            WebIcon(WebIcon.More, strings.text("更多操作", "More actions"), Modifier.size(16.dp))
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false },
            modifier = Modifier.platformTestTags()) {
            actions.forEach { action ->
                DropdownMenuItem(
                    text = { Text(action.label, color = if (action.danger) MaterialTheme.colorScheme.error else MoeriseColors.Ink) },
                    enabled = action.enabled, onClick = { open = false; action.onClick() },
                    modifier = Modifier.heightIn(min = 44.dp).then(action.tag?.let { Modifier.testTag(it) } ?: Modifier),
                )
            }
        }
    }
}

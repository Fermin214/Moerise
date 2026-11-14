package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp

@Composable
fun NoteDetailScreen(state: NoteDetailState, mobile: MobileController, strings: Strings, scroll: ScrollState) {
    var confirmDelete by remember(state.name) { mutableStateOf(false) }
    Column(Modifier.safeDrawingPadding().verticalScroll(scroll).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        TextButton(onClick = mobile::closeNote, modifier = Modifier.testTag("detail_back")) { Text(strings.text("返回列表", "Back to notes")) }
        if (state.loading) LinearProgressIndicator(Modifier.fillMaxWidth())
        state.error?.let { Text(editorError(it, strings), color = MaterialTheme.colorScheme.error) }
        val note = state.note
        if (note != null) {
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = { mobile.editNote(note) }, modifier = Modifier.testTag("note_edit"), enabled = !state.busy && !state.pendingAction) { Text(strings.text("编辑", "Edit")) }
                TextButton(onClick = mobile.detail::changeState, modifier = Modifier.testTag("note_archive"), enabled = !state.busy && !state.deleteUnknown) {
                    Text(if (state.pendingAction) strings.text("重试操作", "Retry action")
                        else if (note.state == "ARCHIVED") strings.text("恢复", "Restore") else strings.text("归档", "Archive"))
                }
                TextButton(onClick = { confirmDelete = true }, modifier = Modifier.testTag("note_delete"), enabled = !state.busy && !state.pendingAction) { Text(strings.text("删除", "Delete")) }
            }
            if (state.deleteUnknown) TextButton(onClick = mobile.detail::checkDeletion, enabled = !state.busy) { Text(strings.text("检查删除结果", "Check deletion")) }
            if (state.error == "SOURCE_CHANGED") TextButton(onClick = { mobile.detail.open(note.name) }) { Text(strings.text("重新读取", "Read again")) }
            Text(note.createTime, style = MaterialTheme.typography.labelSmall)
            MarkdownBody(note.content, modifier = Modifier.testTag("note_detail"), note = note, core = mobile.session.core)
            mobile.session.core?.let { core ->
                note.attachments.forEach { item -> OpenAttachmentButton(note, item, core, strings) }
            }
        } else if (!state.loading && state.name != null) TextButton(onClick = { mobile.detail.open(state.name) }) { Text(strings.text("重试读取", "Read again")) }
    }
    if (confirmDelete) AlertDialog(
        onDismissRequest = { confirmDelete = false },
        title = { Text(strings.text("删除这篇笔记？", "Delete this note?")) },
        confirmButton = { TextButton(onClick = { confirmDelete = false; mobile.detail.delete() }, modifier = Modifier.testTag("delete_confirm")) { Text(strings.text("删除", "Delete")) } },
        dismissButton = { TextButton(onClick = { confirmDelete = false }) { Text(strings.text("取消", "Cancel")) } },
    )
}

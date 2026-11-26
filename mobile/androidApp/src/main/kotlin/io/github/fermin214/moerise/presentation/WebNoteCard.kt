package io.github.fermin214.moerise.presentation

import android.content.ClipData
import android.content.ClipboardManager
import android.os.Build
import android.widget.Toast
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.DraftEntity
import io.github.fermin214.moerise.data.attachmentEdits
import io.github.fermin214.moerise.domain.Note
import kotlinx.serialization.json.jsonPrimitive

@Composable
fun TimelineNoteCard(
    entry: TimelineEntry,
    timezone: String,
    strings: Strings,
    core: CoreClient?,
    onTag: (String) -> Unit,
    onOpenLocal: (DraftEntity) -> Unit,
    modifier: Modifier = Modifier,
    onEdit: ((Note) -> Unit)? = null,
    onState: ((Note) -> Unit)? = null,
    onDelete: ((Note) -> Unit)? = null,
    onWalk: ((Note) -> Unit)? = null,
    online: Boolean = true,
    busy: Boolean = false,
    error: String? = null,
    pendingAction: Boolean = false,
    deleteUnknown: Boolean = false,
    onCheckDeletion: ((Note) -> Unit)? = null,
) {
    val draft = (entry as? TimelineEntry.Local)?.draft
    val remote = (entry as? TimelineEntry.Remote)?.note
    val content = draft?.content ?: requireNotNull(remote).content
    val edits = remember(draft?.attachmentsJson) { draft?.attachmentEdits() }
    val baseline = remember(draft?.baselineJson, edits) {
        draft?.baselineJson?.let { runCatching { CoreJson.decodeFromString<Note>(it) }.getOrNull() }?.let { note ->
            note.copy(attachments = note.attachments.filterNot { it["name"]?.jsonPrimitive?.content in edits?.removed.orEmpty() })
        }
    }
    val note = remote ?: baseline
    val presentation = rememberNotePresentation(content, remote?.tags)
    val date = note?.createTime?.let { displayNoteDate(it, timezone, strings.text("zh", "en") == "en") }
        ?: displayLocalNoteDate(draft?.createdAt ?: 0L, timezone, strings.text("zh", "en") == "en")
    val hasFailure = draft?.conflictJson != null
    val id = draft?.id ?: requireNotNull(remote).name
    val leaveWriting = LocalLeaveWriting.current
    val context = LocalContext.current
    var confirmDelete by remember(remote?.name) { mutableStateOf(false) }
    val menuActions = buildList {
        if (draft != null) add(NoteMenuAction(
            if (hasFailure) strings.text("处理冲突", "Resolve conflict") else strings.text("编辑", "Edit"),
            { onOpenLocal(draft) }, tag = "edit_note_$id",
        ))
        if (remote != null) {
            onEdit?.let { add(NoteMenuAction(strings.text("编辑", "Edit"), { it(remote) }, !busy && !pendingAction, tag = "edit_note_$id")) }
            onState?.let { add(NoteMenuAction(
                if (pendingAction && !deleteUnknown) strings.text("重试操作", "Retry action")
                else if (remote.state == "ARCHIVED") strings.text("恢复到主页", "Restore to home") else strings.text("归档", "Archive"),
                { it(remote) }, online && !busy && !deleteUnknown, tag = "archive_note_$id")) }
            if (remote.state == "NORMAL") onWalk?.let { add(NoteMenuAction(strings.text("从这条漫步", "Walk from this note"), { it(remote) }, online && !busy, tag = "walk_note_$id")) }
            if (deleteUnknown) onCheckDeletion?.let { add(NoteMenuAction(strings.text("检查删除结果", "Check deletion"), { it(remote) }, online && !busy, tag = "check_delete_$id")) }
        }
        add(NoteMenuAction(strings.text("复制", "Copy"), {
            val clipboard = context.getSystemService(ClipboardManager::class.java)
            val copied = runCatching { requireNotNull(clipboard).setPrimaryClip(ClipData.newPlainText("Moerise", content)) }.isSuccess
            if (!copied || Build.VERSION.SDK_INT < 33) Toast.makeText(context,
                if (copied) strings.text("已复制", "Copied") else strings.text("复制失败", "Copy failed"), Toast.LENGTH_SHORT).show()
        }, tag = "copy_note_$id"))
        if (remote != null && onDelete != null) add(NoteMenuAction(strings.text("删除", "Delete"), { confirmDelete = true },
            online && !busy && !pendingAction, danger = true, tag = "delete_note_$id"))
    }
    NoteCardScaffold(
        date = date,
        content = content,
        presentation = presentation,
        strings = strings,
        modifier = modifier.testTag(if (draft != null) "queued_note_$id" else "note_card_$id"),
        core = core,
        note = note,
        onTag = onTag.takeIf { remote != null },
        headerActions = if (hasFailure) 2 else 1,
        trailingHeader = {
            if (hasFailure) {
                Box(
                    Modifier.size(44.dp).clickable(role = Role.Button) { leaveWriting(); onOpenLocal(draft) }
                        .testTag("queued_error_${draft.id}")
                        .semantics { contentDescription = strings.text("同步失败或冲突", "Sync failed or conflicted") },
                    contentAlignment = Alignment.Center,
                ) {
                    Box(
                        Modifier.size(20.dp).background(Color(0xFFB42318), CircleShape),
                        contentAlignment = Alignment.Center,
                    ) {
                        Text("!", color = Color.White, fontSize = 12.sp, fontWeight = FontWeight.Bold, lineHeight = 12.sp)
                    }
                }
            }
            WebNoteMenu(strings, menuActions, tag = "note_more_$id")
        },
        extraContent = {
            edits?.staged.orEmpty().forEach {
                Text(it.filename, fontSize = 13.sp, color = MoeriseColors.Muted, modifier = Modifier.padding(top = 8.dp))
            }
            error?.let { Text(editorError(it, strings), color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(top = 8.dp)) }
            if (busy) LinearProgressIndicator(Modifier.fillMaxWidth().padding(top = 8.dp))
            if (confirmDelete) {
                HorizontalDivider(Modifier.padding(top = 10.dp), color = MoeriseColors.Line)
                Text(strings.text("删除这条笔记？删除后无法恢复。", "Delete this note? This cannot be undone."),
                    style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 8.dp))
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
                    TextButton(onClick = { confirmDelete = false }, enabled = !busy,
                        modifier = Modifier.testTag("delete_cancel_$id")) { Text(strings.text("取消", "Cancel")) }
                    TextButton(onClick = { confirmDelete = false; remote?.let { onDelete?.invoke(it) } }, enabled = online && !busy && !pendingAction,
                        modifier = Modifier.testTag("delete_confirm_$id")) { Text(strings.text("删除", "Delete"), color = MaterialTheme.colorScheme.error) }
                }
            }
        },
    )
}

@Composable
private fun NoteCardScaffold(
    date: String,
    content: String,
    presentation: NotePresentation,
    strings: Strings,
    modifier: Modifier,
    core: CoreClient?,
    note: Note?,
    onTag: ((String) -> Unit)?,
    headerActions: Int,
    trailingHeader: @Composable RowScope.() -> Unit,
    extraContent: @Composable ColumnScope.() -> Unit,
) {
    Surface(modifier.fillMaxWidth(),
        shape = RoundedCornerShape(14.dp), color = Color.White,
        border = BorderStroke(1.dp, MoeriseColors.Line)) {
        Box {
            Column(Modifier.padding(start = 18.dp, end = 18.dp, bottom = 14.dp)) {
                Box(Modifier.fillMaxWidth().heightIn(min = if (headerActions > 0) 42.dp else 0.dp)
                    .padding(top = 10.dp, end = (headerActions * 44).dp)) {
                    Text(date, fontSize = 13.sp, color = MoeriseColors.Muted, modifier = Modifier.testTag("note_card_date"))
                }
                if (presentation.body.isNotBlank()) Box(Modifier.fillMaxWidth().padding(top = 2.dp, bottom = 8.dp).testTag("note_card_body")) {
                    MarkdownBody(presentation.body, note = note, core = core, tags = presentation.inlineTags, onTag = onTag,
                        originalContent = content)
                }
                if (note != null) NoteAttachments(note, core, strings = strings)
                NoteTags(presentation.tags, onTag = onTag)
                extraContent()
            }
            if (headerActions > 0) {
                Row(Modifier.align(Alignment.TopEnd).padding(end = 6.dp), content = trailingHeader)
            }
        }
    }
}

data class NoteMenuAction(val label: String, val onClick: () -> Unit, val enabled: Boolean = true, val danger: Boolean = false,
    val tag: String? = null)

@Composable
fun WebNoteMenu(strings: Strings, actions: List<NoteMenuAction>, tag: String = "note_more") {
    val leaveWriting = LocalLeaveWriting.current
    var open by remember { mutableStateOf(false) }
    Box {
        Box(Modifier.size(44.dp).clip(CircleShape).clickable(role = Role.Button) {
            if (!open) leaveWriting()
            open = !open
        }.testTag(tag).semantics {
            stateDescription = if (open) strings.text("已展开", "Expanded") else strings.text("已收起", "Closed")
        }, contentAlignment = Alignment.Center) {
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

package io.github.fermin214.moerise.presentation

import android.content.ClipData
import android.content.ClipboardManager
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.HorizontalAlignmentLine
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.LineHeightStyle
import androidx.compose.ui.unit.Constraints
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
    stateActionSending: Boolean = false,
    deleteUnknown: Boolean = false,
    onCheckDeletion: ((Note) -> Unit)? = null,
    expanded: Boolean? = null,
    onExpand: (Boolean) -> Unit = {},
    today: String = serviceToday(timezone),
    first: Boolean = true,
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
    val english = strings.text("zh", "en") == "en"
    val date = note?.createTime?.let { displayTimelineTime(it, timezone, today, english) }
        ?: displayLocalTimelineTime(draft?.createdAt ?: 0L, timezone, today, english)
    val hasFailure = draft?.conflictJson != null
    val id = draft?.id ?: requireNotNull(remote).name
    val leaveWriting = LocalLeaveWriting.current
    val context = LocalContext.current
    val notice = LocalMobileNotice.current
    val haptic = rememberMoeriseHaptic()
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
                else if (remote.state == "ARCHIVED") strings.text("恢复", "Restore") else strings.text("归档", "Archive"),
                { it(remote) }, online && !busy && !stateActionSending && !deleteUnknown, tag = "archive_note_$id")) }
            if (remote.state == "NORMAL") onWalk?.let { add(NoteMenuAction(strings.text("从这条漫步", "Walk from this note"), { it(remote) }, online && !busy, tag = "walk_note_$id")) }
            if (deleteUnknown) onCheckDeletion?.let { add(NoteMenuAction(strings.text("检查删除结果", "Check deletion"), { it(remote) }, online && !busy, tag = "check_delete_$id")) }
        }
        add(NoteMenuAction(strings.text("复制", "Copy"), {
            val clipboard = context.getSystemService(ClipboardManager::class.java)
            val copied = runCatching { requireNotNull(clipboard).setPrimaryClip(ClipData.newPlainText("Moerise", content)) }.isSuccess
            if (copied) haptic()
            notice(if (copied) "COPIED" else "COPY_FAILED")
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
        expanded = expanded,
        onExpand = onExpand,
        first = first,
        colophonActions = {
            if (hasFailure) {
                Box(
                    Modifier.size(44.dp).clickable(role = Role.Button) { leaveWriting(); onOpenLocal(draft) }
                        .testTag("queued_error_${draft.id}")
                        .semantics { contentDescription = strings.text("同步失败或冲突", "Sync failed or conflicted") },
                    contentAlignment = Alignment.TopCenter,
                ) {
                    Box(
                        Modifier.padding(top = 8.dp).size(20.dp).background(MoeriseColors.Danger, CircleShape),
                        contentAlignment = Alignment.Center,
                    ) {
                        Text("!", color = MoeriseColors.OnDanger, fontSize = 13.sp, fontWeight = FontWeight.Bold, lineHeight = 12.sp)
                    }
                }
            }
            WebNoteMenu(strings, menuActions, tag = "note_more_$id")
        },
        stagedAttachments = {
            edits?.staged.orEmpty().forEach {
                Text(it.filename, fontSize = 13.sp, color = MoeriseColors.Muted, modifier = Modifier.padding(top = 8.dp))
            }
        },
        extraContent = {
            error?.let { Text(editorError(it, strings), color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(top = 8.dp)) }
            if (busy) LinearProgressIndicator(Modifier.fillMaxWidth().padding(top = 8.dp))
            if (confirmDelete) {
                Column(Modifier.motionEnter("delete confirm", distance = 4f, initialScale = .98f)
                    .background(MoeriseColors.DangerSurface, RoundedCornerShape(10.dp)).padding(12.dp)) {
                    Text(strings.text("删除这条笔记？删除后无法恢复。", "Delete this note? This cannot be undone."),
                        style = MaterialTheme.typography.bodySmall)
                    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
                        TextButton(onClick = { confirmDelete = false }, enabled = !busy,
                            modifier = Modifier.testTag("delete_cancel_$id")) { Text(strings.text("取消", "Cancel")) }
                        TextButton(onClick = { confirmDelete = false; remote?.let { onDelete?.invoke(it) } }, enabled = online && !busy && !pendingAction,
                            modifier = Modifier.testTag("delete_confirm_$id")) { Text(strings.text("删除", "Delete"), color = MaterialTheme.colorScheme.error) }
                    }
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
    expanded: Boolean?,
    onExpand: (Boolean) -> Unit,
    first: Boolean,
    colophonActions: @Composable RowScope.() -> Unit,
    stagedAttachments: @Composable () -> Unit,
    extraContent: @Composable ColumnScope.() -> Unit,
) {
    Column(modifier.fillMaxWidth()) {
        if (!first) HorizontalDivider(Modifier.padding(horizontal = 16.dp), thickness = 1.dp, color = MoeriseColors.Line)
        Column(Modifier.padding(top = if (first) 10.dp else 14.dp)) {
            Box(Modifier.fillMaxWidth().testTag("note_card_body")) {
                FoldableNoteBody(expanded, onExpand, strings) {
                    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        if (presentation.body.isNotBlank()) MarkdownBody(presentation.body, note = note, core = core,
                            tags = presentation.inlineTags, onTag = onTag, originalContent = content, strings = strings,
                            modifier = Modifier.testTag("note_markdown"))
                        if (note != null) NoteAttachments(note, core, strings = strings)
                        stagedAttachments()
                        NoteTags(presentation.tags, onTag = onTag, plain = true)
                    }
                }
            }
            Row(Modifier.fillMaxWidth().height(44.dp), verticalAlignment = Alignment.Top) {
                Box(Modifier.weight(1f).padding(top = 10.dp).height(16.dp).testTag("note_card_date")
                    .semantics(mergeDescendants = true) {},
                    contentAlignment = Alignment.CenterStart) {
                    Text(date, style = MaterialTheme.typography.labelSmall.copy(lineHeight = 16.sp, fontFeatureSettings = "tnum",
                        lineHeightStyle = LineHeightStyle(LineHeightStyle.Alignment.Center, LineHeightStyle.Trim.None)),
                        color = MoeriseColors.InkColophon, maxLines = 1)
                }
                colophonActions()
            }
            extraContent()
        }
    }
}

@Composable
private fun FoldableNoteBody(
    expanded: Boolean?, onExpand: (Boolean) -> Unit, strings: Strings, body: @Composable () -> Unit,
) {
    if (expanded == null) {
        body()
        return
    }
    SubcomposeLayout(Modifier.fillMaxWidth()) { constraints ->
        val content = subcompose(FoldSlot.Body) {
            Layout(content = { Box { body() } }, modifier = Modifier.clipToBounds()) { measurables, available ->
                val note = measurables.single().measure(available.copy(minHeight = 0, maxHeight = Constraints.Infinity))
                val long = note.height > 400.dp.roundToPx()
                val height = if (long && !expanded) 280.dp.roundToPx() else note.height
                layout(note.width, height, if (long) mapOf(LongBody to 0) else emptyMap()) { note.placeRelative(0, 0) }
            }
        }.single().measure(constraints.copy(minHeight = 0))
        val long = content[LongBody] == 0
        val shade = if (long && !expanded) subcompose(FoldSlot.Shade) {
            Box(Modifier.fillMaxWidth().height(36.dp).background(Brush.verticalGradient(
                listOf(Color.Transparent, MoeriseColors.ScrollPaper))))
        }.single().measure(constraints.copy(minHeight = 0)) else null
        val toggle = if (long) subcompose(FoldSlot.Toggle) {
            val interactions = remember { MutableInteractionSource() }
            val pressed by interactions.collectIsPressedAsState()
            Box(Modifier.heightIn(min = 36.dp).clip(RoundedCornerShape(6.dp))
                .background(if (pressed) MoeriseColors.AccentSurface else Color.Transparent)
                .clickable(interactionSource = interactions, indication = null, role = Role.Button) { onExpand(!expanded) },
                contentAlignment = Alignment.CenterStart) {
                Text(if (expanded) strings.text("收起", "Collapse") else strings.text("展开", "Expand"),
                    style = MaterialTheme.typography.bodySmall, color = MoeriseColors.Accent)
            }
        }.single().measure(constraints.copy(minWidth = 0, minHeight = 0)) else null
        layout(content.width, content.height + (toggle?.height ?: 0)) {
            content.placeRelative(0, 0)
            shade?.placeRelative(0, content.height - shade.height)
            toggle?.placeRelative(0, content.height)
        }
    }
}

private val LongBody = HorizontalAlignmentLine(::minOf)
private enum class FoldSlot { Body, Shade, Toggle }

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
        }, contentAlignment = Alignment.TopCenter) {
            WebIcon(WebIcon.More, strings.text("更多操作", "More actions"), Modifier.padding(top = 10.dp).size(16.dp),
                tint = MoeriseColors.FieldBorder)
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

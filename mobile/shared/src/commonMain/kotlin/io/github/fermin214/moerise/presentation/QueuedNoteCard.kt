package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.DraftEntity
import io.github.fermin214.moerise.data.attachmentEdits
import io.github.fermin214.moerise.domain.Note

@Composable
fun QueuedNoteCard(
    draft: DraftEntity, strings: Strings, timezone: String = "UTC", createdAt: Long = 0,
    onOpen: () -> Unit,
) {
    val presentation = remember(draft.content) { localNotePresentation(draft.content) }
    val baseline = remember(draft.baselineJson) {
        draft.baselineJson?.let { runCatching { CoreJson.decodeFromString<Note>(it) }.getOrNull() }
    }
    val date = baseline?.createTime?.let { displayNoteDate(it, timezone, strings.text("zh", "en") == "en") }
        ?: displayLocalNoteDate(createdAt, timezone, strings.text("zh", "en") == "en")
    Surface(
        Modifier.fillMaxWidth().testTag("queued_note_${draft.id}"),
        color = Color.White, shape = RoundedCornerShape(14.dp), border = BorderStroke(1.dp, MoeriseColors.Line),
    ) {
        Column(Modifier.padding(start = 18.dp, top = 14.dp, end = 18.dp, bottom = 16.dp)) {
            Row(Modifier.fillMaxWidth().heightIn(min = 32.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(date, fontSize = 13.sp, color = MoeriseColors.Muted, modifier = Modifier.weight(1f)
                    .heightIn(min = 44.dp).wrapContentHeight())
                SyncGlyph(if (draft.conflictJson != null) SyncStatus.ATTENTION else SyncStatus.PENDING,
                    Modifier.size(16.dp).semantics {
                        contentDescription = if (draft.conflictJson != null) strings.text("同步冲突", "Sync conflict")
                            else strings.text("等待同步", "Pending sync")
                    })
                Spacer(Modifier.width(8.dp))
                WebNoteMenu(strings, listOf(NoteMenuAction(
                    if (draft.conflictJson != null) strings.text("处理冲突", "Resolve conflict") else strings.text("编辑", "Edit"),
                    onOpen, tag = "edit_note_${draft.id}",
                )), tag = "note_more_${draft.id}")
            }
            if (presentation.body.isNotBlank()) MarkdownBody(presentation.body, Modifier.fillMaxWidth().padding(top = 4.dp, bottom = 10.dp))
            NoteTags(presentation.tags)
            draft.attachmentEdits().staged.forEach {
                Text(it.filename, fontSize = 13.sp, color = MoeriseColors.Muted, modifier = Modifier.padding(top = 8.dp))
            }
        }
    }
}

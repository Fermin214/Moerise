package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.fermin214.moerise.data.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*

data class LocalDraftState(
    val drafts: List<DraftEntity> = emptyList(), val pending: Set<String> = emptySet(),
    val loading: Boolean = false, val error: String? = null,
)

class LocalDraftController(
    private val repository: suspend () -> NoteRepository, private val account: () -> String?, private val scope: CoroutineScope,
) {
    private val mutable = MutableStateFlow(LocalDraftState())
    val state = mutable.asStateFlow()
    private var request: Job? = null
    private var generation = 0L
    fun clear() { ++generation; request?.cancel(); mutable.value = LocalDraftState() }
    suspend fun stopForRemoval() { ++generation; request?.cancelAndJoin(); mutable.value = LocalDraftState() }
    fun refresh() {
        val key = account() ?: return
        val stamp = ++generation
        request?.cancel()
        mutable.update { it.copy(loading = true, error = null) }
        request = scope.launch {
            try {
                val repo = repository()
                val drafts = repo.drafts(key)
                val pending = repo.pendingDraftIds(key).toSet()
                if (stamp == generation && key == account()) mutable.value = LocalDraftState(drafts, pending)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (stamp == generation) mutable.update { it.copy(loading = false, error = "LOCAL_READ_FAILED") }
            }
        }
    }
}

@Composable
fun LocalDraftScreen(controller: LocalDraftController, strings: Strings, scroll: LazyListState,
    onBack: () -> Unit, onOpen: (DraftEntity) -> Unit) {
    val state by controller.state.collectAsState()
    Column(Modifier.fillMaxSize().safeDrawingPadding().testTag("local_drafts_page")) {
        Row(Modifier.fillMaxWidth().height(56.dp).padding(horizontal = 8.dp),
            verticalAlignment = Alignment.CenterVertically) {
            TextButton(onClick = onBack, modifier = Modifier.heightIn(min = 44.dp)) {
                WebIcon(WebIcon.ArrowLeft, modifier = Modifier.size(20.dp))
                Spacer(Modifier.width(8.dp))
                Text(strings.text("返回设置", "Back to settings"))
            }
            Text(strings.text("本机草稿", "Drafts on this device"), style = MaterialTheme.typography.titleMedium,
                maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f).padding(horizontal = 8.dp))
            TextButton(onClick = controller::refresh, enabled = !state.loading, modifier = Modifier.heightIn(min = 44.dp)) {
                Text(strings.text("刷新", "Refresh"))
            }
        }
        HorizontalDivider(color = MoeriseColors.Line, thickness = 1.dp)
        Box(Modifier.weight(1f).fillMaxWidth().padding(horizontal = 16.dp), contentAlignment = Alignment.TopCenter) {
            LazyColumn(Modifier.widthIn(max = 800.dp).fillMaxWidth(), state = scroll,
                contentPadding = PaddingValues(vertical = 16.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
                if (state.loading) item { LinearProgressIndicator(Modifier.fillMaxWidth()) }
                state.error?.let { error ->
                    item { Text(editorError(error, strings), color = MaterialTheme.colorScheme.error) }
                }
                if (state.drafts.isEmpty() && !state.loading) item {
                    Surface(Modifier.fillMaxWidth(), color = Color.White, shape = RoundedCornerShape(14.dp),
                        border = BorderStroke(1.dp, MoeriseColors.Line)) {
                        Text(strings.text("没有需要继续处理的草稿。", "No drafts need attention."), modifier = Modifier.padding(16.dp))
                    }
                }
                items(state.drafts, key = { it.id }) { draft ->
                    Surface(shape = RoundedCornerShape(14.dp), color = Color.White, border = BorderStroke(1.dp, MoeriseColors.Line),
                        modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { onOpen(draft) }.testTag("local_draft_${draft.id}")) {
                        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Text(when {
                                draft.conflictJson != null -> strings.text("冲突记录", "Conflict record")
                                draft.id in state.pending -> strings.text("保存结果待确认", "Save result unknown")
                                else -> strings.text("本机草稿", "Draft on this device")
                            }, style = MaterialTheme.typography.labelMedium, color = MoeriseColors.Blue)
                            Text(draft.content.ifBlank { strings.text("附件草稿", "Attachment draft") }, maxLines = 5)
                            Text(draft.targetName ?: strings.text("新笔记", "New note"), style = MaterialTheme.typography.labelSmall, color = MoeriseColors.Muted)
                        }
                    }
                }
            }
        }
    }
}

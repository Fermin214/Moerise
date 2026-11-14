package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
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
                if (stamp == generation) mutable.update { it.copy(loading = false, error = "LOCAL_WRITE_FAILED") }
            }
        }
    }
}

@Composable
fun LocalDraftScreen(controller: LocalDraftController, strings: Strings, scroll: LazyListState,
    onBack: () -> Unit, onOpen: (DraftEntity) -> Unit) {
    val state by controller.state.collectAsState()
    Column(Modifier.fillMaxSize().safeDrawingPadding().testTag("local_drafts_page")) {
        Row(Modifier.fillMaxWidth().padding(8.dp), horizontalArrangement = Arrangement.SpaceBetween) {
            TextButton(onClick = onBack) { Text(strings.text("返回设置", "Back to settings")) }
            TextButton(onClick = controller::refresh, enabled = !state.loading) { Text(strings.text("刷新", "Refresh")) }
        }
        Text(strings.text("本机草稿", "Drafts on this device"), style = MaterialTheme.typography.headlineSmall, modifier = Modifier.padding(16.dp))
        if (state.loading) LinearProgressIndicator(Modifier.fillMaxWidth())
        state.error?.let { Text(editorError(it, strings), color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(16.dp)) }
        LazyColumn(Modifier.weight(1f), state = scroll, contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            if (state.drafts.isEmpty() && !state.loading) item { Text(strings.text("没有需要继续处理的草稿。", "No drafts need attention.")) }
            items(state.drafts, key = { it.id }) { draft ->
                Surface(shape = RoundedCornerShape(14.dp), border = BorderStroke(1.dp, MoeriseColors.Line),
                    modifier = Modifier.fillMaxWidth().clickable { onOpen(draft) }.testTag("local_draft_${draft.id}")) {
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

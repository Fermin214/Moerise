package io.github.fermin214.moerise.presentation

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.MutableTransitionState
import androidx.compose.animation.core.VectorConverter
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInParent
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun WebFilterBar(query: SearchQuery, strings: Strings, onChange: (SearchQuery) -> Unit) {
    val labels = query.tags.map { "#$it" } + listOfNotNull(query.date.takeIf { it.isNotBlank() })
    var shown by remember { mutableStateOf(labels) }
    val reduced = LocalReducedMotion.current
    LaunchedEffect(labels) {
        shown = labels + shown.filterNot(labels::contains)
        if (shown != labels) delay(if (reduced) 80 else 220)
        shown = labels
    }
    FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(6.dp),
        itemVerticalAlignment = Alignment.CenterVertically) {
        Text(strings.text("筛选", "Filters"), style = MaterialTheme.typography.bodySmall, color = MoeriseColors.Muted)
        for (label in shown) key(label) {
            val entering = remember { MutableTransitionState(false) }.apply { targetState = label in labels }
            val previous = remember { mutableStateOf<Offset?>(null) }
            val translation = remember { Animatable(Offset.Zero, Offset.VectorConverter) }
            val scope = rememberCoroutineScope()
            AnimatedVisibility(entering,
                modifier = Modifier.onGloballyPositioned {
                    val point = it.positionInParent()
                    val start = previous.value
                    previous.value = point
                    if (!reduced && start != null && point != start) scope.launch {
                        translation.snapTo(start - point)
                        translation.animateTo(Offset.Zero, MoeriseMotion.responsive())
                    }
                }.graphicsLayer { translationX = translation.value.x; translationY = translation.value.y },
                enter = fadeIn(if (reduced) MoeriseMotion.fade() else MoeriseMotion.tactile()),
                exit = fadeOut(if (reduced) MoeriseMotion.fade() else MoeriseMotion.tactile()),
            ) {
                FilterPill(label, strings) {
                    if (label.startsWith("#")) onChange(query.copy(tags = query.tags - label.drop(1)))
                    else onChange(query.copy(date = ""))
                }
            }
        }
        TextButton(onClick = { onChange(query.copy(tags = emptySet(), date = "")) }) { Text(strings.text("清除全部", "Clear all")) }
    }
}

@Composable
private fun FilterPill(label: String, strings: Strings, onRemove: () -> Unit) {
    Surface(color = MoeriseColors.Hover, shape = RoundedCornerShape(50)) {
        Row(Modifier.padding(start = 10.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(label, style = MaterialTheme.typography.bodySmall)
            IconButton(onRemove, Modifier.size(28.dp)) {
                WebIcon(WebIcon.Close, strings.text("移除筛选 $label", "Remove filter $label"), Modifier.size(12.dp))
            }
        }
    }
}

@Composable
fun SearchStatus(state: SearchState, strings: Strings) {
    if (state.query.mode != "keyword" && state.responseMode != null) {
        Text(if (state.responseMode == "keyword")
            strings.text("语义检索暂不可用，当前显示按相关度排列的关键词结果。", "Semantic search is unavailable. Keyword results are ranked by relevance.")
            else strings.text("搜索结果按相关度排列。", "Search results are ranked by relevance."),
            style = MaterialTheme.typography.bodySmall, color = MoeriseColors.Muted)
    }
}

fun searchError(code: String, strings: Strings): String = when (code) {
    "INVALID_ARGUMENT" -> strings.text("搜索条件无效，请检查搜索词和日期。", "Invalid search filters. Check your query and date.")
    "SEARCH_QUERY_REQUIRED" -> strings.text("语义和混合检索需要填写搜索词。", "Enter a query for semantic or hybrid search.")
    "EMBEDDING_DISABLED", "EMBEDDING_UNAVAILABLE", "VECTOR_NOT_READY" -> strings.text("语义检索不可用，可改用关键词搜索。", "Semantic search unavailable. Use keyword search.")
    "INVALID_PAGE_TOKEN", "SOURCE_CHANGED" -> strings.text("搜索结果已变化，请重新搜索。", "Results changed. Search again.")
    else -> connectionError(code, strings)
}

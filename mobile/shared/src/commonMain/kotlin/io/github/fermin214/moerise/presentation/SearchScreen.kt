package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun WebFilterBar(query: SearchQuery, strings: Strings, onChange: (SearchQuery) -> Unit) {
    FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(6.dp),
        itemVerticalAlignment = Alignment.CenterVertically) {
        Text(strings.text("筛选", "Filters"), style = MaterialTheme.typography.bodySmall, color = MoeriseColors.Muted)
        query.tags.forEach { tag -> FilterPill("#$tag", strings) { onChange(query.copy(tags = query.tags - tag)) } }
        if (query.date.isNotBlank()) FilterPill(query.date, strings) { onChange(query.copy(date = "")) }
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

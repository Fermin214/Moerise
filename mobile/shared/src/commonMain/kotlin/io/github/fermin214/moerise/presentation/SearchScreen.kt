package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp

@Composable
fun SearchScreen(mobile: MobileController, strings: Strings, scroll: LazyListState, onBack: () -> Unit) {
    val controller = mobile.search
    val state by controller.state.collectAsState()
    val query = state.query
    val timezone = mobile.session.state.value.profile?.timezone ?: "UTC"
    val tab = state.tab
    val month = state.month ?: serviceMonth(timezone)
    LaunchedEffect(state.month) { if (state.month == null) controller.month(month) }
    LaunchedEffect(query.state, month, tab) { if (tab != 0) controller.filters(month) }
    Column(Modifier.safeDrawingPadding().imePadding().fillMaxSize()) {
    LazyColumn(Modifier.weight(1f), state = scroll,
        contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item { TextButton(onClick = onBack, modifier = Modifier.testTag("search_back")) { Text(strings.text("返回列表", "Back to notes")) } }
        item { Text(strings.text("搜索", "Search"), style = MaterialTheme.typography.headlineSmall) }
        item {
            OutlinedTextField(query.text, { controller.change(query.copy(text = it)) },
                modifier = Modifier.fillMaxWidth().testTag("search_query"), label = { Text(strings.text("搜索笔记", "Search notes")) })
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                listOf("keyword", "semantic", "hybrid").forEach { mode ->
                    FilterChip(query.mode == mode, onClick = { controller.change(query.copy(mode = mode)) }, modifier = Modifier.testTag("search_mode_$mode"), label = {
                        Text(when (mode) {
                            "keyword" -> strings.text("关键词", "Keyword")
                            "semantic" -> strings.text("语义", "Semantic")
                            else -> strings.text("混合", "Hybrid")
                        })
                    })
                }
            }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                listOf("NORMAL", "ARCHIVED", "ALL").forEach { noteState ->
                    FilterChip(query.state == noteState, onClick = { controller.change(query.copy(state = noteState)) }, label = {
                        Text(when (noteState) {
                            "NORMAL" -> strings.text("正常", "Active")
                            "ARCHIVED" -> strings.text("归档", "Archived")
                            else -> strings.text("全部", "All")
                        })
                    })
                }
            }
        }
        item {
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = { controller.tab(0) }) { Text(strings.text("结果", "Results")) }
                TextButton(onClick = { controller.tab(1) }) { Text(strings.text("标签", "Tags")) }
                TextButton(onClick = { controller.tab(2) }) { Text(strings.text("日历", "Calendar")) }
            }
            if (query.tags.isNotEmpty() || query.date.isNotEmpty()) {
                Text((query.tags.sorted().map { "#$it" } + query.date.takeIf { it.isNotEmpty() }.orEmpty()).joinToString("  "))
                TextButton(onClick = { controller.change(query.copy(tags = emptySet(), date = "")) }) { Text(strings.text("清除筛选", "Clear filters")) }
            }
        }
        if (tab == 0) {
            item {
                state.error?.let { Text(searchError(it, strings), color = MaterialTheme.colorScheme.error) }
                if (state.responseMode != null) {
                    Text(when {
                        state.semantic?.used == true -> strings.text("已使用语义检索", "Semantic search used")
                        query.mode == "hybrid" && state.responseMode == "keyword" -> strings.text("语义检索不可用，已使用关键词结果。", "Semantic search unavailable. Showing keyword results.")
                        else -> strings.text("关键词结果", "Keyword results")
                    }, color = MoeriseColors.Muted)
                }
                if (state.loading) LinearProgressIndicator(Modifier.fillMaxWidth())
            }
            items(state.notes, key = { it.name }) { note ->
                Surface(Modifier.fillMaxWidth().clickable { mobile.openNote(note.name) }, shape = MaterialTheme.shapes.medium) {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text(note.content, maxLines = 8)
                        Text(displayServiceTime(note.createTime, timezone), style = MaterialTheme.typography.labelSmall)
                    }
                }
            }
            item {
                if (state.next.isNotEmpty()) TextButton(onClick = { controller.search(true) }, enabled = !state.loading) { Text(strings.text("加载更多", "Load more")) }
                else if (state.responseMode != null && state.notes.isEmpty()) Text(strings.text("没有匹配的笔记。", "No matching notes."))
            }
        } else {
            item {
                if (state.filtersLoading) LinearProgressIndicator(Modifier.fillMaxWidth())
                state.filtersError?.let {
                    Text(searchError(it, strings), color = MaterialTheme.colorScheme.error)
                    TextButton(onClick = { controller.filters(month) }) { Text(strings.text("重试", "Retry")) }
                }
            }
            if (tab == 1) items(state.tags, key = { it.tag }) { tag ->
                Row(Modifier.fillMaxWidth().clickable {
                    controller.change(query.copy(tags = if (tag.tag in query.tags) query.tags - tag.tag else query.tags + tag.tag))
                }) {
                    Checkbox(tag.tag in query.tags, onCheckedChange = { selected -> controller.change(query.copy(tags = if (selected) query.tags + tag.tag else query.tags - tag.tag)) })
                    Text("#${tag.tag}  ${tag.count}", modifier = Modifier.padding(top = 12.dp))
                }
            } else item {
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                    TextButton(onClick = { controller.month(shiftMonth(month, -1)) }, modifier = Modifier.semantics { contentDescription = strings.text("上个月", "Previous month") }) { Text("‹") }
                    Text(month, modifier = Modifier.padding(top = 12.dp))
                    TextButton(onClick = { controller.month(shiftMonth(month, 1)) }, modifier = Modifier.semantics { contentDescription = strings.text("下个月", "Next month") }) { Text("›") }
                }
                val grid = monthGrid(month)
                val counts = state.calendar?.takeIf { it.month == month }?.days?.associate { it.date to it.count } ?: emptyMap()
                val labels = listOf(strings.text("一", "Mo"), strings.text("二", "Tu"), strings.text("三", "We"), strings.text("四", "Th"), strings.text("五", "Fr"), strings.text("六", "Sa"), strings.text("日", "Su"))
                Column(Modifier.horizontalScroll(rememberScrollState()).width(336.dp)) {
                    Row { labels.forEach { Text(it, Modifier.weight(1f).padding(12.dp)) } }
                    for (week in 0 until (grid.offset + grid.days + 6) / 7) {
                      Row {
                        for (dayOfWeek in 0..6) {
                            val day = week * 7 + dayOfWeek - grid.offset + 1
                            if (day !in 1..grid.days) Spacer(Modifier.weight(1f).height(48.dp))
                            else {
                                val date = "$month-${day.toString().padStart(2, '0')}"
                                TextButton(
                                    onClick = { controller.change(query.copy(date = if (query.date == date) "" else date)) },
                                    colors = ButtonDefaults.textButtonColors(containerColor = if (query.date == date) MoeriseColors.BlueSurface else Color.Transparent),
                                    modifier = Modifier.weight(1f).heightIn(min = 48.dp).semantics {
                                        selected = query.date == date
                                        contentDescription = "$date, ${counts.getOrDefault(date, 0)} " + strings.text("条笔记", "notes")
                                    },
                                ) {
                                    Text(if (counts.getOrDefault(date, 0) > 0) "$day·" else "$day")
                                }
                            }
                        }
                      }
                    }
                }
            }
        }
    }
    Button(onClick = { controller.search(); controller.tab(0) },
        enabled = !state.loading && (query.mode == "keyword" || query.text.isNotBlank()),
        modifier = Modifier.fillMaxWidth().padding(16.dp).heightIn(min = 48.dp).testTag("search_submit")) {
        Text(strings.text("搜索", "Search"))
    }
    }
}

fun searchError(code: String, strings: Strings): String = when (code) {
    "INVALID_ARGUMENT" -> strings.text("搜索条件无效，请检查搜索词和日期。", "Invalid search filters. Check your query and date.")
    "SEARCH_QUERY_REQUIRED" -> strings.text("语义和混合检索需要填写搜索词。", "Enter a query for semantic or hybrid search.")
    "EMBEDDING_DISABLED", "EMBEDDING_UNAVAILABLE", "VECTOR_NOT_READY" -> strings.text("语义检索不可用，可改用关键词搜索。", "Semantic search unavailable. Use keyword search.")
    "INVALID_PAGE_TOKEN", "SOURCE_CHANGED" -> strings.text("搜索结果已变化，请重新搜索。", "Results changed. Search again.")
    else -> connectionError(code, strings)
}

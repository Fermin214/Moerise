package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import io.github.fermin214.moerise.data.epochMillis
import io.github.fermin214.moerise.domain.Note

@Composable
fun StarMapScreen(mobile: MobileController, strings: Strings, topicScroll: LazyListState, noteScroll: LazyListState,
    readerScroll: ScrollState, onMenu: () -> Unit, onWalk: (Note) -> Unit, onInsight: (Note) -> Unit) {
    val controller = mobile.stars
    val state by controller.state.collectAsState()
    val connection by mobile.session.state.collectAsState()
    val map = state.snapshot
    PlatformMapFullscreen(state.fullScreen)
    var confirmRebuild by remember { mutableStateOf(false) }
    val unplacedOnly = state.unplacedOnly
    val uri = LocalUriHandler.current
    Column(Modifier.fillMaxSize().safeDrawingPadding().testTag("map_page")) {
        if (!state.fullScreen) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp), horizontalArrangement = Arrangement.SpaceBetween) {
                TextButton(onClick = onMenu, modifier = Modifier.testTag("open_menu")) { Text(strings.text("导航", "Menu")) }
                Text(strings.text("观星", "Stargazing"), style = MaterialTheme.typography.titleLarge, modifier = Modifier.padding(top = 12.dp))
                TextButton(onClick = controller::refresh, enabled = connection.connected && !state.loading && !state.posting,
                    modifier = Modifier.testTag("map_refresh")) { Text(strings.text("刷新", "Refresh")) }
            }
            PrimaryTabRow(state.tab) {
                listOf(strings.text("星图", "Map"), strings.text("主题", "Topics"), strings.text("笔记", "Notes")).forEachIndexed { index, label ->
                    Tab(state.tab == index, onClick = { controller.tab(index) }, text = { Text(label) }, modifier = Modifier.testTag("map_tab_$index"))
                }
            }
        }
        if (state.loading || state.posting) LinearProgressIndicator(Modifier.fillMaxWidth())
        val code = state.error ?: when {
            map?.updateState == "unavailable" -> "MAP_UPDATE_UNAVAILABLE"
            map?.updateState == "building" -> "MAP_UPDATING"
            state.status !in setOf("", "ready") -> "MAP_${state.status.uppercase()}"
            state.rebuild?.state in setOf("unknown", "observing") -> "MAP_REBUILD_OBSERVING"
            else -> null
        }
        if (code != null) {
            Text(mapError(code, strings), modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp).testTag("map_status"),
                color = if (code in setOf("MAP_UNAVAILABLE", "MAP_UPDATE_UNAVAILABLE", "MAP_INCOMPATIBLE", "MAP_PROTOCOL")) MaterialTheme.colorScheme.error else MoeriseColors.Muted)
            if (state.status == "disabled") TextButton(onClick = { connection.profile?.let { runCatching { uri.openUri("${it.origin}/settings") } } }) {
                Text(strings.text("打开 Web 设置", "Open web settings"))
            }
        }
        Box(Modifier.weight(1f).fillMaxWidth()) {
            if (state.tab == 0 || state.fullScreen) {
                if (map != null) StarCanvas(state, controller, strings)
                else Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    if (state.status.isEmpty() && !state.loading) TextButton(onClick = controller::refresh, enabled = connection.connected) {
                        Text(strings.text("读取星图", "Load star map"))
                    }
                }
                Column(Modifier.align(Alignment.TopEnd).padding(8.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    FilledTonalButton(onClick = { controller.transform(1.35) }, enabled = state.view != null, contentPadding = PaddingValues(12.dp),
                        modifier = Modifier.testTag("map_zoom_in")) { Text("+") }
                    FilledTonalButton(onClick = { controller.transform(1 / 1.35) }, enabled = state.view != null, contentPadding = PaddingValues(12.dp),
                        modifier = Modifier.testTag("map_zoom_out")) { Text("−") }
                    FilledTonalButton(onClick = controller::fullScreen, contentPadding = PaddingValues(12.dp), modifier = Modifier.testTag("map_fullscreen")) {
                        Text(if (state.fullScreen) strings.text("退出全屏", "Exit full screen") else strings.text("全屏", "Full screen"))
                    }
                }
            } else if (state.tab == 1) {
                LazyColumn(state = topicScroll, contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    if (map?.topics.isNullOrEmpty()) item { Text(strings.text("没有已识别的主题。", "No topics identified.")) }
                    items(map?.topics.orEmpty(), key = { it.id }) { topic ->
                        Surface(shape = RoundedCornerShape(14.dp), border = BorderStroke(1.dp, MoeriseColors.Line)) {
                            Column(Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                TextButton(onClick = { controller.focus(topic.members) }) { Text("${topic.label} · ${topic.members.size}") }
                                topic.districts.forEach { district ->
                                    TextButton(onClick = { controller.focus(district.members) }) { Text("${district.label} · ${district.members.size}") }
                                }
                            }
                        }
                    }
                }
            } else {
                LazyColumn(state = noteScroll, contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    item {
                        FilterChip(unplacedOnly, onClick = controller::unplaced,
                            label = { Text(strings.text("未定位笔记", "Unplaced notes") + " · ${map?.points?.count { !it.semantic } ?: 0}") },
                            modifier = Modifier.testTag("map_unplaced"))
                    }
                    items(map?.points.orEmpty().filter { !unplacedOnly || !it.semantic }, key = { it.name }) { point ->
                        Surface(shape = RoundedCornerShape(12.dp), border = BorderStroke(1.dp, MoeriseColors.Line),
                            modifier = Modifier.fillMaxWidth().clickable { controller.select(point.name) }) {
                            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                                Text(point.name)
                                if (point.tags.isNotEmpty()) Text(point.tags.joinToString(" ") { "#$it" }, color = MoeriseColors.Muted)
                            }
                        }
                    }
                }
            }
        }
        if (state.selected != null) {
            Surface(tonalElevation = 3.dp) {
                Column(Modifier.fillMaxWidth().heightIn(max = 240.dp).verticalScroll(readerScroll),
                    verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                        Text(state.selected!!, modifier = Modifier.weight(1f).padding(12.dp), style = MaterialTheme.typography.labelMedium)
                        TextButton(onClick = controller::closeReader, modifier = Modifier.testTag("map_close_reader")) { Text(strings.text("关闭", "Close")) }
                    }
                    if (state.reading) LinearProgressIndicator(Modifier.fillMaxWidth())
                    state.readerError?.let {
                        Text(editorError(it, strings), modifier = Modifier.padding(horizontal = 16.dp), color = MaterialTheme.colorScheme.error)
                        TextButton(onClick = { controller.select(state.selected!!) }) { Text(strings.text("重新读取", "Read again")) }
                    }
                    state.reader?.let { note ->
                        MarkdownBody(note.content, modifier = Modifier.padding(horizontal = 16.dp).testTag("map_reader"),
                            note = note, core = mobile.session.core)
                        Row(Modifier.horizontalScroll(rememberScrollState())) {
                            TextButton(onClick = { mobile.openNote(note.name) }, modifier = Modifier.testTag("map_open_note")) { Text(strings.text("完整详情", "Full note")) }
                            if (note.state == "NORMAL") TextButton(onClick = { onWalk(note) }, enabled = connection.connected) { Text(strings.text("从这条漫步", "Walk from this note")) }
                            TextButton(onClick = { onInsight(note) }, enabled = connection.connected) { Text(strings.text("洞察这条", "Explore this note")) }
                        }
                    }
                }
            }
        }
        Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(8.dp), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            TextButton(onClick = { controller.fit(!state.allFraming) }, enabled = map != null, modifier = Modifier.testTag("map_fit")) {
                Text(if (state.allFraming) strings.text("主体取景", "Fit subject") else strings.text("全图取景", "Fit all"))
            }
            TextButton(onClick = controller::atmosphere, modifier = Modifier.testTag("map_atmosphere")) {
                Text(if (state.photo) strings.text("星图氛围", "Atlas mode") else strings.text("摄影氛围", "Photo mode"))
            }
            OutlinedButton(onClick = { confirmRebuild = true }, enabled = connection.connected && !state.loading &&
                !state.posting && state.compatible && state.status.isNotEmpty() && state.status != "disabled" &&
                (state.rebuild?.retryAfter ?: 0) <= epochMillis(), modifier = Modifier.testTag("map_rebuild")) {
                Text(if (state.rebuild?.state in setOf("unknown", "observing")) strings.text("再次生成", "Generate again") else strings.text("重新生成", "Rebuild"))
            }
        }
    }
    if (confirmRebuild) AlertDialog(onDismissRequest = { confirmRebuild = false },
        title = { Text(strings.text("重新生成星图？", "Rebuild the star map?")) },
        confirmButton = { TextButton(onClick = { confirmRebuild = false; controller.rebuild(explicitAgain = true) },
            modifier = Modifier.testTag("map_rebuild_confirm")) { Text(strings.text("重新生成", "Rebuild")) } },
        dismissButton = { TextButton(onClick = { confirmRebuild = false }) { Text(strings.text("取消", "Cancel")) } })
}

fun mapError(code: String, strings: Strings): String = when (code) {
    "MAP_DISABLED" -> strings.text("启用向量索引后可以观星。", "Enable the vector index to explore your star map.")
    "MAP_WAITING" -> strings.text("等待向量索引就绪。", "Waiting for the vector index.")
    "MAP_BUILDING" -> strings.text("正在生成星图。", "Building the star map.")
    "MAP_UNAVAILABLE" -> strings.text("星图服务暂时不可用，可以稍后刷新。", "The star map is unavailable. Refresh later.")
    "MAP_EMPTY" -> strings.text("还没有可展示的笔记。", "No notes to display yet.")
    "MAP_UPDATING" -> strings.text("正在更新，继续显示已有星图。", "Updating. The saved star map remains visible.")
    "MAP_UPDATE_UNAVAILABLE" -> strings.text("星图更新失败，可以继续浏览已有快照。", "The update failed. You can keep exploring the saved map.")
    "MAP_REBUILD_OBSERVING" -> strings.text("正在等待新星图。", "Waiting for a new star map.")
    "MAP_INCOMPATIBLE", "MAP_PROTOCOL" -> strings.text("服务的星图契约不兼容，请升级服务后刷新。", "This star map contract is incompatible. Update the service and refresh.")
    else -> connectionError(code, strings)
}

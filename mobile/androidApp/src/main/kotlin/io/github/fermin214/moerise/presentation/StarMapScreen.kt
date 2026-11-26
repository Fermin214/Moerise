package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.focusable
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.withTransform
import androidx.compose.ui.graphics.vector.PathParser
import androidx.compose.ui.input.key.*
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.toggleableState
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.fermin214.moerise.data.CoreClient

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun StarMapScreen(mobile: MobileController, strings: Strings, noteScroll: LazyListState,
    onMenu: () -> Unit, onSearch: () -> Unit, onTag: ((String) -> Unit)? = null) {
    val connection by mobile.session.state.collectAsState()
    StarMapScreen(
        mobile.stars,
        mobile.session.core,
        connection.profile?.stargazingEnabled != false,
        strings,
        noteScroll,
        onMenu,
        onSearch,
        timezone = connection.profile?.timezone ?: "UTC",
        onTag = onTag,
    )
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun StarMapScreen(controller: StarMapController, core: CoreClient?, enabled: Boolean, strings: Strings,
    noteScroll: LazyListState, onMenu: () -> Unit, onSearch: () -> Unit, timezone: String = "UTC", onTag: ((String) -> Unit)? = null) {
    val state by controller.state.collectAsState()
    val map = state.snapshot
    if (!enabled || state.disabledReason == "stargazing") {
        PlatformMapFullscreen(false)
        Column(Modifier.fillMaxSize().safeDrawingPadding().testTag("map_page")) {
            WebToolbar(strings, onMenu, onSearch)
            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
                Text(strings.text("观星", "Stargazing"), style = MaterialTheme.typography.titleLarge)
                Text(strings.text("观星已关闭。", "Stargazing is disabled."), Modifier.testTag("map_status"))
            }
        }
        return
    }
    val mapFocus = remember { FocusRequester() }
    val density = LocalDensity.current.density
    var fieldFocused by remember { mutableStateOf(false) }
    val readerVisible = state.selected != null || state.unplacedOnly && map?.readable == true
    val closeReader: () -> Unit = {
        controller.dismissReader()
        mapFocus.requestFocus()
    }
    PlatformMapFullscreen(state.fullScreen)
    PlatformBackHandler(readerVisible) { closeReader() }
    LaunchedEffect(state.unplacedOnly) {
        if (state.unplacedOnly) noteScroll.scrollToItem(0)
    }
    val code = state.error ?: when {
        state.unavailableReason == "layout" -> "MAP_LAYOUT_UNAVAILABLE"
        state.unavailableReason == "layout_incompatible" -> "MAP_LAYOUT_INCOMPATIBLE"
        map?.updateState == "unavailable" -> "MAP_UPDATE_UNAVAILABLE"
        state.rebuild?.state in setOf("unknown", "observing") && state.status in setOf("ready", "empty") -> "MAP_REBUILD_OBSERVING"
        state.status !in setOf("", "ready") -> "MAP_${state.status.uppercase()}"
        else -> null
    }
    val message = when {
        code != null -> mapError(code, strings)
        state.loading && map == null -> strings.text("正在加载观星…", "Loading stargazing…")
        else -> null
    }
    val messageError = state.error != null ||
        code in setOf("MAP_UNAVAILABLE", "MAP_UPDATE_UNAVAILABLE", "MAP_LAYOUT_UNAVAILABLE", "MAP_LAYOUT_INCOMPATIBLE", "MAP_INCOMPATIBLE", "MAP_PROTOCOL")

    Column(Modifier.fillMaxSize().safeDrawingPadding().testTag("map_page")) {
        if (!state.fullScreen) WebToolbar(strings, onMenu, onSearch)
        Column(Modifier.weight(1f).fillMaxWidth().then(
            if (state.fullScreen) Modifier else Modifier.padding(horizontal = 16.dp, vertical = 20.dp)),
            verticalArrangement = Arrangement.spacedBy(16.dp)) {
            if (!state.fullScreen) Text(strings.text("观星", "Stargazing"), style = MaterialTheme.typography.titleLarge)
            BoxWithConstraints(Modifier.weight(1f).fillMaxWidth()) {
                val wideReader = maxWidth > 1024.dp && readerVisible
                val readerHeight = minOf(maxHeight * .65f, 544.dp)
                val readerWidth = (maxWidth * .28f).coerceIn(352.dp, 480.dp)
                Row(Modifier.fillMaxSize(), horizontalArrangement = Arrangement.spacedBy(16.dp)) {
                    Surface(Modifier.weight(1f).fillMaxHeight().drawerGestureRegion(),
                        shape = RoundedCornerShape(if (state.fullScreen) 0.dp else 14.dp),
                        color = Color(0xFF030A14),
                        border = BorderStroke(1.dp, Color(0xFF172638))) {
                        BoxWithConstraints(Modifier.fillMaxSize()) {
                            Box(Modifier.fillMaxSize().focusRequester(mapFocus).onFocusChanged { fieldFocused = it.isFocused }
                                .onKeyEvent { event ->
                                    if (!fieldFocused || event.type != KeyEventType.KeyDown ||
                                        event.isAltPressed || event.isCtrlPressed || event.isMetaPressed) false
                                    else {
                                        val distance = (if (event.isShiftPressed) 100.0 else 40.0) * density
                                        when (event.key) {
                                            Key.R -> { controller.refresh(); true }
                                            Key.Home, Key.MoveHome -> { controller.fit(true); true }
                                            Key.Plus, Key.Equals -> { controller.transform(1.25); true }
                                            Key.Minus -> { controller.transform(.8); true }
                                            Key.DirectionLeft -> { controller.transform(1.0, panX = distance); true }
                                            Key.DirectionRight -> { controller.transform(1.0, panX = -distance); true }
                                            Key.DirectionUp -> { controller.transform(1.0, panY = distance); true }
                                            Key.DirectionDown -> { controller.transform(1.0, panY = -distance); true }
                                            else -> false
                                        }
                                    }
                                }.focusable()) {
                                if (map != null) StarCanvas(state, controller, strings)
                            }
                            Surface(
                                Modifier.align(Alignment.TopEnd).padding(12.dp)
                                    .widthIn(max = (maxWidth - 24.dp).coerceAtLeast(44.dp)),
                                color = Color(0xEE081321),
                                shape = RoundedCornerShape(8.dp),
                                border = BorderStroke(1.dp, Color(0xFF263749)),
                            ) {
                                FlowRow(Modifier.padding(3.dp)) {
                                    MapTool(MapToolIcon.Fit, strings.text("显示全图", "Fit map"), enabled = map != null,
                                        tag = "map_fit", onClick = { controller.fit(true) })
                                    MapTool(MapToolIcon.Refresh, strings.text("重新生成星图", "Rebuild star map"),
                                        enabled = map != null && !state.posting && !state.loading,
                                        tag = "map_rebuild", onClick = { controller.rebuild(true) })
                                    MapTool(MapToolIcon.Frame, strings.text(if (state.fullScreen) "退出全屏" else "全屏预览",
                                        if (state.fullScreen) "Exit full screen" else "Fullscreen"),
                                        pressed = state.fullScreen, tag = "map_fullscreen", onClick = controller::fullScreen)
                                }
                            }
                            val missing = map?.points?.count { !it.semantic } ?: 0
                            if (missing > 0) OutlinedButton(
                                onClick = { controller.closeReader(); if (!state.unplacedOnly) controller.unplaced() },
                                modifier = Modifier.align(Alignment.BottomStart).padding(14.dp).testTag("map_unplaced"),
                                shape = RoundedCornerShape(10.dp), border = BorderStroke(1.dp, Color(0xFF263749)),
                                colors = ButtonDefaults.outlinedButtonColors(containerColor = Color(0xEE0B192A), contentColor = Color(0xFFB8C8D9)),
                            ) { Text(strings.text("未定位笔记（$missing）", "Notes without positions ($missing)")) }
                            if (message != null) Surface(
                                Modifier.align(Alignment.BottomStart).padding(start = 16.dp, end = 16.dp, bottom = if (missing > 0) 72.dp else 12.dp),
                                color = Color(0xEE0B192A), shape = RoundedCornerShape(6.dp),
                            ) {
                                Column {
                                Text(message, style = MaterialTheme.typography.bodySmall,
                                    color = if (messageError) Color(0xFFFFB4AB) else Color(0xFFB8C8D9),
                                    modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp).testTag("map_status")
                                        .semantics { liveRegion = if (messageError) LiveRegionMode.Assertive else LiveRegionMode.Polite })
                                if (messageError) TextButton(onClick = controller::refresh) { Text(strings.text("重试", "Retry")) }
                                }
                            }
                        }
                    }
                    if (wideReader) MapUnplacedNotes(controller, core, state, strings, timezone, noteScroll, closeReader,
                        Modifier.width(readerWidth).fillMaxHeight(), onTag = onTag)
                }
                if (readerVisible && !wideReader) MapUnplacedNotes(controller, core, state, strings, timezone, noteScroll, closeReader,
                    Modifier.align(Alignment.BottomCenter).fillMaxWidth().heightIn(max = readerHeight), bottomSheet = true, onTag = onTag)
            }
        }
    }
}

@Composable
private fun MapUnplacedNotes(controller: StarMapController, core: CoreClient?, state: StarMapState, strings: Strings,
    timezone: String, noteScroll: LazyListState, onClose: () -> Unit,
    modifier: Modifier, bottomSheet: Boolean = false, onTag: ((String) -> Unit)? = null) {
    Surface(modifier, color = Color.White,
        shape = if (bottomSheet) RoundedCornerShape(topStart = 16.dp, topEnd = 16.dp) else RoundedCornerShape(14.dp),
        border = BorderStroke(1.dp, MoeriseColors.Line), shadowElevation = if (bottomSheet) 8.dp else 0.dp) {
        Column {
            val selectedPoint = state.selected?.let { name -> state.snapshot?.points?.firstOrNull { it.name == name } }
            val readerDate = (state.reader?.createTime ?: selectedPoint?.createTime)?.takeIf { it.isNotBlank() }
                ?.let { displayNoteDate(it, timezone, strings.text("zh", "en") == "en") }
            Row(Modifier.fillMaxWidth().padding(start = 18.dp, top = 6.dp, end = 6.dp),
                verticalAlignment = Alignment.CenterVertically) {
                if (readerDate != null) Text(readerDate, fontSize = 13.sp, color = MoeriseColors.Muted,
                    modifier = Modifier.weight(1f)) else Spacer(Modifier.weight(1f))
                IconButton(onClick = onClose, modifier = Modifier.size(44.dp).testTag("map_close_reader")) {
                    WebIcon(WebIcon.Close, strings.text("关闭阅读卡", "Close reader"))
                }
            }
            if (state.selected != null) {
                key(state.selected) {
                    Column(
                        Modifier.verticalScroll(rememberScrollState()).padding(start = 18.dp, top = 0.dp, end = 18.dp, bottom = 16.dp).testTag("map_reader"),
                        verticalArrangement = Arrangement.spacedBy(10.dp),
                    ) {
                        state.reader?.let { note ->
                            val presentation = rememberNotePresentation(note.content, note.tags)
                            if (presentation.body.isNotBlank()) MarkdownBody(presentation.body, note = note, core = core,
                                tags = presentation.inlineTags, onTag = onTag)
                            NoteAttachments(note, core, strings)
                            NoteTags(presentation.tags, onTag = onTag)
                        }
                        if (state.reading) Text(strings.text("正在读取笔记…", "Loading note…"))
                        state.readerError?.let { Text(editorError(it, strings), color = MaterialTheme.colorScheme.error) }
                    }
                }
            } else if (state.unplacedOnly) {
                Text(strings.text("未定位笔记", "Notes without positions"), style = MaterialTheme.typography.titleMedium,
                    modifier = Modifier.padding(horizontal = 16.dp))
                LazyColumn(Modifier.weight(1f, fill = false).fillMaxWidth(), state = noteScroll,
                    contentPadding = PaddingValues(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    items(state.snapshot?.points.orEmpty().filter { !it.semantic }, key = { it.name }) { point ->
                        OutlinedButton(onClick = { controller.unplaced(); controller.select(point.name) },
                            modifier = Modifier.fillMaxWidth().heightIn(min = 44.dp).testTag("map_unplaced_${point.name}"), shape = RoundedCornerShape(8.dp),
                            contentPadding = PaddingValues(horizontal = 12.dp, vertical = 8.dp)) {
                            Text(strings.text("笔记 ${point.name.removePrefix("memos/")}", "Note ${point.name.removePrefix("memos/")}"))
                        }
                    }
                }
            }
        }
    }
}

private enum class MapToolIcon(val path: String) {
    Fit("M3 12h4m10 0h4M12 3v4m0 10v4M18 12a6 6 0 1 1-12 0 6 6 0 0 1 12 0"),
    Refresh("M21 12a9 9 0 1 1-2.64-6.36L21 8M21 3v5h-5"),
    Frame("M8 3H3v5M16 3h5v5M21 16v5h-5M8 21H3v-5"),
}

@Composable
private fun MapTool(icon: MapToolIcon, label: String, enabled: Boolean = true,
    pressed: Boolean? = null, tag: String, onClick: () -> Unit) {
    val tint = Color(0xFFAEBFD2)
    val buttonSize = 32.dp
    CompositionLocalProvider(LocalMinimumInteractiveComponentSize provides buttonSize) {
        Surface(color = if (pressed == true) Color(0xFF1A2A3C) else Color.Transparent, shape = RoundedCornerShape(6.dp)) {
            IconButton(onClick, enabled = enabled, modifier = Modifier.size(buttonSize).testTag(tag).semantics {
                contentDescription = label
                if (pressed != null) toggleableState = if (pressed) ToggleableState.On else ToggleableState.Off
            }) {
                val path = remember(icon) { PathParser().parsePathString(icon.path).toPath() }
                Canvas(Modifier.size(18.dp)) {
                    withTransform({ scale(size.width / 24f, size.height / 24f, pivot = androidx.compose.ui.geometry.Offset.Zero) }) {
                        drawPath(path, if (enabled) tint else tint.copy(alpha = .4f),
                            style = Stroke(width = 1.7f, cap = StrokeCap.Round, join = StrokeJoin.Round))
                    }
                }
            }
        }
    }
}

fun mapError(code: String, strings: Strings): String = when (code) {
    "MAP_DISABLED" -> strings.text("请在设置中启用 Embedding。", "Enable embeddings in Settings.")
    "MAP_WAITING" -> strings.text("语义索引正在等待同步或构建。", "The semantic index is waiting for sync or build.")
    "MAP_BUILDING" -> strings.text("正在生成观星布局…", "Generating star map…")
    "MAP_UNAVAILABLE" -> strings.text("观星暂时不可用，稍后重试。", "Stargazing is unavailable. Please try again shortly.")
    "MAP_LAYOUT_UNAVAILABLE" -> strings.text("观星布局服务不可用，请检查 layout 服务是否已启动。",
        "The stargazing layout service is unavailable. Check that the layout service is running.")
    "MAP_LAYOUT_INCOMPATIBLE" -> strings.text("观星布局服务版本不兼容，请与 Core 一起升级并重启。",
        "The stargazing layout service is incompatible. Upgrade and restart it alongside Core.")
    "MAP_EMPTY" -> strings.text("还没有可显示的笔记。", "No notes to display yet.")
    "MAP_UPDATING" -> strings.text("正在更新，继续显示已有星图。", "Updating. The saved star map remains visible.")
    "MAP_UPDATE_UNAVAILABLE" -> strings.text("星图生成失败，请重试。", "Star map generation failed. Please try again.")
    "MAP_REBUILD_OBSERVING" -> strings.text("正在重新生成星图…", "Rebuilding star map…")
    "MAP_INCOMPATIBLE", "MAP_PROTOCOL", "MAP_RESPONSE_INVALID" -> strings.text("服务返回的地图格式无效，请刷新页面。", "Invalid map response. Please reload the page.")
    "MAP_RESOURCE_LIMIT" -> strings.text("地图读取超过技术资源预算，请稍后刷新。", "Loading the map exceeded a resource limit. Try refreshing later.")
    "MAP_LAYOUT_INVALID" -> strings.text("地图布局无效，请重建索引。", "The map layout is invalid. Rebuild the index.")
    "MAP_REQUEST_CANCELED" -> strings.text("地图读取已取消。", "Map loading canceled.")
    "REQUEST_TIMEOUT" -> strings.text("地图读取超时，请稍后刷新。", "Map loading timed out. Try refreshing later.")
    else -> connectionError(code, strings)
}

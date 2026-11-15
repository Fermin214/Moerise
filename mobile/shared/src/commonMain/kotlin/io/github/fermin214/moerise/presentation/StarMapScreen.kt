package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.focusable
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
import io.github.fermin214.moerise.data.epochMillis

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun StarMapScreen(mobile: MobileController, strings: Strings, noteScroll: LazyListState,
    onMenu: () -> Unit, onSearch: () -> Unit) {
    val controller = mobile.stars
    val state by controller.state.collectAsState()
    val connection by mobile.session.state.collectAsState()
    val map = state.snapshot
    if (connection.profile?.stargazingEnabled == false || state.disabledReason == "stargazing") {
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
    val stellar = map?.algorithm == "umap-balanced-v1" || map == null
    val mapFocus = remember { FocusRequester() }
    val density = LocalDensity.current.density
    var fieldFocused by remember { mutableStateOf(false) }
    val readerVisible = state.unplacedOnly && map?.readable == true
    val closeReader: () -> Unit = {
        controller.closeReader()
        if (state.unplacedOnly) controller.unplaced()
        mapFocus.requestFocus()
    }
    PlatformMapFullscreen(state.fullScreen)
    PlatformBackHandler(state.unplacedOnly && !state.fullScreen) { closeReader() }
    LaunchedEffect(state.unplacedOnly) {
        if (state.unplacedOnly) noteScroll.scrollToItem(0)
    }
    val code = state.error ?: when {
        map?.updateState == "unavailable" -> "MAP_UPDATE_UNAVAILABLE"
        state.rebuild?.state in setOf("unknown", "observing") && state.status in setOf("ready", "empty") -> "MAP_REBUILD_OBSERVING"
        state.status !in setOf("", "ready") -> "MAP_${state.status.uppercase()}"
        else -> null
    }
    val message = when {
        state.readerError != null -> editorError(state.readerError!!, strings)
        state.reading -> strings.text("正在读取笔记…", "Loading note…")
        code != null -> mapError(code, strings)
        state.loading && map == null -> strings.text("正在加载观星…", "Loading stargazing…")
        else -> null
    }
    val messageError = state.readerError != null || state.error != null ||
        code in setOf("MAP_UNAVAILABLE", "MAP_UPDATE_UNAVAILABLE", "MAP_INCOMPATIBLE", "MAP_PROTOCOL")

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
                    Surface(Modifier.weight(1f).fillMaxHeight(),
                        shape = RoundedCornerShape(if (state.fullScreen) 0.dp else 14.dp),
                        color = if (stellar) Color(0xFF030A14) else Color(0xFFFBFCFE),
                        border = BorderStroke(1.dp, if (stellar) Color(0xFF172638) else MoeriseColors.Line)) {
                        BoxWithConstraints(Modifier.fillMaxSize()) {
                            Box(Modifier.fillMaxSize().focusRequester(mapFocus).onFocusChanged { fieldFocused = it.isFocused }
                                .onKeyEvent { event ->
                                    if (!fieldFocused || event.type != KeyEventType.KeyDown ||
                                        event.isAltPressed || event.isCtrlPressed || event.isMetaPressed) false
                                    else {
                                        val distance = (if (event.isShiftPressed) 100.0 else 40.0) * density
                                        when (event.key) {
                                            Key.R -> { controller.refresh(); true }
                                            Key.Home, Key.MoveHome -> { controller.fit(); true }
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
                                if (map != null) StarCanvas(state, controller, strings, onSelect = { mobile.openNote(it) })
                            }
                            Surface(
                                Modifier.align(Alignment.TopEnd).padding(12.dp)
                                    .widthIn(max = (maxWidth - 24.dp).coerceAtLeast(44.dp)),
                                color = if (stellar) Color(0xEE081321) else Color(0xF2FFFFFF),
                                shape = RoundedCornerShape(if (stellar) 8.dp else 12.dp),
                                border = BorderStroke(1.dp, if (stellar) Color(0xFF263749) else MoeriseColors.Line),
                            ) {
                                FlowRow(Modifier.padding(3.dp)) {
                                    MapTool(MapToolIcon.Plus, strings.text("放大", "Zoom in"), stellar, enabled = state.view != null,
                                        tag = "map_zoom_in", onClick = { controller.transform(1.35) })
                                    MapTool(MapToolIcon.Minus, strings.text("缩小", "Zoom out"), stellar, enabled = state.view != null,
                                        tag = "map_zoom_out", onClick = { controller.transform(1 / 1.35) })
                                    MapTool(MapToolIcon.Frame, strings.text("显示全图", "Fit map"), stellar, enabled = map != null,
                                        tag = "map_fit", onClick = { controller.fit() })
                                    MapTool(MapToolIcon.Refresh, strings.text("刷新地图", "Refresh map"), stellar,
                                        enabled = connection.connected && !state.loading && !state.posting,
                                        tag = "map_refresh", onClick = controller::refresh)
                                    if (stellar) {
                                        MapTool(MapToolIcon.Moon, strings.text("切换星图氛围", "Toggle star glow"), stellar,
                                            pressed = state.photo, tag = "map_atmosphere", onClick = controller::atmosphere)
                                        MapTool(MapToolIcon.Frame, strings.text("切换主体与全部取景", "Toggle subject and full framing"), stellar,
                                            enabled = map != null, pressed = state.allFraming, tag = "map_framing",
                                            onClick = { controller.fit(!state.allFraming) })
                                    }
                                    MapTool(MapToolIcon.Frame, strings.text(if (state.fullScreen) "退出全屏" else "全屏预览",
                                        if (state.fullScreen) "Exit full screen" else "Fullscreen"), stellar,
                                        pressed = state.fullScreen, tag = "map_fullscreen", onClick = controller::fullScreen)
                                    if (stellar) MapTool(MapToolIcon.Rebuild, strings.text("重新生成星图", "Rebuild star map"), stellar,
                                        enabled = connection.connected && !state.loading && !state.posting && state.compatible &&
                                            state.status.isNotEmpty() && state.status != "disabled" &&
                                            state.rebuild?.state !in setOf("unknown", "observing") &&
                                            (state.rebuild?.retryAfter ?: 0) <= epochMillis(),
                                        tag = "map_rebuild", onClick = { controller.rebuild() })
                                }
                            }
                            val missing = map?.points?.count { !it.semantic } ?: 0
                            if (stellar && missing > 0) OutlinedButton(
                                onClick = { controller.closeReader(); if (!state.unplacedOnly) controller.unplaced() },
                                modifier = Modifier.align(Alignment.BottomStart).padding(14.dp).testTag("map_unplaced"),
                                shape = RoundedCornerShape(10.dp), border = BorderStroke(1.dp, Color(0xFF263749)),
                                colors = ButtonDefaults.outlinedButtonColors(containerColor = Color(0xEE0B192A), contentColor = Color(0xFFB8C8D9)),
                            ) { Text(strings.text("未定位笔记（$missing）", "Notes without positions ($missing)")) }
                            if (message != null) Surface(
                                Modifier.align(Alignment.BottomStart).padding(start = 16.dp, end = 16.dp, bottom = if (stellar && missing > 0) 72.dp else 12.dp),
                                color = if (stellar) Color(0xEE0B192A) else Color(0xFFFBFCFE), shape = RoundedCornerShape(6.dp),
                            ) {
                                Text(message, style = MaterialTheme.typography.bodySmall,
                                    color = if (messageError) {
                                        if (stellar) Color(0xFFFFB4AB) else MaterialTheme.colorScheme.error
                                    } else if (stellar) Color(0xFFB8C8D9) else MoeriseColors.Muted,
                                    modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp).testTag("map_status")
                                        .semantics { liveRegion = if (messageError) LiveRegionMode.Assertive else LiveRegionMode.Polite })
                            }
                        }
                    }
                    if (wideReader) MapUnplacedNotes(mobile, state, strings, noteScroll, closeReader,
                        Modifier.width(readerWidth).fillMaxHeight())
                }
                if (readerVisible && !wideReader) MapUnplacedNotes(mobile, state, strings, noteScroll, closeReader,
                    Modifier.align(Alignment.BottomCenter).fillMaxWidth().heightIn(max = readerHeight), bottomSheet = true)
            }
        }
    }
}

@Composable
private fun MapUnplacedNotes(mobile: MobileController, state: StarMapState, strings: Strings,
    noteScroll: LazyListState, onClose: () -> Unit,
    modifier: Modifier, bottomSheet: Boolean = false) {
    Surface(modifier, color = Color.White,
        shape = if (bottomSheet) RoundedCornerShape(topStart = 16.dp, topEnd = 16.dp) else RoundedCornerShape(14.dp),
        border = BorderStroke(1.dp, MoeriseColors.Line), shadowElevation = if (bottomSheet) 8.dp else 0.dp) {
        Column {
            Row(Modifier.fillMaxWidth().padding(horizontal = 6.dp, vertical = 4.dp), horizontalArrangement = Arrangement.End) {
                IconButton(onClick = onClose, modifier = Modifier.size(44.dp).testTag("map_close_reader")) {
                    WebIcon(WebIcon.Close, strings.text("关闭笔记列表", "Close notes list"))
                }
            }
            if (state.unplacedOnly) {
                Text(strings.text("未定位笔记", "Notes without positions"), style = MaterialTheme.typography.titleMedium,
                    modifier = Modifier.padding(horizontal = 16.dp))
                LazyColumn(Modifier.weight(1f, fill = false).fillMaxWidth(), state = noteScroll,
                    contentPadding = PaddingValues(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    items(state.snapshot?.points.orEmpty().filter { !it.semantic }, key = { it.name }) { point ->
                        OutlinedButton(onClick = { mobile.stars.unplaced(); mobile.openNote(point.name) },
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
    Plus("M5 12h14M12 5v14"),
    Minus("M5 12h14"),
    Frame("M8 3H3v5M16 3h5v5M21 16v5h-5M8 21H3v-5"),
    Refresh("M20 7v5h-5M4 17v-5h5M6 7a7 7 0 0 1 12-1l2 3M4 15l2 3a7 7 0 0 0 12-1"),
    Moon("M12 3a9 9 0 1 0 9 9 7 7 0 0 1-9-9"),
    Rebuild("M12 3v4m0 10v4M3 12h4m10 0h4M6 6l3 3m6 6 3 3M6 18l3-3m6-6 3-3"),
}

@Composable
private fun MapTool(icon: MapToolIcon, label: String, stellar: Boolean, enabled: Boolean = true,
    pressed: Boolean? = null, tag: String, onClick: () -> Unit) {
    val tint = if (stellar) Color(0xFFAEBFD2) else MoeriseColors.Muted
    val buttonSize = if (stellar) 32.dp else 36.dp
    CompositionLocalProvider(LocalMinimumInteractiveComponentSize provides buttonSize) {
        Surface(color = if (pressed == true) {
            if (stellar) Color(0xFF1A2A3C) else MoeriseColors.Selected
        } else Color.Transparent, shape = RoundedCornerShape(6.dp)) {
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

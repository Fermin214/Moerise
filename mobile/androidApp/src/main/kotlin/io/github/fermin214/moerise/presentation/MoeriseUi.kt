package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.relocation.BringIntoViewRequester
import androidx.compose.foundation.relocation.bringIntoViewRequester
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.remember
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.tween
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch

val LocalWebSearchPanel = staticCompositionLocalOf<(@Composable () -> Unit)?> { null }
val LocalWebSearchOpen = staticCompositionLocalOf { false }
val LocalWebSyncAction = staticCompositionLocalOf<(() -> Unit)?> { null }
internal val LocalLeaveWriting = staticCompositionLocalOf<() -> Unit> { {} }

@Composable
fun WebSearchPanel(query: SearchQuery, strings: Strings, onChange: (SearchQuery) -> Unit, onClose: () -> Unit, onSubmit: () -> Unit) {
    val focus = remember { FocusRequester() }
    var modeMenu by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) { focus.requestFocus() }
    Column(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, bottom = 12.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            Surface(Modifier.weight(1f), color = Color.White, shape = RoundedCornerShape(10.dp),
                border = BorderStroke(1.dp, MoeriseColors.Line)) {
                Row(Modifier.padding(horizontal = 11.dp), verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    WebIcon(WebIcon.Search, modifier = Modifier.size(16.dp))
                    BasicTextField(query.text, { onChange(query.copy(text = it)) }, singleLine = true,
                        textStyle = MaterialTheme.typography.bodyLarge.copy(color = MoeriseColors.Ink),
                        cursorBrush = SolidColor(MoeriseColors.Blue),
                        keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
                        keyboardActions = KeyboardActions(onSearch = { onSubmit() }),
                        modifier = Modifier.weight(1f).heightIn(min = 44.dp).drawerGestureRegion().focusRequester(focus).testTag("search_query")
                            .semantics { contentDescription = strings.text("搜索笔记", "Search notes") }
                            .onPreviewKeyEvent { if (it.key == Key.Escape && it.type == KeyEventType.KeyDown) { onClose(); true } else false },
                        decorationBox = { input -> Box(
                            modifier = Modifier.testTag(if (query.text.isEmpty()) "search_query_empty" else "search_query_filled"),
                            contentAlignment = androidx.compose.ui.Alignment.CenterStart,
                        ) {
                            if (query.text.isEmpty()) Text(strings.text("搜索笔记", "Search notes"), color = MoeriseColors.Muted)
                            input()
                        } })
                }
            }
            IconButton(onClose, Modifier.size(44.dp).testTag("search_close")) {
                WebIcon(WebIcon.Close, strings.text("收起搜索", "Close search"))
            }
        }
        Row(Modifier.fillMaxWidth(), verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            Text(strings.text("搜索方式", "Search mode"), style = MaterialTheme.typography.bodySmall,
                color = MoeriseColors.Muted, modifier = Modifier.weight(1f))
            Box {
                TextButton(onClick = { modeMenu = true }) {
                    Text(when (query.mode) { "semantic" -> strings.text("语义", "Semantic"); "hybrid" -> strings.text("混合", "Hybrid"); else -> strings.text("关键词", "Keyword") })
                    WebIcon(WebIcon.ChevronRight, modifier = Modifier.size(14.dp))
                }
                DropdownMenu(modeMenu, onDismissRequest = { modeMenu = false },
                    modifier = Modifier.platformTestTags()) {
                    listOf("keyword" to strings.text("关键词", "Keyword"), "semantic" to strings.text("语义", "Semantic"), "hybrid" to strings.text("混合", "Hybrid")).forEach { (value, label) ->
                        DropdownMenuItem(text = { Text(label) }, onClick = { modeMenu = false; onChange(query.copy(mode = value)) })
                    }
                }
            }
        }
    }
}

object MoeriseColors {
    val Paper = Color(0xFFF7F9FC)
    val Ink = Color(0xFF1F2328)
    val Muted = Color(0xFF5F6672)
    val Line = Color(0xFFE3E8EF)
    val Blue = Color(0xFF1E4A9A)
    val BlueSurface = Color(0xFFDCE8FB)
    val Hover = Color(0xFFE9EEF5)
    val Selected = Color(0xFFE3ECFA)
    val TagSurface = Color(0xFFEEF4FD)
    val TagLine = Color(0xFFC9DAF5)
}

private fun webText(size: Int, height: Float, weight: FontWeight = FontWeight.Normal) = TextStyle(
    fontFamily = FontFamily.SansSerif, fontSize = size.sp, lineHeight = height.sp,
    letterSpacing = 0.sp, fontWeight = weight,
)

@Composable
fun MoeriseTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = lightColorScheme(
            primary = MoeriseColors.Blue, onPrimary = Color.White,
            primaryContainer = MoeriseColors.BlueSurface, onPrimaryContainer = MoeriseColors.Blue,
            secondary = MoeriseColors.Blue, secondaryContainer = MoeriseColors.Selected,
            onSecondaryContainer = MoeriseColors.Blue,
            background = MoeriseColors.Paper, onBackground = MoeriseColors.Ink,
            surface = Color.White, onSurface = MoeriseColors.Ink,
            surfaceVariant = MoeriseColors.Paper, onSurfaceVariant = MoeriseColors.Muted,
            surfaceContainer = MoeriseColors.Paper, surfaceContainerLow = MoeriseColors.Paper,
            surfaceContainerHigh = Color.White, surfaceTint = Color.White,
            outline = MoeriseColors.Line, outlineVariant = MoeriseColors.Line,
            error = Color(0xFFB42318),
        ),
        typography = Typography(
            headlineLarge = webText(28, 39.2f, FontWeight.SemiBold),
            headlineMedium = webText(24, 33.6f, FontWeight.SemiBold),
            headlineSmall = webText(22, 30.8f, FontWeight.SemiBold),
            titleLarge = webText(22, 30.8f, FontWeight.SemiBold),
            titleMedium = webText(17, 27.2f, FontWeight.SemiBold),
            titleSmall = webText(14, 22.4f, FontWeight.SemiBold),
            bodyLarge = webText(16, 29.6f),
            bodyMedium = webText(14, 22.4f),
            bodySmall = webText(13, 20.8f),
            labelLarge = webText(14, 22.4f, FontWeight.SemiBold),
            labelMedium = webText(12, 19.2f),
            labelSmall = webText(11, 17.6f),
        ),
        shapes = Shapes(
            extraSmall = RoundedCornerShape(6.dp), small = RoundedCornerShape(8.dp),
            medium = RoundedCornerShape(10.dp), large = RoundedCornerShape(14.dp),
            extraLarge = RoundedCornerShape(18.dp),
        ),
    ) {
        CompositionLocalProvider(LocalMinimumInteractiveComponentSize provides 44.dp, content = content)
    }
}

@Composable
fun WebToolbar(
    strings: Strings,
    onMenu: () -> Unit,
    onSearch: (() -> Unit)? = null,
    modifier: Modifier = Modifier,
    title: String = "Moerise",
    syncStatus: SyncStatus? = null,
    onSync: (() -> Unit)? = null,
) {
    val searchExpanded = LocalWebSearchOpen.current
    val syncAction = onSync ?: LocalWebSyncAction.current
    Column(modifier.fillMaxWidth().platformTestTags()) {
        Row(Modifier.fillMaxWidth().height(56.dp).padding(horizontal = 8.dp),
            verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            IconButton(onClick = onMenu, modifier = Modifier.size(44.dp).drawerOpener().testTag("open_menu")) {
                WebIcon(WebIcon.Menu, strings.text("打开导航", "Open navigation"), Modifier.size(20.dp))
            }
            Text(title, style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.weight(1f).padding(start = 8.dp))
            if (syncAction != null && syncStatus != null && syncStatus != SyncStatus.CONNECTED) SyncButton(syncStatus, strings, syncAction)
            if (onSearch != null) IconButton(onClick = onSearch, modifier = Modifier.size(44.dp).testTag("open_search").semantics {
                stateDescription = if (searchExpanded) strings.text("已展开", "Expanded") else strings.text("已收起", "Closed")
            }) {
                WebIcon(WebIcon.Search, strings.text("搜索笔记", "Search notes"), Modifier.size(20.dp))
            }
        }
        LocalWebSearchPanel.current?.invoke()
        HorizontalDivider(color = MoeriseColors.Line, thickness = 1.dp)
    }
}

@Composable
fun SyncButton(status: SyncStatus, strings: Strings, onClick: () -> Unit) {
    val state = when (status) {
        SyncStatus.SYNCING -> strings.text("同步中", "Syncing")
        SyncStatus.CONNECTED -> strings.text("连接成功", "Connected")
        SyncStatus.PENDING -> strings.text("等待同步", "Pending sync")
        SyncStatus.ATTENTION -> strings.text("同步冲突", "Sync conflict")
        SyncStatus.SIGN_IN -> strings.text("重新登录", "Sign in again")
        SyncStatus.OFFLINE -> strings.text("离线", "Offline")
    }
    IconButton(onClick = onClick, modifier = Modifier.size(44.dp).testTag("sync_notes").semantics {
        contentDescription = if (status == SyncStatus.SIGN_IN) strings.text("重新登录", "Sign in again")
            else strings.text("同步笔记", "Sync notes")
        stateDescription = state
    }) {
        SyncGlyph(status)
    }
}

@Composable
fun SyncGlyph(status: SyncStatus, modifier: Modifier = Modifier) {
    val pulse = if (status == SyncStatus.SYNCING)
        rememberInfiniteTransition(label = "sync").animateFloat(0.45f, 1f,
            animationSpec = infiniteRepeatable(tween(900, easing = LinearEasing)), label = "breath") else null
    val (icon, tint, dotColor) = when (status) {
        SyncStatus.SYNCING -> Triple(WebIcon.Sync, MoeriseColors.Blue, MoeriseColors.Blue)
        SyncStatus.CONNECTED -> Triple(WebIcon.Sync, Color(0xFF51816A), null)
        SyncStatus.PENDING -> Triple(WebIcon.Sync, MoeriseColors.Blue, MoeriseColors.Blue)
        SyncStatus.ATTENTION -> Triple(WebIcon.Alert, Color(0xFFB42318), Color(0xFFB42318))
        SyncStatus.SIGN_IN -> Triple(WebIcon.SignIn, Color(0xFFB47724), Color(0xFFB47724))
        SyncStatus.OFFLINE -> Triple(WebIcon.CloudOff, Color(0xFFE05246), Color(0xFFE05246))
    }
    Box(modifier.size(20.dp), contentAlignment = androidx.compose.ui.Alignment.Center) {
        WebIcon(icon, modifier = Modifier.fillMaxSize().graphicsLayer { alpha = pulse?.value ?: 1f }, tint = tint)
        if (dotColor != null && status != SyncStatus.ATTENTION && status != SyncStatus.SIGN_IN) {
            Box(Modifier.align(androidx.compose.ui.Alignment.TopEnd).size(5.dp).background(dotColor, CircleShape).clearAndSetSemantics {})
        }
    }
}

@Composable
fun WebAction(
    text: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true,
) {
    Button(onClick = onClick, enabled = enabled, modifier = modifier.heightIn(min = 44.dp),
        shape = RoundedCornerShape(10.dp), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp),
        colors = ButtonDefaults.buttonColors(
            containerColor = MoeriseColors.BlueSurface, contentColor = MoeriseColors.Blue,
            disabledContainerColor = MoeriseColors.BlueSurface.copy(alpha = .6f),
            disabledContentColor = MoeriseColors.Blue.copy(alpha = .45f),
        )) { Text(text, fontSize = 14.sp, fontWeight = FontWeight.SemiBold) }
}

@Composable
fun WebTabs(labels: List<String>, selected: Int, onSelect: (Int) -> Unit, modifier: Modifier = Modifier,
    tagPrefix: String? = null, enabled: Boolean = true) {
    val focus = remember(labels.size) { List(labels.size) { FocusRequester() } }
    val scope = rememberCoroutineScope()
    Surface(modifier.fillMaxWidth(), color = Color(0xFFF0F4F9), shape = RoundedCornerShape(12.dp)) {
        Row(Modifier.horizontalScroll(rememberScrollState()).padding(4.dp), horizontalArrangement = Arrangement.spacedBy(2.dp)) {
            labels.forEachIndexed { index, label ->
                val bringIntoView = remember { BringIntoViewRequester() }
                Surface(
                    modifier = Modifier.then(if (tagPrefix != null) Modifier.testTag("${tagPrefix}_$index") else Modifier)
                        .focusRequester(focus[index]).bringIntoViewRequester(bringIntoView)
                        .onFocusChanged { if (it.isFocused) scope.launch { bringIntoView.bringIntoView() } }
                        .onPreviewKeyEvent { event ->
                            if (!enabled || event.type != KeyEventType.KeyDown) false
                            else {
                                val next = when (event.key) {
                                    Key.DirectionLeft -> (selected - 1 + labels.size) % labels.size
                                    Key.DirectionRight -> (selected + 1) % labels.size
                                    Key.MoveHome -> 0
                                    Key.MoveEnd -> labels.lastIndex
                                    else -> return@onPreviewKeyEvent false
                                }
                                onSelect(next); focus[next].requestFocus(); true
                            }
                        }
                        .semantics { this.selected = selected == index }
                        .clickable(enabled = enabled, role = Role.Tab) { onSelect(index) },
                    color = if (selected == index) Color.White else Color.Transparent,
                    shape = RoundedCornerShape(8.dp),
                ) {
                    Box(Modifier.heightIn(min = 44.dp).padding(horizontal = 14.dp, vertical = 10.dp),
                        contentAlignment = androidx.compose.ui.Alignment.Center) {
                        Text(label, fontSize = 14.sp, color = if (selected == index) MoeriseColors.Blue else MoeriseColors.Muted,
                            fontWeight = if (selected == index) FontWeight.SemiBold else FontWeight.Normal)
                    }
                }
            }
        }
    }
}

@Composable
fun WebTag(text: String, onClick: () -> Unit, modifier: Modifier = Modifier) {
    Surface(modifier.clickable(onClick = onClick), color = MoeriseColors.TagSurface,
        shape = RoundedCornerShape(50), border = BorderStroke(1.dp, MoeriseColors.TagLine)) {
        Text("#${text.removePrefix("#")}", fontSize = 13.sp, lineHeight = 22.sp,
            color = Color(0xFF2A5BB5), modifier = Modifier.padding(horizontal = 9.dp))
    }
}

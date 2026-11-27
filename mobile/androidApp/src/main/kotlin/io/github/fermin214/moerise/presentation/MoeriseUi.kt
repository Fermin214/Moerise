package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsFocusedAsState
import androidx.compose.foundation.interaction.collectIsHoveredAsState
import androidx.compose.foundation.interaction.collectIsPressedAsState
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
import androidx.compose.runtime.mutableStateMapOf
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
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInParent
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import kotlinx.coroutines.delay
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
                border = BorderStroke(1.dp, MoeriseColors.FieldBorder)) {
                Row(Modifier.padding(horizontal = 11.dp), verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    WebIcon(WebIcon.Search, modifier = Modifier.size(16.dp))
                    BasicTextField(query.text, { onChange(query.copy(text = it)) }, singleLine = true,
                        textStyle = MaterialTheme.typography.bodyLarge.copy(color = MoeriseColors.Ink),
                        cursorBrush = SolidColor(MoeriseColors.Accent),
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
    val Paper = Color(0xFFF5F4F0)
    val Surface = Color.White
    val SurfaceMuted = Color(0xFFECEAE4)
    val Hover = Color(0xFFE6E3DC)
    val Line = Color(0xFFE3E0D8)
    val FieldBorder = Color(0xFF918B80)
    val Ink = Color(0xFF1F1E1C)
    val Muted = Color(0xFF68645E)
    val Accent = Color(0xFF2F5D62)
    val AccentHover = Color(0xFF254B4F)
    val AccentSurface = Color(0xFFE8F0EE)
    val AccentSurfaceStrong = Color(0xFFDCE8E5)
    val HeatmapLight = Color(0xFFE3E7E3)
    val HeatmapMedium = Color(0xFFCCD7D3)
    val Danger = Color(0xFFB42318)
    val DangerSurface = Color(0xFFFDF2F1)
}

private fun webText(size: Int, height: Float, weight: FontWeight = FontWeight.Normal) = TextStyle(
    fontFamily = FontFamily.SansSerif, fontSize = size.sp, lineHeight = height.sp,
    letterSpacing = 0.sp, fontWeight = weight,
)

@Composable
fun MoeriseTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = lightColorScheme(
            primary = MoeriseColors.Accent, onPrimary = MoeriseColors.Surface,
            primaryContainer = MoeriseColors.AccentSurface, onPrimaryContainer = MoeriseColors.Accent,
            secondary = MoeriseColors.Accent, secondaryContainer = MoeriseColors.AccentSurface,
            onSecondary = MoeriseColors.Surface, onSecondaryContainer = MoeriseColors.Accent,
            background = MoeriseColors.Paper, onBackground = MoeriseColors.Ink,
            surface = MoeriseColors.Surface, onSurface = MoeriseColors.Ink,
            surfaceVariant = MoeriseColors.SurfaceMuted, onSurfaceVariant = MoeriseColors.Muted,
            surfaceContainer = MoeriseColors.Surface, surfaceContainerLow = MoeriseColors.Paper,
            surfaceContainerHigh = MoeriseColors.Surface, surfaceTint = MoeriseColors.Surface,
            outline = MoeriseColors.FieldBorder, outlineVariant = MoeriseColors.Line,
            error = MoeriseColors.Danger, errorContainer = MoeriseColors.DangerSurface,
            onErrorContainer = MoeriseColors.Danger,
        ),
        typography = Typography(
            displayLarge = webText(20, 28f, FontWeight.SemiBold),
            displayMedium = webText(20, 28f, FontWeight.SemiBold),
            displaySmall = webText(20, 28f, FontWeight.SemiBold),
            headlineLarge = webText(20, 28f, FontWeight.SemiBold),
            headlineMedium = webText(20, 28f, FontWeight.SemiBold),
            headlineSmall = webText(20, 28f, FontWeight.SemiBold),
            titleLarge = webText(20, 28f, FontWeight.SemiBold),
            titleMedium = webText(17, 25.5f, FontWeight.SemiBold),
            titleSmall = webText(15, 24f, FontWeight.SemiBold),
            bodyLarge = webText(15, 24f),
            bodyMedium = webText(15, 24f),
            bodySmall = webText(13, 19.5f),
            labelLarge = webText(15, 24f, FontWeight.Medium),
            labelMedium = webText(13, 19.5f),
            labelSmall = webText(13, 19.5f),
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
    onCompose: (() -> Unit)? = null,
) {
    val searchExpanded = LocalWebSearchOpen.current
    val syncAction = onSync ?: LocalWebSyncAction.current
    var completedSync by remember { mutableStateOf(false) }
    var transferring by remember { mutableStateOf(false) }
    LaunchedEffect(syncStatus) {
        completedSync = syncStatus == SyncStatus.CONNECTED && transferring
        transferring = syncStatus == SyncStatus.SYNCING
        if (completedSync) { delay(1200); completedSync = false }
    }
    Column(modifier.fillMaxWidth().platformTestTags()) {
        Row(Modifier.fillMaxWidth().height(56.dp).padding(horizontal = 8.dp),
            verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            IconButton(onClick = onMenu, modifier = Modifier.size(44.dp).drawerOpener().testTag("open_menu")) {
                WebIcon(WebIcon.Menu, strings.text("打开导航", "Open navigation"), Modifier.size(20.dp))
            }
            Text(title, style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.weight(1f).padding(start = 8.dp))
            if (syncAction != null && syncStatus != null && (syncStatus != SyncStatus.CONNECTED || completedSync)) {
                SyncButton(syncStatus, strings, syncAction)
            }
            if (onSearch != null) IconButton(onClick = onSearch, modifier = Modifier.size(44.dp).testTag("open_search").semantics {
                stateDescription = if (searchExpanded) strings.text("已展开", "Expanded") else strings.text("已收起", "Closed")
            }) {
                WebIcon(WebIcon.Search, strings.text("搜索笔记", "Search notes"), Modifier.size(20.dp))
            }
            if (onCompose != null) IconButton(onClick = onCompose, modifier = Modifier.size(44.dp).testTag("quick_compose")) {
                WebIcon(WebIcon.Plus, strings.text("新建笔记", "New note"), Modifier.size(20.dp))
            }
        }
        LocalWebSearchPanel.current?.invoke()
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
            else strings.text("同步", "Sync")
        stateDescription = state
    }) {
        SyncGlyph(status)
    }
}

@Composable
fun SyncGlyph(status: SyncStatus, modifier: Modifier = Modifier) {
    val pulse = if (status == SyncStatus.SYNCING && !LocalReducedMotion.current)
        rememberInfiniteTransition(label = "sync").animateFloat(1f, .55f,
            animationSpec = infiniteRepeatable(tween(550, easing = FastOutSlowInEasing), RepeatMode.Reverse), label = "breath") else null
    val (icon, tint, dotColor) = when (status) {
        SyncStatus.SYNCING -> Triple(WebIcon.Sync, MoeriseColors.Accent, MoeriseColors.Accent)
        SyncStatus.CONNECTED -> Triple(WebIcon.Check, MoeriseColors.Accent, null)
        SyncStatus.PENDING -> Triple(WebIcon.Sync, MoeriseColors.Accent, MoeriseColors.Accent)
        SyncStatus.ATTENTION -> Triple(WebIcon.Alert, MoeriseColors.Danger, MoeriseColors.Danger)
        SyncStatus.SIGN_IN -> Triple(WebIcon.SignIn, MoeriseColors.Danger, MoeriseColors.Danger)
        SyncStatus.OFFLINE -> Triple(WebIcon.CloudOff, MoeriseColors.Danger, MoeriseColors.Danger)
    }
    Box(modifier.size(20.dp), contentAlignment = androidx.compose.ui.Alignment.Center) {
        WebIcon(icon, modifier = Modifier.fillMaxSize().graphicsLayer { alpha = pulse?.value ?: 1f }, tint = tint)
        if (dotColor != null && status != SyncStatus.ATTENTION && status != SyncStatus.SIGN_IN) {
            Box(Modifier.align(androidx.compose.ui.Alignment.TopEnd).size(5.dp).background(dotColor, CircleShape).clearAndSetSemantics {})
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun WebAction(
    text: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true,
    contentPadding: PaddingValues = PaddingValues(horizontal = 16.dp, vertical = 8.dp),
) {
    val interaction = remember { MutableInteractionSource() }
    val active = actionHighlighted(interaction)
    CompositionLocalProvider(LocalRippleConfiguration provides null) {
        Button(onClick = onClick, enabled = enabled, modifier = modifier.heightIn(min = 44.dp),
            shape = RoundedCornerShape(10.dp), contentPadding = contentPadding, interactionSource = interaction,
            colors = ButtonDefaults.buttonColors(
                containerColor = if (active) MoeriseColors.AccentHover else MoeriseColors.Accent,
                contentColor = MoeriseColors.Surface,
                disabledContainerColor = MoeriseColors.Accent.copy(alpha = .65f),
                disabledContentColor = MoeriseColors.Surface,
            )) { Text(text, style = MaterialTheme.typography.labelLarge) }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun WebSecondaryAction(
    text: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true,
) {
    val interaction = remember { MutableInteractionSource() }
    val active = actionHighlighted(interaction)
    CompositionLocalProvider(LocalRippleConfiguration provides null) {
        OutlinedButton(onClick = onClick, enabled = enabled, modifier = modifier.heightIn(min = 44.dp),
            shape = RoundedCornerShape(10.dp), contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp),
            interactionSource = interaction, border = BorderStroke(1.dp, MoeriseColors.Line),
            colors = ButtonDefaults.outlinedButtonColors(
                containerColor = if (active) MoeriseColors.SurfaceMuted else MoeriseColors.Surface,
                contentColor = MoeriseColors.Ink,
                disabledContainerColor = MoeriseColors.Surface,
                disabledContentColor = MoeriseColors.Muted,
            )) { Text(text, style = MaterialTheme.typography.labelLarge.copy(fontWeight = FontWeight.Medium)) }
    }
}

@Composable
internal fun actionHighlighted(interaction: MutableInteractionSource): Boolean {
    val pressed by interaction.collectIsPressedAsState()
    val hovered by interaction.collectIsHoveredAsState()
    val focused by interaction.collectIsFocusedAsState()
    return pressed || hovered || focused
}

@Composable
fun WebTabs(labels: List<String>, selected: Int, onSelect: (Int) -> Unit, modifier: Modifier = Modifier,
    tagPrefix: String? = null, enabled: Boolean = true) {
    val focus = remember(labels.size) { List(labels.size) { FocusRequester() } }
    val scope = rememberCoroutineScope()
    val measured = remember(labels) { mutableStateMapOf<Int, Pair<Float, Float>>() }
    val reduced = LocalReducedMotion.current
    val target = measured[selected]
    val position = animateFloatAsState(target?.first ?: 0f, if (reduced) snap() else MoeriseMotion.responsive(), label = "tab position")
    val width = animateFloatAsState(target?.second ?: 0f, if (reduced) snap() else MoeriseMotion.responsive(), label = "tab width")
    val density = LocalDensity.current
    Surface(modifier.fillMaxWidth(), color = MoeriseColors.SurfaceMuted, shape = RoundedCornerShape(12.dp)) {
        Box(Modifier.horizontalScroll(rememberScrollState()).padding(4.dp)) {
        if (target != null) Box(Modifier.width(with(density) { width.value.toDp() }).height(44.dp).graphicsLayer {
            translationX = position.value
        }.background(MoeriseColors.Surface, RoundedCornerShape(8.dp)))
        Row(horizontalArrangement = Arrangement.spacedBy(2.dp)) {
            labels.forEachIndexed { index, label ->
                val bringIntoView = remember { BringIntoViewRequester() }
                Surface(
                    modifier = Modifier.then(if (tagPrefix != null) Modifier.testTag("${tagPrefix}_$index") else Modifier)
                        .onGloballyPositioned { coordinates -> measured[index] = coordinates.positionInParent().x to coordinates.size.width.toFloat() }
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
                    color = Color.Transparent,
                    shape = RoundedCornerShape(8.dp),
                ) {
                    Box(Modifier.heightIn(min = 44.dp).padding(horizontal = 14.dp, vertical = 10.dp),
                        contentAlignment = androidx.compose.ui.Alignment.Center) {
                        Text(label, fontSize = 15.sp, color = if (selected == index) MoeriseColors.Accent else MoeriseColors.Muted,
                            fontWeight = if (selected == index) FontWeight.SemiBold else FontWeight.Normal)
                    }
                }
            }
        }
        }
    }
}

@Composable
fun WebTag(text: String, onClick: () -> Unit, modifier: Modifier = Modifier) {
    Surface(modifier.clickable(onClick = onClick), color = MoeriseColors.AccentSurface,
        shape = RoundedCornerShape(6.dp)) {
        Text("#${text.removePrefix("#")}", style = MaterialTheme.typography.bodySmall,
            color = MoeriseColors.Accent, modifier = Modifier.padding(horizontal = 8.dp, vertical = 1.dp))
    }
}

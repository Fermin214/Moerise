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
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInParent
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
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
            Surface(Modifier.weight(1f), color = MoeriseColors.Surface, shape = RoundedCornerShape(10.dp),
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

private fun webText(size: Int, height: Float, weight: FontWeight = FontWeight.Normal, spacing: Float = 0f) = TextStyle(
    fontFamily = FontFamily.SansSerif, fontSize = size.sp, lineHeight = height.sp,
    letterSpacing = spacing.sp, fontWeight = weight,
)

@Composable
fun MoeriseTheme(darkTheme: Boolean = false, content: @Composable () -> Unit) {
    val palette = if (darkTheme) DarkMoerisePalette else LightMoerisePalette
    val base = if (darkTheme) darkColorScheme() else lightColorScheme()
    MaterialTheme(
        colorScheme = base.copy(
            primary = palette.Accent, onPrimary = palette.OnAccent,
            primaryContainer = palette.AccentSurface, onPrimaryContainer = palette.Accent,
            secondary = palette.Accent, secondaryContainer = palette.AccentSurface,
            onSecondary = palette.OnAccent, onSecondaryContainer = palette.Accent,
            tertiary = palette.Accent, onTertiary = palette.OnAccent,
            tertiaryContainer = palette.AccentSurface, onTertiaryContainer = palette.Accent,
            background = palette.Paper, onBackground = palette.Ink,
            surface = palette.Surface, onSurface = palette.Ink,
            surfaceVariant = palette.SurfaceMuted, onSurfaceVariant = palette.Muted,
            surfaceContainerLowest = palette.Paper, surfaceContainerLow = palette.Paper,
            surfaceContainer = palette.Surface, surfaceContainerHigh = palette.SheetSurface,
            surfaceContainerHighest = palette.SheetSurface, surfaceTint = palette.Surface,
            surfaceDim = palette.Paper, surfaceBright = palette.SheetSurface,
            outline = palette.FieldBorder, outlineVariant = palette.Line,
            error = palette.Danger, errorContainer = palette.DangerSurface,
            onError = palette.OnDanger, onErrorContainer = palette.Danger,
            inverseSurface = palette.Ink, inverseOnSurface = palette.Paper, inversePrimary = palette.OnAccent,
            scrim = palette.DialogScrim,
        ),
        typography = Typography(
            displayLarge = webText(20, 28f, palette.HeadingWeight),
            displayMedium = webText(20, 28f, palette.HeadingWeight),
            displaySmall = webText(20, 28f, palette.HeadingWeight),
            headlineLarge = webText(20, 28f, palette.HeadingWeight),
            headlineMedium = webText(20, 28f, palette.HeadingWeight),
            headlineSmall = webText(20, 28f, palette.HeadingWeight),
            titleLarge = webText(20, 28f, palette.HeadingWeight),
            titleMedium = webText(16, 23f, palette.HeadingWeight),
            titleSmall = webText(15, 22f, palette.HeadingWeight),
            bodyLarge = webText(15, if (darkTheme) 24.3f else 24f, spacing = if (darkTheme) .3f else 0f),
            bodyMedium = webText(15, if (darkTheme) 24.3f else 24f, spacing = if (darkTheme) .3f else 0f),
            bodySmall = webText(13, 19.5f),
            labelLarge = webText(15, 24f, FontWeight.Medium),
            labelMedium = webText(13, 19.5f),
            labelSmall = webText(12, 18f),
        ),
        shapes = Shapes(
            extraSmall = RoundedCornerShape(6.dp), small = RoundedCornerShape(8.dp),
            medium = RoundedCornerShape(10.dp), large = RoundedCornerShape(14.dp),
            extraLarge = RoundedCornerShape(18.dp),
        ),
    ) {
        CompositionLocalProvider(LocalMoeriseColors provides palette, LocalMinimumInteractiveComponentSize provides 44.dp, content = content)
    }
}

@Composable
internal fun BrandTitle(modifier: Modifier = Modifier, title: String = "Moerise") {
    Text(title, modifier, fontFamily = FontFamily.Serif, fontSize = 18.sp, fontWeight = FontWeight.Normal,
        letterSpacing = 0.7.sp, color = MoeriseColors.BrandInk)
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
    Column(modifier.fillMaxWidth().platformTestTags()) {
        Row(Modifier.fillMaxWidth().height(56.dp).padding(horizontal = 8.dp),
            verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            IconButton(onClick = onMenu, modifier = Modifier.size(44.dp).drawerOpener().testTag("open_menu")) {
                WebIcon(WebIcon.Menu, strings.text("打开导航", "Open navigation"), Modifier.size(20.dp))
            }
            BrandTitle(Modifier.weight(1f).padding(start = 8.dp), title)
            if (syncAction != null && syncStatus != null && syncStatus != SyncStatus.CONNECTED && syncStatus != SyncStatus.SYNCING) {
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
    val (icon, tint) = when (status) {
        SyncStatus.SYNCING, SyncStatus.PENDING -> WebIcon.Sync to MoeriseColors.Accent
        SyncStatus.CONNECTED -> WebIcon.Check to MoeriseColors.Accent
        SyncStatus.ATTENTION -> WebIcon.Alert to MoeriseColors.Danger
        SyncStatus.SIGN_IN -> WebIcon.SignIn to MoeriseColors.Danger
        SyncStatus.OFFLINE -> WebIcon.CloudOff to MoeriseColors.Danger
    }
    WebIcon(icon, modifier = modifier.size(20.dp), tint = tint)
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
                contentColor = MoeriseColors.OnAccent,
                disabledContainerColor = MoeriseColors.Accent.copy(alpha = .65f),
                disabledContentColor = MoeriseColors.OnAccent,
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
    Surface(modifier.fillMaxWidth(), color = MoeriseColors.TabTrack, shape = RoundedCornerShape(12.dp),
        border = BorderStroke(1.dp, MoeriseColors.CardBorder)) {
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

package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.animation.core.LinearEasing
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.scale
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.fermin214.moerise.data.epochMillis
import io.github.fermin214.moerise.domain.Note
import kotlinx.coroutines.launch

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun DiscoveryScreen(page: String, mobile: MobileController, strings: Strings,
    onMenu: () -> Unit, onSearch: () -> Unit,
    onWalk: (Note) -> Unit, onSettings: () -> Unit,
    onCompose: (() -> Unit)? = null, onTag: ((String) -> Unit)? = null) {
    val review by mobile.review.state.collectAsState()
    val walk by mobile.walk.state.collectAsState()
    val connection by mobile.session.state.collectAsState()
    val isReview = page == "review"
    val loading = if (isReview) review.loading else walk.loading
    val error = if (isReview) review.error else walk.error
    val timezone = connection.profile?.timezone ?: "UTC"
    val reviewItem = review.batch?.items?.getOrNull(review.position)
    val reviewDragOffset = remember(review.batch?.id) { mutableFloatStateOf(0f) }
    val haptic = rememberMoeriseHaptic()
    fun selectReview(index: Int) {
        if (index != review.position) { haptic(); mobile.review.select(index) }
    }
    val pageScroll = rememberScrollState()

    Column(Modifier.fillMaxSize().safeDrawingPadding().testTag("${page}_page")) {
        WebToolbar(strings, onMenu, onSearch, onCompose = onCompose)
        BoxWithConstraints(Modifier.weight(1f).fillMaxWidth()) {
            val readingHeight = (maxHeight - 184.dp).coerceIn(360.dp, 480.dp)
            Column(
                Modifier.fillMaxSize().verticalScroll(pageScroll),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Column(
                    Modifier.widthIn(max = 800.dp).fillMaxWidth()
                        .padding(start = 16.dp, end = 16.dp, top = 16.dp, bottom = 20.dp),
                    verticalArrangement = Arrangement.spacedBy(16.dp),
                ) {
                    Text(strings.text(if (isReview) "回顾" else "漫步", if (isReview) "Review" else "Walk"),
                        style = MaterialTheme.typography.titleLarge)
                    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        if (isReview) {
                            val batch = review.batch
                            when {
                                batch == null && loading -> DiscoveryMessage(strings.text("正在加载回顾…", "Loading review…"))
                                batch?.items?.isEmpty() == true ->
                                    DiscoveryMessage(strings.text("今天没有可回顾的旧笔记。", "No old notes to review today."))
                                reviewItem != null -> Box(Modifier.fillMaxWidth().height(readingHeight).padding(bottom = 22.dp)) {
                                    val remaining = batch!!.items.size - review.position - 1
                                    for (depth in minOf(2, remaining) downTo 1) {
                                        Surface(
                                            Modifier.fillMaxSize().offset(y = (depth * 8).dp)
                                                .scale(1f - depth * .04f).alpha(if (depth == 1) .8f else .5f),
                                            shape = RoundedCornerShape(12.dp), color = MoeriseColors.Surface,
                                            border = BorderStroke(1.dp, MoeriseColors.Line),
                                        ) {}
                                    }
                                    ReviewStack(batch.id, batch.items.size, review.position, reviewDragOffset, ::selectReview) { index, layer ->
                                        val item = batch.items[index]
                                        DiscoveryCard(layer.fillMaxSize(), rememberScrollState()) {
                                            if (item.unavailable) DiscoveryMessage(strings.text(
                                                "这条笔记已不存在或无法访问。", "This note was deleted or is no longer accessible."))
                                            else item.note?.let { note ->
                                                DiscoveryNoteContent(note, batch.timezone, mobile, strings, review = true, onTag = onTag)
                                            }
                                        }
                                    }
                                }
                            }
                        } else {
                            if (!walk.offline && walk.embeddingEnabled == false) {
                                DiscoveryMessage(strings.text("请在设置中启用 Embedding，再开始漫步。", "Enable embeddings in Settings before starting a walk."))
                                TextButton(onClick = onSettings, modifier = Modifier.testTag("walk_settings")) {
                                    Text(strings.text("打开设置", "Open Settings"))
                                }
                            }
                            walk.configurationError?.let {
                                DiscoveryMessage(discoveryError(it, strings), error = true, modifier = Modifier.testTag("walk_configuration_error"))
                                TextButton(onClick = mobile.walk::retryConfiguration,
                                    enabled = connection.connected && !walk.configurationLoading,
                                    modifier = Modifier.testTag("walk_configuration_retry")) { Text(strings.text("重试", "Retry")) }
                            }
                            val result = walk.result
                            if (result?.item != null) {
                                WalkCards(result.item.note, walk.previous, loading, pageScroll, timezone, mobile, strings, onTag)
                            } else if (result?.exhausted != true) {
                                WalkStart(walk, connection.connected, timezone, strings) { name -> mobile.walk.start(name) }
                                walk.floaterError?.let {
                                    DiscoveryMessage(discoveryError(it, strings), error = true, modifier = Modifier.testTag("walk_floaters_error"))
                                    TextButton(onClick = mobile.walk::retryFloaters, enabled = connection.connected && !walk.floatersLoading,
                                        modifier = Modifier.testTag("walk_floaters_retry")) { Text(strings.text("重试加载笔记", "Retry loading notes")) }
                                }
                            }
                            result?.warnings?.forEach { DiscoveryMessage(discoveryError(it, strings)) }
                            if (result?.exhausted == true) DiscoveryMessage(strings.text(
                                if (result.item != null) "能走的路都走完了。" else "没有可漫步的笔记。",
                                if (result.item != null) "You've reached the end of this walk." else "No notes available for a walk.",
                            ))
                        }
                        error?.let {
                            DiscoveryMessage(discoveryError(it, strings), error = true, modifier = Modifier.testTag("discovery_error"))
                            if (isReview || walk.retry != null) TextButton(
                                onClick = { if (isReview) mobile.review.refresh() else mobile.walk.retry() },
                                enabled = connection.connected && !loading,
                                modifier = Modifier.align(Alignment.CenterHorizontally).testTag("discovery_retry"),
                            ) { Text(strings.text("重试", "Retry")) }
                            if (it == "EMBEDDING_DISABLED" && walk.embeddingEnabled != false) TextButton(onClick = onSettings) {
                                Text(strings.text("打开设置", "Open Settings"))
                            }
                        }
                    }
                    if (isReview) {
                        val batch = review.batch
                        if (batch != null && batch.items.isNotEmpty()) FlowRow(
                            Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween,
                            verticalArrangement = Arrangement.spacedBy(8.dp),
                        ) {
                            FlowRow {
                                repeat(batch.items.size) { index ->
                                    val dotScale = animateFloatAsState(if (index == review.position) 1f else .4f,
                                        if (LocalReducedMotion.current) snap() else MoeriseMotion.tactile(), label = "review position")
                                    Box(
                                        Modifier.size(25.dp, 24.dp).testTag("review_item_$index")
                                            .semantics { contentDescription = strings.text(
                                                "第 ${index + 1} / ${batch.items.size} 条", "Note ${index + 1} of ${batch.items.size}") }
                                            .selectable(index == review.position, role = Role.RadioButton) { selectReview(index) },
                                        contentAlignment = Alignment.Center,
                                    ) {
                                        Box(Modifier.size(20.dp, 8.dp).graphicsLayer { scaleX = dotScale.value }
                                            .background(if (index == review.position) MoeriseColors.Accent else MoeriseColors.FieldBorder, RoundedCornerShape(4.dp)))
                                    }
                                }
                            }
                            FlowRow(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                                reviewItem?.takeUnless { it.unavailable }?.note?.let { note ->
                                    if (note.state == "NORMAL") WebSecondaryAction(strings.text("从这条漫步", "Walk from here"),
                                        onClick = { onWalk(note) }, enabled = connection.connected, modifier = Modifier.testTag("review_walk"),
                                    )
                                }
                                WebAction(
                                    if (review.position + 1 < batch.items.size) strings.text("下一条", "Next note") else strings.text("再看一遍", "Start again"),
                                    onClick = {
                                        reviewDragOffset.floatValue = 0f
                                        selectReview((review.position + 1) % batch.items.size)
                                    }, modifier = Modifier.testTag("review_next"),
                                )
                            }
                        }
                    }
                }
            }
        }
        if (!isReview && (walk.result != null || walk.retry != null || walk.loading)) {
            Column(Modifier.fillMaxWidth().background(MoeriseColors.Paper), horizontalAlignment = Alignment.CenterHorizontally) {
                HorizontalDivider(color = MoeriseColors.Line)
                FlowRow(Modifier.widthIn(max = 800.dp).fillMaxWidth().padding(horizontal = 16.dp, vertical = 10.dp),
                    horizontalArrangement = Arrangement.spacedBy(4.dp, Alignment.End),
                    verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    WebSecondaryAction(strings.text("停止", "Stop"), mobile.walk::end, Modifier.testTag("walk_end"))
                    if (walk.result?.item != null && walk.result?.exhausted == false) WebAction(
                        if (loading) strings.text("翻找中…", "Looking…") else strings.text("继续漫步", "Continue walk"),
                        onClick = mobile.walk::next,
                        enabled = !loading && (walk.offline && walk.hasLocalNotes || connection.connected && walk.embeddingEnabled == true),
                        modifier = Modifier.testTag("walk_next"),
                    )
                }
            }
        }
    }
}

@Composable
private fun WalkCards(note: Note, previous: Note?, loading: Boolean, pageScroll: ScrollState, timezone: String,
    mobile: MobileController, strings: Strings, onTag: ((String) -> Unit)?) {
    val reduced = LocalReducedMotion.current
    var displayed by remember { mutableStateOf(note to previous) }
    val progress = remember { Animatable(0f) }
    LaunchedEffect(Unit) { pageScroll.scrollTo(0) }
    LaunchedEffect(note, reduced, loading) {
        if (displayed.first.name == note.name) {
            progress.snapTo(0f)
            displayed = note to previous
        } else {
            progress.animateTo(-1f, tween(if (reduced) 80 else 100))
            pageScroll.scrollTo(0)
            progress.snapTo(1f)
            displayed = note to previous
            progress.animateTo(0f, tween(if (reduced) 80 else 140))
        }
    }
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        displayed.second?.let { prior ->
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.width(1.dp).height(20.dp).background(MoeriseColors.Line))
                Text("↑", style = MaterialTheme.typography.bodySmall, color = MoeriseColors.Muted)
                val summary = remember(prior.content, prior.tags, prior.attachments) { walkSummaryPreview(prior) }
                Text(summary.ifBlank { strings.text("笔记", "Note") }, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    style = MaterialTheme.typography.bodySmall, color = MoeriseColors.Muted)
            }
        }
        key(displayed.first.name) {
            Surface(Modifier.fillMaxWidth().graphicsLayer {
                val displacement = progress.value
                alpha = 1f - kotlin.math.abs(displacement)
                if (!reduced) translationY = 24.dp.toPx() * displacement
            }, shape = RoundedCornerShape(12.dp), color = MoeriseColors.Surface) {
                Column(Modifier.padding(horizontal = 20.dp, vertical = 16.dp)) {
                    DiscoveryNoteContent(displayed.first, timezone, mobile, strings, onTag = onTag)
                }
            }
        }
    }
}

@Composable
internal fun ReviewStack(batch: String, size: Int, position: Int, offset: MutableFloatState, select: (Int) -> Unit,
    card: @Composable (Int, Modifier) -> Unit) {
    val reduced = LocalReducedMotion.current
    val rebound = remember(batch) { Animatable(0f) }
    val lastPosition = remember(batch) { intArrayOf(position) }
    val from = remember(batch, position) { lastPosition[0] }
    val origin = remember(batch, position) { offset.floatValue }
    val direction = if (origin != 0f) kotlin.math.sign(origin) else if (position > from || from == size - 1 && position == 0) -1f else 1f
    val progress = remember(batch, position) { Animatable(if (from == position) 1f else 0f) }
    val fadeIn = remember(batch, position) { Animatable(if (from == position) 1f else 0f) }
    var outgoing by remember(batch, position) { mutableStateOf(from != position) }
    SideEffect { lastPosition[0] = position }
    val selected by rememberUpdatedState(select)
    val scope = rememberCoroutineScope()
    val threshold = with(LocalDensity.current) { 72.dp.toPx() }
    LaunchedEffect(progress, reduced) {
        offset.floatValue = 0f
        if (from != position) {
            progress.animateTo(1f, tween(if (reduced) 80 else 240, easing = LinearEasing))
            outgoing = false
            if (reduced) fadeIn.animateTo(1f, tween(80))
        }
    }
    fun settle() {
        val distance = offset.floatValue
        offset.floatValue = 0f
        scope.launch {
            rebound.snapTo(distance)
            rebound.animateTo(0f, if (reduced) snap() else MoeriseMotion.responsive())
        }
    }
    Box(Modifier.fillMaxSize().clipToBounds().drawerGestureRegion().pointerInput(batch, size, position) {
        if (size < 2) return@pointerInput
        detectHorizontalDragGestures(
            onDragStart = { outgoing = false; scope.launch { fadeIn.snapTo(1f); rebound.snapTo(0f) } },
            onDragCancel = ::settle,
            onDragEnd = {
                val distance = offset.floatValue
                if (kotlin.math.abs(distance) < threshold) settle()
                else {
                    selected((position + (if (distance < 0f) 1 else -1) + size) % size)
                }
            },
        ) { change, distance ->
            change.consume()
            offset.floatValue += distance
        }
    }) {
        if (!reduced || !outgoing) key(position) {
            card(position, Modifier.graphicsLayer {
                if (reduced) alpha = fadeIn.value
                else if (outgoing) {
                    translationY = 8.dp.toPx() * (1f - progress.value)
                    scaleX = .96f + .04f * progress.value
                    scaleY = scaleX
                    alpha = .92f + .08f * progress.value
                } else translationX = offset.floatValue + rebound.value
            })
        }
        if (outgoing) key(from) {
            if (reduced) {
                card(from, Modifier.clearAndSetSemantics {}.graphicsLayer { alpha = 1f - progress.value })
            } else {
                Box(Modifier.fillMaxSize().clearAndSetSemantics {}.graphicsLayer {
                    val fraction = progress.value
                    val end = direction * this.size.width * .92f
                    translationX = origin + (end - origin) * fraction
                    rotationZ = direction * 2.5f * fraction
                }.clip(RoundedCornerShape(12.dp)).background(MoeriseColors.Surface)) {
                    card(from, Modifier.graphicsLayer { alpha = ((1f - progress.value) / .3f).coerceAtMost(1f) })
                }
            }
        }
    }
}

@Composable
private fun DiscoveryMessage(text: String, error: Boolean = false, modifier: Modifier = Modifier) {
    Text(text, style = MaterialTheme.typography.bodySmall,
        color = if (error) MaterialTheme.colorScheme.error else MoeriseColors.Muted,
        modifier = modifier.semantics { liveRegion = if (error) LiveRegionMode.Assertive else LiveRegionMode.Polite })
}

@Composable
private fun WalkStart(walk: WalkState, connected: Boolean, timezone: String, strings: Strings, onStart: (String) -> Unit) {
    val enabled = !walk.loading && (walk.offline && walk.hasLocalNotes || connected && walk.embeddingEnabled == true)
    val looking = walk.loading || walk.initialConfigurationLoading
    Column {
        WebAction(if (looking) strings.text("翻找中…", "Looking…") else strings.text("随机开始", "Random start"),
            onClick = { onStart("") }, modifier = Modifier.heightIn(min = 52.dp).testTag("walk_start"),
            enabled = enabled, contentPadding = PaddingValues(horizontal = 28.dp, vertical = 8.dp))
        if (walk.floaters.isNotEmpty()) {
            Spacer(Modifier.height(20.dp))
            Text(strings.text("从一条笔记开始", "Start from a note"), style = MaterialTheme.typography.labelMedium, color = MoeriseColors.Muted)
            Spacer(Modifier.height(8.dp))
            BoxWithConstraints(Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).background(MoeriseColors.Surface)) {
                val count = if (maxWidth < 520.dp) 4 else 6
                Column {
                    walk.floaters.take(count).forEachIndexed { index, note ->
                        if (index > 0) HorizontalDivider(color = MoeriseColors.Line)
                        val summary = note.summary.ifBlank { strings.text("笔记", "Note") }
                        val date = displayNoteDay(note.createTime, timezone, strings.text("zh", "en") == "en")
                        Row(Modifier.fillMaxWidth().heightIn(min = 52.dp).testTag("walk_floater_${note.name}")
                            .clickable(enabled = enabled, role = Role.Button) { onStart(note.name) }
                            .semantics { contentDescription = strings.text("从这条笔记开始：$summary", "Start from this note: $summary") }
                            .padding(horizontal = 16.dp, vertical = 14.dp),
                            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                            Text(summary, Modifier.weight(1f).clearAndSetSemantics {}, maxLines = 2, overflow = TextOverflow.Ellipsis,
                                style = MaterialTheme.typography.bodyMedium, color = if (enabled) MoeriseColors.Ink else MoeriseColors.Muted)
                            Text(date, style = MaterialTheme.typography.bodySmall, color = MoeriseColors.Muted)
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun DiscoveryCard(modifier: Modifier, scroll: ScrollState, content: @Composable () -> Unit) {
    Surface(modifier, shape = RoundedCornerShape(12.dp), color = MoeriseColors.Surface,
        border = BorderStroke(1.dp, MoeriseColors.Line)) {
        Column(Modifier.fillMaxSize().verticalScroll(scroll).padding(horizontal = 20.dp, vertical = 22.dp)) { content() }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun DiscoveryNoteContent(note: Note, timezone: String, mobile: MobileController, strings: Strings,
    review: Boolean = false, onTag: ((String) -> Unit)? = null) {
    val presentation = rememberNotePresentation(note.content, note.tags)
    val date = displayNoteDay(note.createTime, timezone, strings.text("zh", "en") == "en")
    val age = if (review) remember(note.createTime) { elapsedNoteDays(note.createTime, epochMillis()) } else null
    val core = mobile.session.core
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        MarkdownBody(presentation.body, note = note, core = core, tags = presentation.inlineTags, onTag = onTag, strings = strings)
        NoteAttachments(note, core, strings)
        NoteTags(presentation.tags, plain = true, onTag = onTag)
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.SpaceBetween) {
            Text(when {
                !review || age == null -> date
                age == 0L -> strings.text("今天", "Today")
                else -> strings.text("$age 天前", "$age ${if (age == 1L) "day" else "days"} ago")
            }, color = MoeriseColors.Muted, style = MaterialTheme.typography.bodySmall)
            val interaction = remember { MutableInteractionSource() }
            val active = actionHighlighted(interaction)
            Box(Modifier.heightIn(min = 44.dp).testTag("discovery_open")
                .clickable(interactionSource = interaction, indication = null, role = Role.Button) { mobile.openNote(note.name) },
                contentAlignment = Alignment.CenterEnd) {
                Text(strings.text("在主页查看", "View on home"), style = MaterialTheme.typography.bodySmall,
                    color = if (active) MoeriseColors.Accent else MoeriseColors.Muted)
            }
        }
    }
}

fun discoveryError(code: String, strings: Strings): String = when (code) {
    "EMBEDDING_DISABLED" -> strings.text("请在设置中启用 Embedding。", "Enable embeddings in Settings.")
    "EMBEDDING_UNAVAILABLE" -> strings.text("Embedding 暂时无法连接，请重试或在设置中检查连接。", "The embedding service is unavailable. Retry or check the connection in Settings.")
    "EMBEDDING_RATE_LIMITED" -> strings.text("Embedding 调用受限，请稍后重试。", "Embedding requests are rate limited. Please try again later.")
    "EMBEDDING_PROTOCOL" -> strings.text("Embedding 返回格式或维度与配置不符，请在设置中检查连接。", "The embedding response format or dimensions do not match. Check the connection in Settings.")
    "INDEX_NOT_READY" -> strings.text("语义索引尚未就绪，请在设置中查看构建进度后重试。", "The semantic index is not ready. Check its progress in Settings.")
    "NO_SOURCE_VECTORS" -> strings.text("这条笔记没有可用的正文向量，请从其他笔记开始。", "This note has no usable text embedding. Start from another note.")
    "SEMANTIC_UNAVAILABLE" -> strings.text("语义索引暂时不可用，请稍后重试。", "The semantic index is unavailable. Please try again later.")
    "WALK_ARCHIVED" -> strings.text("归档笔记不参与漫步，请选择其他笔记。", "Archived notes are excluded from walks. Choose another note.")
    "SOURCE_NOT_FOUND" -> strings.text("这条笔记已不存在或无法访问。", "This note was deleted or is no longer accessible.")
    "SOURCE_CHANGED" -> strings.text("笔记发生了变化，请重新读取。", "This note changed. Read it again.")
    "WALK_INTERRUPTED" -> strings.text("漫步读取已中断，可以重试。", "The walk was interrupted. You can retry.")
    "WALK_RESOURCE_LIMIT", "RESOURCE_LIMIT" -> strings.text("漫步暂时达到技术资源限制，请重试。", "The walk reached a resource limit. Please try again.")
    "WALK_RESPONSE_INVALID" -> strings.text("服务返回的漫步内容不完整，请重试。", "The walk response is incomplete. Please try again.")
    "REVIEW_RESPONSE_INVALID" -> strings.text("服务返回的回顾内容不完整，请重试。", "The review response is incomplete. Please try again.")
    "REQUEST_TIMEOUT" -> strings.text("读取超时，请重试。", "Reading timed out. Please try again.")
    else -> connectionError(code, strings)
}

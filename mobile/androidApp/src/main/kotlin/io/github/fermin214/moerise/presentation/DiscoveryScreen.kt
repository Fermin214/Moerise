package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsFocusedAsState
import androidx.compose.foundation.interaction.collectIsHoveredAsState
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.scale
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.fermin214.moerise.data.epochMillis
import io.github.fermin214.moerise.domain.Note
import kotlinx.serialization.json.*

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun DiscoveryScreen(page: String, mobile: MobileController, strings: Strings, readingScroll: ScrollState,
    onMenu: () -> Unit, onSearch: () -> Unit,
    onInsight: (Note) -> Unit, onWalk: (Note) -> Unit, onSettings: () -> Unit) {
    val review by mobile.review.state.collectAsState()
    val walk by mobile.walk.state.collectAsState()
    val connection by mobile.session.state.collectAsState()
    val isReview = page == "review"
    val loading = if (isReview) review.loading else walk.loading
    val error = if (isReview) review.error else walk.error
    val timezone = connection.profile?.timezone ?: "UTC"
    val reviewItem = review.batch?.items?.getOrNull(review.position)
    LaunchedEffect(page, reviewItem?.name, walk.result?.item?.note?.name) { readingScroll.scrollTo(0) }

    Column(Modifier.fillMaxSize().safeDrawingPadding().testTag("${page}_page")) {
        WebToolbar(strings, onMenu, onSearch)
        BoxWithConstraints(Modifier.weight(1f).fillMaxWidth()) {
            val readingHeight = (maxHeight - 184.dp).coerceIn(360.dp, 480.dp)
            val topSpace = 20.dp + ((maxHeight - readingHeight - 151.dp) / 2).coerceAtLeast(0.dp)
            Column(
                Modifier.fillMaxSize().verticalScroll(rememberScrollState()),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Column(
                    Modifier.widthIn(max = 800.dp).fillMaxWidth()
                        .padding(start = 16.dp, end = 16.dp, top = topSpace, bottom = 20.dp),
                    verticalArrangement = Arrangement.spacedBy(16.dp),
                ) {
                    Text(strings.text(if (isReview) "回顾" else "漫步", if (isReview) "Review" else "Walk"),
                        style = MaterialTheme.typography.titleLarge, modifier = Modifier.heightIn(min = 35.dp))
                    Column(Modifier.fillMaxWidth().height(readingHeight),
                        verticalArrangement = Arrangement.spacedBy(12.dp, Alignment.CenterVertically)) {
                        if (isReview) {
                            val batch = review.batch
                            when {
                                batch == null && loading -> DiscoveryMessage(strings.text("正在加载回顾…", "Loading review…"))
                                batch?.items?.isEmpty() == true ->
                                    DiscoveryMessage(strings.text("今天没有可回顾的旧笔记。", "No old notes to review today."))
                                reviewItem != null -> Box(Modifier.weight(1f).fillMaxWidth().padding(bottom = 22.dp)) {
                                    val remaining = batch!!.items.size - review.position - 1
                                    for (depth in minOf(2, remaining) downTo 1) {
                                        Surface(
                                            Modifier.fillMaxSize().offset(y = (depth * 8).dp)
                                                .scale(1f - depth * .04f).alpha(if (depth == 1) .8f else .5f),
                                            shape = RoundedCornerShape(22.dp), color = Color.White,
                                            border = BorderStroke(1.dp, MoeriseColors.Line),
                                        ) {}
                                    }
                                    DiscoveryCard(Modifier.fillMaxSize(), readingScroll, "${batch.id}:${review.position}", review = true) {
                                        if (reviewItem.unavailable) DiscoveryMessage(strings.text(
                                            "这条笔记已不存在或无法访问。", "This note was deleted or is no longer accessible."))
                                        else reviewItem.note?.let { note ->
                                            DiscoveryNoteContent(note, batch.timezone, mobile, strings, review = true)
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
                                Column(Modifier.weight(1f).fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(10.dp),
                                    horizontalAlignment = Alignment.CenterHorizontally) {
                                    walk.previous?.let { previous ->
                                        Surface(Modifier.fillMaxWidth(.92f).alpha(.6f), color = Color.White,
                                            shape = RoundedCornerShape(14.dp), border = BorderStroke(1.dp, MoeriseColors.Line)) {
                                            Column(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                                                Text(displayNoteDate(previous.createTime, timezone, strings.text("zh", "en") == "en"),
                                                    style = MaterialTheme.typography.bodySmall, color = MoeriseColors.Muted)
                                                Text(previous.content, maxLines = 1, overflow = TextOverflow.Ellipsis,
                                                    style = MaterialTheme.typography.bodySmall)
                                            }
                                        }
                                    }
                                    DiscoveryCard(Modifier.weight(1f).fillMaxWidth(), readingScroll, result.item.note.name) {
                                        DiscoveryNoteContent(result.item.note, timezone, mobile, strings)
                                    }
                                }
                            } else if (result?.exhausted != true) {
                                BoxWithConstraints(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.Center) {
                                    val startEnabled = !loading && (walk.offline && walk.floaters.isNotEmpty() || connection.connected && walk.embeddingEnabled == true)
                                    walk.floaters.forEachIndexed { index, note ->
                                        WalkFloater(note, index, strings, startEnabled, { mobile.walk.start(note.name) },
                                            Modifier.align(when (index) {
                                                0 -> Alignment.TopStart
                                                1 -> Alignment.TopEnd
                                                2 -> Alignment.BottomStart
                                                3 -> Alignment.BottomEnd
                                                4 -> Alignment.TopCenter
                                                else -> Alignment.BottomCenter
                                            }).padding(horizontal = maxWidth * .01f)
                                                .offset(y = when (index) {
                                                    0 -> 28.dp; 1 -> 20.dp; 2 -> (-40).dp; 3 -> (-32).dp; 4 -> 6.dp; else -> (-8).dp
                                                }).widthIn(max = 170.dp).width(maxWidth * .29f))
                                    }
                                    Button(
                                        onClick = { mobile.walk.start() }, enabled = startEnabled,
                                        modifier = Modifier.heightIn(min = 52.dp).testTag("walk_start"),
                                        shape = RoundedCornerShape(10.dp), contentPadding = PaddingValues(horizontal = 28.dp),
                                        colors = ButtonDefaults.buttonColors(
                                            containerColor = MoeriseColors.BlueSurface, contentColor = MoeriseColors.Blue,
                                            disabledContainerColor = MoeriseColors.BlueSurface.copy(alpha = .6f),
                                            disabledContentColor = MoeriseColors.Blue.copy(alpha = .45f)),
                                    ) { Text(strings.text(if (loading || walk.configurationLoading) "翻找中…" else "随机开始",
                                        if (loading || walk.configurationLoading) "Looking…" else "Random start"), fontSize = 16.sp) }
                                }
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
                                    Box(
                                        Modifier.size(if (index == review.position) 26.dp else 14.dp, 24.dp).testTag("review_item_$index")
                                            .semantics { contentDescription = strings.text(
                                                "第 ${index + 1} / ${batch.items.size} 条", "Note ${index + 1} of ${batch.items.size}") }
                                            .selectable(index == review.position, role = Role.RadioButton) { mobile.review.select(index) },
                                        contentAlignment = Alignment.Center,
                                    ) {
                                        Box(Modifier.size(if (index == review.position) 20.dp else 8.dp, 8.dp)
                                            .background(if (index == review.position) Color(0xFF5B8DEF) else Color(0xFFD5DBE5), RoundedCornerShape(4.dp)))
                                    }
                                }
                            }
                            FlowRow(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                                reviewItem?.takeUnless { it.unavailable }?.note?.let { note ->
                                    if (note.state == "NORMAL") TextButton(
                                        onClick = { onWalk(note) }, enabled = connection.connected, modifier = Modifier.testTag("review_walk"),
                                    ) { Text(strings.text("从这条漫步", "Walk from here")) }
                                    TextButton(onClick = { onInsight(note) }, enabled = connection.connected) {
                                        Text(strings.text("洞察这条", "Discuss with AI"))
                                    }
                                }
                                WebAction(
                                    if (review.position + 1 < batch.items.size) strings.text("下一条", "Next note") else strings.text("再看一遍", "Start again"),
                                    onClick = mobile.review::next, modifier = Modifier.testTag("review_next"),
                                )
                            }
                        }
                    } else if (walk.result != null || walk.retry != null) {
                        FlowRow(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(4.dp, Alignment.End),
                            verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(onClick = mobile.walk::end, shape = RoundedCornerShape(10.dp),
                                modifier = Modifier.heightIn(min = 44.dp).testTag("walk_end"),
                                border = BorderStroke(1.dp, Color(0xFFD5E1F7))) { Text(strings.text("停止", "Stop")) }
                            if (walk.result?.item != null && walk.result?.exhausted == false) WebAction(
                                strings.text(if (loading) "翻找中…" else "继续漫步", if (loading) "Looking…" else "Continue walk"),
                                onClick = mobile.walk::next, enabled = !loading && (walk.offline && walk.floaters.isNotEmpty() || connection.connected && walk.embeddingEnabled == true),
                                modifier = Modifier.testTag("walk_next"),
                            )
                        }
                    }
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
private fun WalkFloater(note: Note, index: Int, strings: Strings, enabled: Boolean, onClick: () -> Unit, modifier: Modifier) {
    val interaction = remember(note.name) { MutableInteractionSource() }
    val focused by interaction.collectIsFocusedAsState()
    val hovered by interaction.collectIsHoveredAsState()
    val active = focused || hovered
    val animation = rememberInfiniteTransition(label = "walk_floater")
    val drift by animation.animateFloat(-5f, 5f,
        infiniteRepeatable(tween(2400 + index * 160), RepeatMode.Reverse), label = "walk_floater_drift")
    val preview = remember(note.content, note.attachments) {
        val text = note.content.replace(Regex("\\s+"), " ").trim()
        var end = 0
        repeat(64) {
            if (end < text.length) {
                val first = text[end++]
                if (first.isHighSurrogate() && end < text.length && text[end].isLowSurrogate()) end++
            }
        }
        text.substring(0, end).ifBlank { note.attachments.firstOrNull()?.get("filename")?.jsonPrimitive?.content.orEmpty() }
    }.ifBlank { strings.text("笔记", "Note") }
    OutlinedButton(onClick, enabled = enabled, interactionSource = interaction,
        modifier = modifier.heightIn(min = 76.dp).testTag("walk_floater_${note.name}").graphicsLayer {
            rotationZ = listOf(-4f, 3f, 2f, -3f, -1f, 1.5f)[index % 6]
            translationY = if (active) 0f else drift.dp.toPx()
        }.semantics { contentDescription = strings.text("从这条笔记开始：$preview", "Start from this note: $preview") },
        shape = RoundedCornerShape(10.dp), contentPadding = PaddingValues(horizontal = 8.dp, vertical = 10.dp),
        border = BorderStroke(1.dp, if (active) MoeriseColors.Blue else MoeriseColors.Line),
        colors = ButtonDefaults.outlinedButtonColors(containerColor = Color.White, contentColor = MoeriseColors.Ink,
            disabledContainerColor = Color.White, disabledContentColor = MoeriseColors.Muted)) {
        Text(preview, maxLines = 4, overflow = TextOverflow.Clip, fontSize = 12.sp, lineHeight = 20.4.sp)
    }
}

@Composable
private fun DiscoveryCard(modifier: Modifier, scroll: ScrollState, animationKey: String,
    review: Boolean = false, content: @Composable () -> Unit) {
    val entrance = remember(animationKey) { Animatable(0f) }
    LaunchedEffect(entrance) { entrance.animateTo(1f, tween(500)) }
    Surface(modifier.graphicsLayer {
        alpha = entrance.value
        translationY = 18.dp.toPx() * (1 - entrance.value)
        scaleX = .97f + entrance.value * .03f
        scaleY = scaleX
    }, shape = RoundedCornerShape(if (review) 22.dp else 14.dp), color = Color.White,
        border = BorderStroke(1.dp, MoeriseColors.Line), shadowElevation = 3.dp) {
        Column(Modifier.fillMaxSize().verticalScroll(scroll).padding(horizontal = 20.dp, vertical = 22.dp)) { content() }
    }
}

@OptIn(ExperimentalLayoutApi::class, kotlin.time.ExperimentalTime::class)
@Composable
internal fun DiscoveryNoteContent(note: Note, timezone: String, mobile: MobileController, strings: Strings,
    review: Boolean = false) {
    val presentation = remember(note.content, note.tags) { notePresentation(note.content, note.tags) }
    val date = displayNoteDate(note.createTime, timezone, strings.text("zh", "en") == "en")
    val age = remember(note.createTime) {
        runCatching { ((epochMillis() - kotlin.time.Instant.parse(note.createTime).toEpochMilliseconds()) / 86_400_000).coerceAtLeast(0) }.getOrNull()
    }
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            if (review && age != null) Surface(color = MoeriseColors.Selected, shape = RoundedCornerShape(50)) {
                Text(if (age == 0L) strings.text("今天", "Today")
                    else strings.text("$age 天前", "$age ${if (age == 1L) "day" else "days"} ago"),
                    style = MaterialTheme.typography.labelMedium, color = MoeriseColors.Blue,
                    modifier = Modifier.padding(horizontal = 10.dp, vertical = 2.dp))
            } else Text(date, color = MoeriseColors.Muted, style = MaterialTheme.typography.bodySmall)
            FlowRow(Modifier.weight(1f), horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End),
                verticalArrangement = Arrangement.spacedBy(8.dp)) {
                if (review && age != null) Text(date, color = MoeriseColors.Muted, style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.align(Alignment.CenterVertically))
                NoteTags(presentation.tags, Modifier.align(Alignment.CenterVertically))
                IconButton(onClick = { mobile.openNote(note.name) }, modifier = Modifier.size(44.dp)
                    .testTag("discovery_open")
                    .semantics { contentDescription = strings.text("在主页查看", "View on home") }) {
                    Canvas(Modifier.size(18.dp)) {
                        val unit = size.width / 24
                        fun point(x: Float, y: Float) = Offset(x * unit, y * unit)
                        val color = MoeriseColors.Muted
                        drawLine(color, point(9f, 6f), point(18f, 6f), 1.7f * unit, StrokeCap.Round)
                        drawLine(color, point(18f, 6f), point(18f, 15f), 1.7f * unit, StrokeCap.Round)
                        drawLine(color, point(18f, 6f), point(7f, 17f), 1.7f * unit, StrokeCap.Round)
                    }
                }
            }
        }
        val typography = MaterialTheme.typography
        MaterialTheme(typography = typography.copy(
            bodyLarge = typography.bodyLarge.copy(fontSize = 18.sp, lineHeight = if (review) 36.sp else 34.2.sp),
        )) {
            MarkdownBody(presentation.body, note = note, core = mobile.session.core)
        }
        mobile.session.core?.let { core ->
            note.attachments.forEach { item ->
                if (item["type"]?.jsonPrimitive?.content in setOf("image/png", "image/jpeg", "image/webp", "image/gif"))
                    item["content_url"]?.jsonPrimitive?.content?.let { MarkdownBody("![]($it)", note = note, core = core) }
                OpenAttachmentButton(note, item, core, strings)
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

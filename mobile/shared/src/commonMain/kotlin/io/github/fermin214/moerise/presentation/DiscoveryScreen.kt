package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import io.github.fermin214.moerise.domain.Note
import kotlinx.serialization.json.*

@Composable
fun DiscoveryScreen(page: String, mobile: MobileController, strings: Strings, readingScroll: ScrollState, onMenu: () -> Unit, onWalk: (Note) -> Unit) {
    val review by mobile.review.state.collectAsState()
    val walk by mobile.walk.state.collectAsState()
    val connection by mobile.session.state.collectAsState()
    val isReview = page == "review"
    val loading = if (isReview) review.loading else walk.loading
    val error = if (isReview) review.error else walk.error
    Column(Modifier.fillMaxSize().safeDrawingPadding().testTag("${page}_page")) {
        Row(Modifier.fillMaxWidth().padding(horizontal = 8.dp), horizontalArrangement = Arrangement.SpaceBetween) {
            TextButton(onClick = onMenu, modifier = Modifier.testTag("open_menu")) { Text(strings.text("导航", "Menu")) }
            Text(strings.text(if (isReview) "回顾" else "漫步", if (isReview) "Review" else "Walk"),
                style = MaterialTheme.typography.titleLarge, modifier = Modifier.padding(top = 12.dp))
            if (isReview) TextButton(onClick = mobile.review::refresh, enabled = connection.connected && !loading) {
                Text(strings.text("刷新", "Refresh"))
            } else Spacer(Modifier.width(64.dp))
        }
        if (loading) LinearProgressIndicator(Modifier.fillMaxWidth())
        Column(Modifier.weight(1f).verticalScroll(readingScroll).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            if (!connection.connected) Text(strings.text("连接中断", "Connection interrupted"), color = MoeriseColors.Muted)
            if (isReview) {
                review.batch?.let { batch ->
                    Text("${batch.date} · ${batch.timezone}", style = MaterialTheme.typography.labelMedium, color = MoeriseColors.Muted)
                    if (batch.items.isEmpty()) Text(strings.text("今天没有可回顾的旧笔记。", "No older notes to review today."))
                    batch.items.getOrNull(review.position)?.let { item ->
                        if (item.unavailable) Text(strings.text("这条笔记已不存在或无法访问。", "This note is missing or unavailable."))
                        else item.note?.let { DiscoveryNote(it, batch.timezone, mobile, strings, onWalk) }
                    }
                }
            } else {
                walk.result?.let { result ->
                    Text(strings.text("已走过 ${result.path.size} 条", "${result.path.size} notes visited"),
                        style = MaterialTheme.typography.labelMedium, color = MoeriseColors.Muted)
                }
                walk.previous?.let { previous ->
                    Surface(shape = RoundedCornerShape(12.dp), color = MoeriseColors.BlueSurface) {
                        Text(previous.content, maxLines = 3, modifier = Modifier.padding(12.dp), color = MoeriseColors.Muted)
                    }
                }
                walk.result?.item?.note?.let { DiscoveryNote(it, connection.profile?.timezone ?: "UTC", mobile, strings, onWalk = null) }
                if (walk.result?.exhausted == true) Text(strings.text(
                    if (walk.result?.item != null) "能走的路都走完了。" else "没有可漫步的笔记。",
                    if (walk.result?.item != null) "You’ve reached the end of this walk." else "No notes available for a walk.",
                ))
                walk.result?.warnings?.forEach { Text(discoveryError(it, strings), color = MoeriseColors.Muted) }
            }
            error?.let {
                Text(discoveryError(it, strings), color = MaterialTheme.colorScheme.error, modifier = Modifier.testTag("discovery_error"))
                if (isReview || walk.retry != null) TextButton(
                    onClick = { if (isReview) mobile.review.refresh() else mobile.walk.retry() },
                    enabled = connection.connected && !loading, modifier = Modifier.testTag("discovery_retry"),
                ) { Text(strings.text("重试", "Retry")) }
                if (it == "EMBEDDING_DISABLED") {
                    val uri = LocalUriHandler.current
                    TextButton(onClick = { connection.profile?.let { profile -> runCatching { uri.openUri("${profile.origin}/settings") } } }) {
                        Text(strings.text("打开 Web 设置", "Open web settings"))
                    }
                }
            }
        }
        HorizontalDivider(color = MoeriseColors.Line)
        Column(Modifier.fillMaxWidth().padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            if (isReview) {
                val count = review.batch?.items?.size ?: 0
                if (count > 0) {
                    Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        repeat(count) { index ->
                            FilterChip(selected = index == review.position, onClick = { mobile.review.select(index) },
                                label = { Text("${index + 1} / $count") }, modifier = Modifier.testTag("review_item_$index"))
                        }
                    }
                    Button(onClick = mobile.review::next, modifier = Modifier.fillMaxWidth().testTag("review_next")) {
                        Text(if (review.position + 1 < count) strings.text("下一条", "Next note") else strings.text("再看一遍", "Review again"))
                    }
                }
            } else Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (walk.result != null || walk.retry != null) OutlinedButton(onClick = mobile.walk::end, modifier = Modifier.testTag("walk_end")) {
                    Text(strings.text("结束漫步", "End walk"))
                }
                if (walk.result == null || walk.result?.exhausted == true) Button(
                    onClick = { mobile.walk.start() }, enabled = connection.connected && !loading,
                    modifier = Modifier.weight(1f).testTag("walk_start"),
                ) { Text(strings.text("随机开始", "Random start")) }
                else Button(onClick = mobile.walk::next, enabled = connection.connected && !loading,
                    modifier = Modifier.weight(1f).testTag("walk_next")) { Text(strings.text("继续漫步", "Continue walk")) }
            }
        }
    }
}

@Composable
private fun DiscoveryNote(note: Note, timezone: String, mobile: MobileController, strings: Strings, onWalk: ((Note) -> Unit)?) {
    Surface(shape = RoundedCornerShape(16.dp), border = BorderStroke(1.dp, MoeriseColors.Line)) {
        Column(Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text(displayServiceTime(note.createTime, timezone), color = MoeriseColors.Muted, style = MaterialTheme.typography.labelMedium)
            MarkdownBody(note.content, note = note, core = mobile.session.core)
            mobile.session.core?.let { core ->
                note.attachments.forEach { item ->
                    if (item["type"]?.jsonPrimitive?.content in setOf("image/png", "image/jpeg", "image/webp", "image/gif"))
                        item["content_url"]?.jsonPrimitive?.content?.let { MarkdownBody("![]($it)", note = note, core = core) }
                    OpenAttachmentButton(note, item, core, strings)
                }
            }
            Row(Modifier.horizontalScroll(rememberScrollState())) {
                TextButton(onClick = { mobile.openNote(note.name) }, modifier = Modifier.testTag("discovery_open")) {
                    Text(strings.text("打开笔记", "Open note"))
                }
                if (note.state == "NORMAL" && onWalk != null) TextButton(onClick = { onWalk(note) },
                    enabled = mobile.session.state.value.connected, modifier = Modifier.testTag("review_walk")) {
                    Text(strings.text("从这条漫步", "Walk from this note"))
                }
            }
        }
    }
}

fun discoveryError(code: String, strings: Strings): String = when (code) {
    "EMBEDDING_DISABLED" -> strings.text("漫步需要启用向量索引。", "Enable the vector index to start a walk.")
    "INDEX_NOT_READY" -> strings.text("索引尚未准备好，稍后重试。", "The index is not ready. Try again later.")
    "WALK_ARCHIVED" -> strings.text("归档笔记不能作为漫步起点。", "Archived notes cannot start a walk.")
    "SOURCE_NOT_FOUND" -> strings.text("这条笔记已不存在或无法访问。", "This note is missing or unavailable.")
    "SOURCE_CHANGED" -> strings.text("笔记发生了变化，请重新读取。", "This note changed. Read it again.")
    "WALK_INTERRUPTED" -> strings.text("漫步读取已中断，可以重试。", "The walk was interrupted. You can retry.")
    "WALK_RESOURCE_LIMIT" -> strings.text("漫步路径超出服务的读取预算，可以开始新的漫步。", "This path exceeds the service budget. Start a new walk.")
    "WALK_RESPONSE_INVALID", "REVIEW_RESPONSE_INVALID" -> strings.text("服务返回的数据不完整。", "The service returned incomplete data.")
    else -> connectionError(code, strings)
}

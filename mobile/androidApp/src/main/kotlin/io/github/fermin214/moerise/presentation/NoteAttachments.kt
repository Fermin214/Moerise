package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.rememberTransformableState
import androidx.compose.foundation.gestures.transformable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.IconButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.domain.Note
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonPrimitive

internal fun JsonObject.isImageAttachment() = this["type"]?.jsonPrimitive?.content?.startsWith("image/") == true

@Composable
fun NoteAttachments(note: Note, core: CoreClient?, strings: Strings) {
    if (core == null || note.attachments.isEmpty()) return
    val (images, other) = note.attachments.partition(JsonObject::isImageAttachment)
    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        if (images.size == 1) ProtectedAttachmentImage(note, images.single(), core, strings)
        if (images.size > 1) {
            val columns = if (images.size == 2) 2 else 3
            Column(Modifier.widthIn(max = 360.dp).fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                images.chunked(columns).forEach { row ->
                    Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        for (item in row) key(item["name"], item["content_url"]) {
                            ProtectedAttachmentImage(note, item, core, strings, Modifier.weight(1f), square = true)
                        }
                        repeat(columns - row.size) { Spacer(Modifier.weight(1f)) }
                    }
                }
            }
        }
        for (item in other) key(item["name"]) { OpenAttachmentButton(note, item, core, strings) }
    }
}

@Composable
internal fun ProtectedAttachmentImage(note: Note, item: JsonObject, core: CoreClient, strings: Strings,
    modifier: Modifier = Modifier, square: Boolean = false, onRatio: ((Float) -> Unit)? = null) {
    val context = LocalContext.current.applicationContext
    val epoch by ProtectedImageCache.epoch.collectAsState()
    key(epoch, core, item["name"], item["content_url"]) {
        var retry by remember { mutableIntStateOf(0) }
        var preview by remember { mutableStateOf(false) }
        var visible by remember { mutableStateOf(false) }
        var result by remember {
            mutableStateOf<ProtectedImageResult?>(ProtectedImageCache.peek(core, item, epoch)?.let { ProtectedImageResult.Loaded(it) })
        }
        LaunchedEffect(visible, retry) {
            if (visible && result !is ProtectedImageResult.Loaded) {
                result = ProtectedImageCache.load(context, core, note, item, epoch)
            }
        }
        val shape = RoundedCornerShape(10.dp)
        val width = modifier.widthIn(max = 320.dp)
        val filename = item["filename"]?.jsonPrimitive?.content ?: strings.text("图片", "Image")
        val externalLabel = strings.text("外部打开", "Open externally")
        when (val image = result) {
            is ProtectedImageResult.Failed -> Surface(width, shape = shape, color = MoeriseColors.Paper,
                border = BorderStroke(1.dp, MoeriseColors.Line)) {
                Column(Modifier.padding(6.dp)) {
                    Text(filename, Modifier.padding(horizontal = 6.dp), color = MoeriseColors.Muted,
                        maxLines = 1, overflow = TextOverflow.Ellipsis, fontSize = 13.sp)
                    OpenAttachmentButton(note, item, core, strings, externalLabel)
                    if (image.retryable) TextButton(onClick = { result = null; retry++ }) { Text(strings.text("重试", "Retry")) }
                }
            }
            is ProtectedImageResult.Loaded -> BoxWithConstraints(width.fillMaxWidth()) {
                val bitmap = image.bitmap
                val ratio = bitmap.width.toFloat() / bitmap.height
                if (onRatio != null) SideEffect { onRatio(ratio) }
                val imageWidth = minOf(maxWidth, (220 * ratio).dp)
                val size = if (square) Modifier.fillMaxWidth().aspectRatio(1f)
                    else Modifier.width(imageWidth).height((imageWidth / ratio).coerceIn(96.dp, 220.dp))
                Image(bitmap, filename, size.clip(shape).background(MoeriseColors.Paper)
                    .clickable { preview = true }.testTag("decoded_attachment_image"),
                    contentScale = if (square) ContentScale.Crop else ContentScale.Fit)
                if (preview) ImagePreviewDialog(bitmap, filename, note, item, core, strings) { preview = false }
            }
            null -> Box(width.fillMaxWidth().then(if (square) Modifier.aspectRatio(1f) else Modifier.height(112.dp))
                .clip(shape).background(MoeriseColors.Line).onGloballyPositioned {
                    val bounds = it.boundsInWindow()
                    if (bounds.width > 0 && bounds.height > 0) visible = true
                })
        }
    }
}

@Composable
private fun ImagePreviewDialog(bitmap: ImageBitmap, filename: String, note: Note, item: JsonObject, core: CoreClient,
    strings: Strings, close: () -> Unit) {
    Dialog(close, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        var zoom by remember { mutableFloatStateOf(1f) }
        var pan by remember { mutableStateOf(Offset.Zero) }
        val transforms = rememberTransformableState { scale, move, _ ->
            zoom = (zoom * scale).coerceIn(1f, 4f)
            pan = if (zoom == 1f) Offset.Zero else pan + move
        }
        BoxWithConstraints(Modifier.fillMaxSize().background(Color.Black.copy(alpha = .92f))
            .pointerInput(close) { detectTapGestures(onTap = { close() }) }.safeDrawingPadding()) {
            val ratio = bitmap.width.toFloat() / bitmap.height
            val maxImageHeight = (maxHeight - 56.dp).coerceAtLeast(1.dp)
            val imageWidth = minOf(maxWidth, maxImageHeight * ratio)
            Image(bitmap, filename, Modifier.align(Alignment.Center)
                .size(imageWidth, imageWidth / ratio)
                .graphicsLayer {
                    scaleX = zoom
                    scaleY = zoom
                    val horizontal = size.width * (zoom - 1f) / 2
                    val vertical = size.height * (zoom - 1f) / 2
                    translationX = pan.x.coerceIn(-horizontal, horizontal)
                    translationY = pan.y.coerceIn(-vertical, vertical)
                }
                .transformable(transforms)
                .pointerInput(Unit) {
                    detectTapGestures(onTap = {}, onDoubleTap = {
                        zoom = if (zoom == 1f) 2.5f else 1f
                        pan = Offset.Zero
                    })
                }.testTag("attachment_preview"), contentScale = ContentScale.Fit)
            Surface(Modifier.align(Alignment.TopCenter).fillMaxWidth(), color = Color(0xE6222222), contentColor = Color.White) {
                Row(Modifier.padding(horizontal = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                    IconButton(onClick = close) { WebIcon(WebIcon.Close, strings.text("关闭图片", "Close image"), tint = Color.White) }
                    Text(filename, Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis)
                    OpenAttachmentButton(note, item, core, strings, strings.text("外部打开", "Open externally"), textColor = Color.White)
                }
            }
        }
    }
}

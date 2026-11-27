package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.rememberTransformableState
import androidx.compose.foundation.gestures.transformable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
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
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.launch
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
    key(epoch, core, note.name, item["name"], item["content_url"]) {
        var retry by remember { mutableIntStateOf(0) }
        var preview by remember { mutableStateOf(false) }
        var visible by remember { mutableStateOf(false) }
        var result by remember {
            mutableStateOf<ProtectedImageResult?>(ProtectedImageCache.peek(core, note, item, epoch)?.let { ProtectedImageResult.Loaded(it) })
        }
        LaunchedEffect(visible, retry) {
            if (visible && result !is ProtectedImageResult.Loaded) {
                result = ProtectedImageCache.load(context, core, note, item, epoch)
            }
        }
        val shape = RoundedCornerShape(8.dp)
        val width = modifier.widthIn(max = 320.dp)
        val filename = item["filename"]?.jsonPrimitive?.content ?: strings.text("图片", "Image")
        val externalLabel = strings.text("外部打开", "Open externally")
        when (val image = result) {
            is ProtectedImageResult.Failed -> Surface(width, shape = shape, color = MoeriseColors.Paper,
                border = BorderStroke(1.dp, MoeriseColors.Line)) {
                Column(Modifier.padding(6.dp)) {
                    Text(filename, Modifier.padding(horizontal = 6.dp), color = MoeriseColors.Muted,
                        maxLines = 1, overflow = TextOverflow.Ellipsis, fontSize = 13.sp)
                    if (image.external) OpenAttachmentButton(note, item, core, strings, externalLabel)
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
                    .border(0.5.dp, MoeriseColors.Ink.copy(alpha = 0.08f), shape)
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
    var menu by remember { mutableStateOf(false) }
    Dialog({ if (menu) menu = false else close() }, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        val context = LocalContext.current.applicationContext
        val scope = rememberCoroutineScope()
        val haptic = rememberMoeriseHaptic()
        val saveLabel = strings.text("保存到相册", "Save to gallery")
        var saving by remember { mutableStateOf(false) }
        var feedback by remember { mutableStateOf<Boolean?>(null) }
        LaunchedEffect(feedback) { if (feedback != null) { delay(3_000); feedback = null } }
        fun save() {
            if (saving) return
            saving = true
            menu = false
            feedback = null
            scope.launch {
                try {
                    saveImageToGallery(context, note, item, core)
                    feedback = true
                    haptic()
                } catch (failure: Exception) {
                    coroutineContext.ensureActive()
                    feedback = false
                } finally { saving = false }
            }
        }
        var zoom by remember { mutableFloatStateOf(1f) }
        var pan by remember { mutableStateOf(Offset.Zero) }
        val imageSize = remember { mutableStateOf(Offset.Zero) }
        fun boundPan(value: Offset): Offset {
            val horizontal = imageSize.value.x * (zoom - 1f) / 2
            val vertical = imageSize.value.y * (zoom - 1f) / 2
            return Offset(value.x.coerceIn(-horizontal, horizontal), value.y.coerceIn(-vertical, vertical))
        }
        val transforms = rememberTransformableState { scale, move, _ ->
            zoom = (zoom * scale).coerceIn(1f, 4f)
            pan = boundPan(pan + move)
        }
        val dismiss by rememberUpdatedState { if (menu) menu = false else close() }
        BoxWithConstraints(Modifier.fillMaxSize().background(Color.Black.copy(alpha = .94f))
            .pointerInput(Unit) { detectTapGestures(onTap = { dismiss() }) }.safeDrawingPadding()) {
            val ratio = bitmap.width.toFloat() / bitmap.height
            val imageWidth = minOf(maxWidth, maxHeight * ratio)
            Image(bitmap, filename, Modifier.align(Alignment.Center)
                .size(imageWidth, imageWidth / ratio)
                .onGloballyPositioned { imageSize.value = Offset(it.size.width.toFloat(), it.size.height.toFloat()) }
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
                    detectTapGestures(onTap = { dismiss() }, onLongPress = { if (!saving) { haptic(); menu = true } }, onDoubleTap = {
                        zoom = if (zoom == 1f) 2.5f else 1f
                        pan = Offset.Zero
                    })
                }.testTag("attachment_preview"), contentScale = ContentScale.Fit)
            IconButton(onClick = close, modifier = Modifier.align(Alignment.TopEnd).padding(8.dp)
                .background(Color.Black.copy(alpha = .45f), CircleShape)) {
                WebIcon(WebIcon.Close, strings.text("关闭图片", "Close image"), tint = Color.White)
            }
            IconButton(onClick = ::save, enabled = !saving, modifier = Modifier.align(Alignment.BottomEnd).padding(8.dp)
                .background(Color.Black.copy(alpha = .45f), CircleShape).testTag("attachment_save")) {
                WebIcon(WebIcon.Download, saveLabel, tint = Color.White.copy(alpha = if (saving) .55f else 1f))
            }
            if (saving) LinearProgressIndicator(Modifier.align(Alignment.BottomCenter).fillMaxWidth())
            feedback?.let { success ->
                Surface(Modifier.align(Alignment.BottomCenter).padding(bottom = 58.dp, start = 16.dp, end = 16.dp),
                    color = MoeriseColors.Surface, shape = RoundedCornerShape(12.dp)) {
                    Text(if (success) strings.text("已保存到相册", "Saved to gallery")
                        else strings.text("保存失败，请重试。", "Could not save. Try again."),
                        Modifier.padding(horizontal = 14.dp, vertical = 10.dp).testTag("attachment_save_feedback"))
                }
            }
            if (menu) Surface(Modifier.align(Alignment.BottomCenter).fillMaxWidth().padding(8.dp),
                color = MoeriseColors.Surface, shape = RoundedCornerShape(12.dp)) {
                Column(Modifier.fillMaxWidth().padding(4.dp)) {
                    TextButton(onClick = ::save, enabled = !saving, modifier = Modifier.fillMaxWidth()) { Text(saveLabel) }
                    TextButton(onClick = { menu = false }, modifier = Modifier.fillMaxWidth()) { Text(strings.text("取消", "Cancel")) }
                }
            }
        }
    }
}

package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.painter.BitmapPainter
import androidx.compose.ui.graphics.painter.ColorPainter
import androidx.compose.ui.graphics.painter.Painter
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.unit.dp
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.withLink
import com.mikepenz.markdown.compose.components.markdownComponents
import com.mikepenz.markdown.compose.elements.MarkdownImage
import com.mikepenz.markdown.m3.Markdown
import com.mikepenz.markdown.model.*
import com.mikepenz.markdown.utils.getUnescapedTextInNode
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.Url
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.serialization.json.*
import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.MarkdownTokenTypes
import org.intellij.markdown.IElementType
import org.intellij.markdown.ast.ASTNode

private fun ASTNode.findChildOfTypeRecursive(type: IElementType): ASTNode? =
    children.firstOrNull { it.type == type } ?: children.firstNotNullOfOrNull { it.findChildOfTypeRecursive(type) }

expect suspend fun decodeProtectedImage(bytes: ByteArray): ImageBitmap?
private val protectedImages = Semaphore(2)

fun externalLink(link: String): Boolean = runCatching {
    link.none { it.code <= 32 || it == '\\' } && when {
        link.startsWith("mailto:", ignoreCase = true) -> link.length > 7
        Regex("^https?://", RegexOption.IGNORE_CASE).containsMatchIn(link) -> Url(link).host.isNotBlank()
        else -> false
    }
}.getOrDefault(false)

fun managedAttachment(note: Note?, link: String): JsonObject? {
    if (note == null) return null
    val native = Regex("^/file/(attachments/[A-Za-z0-9_-]+)(?:/[^/?#]+)?$").matchEntire(link)?.groupValues?.get(1)
    return note.attachments.firstOrNull { item ->
        val name = item["name"]?.jsonPrimitive?.content
        name != null && (name == native || item["content_url"]?.jsonPrimitive?.content == link)
    }
}

@Composable
fun MarkdownBody(content: String, modifier: Modifier = Modifier, note: Note? = null, core: CoreClient? = null) {
    val platformUri = LocalUriHandler.current
    val safeUri = remember(platformUri) { object : UriHandler {
        override fun openUri(uri: String) { if (externalLink(uri)) runCatching { platformUri.openUri(uri) } }
    } }
    val transformer = remember(note, core) { object : ImageTransformer {
        @Composable override fun intrinsicSize(painter: Painter): Size = Size(1600f, 900f)
        @Composable override fun transform(link: String): ImageData? {
            val item = managedAttachment(note, link) ?: return null
            val type = item["type"]?.jsonPrimitive?.content ?: return null
            if (type !in setOf("image/png", "image/jpeg", "image/webp", "image/gif")) return null
            var visible by remember(link, note?.sourceHash) { mutableStateOf(false) }
            val bitmap by produceState<ImageBitmap?>(null, core, note?.sourceHash, item, visible) {
                value = null
                if (visible && note != null && core != null) {
                    value = try {
                        protectedImages.withPermit { decodeProtectedImage(core.attachmentBytes(note, item, 8 * 1024 * 1024)) }
                    } catch (error: Exception) {
                        if (error is CancellationException) throw error
                        null
                    }
                }
            }
            return ImageData(
                painter = bitmap?.let(::BitmapPainter) ?: ColorPainter(MoeriseColors.Line),
                contentDescription = item["filename"]?.jsonPrimitive?.content,
                modifier = Modifier.fillMaxWidth().height(220.dp).onGloballyPositioned { coordinates ->
                    val bounds = coordinates.boundsInWindow()
                    visible = bounds.width > 0 && bounds.height > 0
                },
                contentScale = ContentScale.Fit,
            )
        }
    } }
    val annotator = remember(note) { markdownAnnotator { text, child ->
        when {
            child.type == MarkdownTokenTypes.HTML_TAG -> { append(child.getUnescapedTextInNode(text)); true }
            child.type == MarkdownElementTypes.IMAGE -> {
                val link = child.findChildOfTypeRecursive(MarkdownElementTypes.LINK_DESTINATION)?.getUnescapedTextInNode(text)
                if (link != null && managedAttachment(note, link) == null) {
                    if (externalLink(link)) withLink(LinkAnnotation.Url(link)) { append(link) }
                    else append(child.getUnescapedTextInNode(text))
                    true
                } else false
            }
            else -> false
        }
    } }
    CompositionLocalProvider(LocalUriHandler provides safeUri) {
        Markdown(
            content = content, modifier = modifier, imageTransformer = transformer, annotator = annotator,
            components = markdownComponents(
                image = { model ->
                    val link = model.node.findChildOfTypeRecursive(MarkdownElementTypes.LINK_DESTINATION)?.getUnescapedTextInNode(model.content)
                    if (link != null && managedAttachment(note, link) != null) MarkdownImage(model.content, model.node)
                    else if (link != null && externalLink(link)) TextButton(onClick = { safeUri.openUri(link) }) { Text(link) }
                    else Text(model.node.getUnescapedTextInNode(model.content))
                },
                custom = { type, model ->
                    if ("HTML" in type.name) Text(model.node.getUnescapedTextInNode(model.content), Modifier.fillMaxWidth())
                },
            ),
        )
    }
}

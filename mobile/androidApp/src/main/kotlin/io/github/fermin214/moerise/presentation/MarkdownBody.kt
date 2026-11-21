package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.painter.BitmapPainter
import androidx.compose.ui.graphics.painter.ColorPainter
import androidx.compose.ui.graphics.painter.Painter
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withLink
import com.mikepenz.markdown.compose.components.markdownComponents
import com.mikepenz.markdown.compose.elements.MarkdownImage
import com.mikepenz.markdown.m3.Markdown
import com.mikepenz.markdown.m3.markdownTypography
import com.mikepenz.markdown.model.*
import com.mikepenz.markdown.model.State as MdState
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
import org.intellij.markdown.flavours.gfm.GFMFlavourDescriptor
import org.intellij.markdown.parser.MarkdownParser

private fun ASTNode.findChildOfTypeRecursive(type: IElementType): ASTNode? =
    children.firstOrNull { it.type == type } ?: children.firstNotNullOfOrNull { it.findChildOfTypeRecursive(type) }

private val protectedImages = Semaphore(2)

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun NoteTags(tags: List<String>, modifier: Modifier = Modifier, onTag: ((String) -> Unit)? = null) {
    if (tags.isEmpty()) return
    FlowRow(modifier, horizontalArrangement = Arrangement.spacedBy(6.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        tags.distinct().forEach { tag ->
            if (onTag != null) WebTag(tag, { onTag(tag) })
            else Surface(color = MoeriseColors.TagSurface, shape = RoundedCornerShape(50),
                border = BorderStroke(1.dp, MoeriseColors.TagLine)) {
                Text("#${tag.removePrefix("#")}", fontSize = 13.sp, lineHeight = 22.sp,
                    color = Color(0xFF2A5BB5), modifier = Modifier.padding(horizontal = 9.dp))
            }
        }
    }
}

@Composable
fun NoteAttachments(note: Note, core: CoreClient?, strings: Strings) {
    if (core == null) return
    note.attachments.forEach { item ->
        if (item["type"]?.jsonPrimitive?.content in setOf("image/png", "image/jpeg", "image/webp", "image/gif")) {
            item["content_url"]?.jsonPrimitive?.content?.let { MarkdownBody("![]($it)", note = note, core = core) }
        }
        OpenAttachmentButton(note, item, core, strings)
    }
}

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

private val referenceDefRegex = Regex("""^\s{0,3}\[[^\]\n]+\]:""", RegexOption.MULTILINE)

internal fun extractReferenceDefinitions(content: String, parser: MarkdownParser = MarkdownParser(GFMFlavourDescriptor())): Map<String, String?> {
    if (content.isBlank()) return emptyMap()
    val result = linkedMapOf<String, String?>()
    fun walk(node: ASTNode) {
        if (node.type == MarkdownElementTypes.LINK_DEFINITION) {
            val label = node.children.firstOrNull { it.type == MarkdownElementTypes.LINK_LABEL }?.getUnescapedTextInNode(content)
            val destination = node.children.firstOrNull { it.type == MarkdownElementTypes.LINK_DESTINATION }?.getUnescapedTextInNode(content)
            if (label != null) result[label] = destination
        } else {
            node.children.forEach(::walk)
        }
    }
    walk(parser.buildMarkdownTreeFromString(content))
    return result
}

@Composable
fun MarkdownBody(content: String, modifier: Modifier = Modifier, note: Note? = null, core: CoreClient? = null) {
    val blocks = remember(content) { splitMarkdownBlocks(content) }
    if (blocks.isEmpty()) return
    val flavour = remember { GFMFlavourDescriptor() }
    val parser = remember(flavour) { MarkdownParser(flavour) }
    val defBlocks = remember(blocks) { blocks.filter { referenceDefRegex.containsMatchIn(it) }.joinToString("\n\n") }
    val refHandler = remember(defBlocks, parser) {
        ReferenceLinkHandlerImpl().apply {
            if (defBlocks.isNotEmpty()) {
                extractReferenceDefinitions(defBlocks, parser).forEach { (label, destination) -> store(label, destination) }
            }
        }
    }
    val platformUri = LocalUriHandler.current
    val safeUri = remember(platformUri) { object : UriHandler {
        override fun openUri(uri: String) { if (externalLink(uri)) runCatching { platformUri.openUri(uri) } }
    } }
    val latestNote by rememberUpdatedState(note)
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
    val body = MaterialTheme.typography.bodyLarge
    val heading = body.copy(fontSize = 18.4.sp, lineHeight = 27.6.sp, fontWeight = FontWeight.Bold)
    val typography = markdownTypography(
        h1 = heading.copy(fontSize = 21.6.sp, lineHeight = 32.4.sp),
        h2 = heading, h3 = heading, h4 = heading,
        h5 = body.copy(fontWeight = FontWeight.Bold),
        h6 = body.copy(fontWeight = FontWeight.Bold),
        text = body, paragraph = body, quote = body.copy(color = MoeriseColors.Muted),
        list = body, ordered = body, bullet = body,
        code = body.copy(fontFamily = FontFamily.Monospace, fontSize = 13.sp, lineHeight = 20.8.sp),
        inlineCode = body.copy(fontFamily = FontFamily.Monospace, fontSize = 13.sp),
        table = body,
    )
    val animations = markdownAnimations(animateTextSize = { this })
    val components = remember(safeUri) {
        markdownComponents(
            image = { model ->
                val link = model.node.findChildOfTypeRecursive(MarkdownElementTypes.LINK_DESTINATION)?.getUnescapedTextInNode(model.content)
                if (link != null && managedAttachment(latestNote, link) != null) MarkdownImage(model.content, model.node)
                else if (link != null && externalLink(link)) TextButton(onClick = { safeUri.openUri(link) }) { Text(link) }
                else Text(model.node.getUnescapedTextInNode(model.content))
            },
            custom = { type, model ->
                if ("HTML" in type.name) Text(model.node.getUnescapedTextInNode(model.content), Modifier.fillMaxWidth())
            },
        )
    }
    CompositionLocalProvider(LocalUriHandler provides safeUri) {
        Column(modifier, verticalArrangement = Arrangement.spacedBy(8.dp)) {
            for (index in 0 until blocks.lastIndex) {
                val block = blocks[index]
                key(defBlocks, index) {
                    val markdownState = rememberMarkdownState(
                        content = block,
                        flavour = flavour,
                        parser = parser,
                        referenceLinkHandler = refHandler,
                        immediate = true,
                    )
                    Markdown(
                        markdownState = markdownState,
                        modifier = Modifier.fillMaxWidth(),
                        imageTransformer = transformer,
                        annotator = annotator,
                        typography = typography,
                        components = components,
                        animations = animations,
                    )
                }
            }
            val tail = blocks.last()
            key(defBlocks, blocks.lastIndex) {
                val tailState = rememberMarkdownState(
                    content = tail,
                    flavour = flavour,
                    parser = parser,
                    referenceLinkHandler = refHandler,
                )
                val parsed by tailState.state.collectAsState()
                var retained by remember { mutableStateOf<MdState.Success?>(null) }
                if (retained != null && !tail.startsWith(retained!!.content.take(minOf(8, retained!!.content.length)))) {
                    retained = null
                }
                if (parsed is MdState.Success) {
                    retained = parsed as MdState.Success
                }
                val rendered = parsed as? MdState.Success ?: retained ?: parsed
                Markdown(
                    state = rendered,
                    modifier = Modifier.fillMaxWidth(),
                    imageTransformer = transformer,
                    annotator = annotator,
                    typography = typography,
                    components = components,
                    animations = animations,
                )
            }
        }
    }
}

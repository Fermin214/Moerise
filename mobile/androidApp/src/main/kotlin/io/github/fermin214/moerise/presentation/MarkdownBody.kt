package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.InlineTextContent
import androidx.compose.foundation.text.appendInlineContent
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.Placeholder
import androidx.compose.ui.text.PlaceholderVerticalAlign
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withLink
import com.mikepenz.markdown.compose.components.markdownComponents
import com.mikepenz.markdown.m3.Markdown
import com.mikepenz.markdown.m3.markdownColor
import com.mikepenz.markdown.model.markdownPadding
import com.mikepenz.markdown.m3.markdownTypography
import com.mikepenz.markdown.compose.elements.MarkdownParagraph
import com.mikepenz.markdown.model.*
import com.mikepenz.markdown.model.State as MdState
import com.mikepenz.markdown.utils.getUnescapedTextInNode
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.domain.Note
import io.ktor.http.Url
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.*
import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.MarkdownTokenTypes
import org.intellij.markdown.IElementType
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.flavours.gfm.GFMFlavourDescriptor
import org.intellij.markdown.html.entities.EntityConverter
import org.intellij.markdown.parser.MarkdownParser

private fun ASTNode.findChildOfTypeRecursive(type: IElementType): ASTNode? =
    children.firstOrNull { it.type == type } ?: children.firstNotNullOfOrNull { it.findChildOfTypeRecursive(type) }

internal val LocalParseMarkdownTail = staticCompositionLocalOf<suspend (String, MarkdownParser, ReferenceLinkHandler) -> MdState.Success?> {
    { content, parser, references ->
        parseMarkdownFlow(content, parser = parser, referenceLinkHandler = references)
            .first { it !is MdState.Loading } as? MdState.Success
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun NoteTags(tags: List<String>, modifier: Modifier = Modifier, plain: Boolean = false, onTag: ((String) -> Unit)? = null) {
    if (tags.isEmpty()) return
    FlowRow(modifier, horizontalArrangement = Arrangement.spacedBy(if (plain) 10.dp else 6.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        tags.distinct().forEach { tag ->
            if (plain) {
                val interactions = remember { MutableInteractionSource() }
                val active = actionHighlighted(interactions)
                Text("#${tag.removePrefix("#")}", style = MaterialTheme.typography.bodySmall,
                    color = if (active) MoeriseColors.Accent else MoeriseColors.InkTag,
                    modifier = if (onTag == null) Modifier else Modifier.clickable(interactions, indication = null, role = Role.Button) { onTag(tag) })
            } else if (onTag != null) WebTag(tag, { onTag(tag) })
            else Surface(color = MoeriseColors.AccentSurface, shape = RoundedCornerShape(6.dp)) {
                Text("#${tag.removePrefix("#")}", style = MaterialTheme.typography.bodySmall,
                    color = MoeriseColors.Accent, modifier = Modifier.padding(horizontal = 8.dp, vertical = 1.dp))
            }
        }
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

private fun inlineAttachmentId(item: JsonObject) = "moerise-attachment:${item["name"]?.jsonPrimitive?.content}"

@Composable
internal fun rememberNotePresentation(content: String, tags: List<String>?): NotePresentation {
    val format = { if (tags == null) localNotePresentation(content) else notePresentation(content, tags) }
    if (content.length <= 4096 || '#' !in content || tags?.isEmpty() == true) return remember(content, tags) { format() }
    return key(content, tags) {
        val presentation by produceState(NotePresentation(content, tags.orEmpty())) {
            value = withContext(Dispatchers.Default) { format() }
        }
        presentation
    }
}

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
fun MarkdownBody(content: String, modifier: Modifier = Modifier, note: Note? = null, core: CoreClient? = null,
    tags: List<String> = emptyList(), onTag: ((String) -> Unit)? = null, originalContent: String = content,
    strings: Strings = Strings(UiLanguage.SYSTEM)) {
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
    val latestCore by rememberUpdatedState(core)
    val latestStrings by rememberUpdatedState(strings)
    val attachments = note?.attachments.orEmpty()
    val epoch = if (attachments.isNotEmpty()) ProtectedImageCache.epoch.collectAsState().value else 0L
    var markdownWidth by remember { mutableIntStateOf(0) }
    val inlineRatios = remember(core, note?.name, attachments, epoch) { mutableStateMapOf<String, Float>() }
    val density = LocalDensity.current
    val inlineMaxWidth = with(density) {
        if (markdownWidth == 0) 120.dp else (markdownWidth.toDp() * .8f).coerceIn(1.dp, 220.dp)
    }
    val inlineContent = markdownInlineContent(attachments.associate { item ->
        val id = inlineAttachmentId(item)
        val ratio = inlineRatios[id]
        val width = if (ratio == null) inlineMaxWidth else minOf(inlineMaxWidth, (220 * ratio).dp)
        val height = if (ratio == null) 148.dp else (width / ratio).coerceIn(96.dp, 220.dp)
        id to InlineTextContent(with(density) { Placeholder(width.toSp(), height.toSp(), PlaceholderVerticalAlign.Bottom) }) { link ->
            val parent = latestNote
            val client = latestCore
            val attachment = managedAttachment(parent, link)
            if (attachment != null && parent != null && client != null) {
                if (attachment.isImageAttachment()) ProtectedAttachmentImage(parent, attachment, client, latestStrings,
                    Modifier.widthIn(max = inlineMaxWidth), onRatio = { inlineRatios[id] = it })
                else OpenAttachmentButton(parent, attachment, client, latestStrings)
            }
        }
    })
    val latestTagAction by rememberUpdatedState(onTag)
    fun mapSpans(): Map<String, List<NoteTagSpan>> {
        if (tags.isEmpty()) return emptyMap()
        val normalized = content.replace("\r\n", "\n")
        val spans = noteTagSpans(normalized, tags)
        val byBlock = mutableMapOf<String, List<NoteTagSpan>>()
        var offset = 0
        var index = 0
        for (block in blocks) {
            val start = normalized.indexOf(block, offset)
            if (start < 0) continue
            val end = start + block.length
            while (index < spans.size && spans[index].start < start) index++
            val entries = mutableListOf<NoteTagSpan>()
            while (index < spans.size && spans[index].end <= end) {
                val span = spans[index++]
                entries += span.copy(start = span.start - start, end = span.end - start)
            }
            byBlock[block] = entries
            offset = end
        }
        return byBlock
    }
    val immediateSpans = remember(content, tags) { if (content.length <= 4096) mapSpans() else emptyMap() }
    val asynchronousSpans = if (content.length > 4096 && tags.isNotEmpty()) key(content, tags) {
        produceState<Map<String, List<NoteTagSpan>>>(emptyMap()) {
            value = withContext(Dispatchers.Default) { mapSpans() }
        }
    } else null
    val renderedSpans = asynchronousSpans?.value ?: immediateSpans
    val annotator = remember(note, renderedSpans, onTag != null) {
        val tagStyle = SpanStyle(color = MoeriseColors.Accent)
        markdownAnnotator(markdownAnnotatorConfig(eolAsNewLine = true)) { text, child ->
            when {
                child.type == MarkdownTokenTypes.TEXT && renderedSpans[text]?.isNotEmpty() == true -> {
                    val spans = renderedSpans.getValue(text)
                    var index = spans.binarySearchBy(child.startOffset) { it.start }
                    if (index < 0) index = -index - 1
                    var offset = child.startOffset
                    while (index < spans.size && spans[index].end <= child.endOffset) {
                        val span = spans[index++]
                        append(EntityConverter.replaceEntities(text.substring(offset, span.start),
                            processEntities = false, processEscapes = true))
                        val tag = text.substring(span.start, span.end)
                        if (onTag == null) withStyle(tagStyle) { append(tag) }
                        else withLink(LinkAnnotation.Clickable(span.tag, TextLinkStyles(style = tagStyle)) {
                            latestTagAction?.invoke(span.tag)
                        }) { append(tag) }
                        offset = span.end
                    }
                    if (offset != child.startOffset) append(EntityConverter.replaceEntities(
                        text.substring(offset, child.endOffset), processEntities = false, processEscapes = true))
                    offset != child.startOffset
                }
                child.type == MarkdownTokenTypes.HTML_TAG -> { append(child.getUnescapedTextInNode(text)); true }
                child.type == MarkdownElementTypes.IMAGE -> {
                    val link = child.findChildOfTypeRecursive(MarkdownElementTypes.LINK_DESTINATION)?.getUnescapedTextInNode(text)
                    val item = link?.let { managedAttachment(note, it) }
                    when {
                        item != null -> appendInlineContent(inlineAttachmentId(item), requireNotNull(link))
                        link != null && externalLink(link) -> withLink(LinkAnnotation.Url(link)) { append(link) }
                        else -> append(child.getUnescapedTextInNode(text))
                    }
                    true
                }
                else -> false
            }
        }
    }
    val body = MaterialTheme.typography.bodyLarge.copy(letterSpacing = 0.2.sp)
    val heading = body.copy(fontSize = 16.sp, lineHeight = 23.5.sp, fontWeight = FontWeight.SemiBold)
    val minorHeading = body.copy(fontWeight = FontWeight.SemiBold)
    val colors = markdownColor(inlineCodeBackground = Color.Transparent, codeBackground = MoeriseColors.SurfaceMuted)
    val padding = markdownPadding(block = 2.dp, list = 0.dp, listItemTop = 2.dp, listItemBottom = 2.dp)
    val typography = markdownTypography(
        h1 = heading.copy(fontSize = 18.sp, lineHeight = 26.sp),
        h2 = heading, h3 = heading, h4 = minorHeading,
        h5 = minorHeading,
        h6 = minorHeading,
        text = body, paragraph = body, quote = body.copy(color = MoeriseColors.Muted),
        list = body, ordered = body, bullet = body,
        code = body.copy(fontFamily = FontFamily.Monospace, fontSize = 13.sp, lineHeight = 19.5.sp),
        inlineCode = body.copy(fontFamily = FontFamily.Monospace, fontSize = 13.sp, color = MoeriseColors.Accent),
        table = body,
    )
    val animations = markdownAnimations(animateTextSize = { this })
    val components = remember(safeUri) {
        markdownComponents(
            image = { model ->
                val link = model.node.findChildOfTypeRecursive(MarkdownElementTypes.LINK_DESTINATION)?.getUnescapedTextInNode(model.content)
                val parent = latestNote
                val client = latestCore
                val item = link?.let { managedAttachment(parent, it) }
                if (item != null && parent != null && client != null) {
                    if (item.isImageAttachment()) Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.Center) {
                        ProtectedAttachmentImage(parent, item, client, latestStrings)
                    }
                    else OpenAttachmentButton(parent, item, client, latestStrings)
                }
                else if (link != null && externalLink(link)) TextButton(onClick = { safeUri.openUri(link) }) { Text(link) }
                else Text(model.node.getUnescapedTextInNode(model.content))
            },
            custom = { type, model ->
                if ("HTML" in type.name) Text(model.node.getUnescapedTextInNode(model.content), Modifier.fillMaxWidth())
            },
        )
    }
    val retainedBlocks = remember(originalContent, defBlocks) { mutableMapOf<String, MdState.Success>() }
    var retainedTail by remember(originalContent) { mutableStateOf<MdState.Success?>(null) }
            paragraph = { model ->
                val image = model.node.children.singleOrNull { it.type != MarkdownTokenTypes.WHITE_SPACE }
                    ?.takeIf { it.type == MarkdownElementTypes.IMAGE }
                val parent = latestNote
                val client = latestCore
                val link = image?.findChildOfTypeRecursive(MarkdownElementTypes.LINK_DESTINATION)?.getUnescapedTextInNode(model.content)
                val item = link?.let { managedAttachment(parent, it) }
                if (item?.isImageAttachment() == true && parent != null && client != null) {
                    Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.Center) {
                        ProtectedAttachmentImage(parent, item, client, latestStrings)
                    }
                } else MarkdownParagraph(model.content, model.node, style = model.typography.paragraph)
            },
    CompositionLocalProvider(LocalUriHandler provides safeUri) {
        Column(modifier.then(if (attachments.isEmpty()) Modifier else Modifier.onSizeChanged { markdownWidth = it.width }),
            verticalArrangement = Arrangement.spacedBy(6.dp)) {
            for (index in 0 until blocks.lastIndex) {
                val block = blocks[index]
                key(defBlocks, index) {
                    val rendered = retainedBlocks[block] ?: key(block) {
                        rememberMarkdownState(block, flavour = flavour, parser = parser, referenceLinkHandler = refHandler,
                            immediate = true).state.collectAsState().value as? MdState.Success
                    }
                    if (rendered != null) {
                        SideEffect { retainedBlocks[block] = rendered }
                        Markdown(
                            state = rendered,
                            modifier = Modifier.fillMaxWidth(),
                            inlineContent = inlineContent,
                            annotator = annotator,
                            typography = typography,
                            colors = colors,
                            padding = padding,
                            components = components,
                            animations = animations,
                        )
                    }
                }
            }
            val tail = blocks.last()
            key(defBlocks, blocks.lastIndex) {
                val rendered = retainedBlocks[tail] ?: if (tail.length <= 4096) {
                    key(tail) {
                        rememberMarkdownState(tail, flavour = flavour, parser = parser, referenceLinkHandler = refHandler,
                            immediate = true).state.collectAsState().value as? MdState.Success
                    }
                } else {
                    val parse = LocalParseMarkdownTail.current
                    val parsed by produceState<MdState.Success?>(null, tail, refHandler, parse) {
                        value = parse(tail, parser, refHandler)
                    }
                    parsed?.takeIf { it.content == tail }
                }
                if (rendered == null) {
                    val previous = retainedTail
                    val images = if (previous != null && core != null && note != null) {
                        remember(previous, tail, note?.attachments) { retainedImages(previous, tail, note) }
                    } else emptyList()
                    var offset = 0
                    if (note != null && core != null) {
                        for (image in images) {
                            if (image.start > offset) Text(tail.substring(offset, image.start), Modifier.fillMaxWidth(), style = body)
                            ProtectedAttachmentImage(note, image.item, core, strings,
                                Modifier.align(Alignment.CenterHorizontally).widthIn(max = inlineMaxWidth))
                            offset = image.end
                        }
                    }
                    if (offset < tail.length) Text(tail.substring(offset), Modifier.fillMaxWidth(), style = body)
                }
                else {
                    SideEffect {
                        retainedBlocks[tail] = rendered
                        if (retainedTail != rendered) retainedTail = rendered
                    }
                    Markdown(
                        state = rendered,
                        modifier = Modifier.fillMaxWidth(),
                        inlineContent = inlineContent,
                        annotator = annotator,
                        typography = typography,
                        colors = colors,
                        padding = padding,
                        components = components,
                        animations = animations,
                    )
                }
            }
        }
    }
}

private data class RetainedImage(val start: Int, val end: Int, val item: JsonObject)

private fun retainedImages(previous: MdState.Success, content: String, note: Note?): List<RetainedImage> = buildList {
    var offset = 0
    fun findImages(node: ASTNode) {
        if (node.type != MarkdownElementTypes.IMAGE) {
            node.children.forEach(::findImages)
            return
        }
        val link = node.findChildOfTypeRecursive(MarkdownElementTypes.LINK_DESTINATION)
            ?.getUnescapedTextInNode(previous.content) ?: return
        val item = managedAttachment(note, link)?.takeIf(JsonObject::isImageAttachment) ?: return
        val markup = previous.content.substring(node.startOffset, node.endOffset)
        val start = content.indexOf(markup, offset)
        if (start < 0) return
        offset = start + markup.length
        add(RetainedImage(start, offset, item))
    }
    findImages(previous.node)
}

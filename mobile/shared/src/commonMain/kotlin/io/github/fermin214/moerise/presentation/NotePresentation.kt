package io.github.fermin214.moerise.presentation

import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.MarkdownTokenTypes
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.flavours.gfm.GFMFlavourDescriptor
import org.intellij.markdown.parser.MarkdownParser

data class NotePresentation(val body: String, val tags: List<String>)

/** 只生成阅读视图；标签以原生元数据为准，正文与标签的存储值不变。 */
fun notePresentation(content: String, tags: List<String>): NotePresentation {
    val uniqueTags = tags.distinct()
    val candidates = uniqueTags.filter { tag ->
        tag.isNotEmpty() && !tag.startsWith("#") && tag.none(Char::isWhitespace)
    }.sortedByDescending(String::length)
    if (candidates.isEmpty() || '#' !in content) return NotePresentation(content, uniqueTags)

    val protected = protectedTagOffsets(content)

    val removed = BooleanArray(content.length)
    var offset = 0
    while (offset < content.length) {
        if (content[offset] != '#' || protected[offset] || escapedHash(content, offset) ||
            (offset > 0 && !tagBoundary(content[offset - 1], before = true))) {
            offset++
            continue
        }
        val tag = candidates.firstOrNull { candidate ->
            val end = offset + candidate.length + 1
            content.startsWith(candidate, offset + 1) &&
                (end == content.length || tagBoundary(content[end], before = false)) &&
                (offset until end).none { protected[it] }
        }
        if (tag == null) {
            offset++
        } else {
            val end = offset + tag.length + 1
            removed.fill(true, offset, end)
            offset = end
        }
    }
    if (removed.none { it }) return NotePresentation(content, uniqueTags)
    return NotePresentation(withoutTagLines(content, removed), uniqueTags)
}

/** 本机笔记还没有标签元数据时，从普通正文提取标签，跳过 Markdown 的代码和链接。 */
fun localNotePresentation(content: String): NotePresentation {
    if ('#' !in content) return NotePresentation(content, emptyList())
    val protected = protectedTagOffsets(content)
    val tags = linkedSetOf<String>()
    var offset = 0
    while (offset < content.length) {
        if (content[offset] != '#' || protected[offset] || escapedHash(content, offset) ||
            offset > 0 && !tagBoundary(content[offset - 1], before = true)) {
            offset++
            continue
        }
        val start = offset + 1
        var end = start
        while (end < content.length && content[end] != '#' &&
            !tagBoundary(content[end], before = false) && !protected[end]) end++
        if (end > start && (end == content.length || tagBoundary(content[end], before = false))) {
            tags += content.substring(start, end)
        }
        offset = maxOf(end, start)
    }
    return notePresentation(content, tags.toList())
}

fun initialEditorSelection(content: String): Int {
    if ('#' !in content) return content.length
    val protected = protectedTagOffsets(content)
    val spans = mutableListOf<IntRange>()
    var offset = 0
    while (offset < content.length) {
        if (content[offset] != '#' || protected[offset] || escapedHash(content, offset) ||
            offset > 0 && !tagBoundary(content[offset - 1], before = true)) {
            offset++
            continue
        }
        val start = offset + 1
        var end = start
        while (end < content.length && content[end] != '#' &&
            !tagBoundary(content[end], before = false) && !protected[end]) end++
        if (end > start && (end == content.length || tagBoundary(content[end], before = false))) {
            spans += offset until end
        }
        offset = maxOf(end, start)
    }
    if (spans.isEmpty()) return content.length
    var cursor = content.length
    while (cursor > 0 && content[cursor - 1].isWhitespace()) cursor--
    var trailing = false
    for (span in spans.asReversed()) {
        if (span.last + 1 != cursor) break
        trailing = true
        cursor = span.first
        while (cursor > 0 && content[cursor - 1].isWhitespace()) cursor--
    }
    return if (trailing && cursor > 0) cursor else content.length
}

private fun protectedTagOffsets(content: String): BooleanArray {
    val protected = BooleanArray(content.length)
    fun protect(node: ASTNode) {
        if (node.type in protectedTagNodes) protected.fill(true, node.startOffset, node.endOffset)
        else node.children.forEach(::protect)
    }
    protect(MarkdownParser(GFMFlavourDescriptor()).buildMarkdownTreeFromString(content))
    bareTagURLs.findAll(content).forEach { protected.fill(true, it.range.first, it.range.last + 1) }
    return protected
}

private val protectedTagNodes = setOf(
    MarkdownElementTypes.CODE_FENCE,
    MarkdownElementTypes.CODE_BLOCK,
    MarkdownElementTypes.CODE_SPAN,
    MarkdownElementTypes.INLINE_LINK,
    MarkdownElementTypes.FULL_REFERENCE_LINK,
    MarkdownElementTypes.SHORT_REFERENCE_LINK,
    MarkdownElementTypes.LINK_DEFINITION,
    MarkdownElementTypes.IMAGE,
    MarkdownElementTypes.AUTOLINK,
    MarkdownElementTypes.HTML_BLOCK,
    MarkdownTokenTypes.AUTOLINK,
    MarkdownTokenTypes.EMAIL_AUTOLINK,
    MarkdownTokenTypes.URL,
    MarkdownTokenTypes.HTML_TAG,
    MarkdownTokenTypes.ATX_HEADER,
)
private val bareTagURLs = Regex("""(?:[A-Za-z][A-Za-z0-9+.-]*://|www\.)[^\s<>]+""")
private const val tagPunctuation = ".,!?;:()[]{}<>\"'，。！？；：、（）【】《》「」『』“”‘’…"

private fun tagBoundary(char: Char, before: Boolean): Boolean =
    char.isWhitespace() || char in tagPunctuation || (before && char == '-')

private fun escapedHash(content: String, offset: Int): Boolean {
    var slash = offset - 1
    while (slash >= 0 && content[slash] == '\\') slash--
    return (offset - slash - 1) % 2 == 1
}

private data class TagDisplayLine(val text: String, val ending: String, val tagOnly: Boolean)

private fun withoutTagLines(content: String, removed: BooleanArray): String {
    val lines = mutableListOf<TagDisplayLine>()
    var start = 0
    while (start < content.length) {
        var end = start
        while (end < content.length && content[end] != '\r' && content[end] != '\n') end++
        var next = end
        if (next < content.length && content[next] == '\r') next++
        if (next < content.length && content[next] == '\n') next++
        val text = buildString { for (index in start until end) if (!removed[index]) append(content[index]) }
        val tagOnly = text.isBlank() && (start until end).any { removed[it] }
        lines += TagDisplayLine(text, content.substring(end, next), tagOnly)
        start = next
    }
    return buildString {
        var index = 0
        while (index < lines.size) {
            if (lines[index].text.isNotBlank()) {
                append(lines[index].text)
                append(lines[index].ending)
                index++
                continue
            }
            val startOfBlanks = index
            while (index < lines.size && lines[index].text.isBlank()) index++
            val blanks = lines.subList(startOfBlanks, index)
            if (blanks.none { it.tagOnly }) {
                blanks.forEach { append(it.text); append(it.ending) }
            } else if (isNotEmpty() && index < lines.size) {
                // 只合并与纯标签行相邻的空行，代码及其他段落的空行保持原样。
                blanks.firstOrNull { !it.tagOnly }?.let { append(it.text); append(it.ending) }
            }
        }
    }
}

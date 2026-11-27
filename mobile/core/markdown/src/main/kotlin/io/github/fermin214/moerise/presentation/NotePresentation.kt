package io.github.fermin214.moerise.presentation

import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.MarkdownTokenTypes
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.flavours.gfm.GFMFlavourDescriptor
import org.intellij.markdown.parser.MarkdownParser

data class NotePresentation(val body: String, val tags: List<String>, val inlineTags: List<String> = emptyList())

data class NoteTagSpan(val start: Int, val end: Int, val tag: String)

fun notePresentation(content: String, tags: List<String>, trailingOnly: Boolean = false): NotePresentation =
    presentTags(content, tags.distinct(), noteTagSpans(content, tags), trailingOnly)

fun localNotePresentation(content: String): NotePresentation {
    val spans = noteTagSpans(content)
    return presentTags(content, spans.map { it.tag }.distinct(), spans)
}

private fun presentTags(content: String, tags: List<String>, spans: List<NoteTagSpan>, trailingOnly: Boolean = false): NotePresentation {
    if (spans.isEmpty()) return NotePresentation(content, tags)
    val removed = BooleanArray(content.length)
    spans.forEach { removed.fill(true, it.start, it.end) }
    val inline = mutableSetOf<String>()
    var index = 0
    val body = withoutTagLines(content, removed, trailingOnly) { start, end ->
        while (index < spans.size && spans[index].start < start) index++
        while (index < spans.size && spans[index].start < end) inline += spans[index++].tag
    }
    return NotePresentation(body, tags.filterNot { it in inline }, inline.toList())
}

private val summaryPrefix = Regex("""^ {0,3}(?:#{1,6}[ \t]+|>[ \t]?|[-+*][ \t]+|\d+[.)][ \t]+)""")
private val summaryDecoration = Regex(
    """(?<!\\)(\*\*|__|~~)(?=\S)([^\n]+?)(?<=\S)\1|(?<![*\\])\*(?![\s*])([^*\n]+?)(?<=\S)\*(?!\*)"""
)
private val summaryParagraph = Regex("""\n[ \t]*\n""")
private val summaryWhitespace = Regex("""(?U)\s+""")

fun walkSummaryBody(content: String, tags: List<String>): String =
    notePresentation(content, tags, trailingOnly = true).body.lineSequence()
        .joinToString("\n") { it.replaceFirst(summaryPrefix, "") }
        .splitToSequence(summaryParagraph).joinToString(" ", transform = ::plainSummaryParagraph)
        .replace(summaryWhitespace, " ").trim()

private fun plainSummaryParagraph(text: String): String = buildString {
    fun appendPlain(start: Int, end: Int) {
        append(summaryDecoration.replace(text.substring(start, end)) { (it.groups[2] ?: it.groups[3])!!.value })
    }
    val runs = backtickRun.findAll(text).map { run ->
        Backticks(run.range.first, run.range.last + 1, escapedBacktick(text, run.range.first))
    }.toList()
    var offset = 0
    forEachBacktickSpan(runs) { open, close ->
        appendPlain(offset, open.start + if (open.escaped) 1 else 0)
        append(text, open.end, close.start)
        offset = close.end
        true
    }
    appendPlain(offset, text.length)
}

fun walkStartPriority(body: String, hasAttachment: Boolean): Int? = when {
    body.codePointCount(0, body.length) >= 12 -> 0
    body.isNotEmpty() -> 1
    hasAttachment -> 2
    else -> null
}

fun limitWalkSummary(text: String): String = if (text.codePointCount(0, text.length) > 64) {
    text.substring(0, text.offsetByCodePoints(0, 64)) + "…"
} else text

class NoteTagScanner private constructor(private val content: String, lightweight: Boolean, tree: ASTNode?) {
    constructor(content: String, lightweight: Boolean = markdownIsLong(content)) : this(content, lightweight, null)
    internal constructor(content: String, tree: ASTNode) : this(content, false, tree)

    private val protected = if ('#' !in content) BooleanArray(content.length) else protectedTagOffsets(content, lightweight, tree)

    private fun validStart(offset: Int) = offset in content.indices && content[offset] == '#' && !protected[offset] &&
        (offset == 0 || tagBoundary(content[offset - 1], before = true))

    private fun wordChar(offset: Int) = offset in content.indices && !protected[offset] &&
        content[offset] != '#' && !tagBoundary(content[offset], before = false)

    fun prefixAt(cursor: Int): NoteTagSpan? {
        if (cursor !in 1..content.length) return null
        var start = cursor - 1
        while (wordChar(start)) start--
        if (!validStart(start)) return null
        var end = cursor
        while (wordChar(end)) end++
        if (content.getOrNull(end) == '#') return null
        return NoteTagSpan(start, end, content.substring(start + 1, cursor))
    }

    fun spans(tags: List<String>? = null): List<NoteTagSpan> {
        val candidates = tags?.filter { tag -> tag.isNotEmpty() && !tag.startsWith('#') && tag.none(Char::isWhitespace) }
            ?.distinct()?.sortedByDescending(String::length)
        if (candidates?.isEmpty() == true) return emptyList()
        val spans = mutableListOf<NoteTagSpan>()
        var offset = 0
        while (offset < content.length) {
            if (!validStart(offset)) {
                offset++
                continue
            }
            val start = offset + 1
            var end = start
            if (candidates == null) {
                while (wordChar(end)) end++
            } else {
                val exact = candidates.firstOrNull { tag ->
                    val stop = start + tag.length
                    content.startsWith(tag, start) && terminatingAt(stop) && (start until stop).none { protected[it] }
                }
                if (exact != null) end += exact.length
            }
            if (end > start && terminatingAt(end)) {
                spans += NoteTagSpan(offset, end, content.substring(start, end))
            }
            offset = maxOf(end, start)
        }
        return spans
    }

    private fun terminatingAt(end: Int) = end == content.length || tagBoundary(content[end], before = false)
}

fun noteTagSpans(content: String, tags: List<String>? = null): List<NoteTagSpan> =
    if ('#' !in content || tags?.isEmpty() == true) emptyList() else NoteTagScanner(content, lightweight = false).spans(tags)

fun tagMarkerAt(content: String, start: Int, end: Int = start): String {
    val before = if (start == 0 || tagBoundary(content[start - 1], before = true)) "" else " "
    val after = if (end == content.length || tagBoundary(content[end], before = false)) "" else " "
    return "$before#$after"
}

fun initialEditorSelection(content: String): Int {
    if ('#' !in content) return content.length
    val spans = NoteTagScanner(content).spans()
    if (spans.isEmpty()) return content.length
    var cursor = content.length
    while (cursor > 0 && content[cursor - 1].isWhitespace()) cursor--
    var trailing = false
    for (span in spans.asReversed()) {
        if (span.end != cursor) break
        trailing = true
        cursor = span.start
        while (cursor > 0 && content[cursor - 1].isWhitespace()) cursor--
    }
    return if (trailing && cursor > 0) cursor else content.length
}

private fun protectedTagOffsets(content: String, lightweight: Boolean, tree: ASTNode?): BooleanArray {
    val protected = BooleanArray(content.length)
    fun protect(node: ASTNode) {
        if (node.type in protectedTagNodes) protected.fill(true, node.startOffset, node.endOffset)
        else node.children.forEach(::protect)
    }
    if (lightweight) protectTagLines(content, protected)
    else protect(tree ?: MarkdownParser(GFMFlavourDescriptor()).buildMarkdownTreeFromString(content))
    bareTagURLs.findAll(content).forEach { protected.fill(true, it.range.first, it.range.last + 1) }
    return protected
}

internal val fencePattern = Regex("""^ {0,3}(`{3,}|~{3,})""")
private val protectedLine = Regex("""^(?: {4}|\t| {0,3}(?:#{1,6}(?:\s|$)|\[[^\]]+]:))""")
private val protectedInline = Regex("""<[^<>\r\n]*>|!?\[[^\[\]\r\n]*](?:\([^()\r\n]*\)|\[[^\[\]\r\n]*])?""")
internal val backtickRun = Regex("`+")
internal val quotePrefix = Regex("""^(?: {0,3}>[ \t]?)+""")
private val listPrefix = Regex("""^ {0,3}(?:[-+*]|\d+[.)])[ \t]+""")
private val escape = Regex("""\\[^\r\n]""")

private data class Backticks(val start: Int, val end: Int, val escaped: Boolean)

internal fun escapedBacktick(text: String, start: Int): Boolean {
    var slash = start
    while (slash > 0 && text[slash - 1] == '\\') slash--
    return (start - slash) % 2 != 0
}

private fun forEachBacktickSpan(ticks: List<Backticks>, accept: (Backticks, Backticks) -> Boolean) {
    val next = mutableMapOf<Int, Int>()
    val closing = IntArray(ticks.size) { -1 }
    for (index in ticks.indices.reversed()) {
        val run = ticks[index]
        val size = run.end - run.start
        closing[index] = next[size - if (run.escaped) 1 else 0] ?: -1
        next[size] = index
    }
    var index = 0
    while (index < ticks.size) {
        val stop = closing[index]
        index = if (stop >= 0 && accept(ticks[index], ticks[stop])) stop + 1 else index + 1
    }
}

private fun protectTagLines(content: String, protected: BooleanArray) {
    val ticks = mutableListOf<Backticks>()
    fun finishBlock() {
        forEachBacktickSpan(ticks) { open, close ->
            val start = open.start + if (open.escaped) 1 else 0
            if (protected[start]) false else {
                protected.fill(true, start, close.end)
                true
            }
        }
        ticks.clear()
    }
    var offset = 0
    var fence: String? = null
    var quoteDepth = 0
    for (line in content.split('\n')) {
        val quote = quotePrefix.find(line)?.value.orEmpty()
        val depth = quote.count { it == '>' }
        val prefix = quote.length
        val body = line.substring(prefix).trimEnd('\r')
        val marker = fencePattern.find(body)?.groupValues?.get(1)
        val open = fence
        if (depth != quoteDepth || body.isBlank() || marker != null ||
            protectedLine.containsMatchIn(body) || listPrefix.containsMatchIn(body)) finishBlock()
        quoteDepth = depth
        when {
            open != null -> {
                protected.fill(true, offset, offset + line.length)
                if (marker != null && marker.first() == open.first() && marker.length >= open.length &&
                    body.trim().drop(marker.length).isBlank()) fence = null
            }
            marker != null || protectedLine.containsMatchIn(body) -> {
                fence = marker
                protected.fill(true, offset, offset + line.length)
            }
            else -> {
                val start = offset + prefix
                protectedInline.findAll(body).forEach { protected.fill(true, start + it.range.first, start + it.range.last + 1) }
                escape.findAll(body).forEach { protected[start + it.range.last] = true }
                backtickRun.findAll(body).forEach { run ->
                    val at = start + run.range.first
                    ticks += Backticks(at, start + run.range.last + 1, escapedBacktick(body, run.range.first))
                }
            }
        }
        offset += line.length + 1
    }
    finishBlock()
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
    MarkdownElementTypes.ATX_1,
    MarkdownElementTypes.ATX_2,
    MarkdownElementTypes.ATX_3,
    MarkdownElementTypes.ATX_4,
    MarkdownElementTypes.ATX_5,
    MarkdownElementTypes.ATX_6,
    MarkdownTokenTypes.AUTOLINK,
    MarkdownTokenTypes.EMAIL_AUTOLINK,
    MarkdownTokenTypes.URL,
    MarkdownTokenTypes.HTML_TAG,
    MarkdownTokenTypes.ATX_HEADER,
)
private val bareTagURLs = Regex("""(?:(?<![A-Za-z0-9+.-])[A-Za-z][A-Za-z0-9+.-]*://|www\.)[^\s<>]+""")
private const val tagPunctuation = ".,!?;:()[]{}<>\"'，。！？；：、（）【】《》「」『』“”‘’…*~`|"

private fun tagBoundary(char: Char, before: Boolean): Boolean =
    char.isWhitespace() || char in tagPunctuation || (before && char == '-')

private data class TagDisplayLine(val text: String, val ending: String, val tagOnly: Boolean)

private fun withoutTagLines(content: String, removed: BooleanArray, trailingOnly: Boolean, onInlineLine: (Int, Int) -> Unit): String {
    val lines = mutableListOf<TagDisplayLine>()
    var start = 0
    while (start < content.length) {
        var end = start
        while (end < content.length && content[end] != '\r' && content[end] != '\n') end++
        var next = end
        if (next < content.length && content[next] == '\r') next++
        if (next < content.length && content[next] == '\n') next++
        val tagOnly = (start until end).all { removed[it] || content[it].isWhitespace() } && (start until end).any { removed[it] }
        val text = if (tagOnly && !trailingOnly) "" else content.substring(start, end)
        if (!tagOnly) onInlineLine(start, end)
        lines += TagDisplayLine(text, content.substring(end, next), tagOnly)
        start = next
    }
    if (trailingOnly) {
        val lastBody = lines.indexOfLast { !it.tagOnly && it.text.isNotBlank() }
        return lines.withIndex().joinToString("") { (index, line) ->
            if (index <= lastBody || !line.tagOnly) line.text + line.ending else ""
        }
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
                blanks.firstOrNull { !it.tagOnly }?.let { append(it.text); append(it.ending) }
            }
        }
    }
}

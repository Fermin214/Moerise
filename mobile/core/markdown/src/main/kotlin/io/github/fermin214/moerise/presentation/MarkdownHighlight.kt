package io.github.fermin214.moerise.presentation

import org.intellij.markdown.MarkdownElementTypes as Element
import org.intellij.markdown.MarkdownTokenTypes as Token
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.flavours.gfm.GFMElementTypes
import org.intellij.markdown.flavours.gfm.GFMFlavourDescriptor
import org.intellij.markdown.parser.MarkdownParser

enum class MarkdownHighlightStyle {
    Heading1, Heading2, Heading3, Heading4, Heading5, Heading6,
    SyntaxMarker, ListPrefix, QuotePrefix, Bold, Italic, Strike, InlineCode, CodeFence, LinkLabel, LinkMeta, Tag,
}

data class MarkdownHighlightSpan(val start: Int, val end: Int, val style: MarkdownHighlightStyle)

internal fun markdownIsLong(text: String) =
    text.length > 16_384 || (text.length > 4096 && text.encodeToByteArray().size > 16_384)

private val headings = mapOf(
    Element.ATX_1 to MarkdownHighlightStyle.Heading1,
    Element.ATX_2 to MarkdownHighlightStyle.Heading2,
    Element.ATX_3 to MarkdownHighlightStyle.Heading3,
    Element.ATX_4 to MarkdownHighlightStyle.Heading4,
    Element.ATX_5 to MarkdownHighlightStyle.Heading5,
    Element.ATX_6 to MarkdownHighlightStyle.Heading6,
)

private val headingLine = Regex("""^ {0,3}(#{1,6})(?:[ \t]+|$)(.*)$""")
private val trailingHeadingMarker = Regex("""[ \t]+(#+)[ \t]*$""")

fun markdownHighlightSpans(text: String): List<MarkdownHighlightSpan> = buildList {
    fun span(start: Int, end: Int, style: MarkdownHighlightStyle) {
        if (start < end) add(MarkdownHighlightSpan(start, end, style))
    }
    fun span(node: ASTNode, style: MarkdownHighlightStyle) = span(node.startOffset, node.endOffset, style)
    val lightweight = markdownIsLong(text)
    val scanner = if (lightweight) {
        var fence: String? = null
        var offset = 0
        var quoteDepth = 0
        for (line in text.split('\n')) {
            val quote = quotePrefix.find(line)?.value.orEmpty()
            val depth = quote.count { it == '>' }
            if (depth != quoteDepth) fence = null
            quoteDepth = depth
            val start = offset + quote.length
            val body = line.substring(quote.length).trimEnd('\r')
            val marker = fencePattern.find(body)?.groups?.get(1)
            val open = fence
            if (open != null || marker != null) {
                span(start, start + body.length, MarkdownHighlightStyle.CodeFence)
                if (marker != null && (open == null || marker.value.first() == open.first() &&
                        marker.value.length >= open.length && body.substring(marker.range.last + 1).isBlank())) {
                    span(start + marker.range.first, start + marker.range.last + 1, MarkdownHighlightStyle.SyntaxMarker)
                    fence = if (open == null) marker.value else null
                }
            } else headingLine.matchEntire(body)?.let { heading ->
                val prefix = requireNotNull(heading.groups[1])
                span(start, start + body.length, MarkdownHighlightStyle.entries[prefix.value.length - 1])
                span(start + prefix.range.first, start + prefix.range.last + 1, MarkdownHighlightStyle.SyntaxMarker)
                trailingHeadingMarker.find(body, prefix.range.last + 1)?.groups?.get(1)?.let { ending ->
                    span(start + ending.range.first, start + ending.range.last + 1, MarkdownHighlightStyle.SyntaxMarker)
                }
            }
            offset += line.length + 1
        }
        NoteTagScanner(text, lightweight = true)
    } else {
        val tree = MarkdownParser(GFMFlavourDescriptor()).buildMarkdownTreeFromString(text)
        val syntax = MarkdownHighlightStyle.SyntaxMarker
        fun delimited(node: ASTNode, style: MarkdownHighlightStyle, width: Int) {
            span(node, style)
            span(node.startOffset, node.startOffset + width, syntax)
            span(node.endOffset - width, node.endOffset, syntax)
        }
        fun walk(node: ASTNode) {
            headings[node.type]?.let { span(node, it) }
            when (node.type) {
                Token.ATX_HEADER -> span(node, syntax)
                Token.LIST_BULLET, Token.LIST_NUMBER -> {
                    if (text[node.endOffset - 1] in " \t") span(node, MarkdownHighlightStyle.ListPrefix)
                }
                Token.BLOCK_QUOTE -> {
                    if (text[node.endOffset - 1] in " \t" || text.getOrNull(node.endOffset) in listOf(' ', '\t'))
                        span(node, MarkdownHighlightStyle.QuotePrefix)
                }
                Element.STRONG -> delimited(node, MarkdownHighlightStyle.Bold, 2)
                Element.EMPH -> delimited(node, MarkdownHighlightStyle.Italic, 1)
                GFMElementTypes.STRIKETHROUGH -> delimited(node, MarkdownHighlightStyle.Strike, 2)
                Element.CODE_SPAN -> {
                    span(node, MarkdownHighlightStyle.InlineCode)
                    node.children.filter { it.type == Token.BACKTICK }.forEach { span(it, syntax) }
                    return
                }
                Element.CODE_BLOCK, Element.CODE_FENCE -> {
                    span(node, MarkdownHighlightStyle.CodeFence)
                    node.children.filter { it.type in setOf(Token.CODE_FENCE_START, Token.CODE_FENCE_END, Token.FENCE_LANG) }
                        .forEach { span(it, syntax) }
                    return
                }
                Element.INLINE_LINK, Element.FULL_REFERENCE_LINK, Element.SHORT_REFERENCE_LINK -> {
                    val label = node.children.firstOrNull { it.type == Element.LINK_TEXT || it.type == Element.LINK_LABEL }
                    if (label != null) {
                        span(label.startOffset + 1, label.endOffset - 1, MarkdownHighlightStyle.LinkLabel)
                        span(label.endOffset, node.endOffset, MarkdownHighlightStyle.LinkMeta)
                        span(label.startOffset, label.startOffset + 1, syntax)
                        span(label.endOffset - 1, label.endOffset, syntax)
                        node.children.filter { it.type == Token.LPAREN || it.type == Token.RPAREN }.forEach { span(it, syntax) }
                    }
                }
                Token.EXCLAMATION_MARK -> span(node, syntax)
            }
            node.children.forEach(::walk)
        }
        walk(tree)
        NoteTagScanner(text, tree)
    }
    scanner.spans().forEach { span(it.start, it.end, MarkdownHighlightStyle.Tag) }
}

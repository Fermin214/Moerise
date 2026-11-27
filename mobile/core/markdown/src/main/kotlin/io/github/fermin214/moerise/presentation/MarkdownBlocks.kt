package io.github.fermin214.moerise.presentation

import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.flavours.gfm.GFMFlavourDescriptor
import org.intellij.markdown.parser.MarkdownParser

private val listItemStart = Regex("""^ {0,3}(?:[-+*]|\d+[.)])(?:[ \t]+|$)""")

internal fun markdownTree(content: String, parseInlines: Boolean): ASTNode =
    MarkdownParser(GFMFlavourDescriptor()).parse(MarkdownElementTypes.MARKDOWN_FILE, content, parseInlines)

fun splitMarkdownBlocks(content: String): List<String> {
    if (content.isBlank()) return emptyList()
    val normalized = content.replace("\r\n", "\n")
    val lines = normalized.split('\n')
    val protected = protectedBlockLines(normalized, lines)
    val blocks = mutableListOf<String>()
    val current = mutableListOf<String>()
    var inList = false
    fun finishBlock() {
        if (current.isNotEmpty()) blocks.add(current.joinToString("\n"))
        current.clear()
        inList = false
    }
    for (index in lines.indices) {
        val line = lines[index]
        if (protected[index]) {
            current += line
        } else if (line.isBlank()) {
            var next = index + 1
            while (next < lines.size && lines[next].isBlank()) next++
            val nextLine = lines.getOrNull(next)
            val continuation = nextLine != null && (nextLine.startsWith("  ") || nextLine.startsWith("\t"))
            if (continuation && current.isNotEmpty()) current.add(line) else finishBlock()
        } else {
            val listItem = listItemStart.containsMatchIn(line)
            if (!line.startsWith(' ') && !line.startsWith('\t') && inList != listItem) finishBlock()
            if (listItem) inList = true
            current += line
        }
    }
    finishBlock()
    return blocks
}

private fun protectedBlockLines(content: String, lines: List<String>): BooleanArray {
    val protected = BooleanArray(lines.size)
    if ('`' !in content && '<' !in content && "~~~" !in content) return protected
    val starts = IntArray(lines.size + 1)
    lines.forEachIndexed { index, line -> starts[index + 1] = starts[index] + line.length + 1 }
    var line = 0
    fun mark(start: Int, end: Int, includeFirst: Boolean) {
        while (line + 1 < starts.size && starts[line + 1] <= start) line++
        var row = if (includeFirst) line else line + 1
        while (row < lines.size && starts[row] < end) protected[row++] = true
    }
    fun visit(node: ASTNode) {
        when (node.type) {
            MarkdownElementTypes.HTML_BLOCK, MarkdownElementTypes.CODE_FENCE ->
                mark(node.startOffset, node.endOffset, includeFirst = true)
            MarkdownElementTypes.PARAGRAPH -> {
                val start = node.startOffset
                val text = content.substring(start, node.endOffset)
                if ('\n' in text) for (span in inlineCodeSpans(text)) {
                    mark(start + span.opening, start + span.end, includeFirst = false)
                }
            }
            else -> node.children.forEach(::visit)
        }
    }
    visit(markdownTree(content, parseInlines = false))
    return protected
}


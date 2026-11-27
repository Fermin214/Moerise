package io.github.fermin214.moerise.presentation

import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.flavours.gfm.GFMFlavourDescriptor
import org.intellij.markdown.parser.MarkdownParser

private val listItemStart = Regex("""^ {0,3}(?:[-+*]|\d+[.)])(?:[ \t]+|$)""")

internal fun markdownFence(line: String): String? {
    val match = fencePattern.find(line) ?: return null
    val marker = match.groupValues[1]
    return marker.takeIf { it.first() == '~' || '`' !in line.substring(match.range.last + 1) }
}

internal fun closesMarkdownFence(line: String, open: String): Boolean {
    val match = fencePattern.find(line) ?: return false
    val marker = match.groupValues[1]
    return marker.first() == open.first() && marker.length >= open.length && line.substring(match.range.last + 1).isBlank()
}

fun splitMarkdownBlocks(content: String): List<String> {
    if (content.isBlank()) return emptyList()
    val normalized = content.replace("\r\n", "\n")
    val lines = normalized.split('\n')
    val starts = IntArray(lines.size + 1)
    lines.forEachIndexed { index, line -> starts[index + 1] = starts[index] + line.length + 1 }
    val html = htmlBlockLines(normalized, starts)
    val fenced = fencedLines(lines, html)
    val inCodeSpan = inlineCodeContinuationLines(normalized, lines, starts, html, fenced)
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
        if (html[index] || fenced[index] || inCodeSpan[index]) {
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

private fun htmlBlockLines(content: String, starts: IntArray): BooleanArray {
    val protected = BooleanArray(starts.size - 1)
    if ('<' !in content) return protected
    val tree = MarkdownParser(GFMFlavourDescriptor()).parse(MarkdownElementTypes.MARKDOWN_FILE, content, false)
    fun mark(node: ASTNode) {
        if (node.type == MarkdownElementTypes.HTML_BLOCK) {
            val found = starts.binarySearch(node.startOffset)
            var line = if (found >= 0) found else -found - 2
            while (line < protected.size && starts[line] < node.endOffset) protected[line++] = true
        } else node.children.forEach(::mark)
    }
    mark(tree)
    return protected
}

private fun fencedLines(lines: List<String>, html: BooleanArray): BooleanArray {
    val protected = BooleanArray(lines.size)
    var open: String? = null
    for ((index, line) in lines.withIndex()) {
        val fence = open
        if (fence != null) {
            protected[index] = true
            if (closesMarkdownFence(line, fence)) open = null
        } else if (!html[index]) {
            open = markdownFence(line)
            protected[index] = open != null
        }
    }
    return protected
}

private fun inlineCodeContinuationLines(
    content: String, lines: List<String>, starts: IntArray, html: BooleanArray, fenced: BooleanArray,
): BooleanArray {
    val protected = BooleanArray(lines.size)
    if ('`' !in content) return protected
    var start = 0
    fun markUntil(end: Int) {
        if (end - start < 2) return
        val spans = inlineCodeSpans(content.substring(starts[start], starts[end] - 1))
        var line = start + 1
        for (span in spans) {
            while (line < end && starts[line] <= starts[start] + span.opening) line++
            while (line < end && starts[line] < starts[start] + span.end) protected[line++] = true
        }
    }
    for ((index, line) in lines.withIndex()) {
        if (html[index] || fenced[index] || line.isBlank()) {
            markUntil(index)
            start = index + 1
        } else if (listItemStart.containsMatchIn(line) && !line.startsWith(' ') && index > start) {
            markUntil(index)
            start = index
        }
    }
    markUntil(lines.size)
    return protected
}


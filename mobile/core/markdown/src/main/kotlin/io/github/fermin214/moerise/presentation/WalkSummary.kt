package io.github.fermin214.moerise.presentation

import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.MarkdownTokenTypes
import org.intellij.markdown.ast.ASTNode

private val summaryPrefix = Regex("""^ {0,3}(?:#{1,6}[ \t]+|>[ \t]?|[-+*][ \t]+|\d+[.)][ \t]+)""", RegexOption.MULTILINE)
private val summaryRuns = Regex("""\*+|_+|~+|\n""")

fun walkSummaryBody(content: String, tags: List<String>): String {
    val normalized = content.replace("\r\n", "\n").replace('\r', '\n')
    val tagTree = if (tags.isNotEmpty() && '#' in normalized) markdownTree(normalized, parseInlines = true) else null
    val body = trailingTagPresentation(normalized, tags, tagTree).body
    val parts = mutableListOf<String>()
    var offset = 0
    fun visit(node: ASTNode) {
        if (node.type == MarkdownElementTypes.CODE_FENCE) {
            val lineStart = body.lastIndexOf('\n', node.startOffset - 1) + 1
            val start = maxOf(lineStart, offset)
            if (start > offset) parts += summarizeProse(body.substring(offset, start))
            for (child in node.children) {
                if (child.type == MarkdownTokenTypes.CODE_FENCE_CONTENT) {
                    parts += body.substring(child.startOffset, child.endOffset)
                }
            }
            offset = node.endOffset
        } else {
            node.children.forEach(::visit)
        }
    }
    if ("```" in body || "~~~" in body) {
        val bodyTree = if (body == normalized && tagTree != null) tagTree else markdownTree(body, parseInlines = false)
        visit(bodyTree)
    }
    parts += summarizeProse(body.substring(offset))
    return foldSummaryWhitespace(parts.joinToString(" "))
}

private fun foldSummaryWhitespace(text: String): String = buildString(text.length) {
    var offset = 0
    var pendingSpace = false
    while (offset < text.length) {
        val codePoint = text.codePointAt(offset)
        if (Character.isWhitespace(codePoint) || Character.isSpaceChar(codePoint)) {
            pendingSpace = isNotEmpty()
        } else {
            if (pendingSpace) append(' ')
            appendCodePoint(codePoint)
            pendingSpace = false
        }
        offset += Character.charCount(codePoint)
    }
}

private fun summarizeProse(text: String): String {
    val parts = mutableListOf<String>()
    val prose = mutableListOf<String>()
    fun finishProse() {
        if (prose.isNotEmpty()) parts += cleanSummaryProse(prose.joinToString("\n"))
        prose.clear()
    }
    for (line in text.lineSequence()) {
        if (line.isBlank()) finishProse()
        else if (prose.isEmpty() && (line.startsWith("    ") || line.startsWith('\t'))) parts += line
        else prose += line
    }
    finishProse()
    return parts.joinToString(" ")
}

private fun cleanSummaryProse(text: String): String {
    val removed = BooleanArray(text.length)
    val code = inlineCodeSpans(text)
    for (span in code) {
        removed.fill(true, span.opening, span.start)
        removed.fill(true, span.stop, span.end)
    }
    var codeIndex = 0
    fun insideCode(offset: Int): Boolean {
        while (codeIndex < code.size && code[codeIndex].end <= offset) codeIndex++
        return codeIndex < code.size && code[codeIndex].opening <= offset
    }
    for (prefix in summaryPrefix.findAll(text)) {
        if (!insideCode(prefix.range.first)) removed.fill(true, prefix.range.first, prefix.range.last + 1)
    }
    codeIndex = 0
    val openers = listOf("*", "**", "__", "~~").associateWith { mutableListOf<Int>() }
    for (run in summaryRuns.findAll(text)) {
        val raw = run.value
        if (raw == "\n") {
            openers.values.forEach { it.clear() }
            continue
        }
        val escaped = escapedDelimiter(text, run.range.first)
        val marker = if (escaped) raw.drop(1) else raw
        val stack = openers[marker] ?: continue
        val start = run.range.first + if (escaped) 1 else 0
        val end = run.range.last + 1
        if (removed[start] || insideCode(start)) continue
        val before = if (start > 0) text.codePointBefore(start) else ' '.code
        val after = if (end < text.length) text.codePointAt(end) else ' '.code
        val beforeSpace = Character.isWhitespace(before) || Character.isSpaceChar(before)
        val afterSpace = Character.isWhitespace(after) || Character.isSpaceChar(after)
        val punctuation = Character.DASH_PUNCTUATION.toInt()..Character.FINAL_QUOTE_PUNCTUATION.toInt()
        val beforePunctuation = Character.getType(before) in punctuation
        val afterPunctuation = Character.getType(after) in punctuation
        val left = !afterSpace && (!afterPunctuation || beforeSpace || beforePunctuation)
        val right = !beforeSpace && (!beforePunctuation || afterSpace || afterPunctuation)
        if (stack.isNotEmpty() && right && (marker != "__" || !left || afterPunctuation)) {
            val opening = stack.removeAt(stack.lastIndex)
            removed.fill(true, opening, opening + marker.length)
            removed.fill(true, start, end)
            for (other in openers.values) {
                while (other.isNotEmpty() && other.last() > opening) other.removeAt(other.lastIndex)
            }
        } else if (left && (marker != "__" || !right || beforePunctuation)) stack += start
    }
    return buildString(text.length) {
        for (index in text.indices) if (!removed[index]) append(text[index])
    }
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

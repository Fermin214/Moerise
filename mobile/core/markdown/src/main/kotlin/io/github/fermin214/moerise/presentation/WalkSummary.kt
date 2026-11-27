package io.github.fermin214.moerise.presentation

private val summaryPrefix = Regex("""^ {0,3}(?:#{1,6}[ \t]+|>[ \t]?|[-+*][ \t]+|\d+[.)][ \t]+)""", RegexOption.MULTILINE)
private val summaryRuns = Regex("""\*+|_+|~+|\n""")
private val summaryWhitespace = Regex("""(?U)\s+""")

fun walkSummaryBody(content: String, tags: List<String>): String {
    val body = notePresentation(content, tags, trailingOnly = true).body
    val parts = mutableListOf<String>()
    val prose = mutableListOf<String>()
    var fence: String? = null
    fun finishProse() {
        if (prose.isNotEmpty()) parts += cleanSummaryProse(prose.joinToString("\n"))
        prose.clear()
    }
    for (line in body.lineSequence()) {
        val open = fence
        if (open != null) {
            if (closesMarkdownFence(line, open)) fence = null else parts += line
        } else {
            fence = markdownFence(line)
            if (fence != null || line.isBlank() || (prose.isEmpty() && (line.startsWith("    ") || line.startsWith('\t')))) {
                finishProse()
                if (fence == null) parts += line
            } else prose += line
        }
    }
    finishProse()
    return parts.joinToString(" ").replace(summaryWhitespace, " ").trim()
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

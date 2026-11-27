package io.github.fermin214.moerise.presentation

private val listItemStart = Regex("""^ {0,3}(?:[-+*]|\d+[.)])(?:[ \t]+|$)""")

fun splitMarkdownBlocks(content: String): List<String> {
    if (content.isBlank()) return emptyList()
    val lines = content.replace("\r\n", "\n").split('\n')
    val blocks = mutableListOf<String>()
    val current = mutableListOf<String>()
    var fence: String? = null
    var inList = false
    var inlineTicks = 0
    fun finishBlock() {
        if (current.isNotEmpty()) blocks.add(current.joinToString("\n"))
        current.clear()
        inList = false
        inlineTicks = 0
    }
    for (index in lines.indices) {
        val line = lines[index]
        val marker = fencePattern.find(line)?.groupValues?.get(1)
        val open = fence
        if (open != null) {
            current.add(line)
            if (marker != null && marker.first() == open.first() && marker.length >= open.length &&
                line.trimStart(' ').drop(marker.length).isBlank()) {
                fence = null
            }
        } else if (line.isBlank()) {
            inlineTicks = 0
            var next = index + 1
            while (next < lines.size && lines[next].isBlank()) next++
            val nextLine = lines.getOrNull(next)
            val continuation = nextLine != null && (nextLine.startsWith("  ") || nextLine.startsWith("\t"))
            if (continuation && current.isNotEmpty()) current.add(line) else finishBlock()
        } else if (inlineTicks == 0 && marker != null &&
            (marker.first() == '~' || '`' !in line.trimStart(' ').drop(marker.length))) {
            fence = marker
            current.add(line)
        } else {
            val listItem = listItemStart.containsMatchIn(line)
            if (inlineTicks == 0 && !line.startsWith(' ') && !line.startsWith('\t') && inList != listItem) finishBlock()
            if (inlineTicks == 0 && listItem) inList = true
            current.add(line)
            for (run in backtickRun.findAll(line)) {
                val size = run.value.length
                if (inlineTicks != 0) {
                    if (size == inlineTicks) inlineTicks = 0
                } else {
                    inlineTicks = size - if (escapedBacktick(line, run.range.first)) 1 else 0
                }
            }
        }
    }
    finishBlock()
    return blocks
}


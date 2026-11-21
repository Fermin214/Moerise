package io.github.fermin214.moerise.presentation

fun splitMarkdownBlocks(content: String): List<String> {
    if (content.isBlank()) return emptyList()
    val lines = content.replace("\r\n", "\n").split('\n')
    val blocks = mutableListOf<String>()
    val current = mutableListOf<String>()
    var fenceChar: Char? = null
    var fenceLen = 0
    for (index in lines.indices) {
        val line = lines[index]
        val trimmed = line.dropWhile { it == ' ' }
        val indent = line.length - trimmed.length
        if (indent <= 3 && trimmed.isNotEmpty()) {
            val ch = trimmed[0]
            if (ch == '`' || ch == '~') {
                var run = 0
                while (run < trimmed.length && trimmed[run] == ch) run++
                if (run >= 3) {
                    if (fenceChar == null) {
                        if (ch == '~' || !trimmed.substring(run).contains('`')) {
                            fenceChar = ch
                            fenceLen = run
                        }
                    } else if (fenceChar == ch && run >= fenceLen && trimmed.substring(run).isBlank()) {
                        fenceChar = null
                        fenceLen = 0
                    }
                }
            }
        }
        if (fenceChar == null && line.isBlank()) {
            var next = index + 1
            while (next < lines.size && lines[next].isBlank()) next++
            val nextLine = if (next < lines.size) lines[next] else null
            val continuation = nextLine != null && (nextLine.startsWith("  ") || nextLine.startsWith("\t"))
            if (continuation && current.isNotEmpty()) {
                current.add(line)
            } else if (current.isNotEmpty()) {
                blocks.add(current.joinToString("\n"))
                current.clear()
            }
        } else {
            current.add(line)
        }
    }
    if (current.isNotEmpty()) blocks.add(current.joinToString("\n"))
    return blocks
}


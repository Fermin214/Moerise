package io.github.fermin214.moerise.presentation

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue
import kotlin.time.Duration.Companion.seconds
import kotlin.time.measureTime
import kotlinx.serialization.json.*
import org.intellij.markdown.MarkdownTokenTypes
import org.intellij.markdown.ast.ASTNode

class NotePresentationTest {
    @Test
    fun sharedReadingContract() {
        val source = requireNotNull(javaClass.getResourceAsStream("/note-presentation.json"))
        val cases = source.bufferedReader().use { Json.parseToJsonElement(it.readText()).jsonArray }
        for (value in cases) {
            val sample = value.jsonObject
            fun text(key: String) = sample.getValue(key).jsonPrimitive.content
            fun strings(key: String) = sample.getValue(key).jsonArray.map { it.jsonPrimitive.content }
            val prefix = "甲".repeat(sample["repeatPrefix"]?.jsonPrimitive?.int ?: 0)
            val result = notePresentation(prefix + text("content"), strings("tags"))
            assertEquals(prefix + text("body"), result.body, text("name"))
            assertEquals(strings("footer"), result.tags, text("name"))
            assertEquals(strings("inline"), result.inlineTags, text("name"))
            if (prefix.isNotEmpty()) assertEquals(listOf("收藏"), NoteTagScanner(prefix + text("content")).spans().map { it.tag })
        }
    }

    @Test
    fun longAsciiTextKeepsTheRealTagAndProtectsUrlFragments() {
        val suffix = " custom+scheme://example.test/#日记 #日记"
        val content = "A".repeat(65_536 - suffix.encodeToByteArray().size) + suffix
        val elapsed = measureTime {
            val spans = NoteTagScanner(content).spans()
            assertEquals(listOf(NoteTagSpan(content.lastIndexOf('#'), content.length, "日记")), spans)
        }
        assertTrue(elapsed < 2.seconds, "64 KiB tag scan took $elapsed")
        assertEquals(listOf("日记"), notePresentation(content, listOf("日记")).inlineTags)
    }


    @Test
    fun writingPrefixSpansWholeWordAndSkipsProtectedTextEvenOnLongInput() {
        val text = "left #abc right"
        val span = NoteTagScanner(text).prefixAt("left #a".length)
        assertEquals(NoteTagSpan(5, 9, "a"), span)
        assertEquals("left #actual  right", text.replaceRange(span!!.start, span.end, "#actual "))
        assertEquals(" #", tagMarkerAt("left", 4))
        assertEquals("# ", tagMarkerAt("左 右", 2))
        val long = "甲".repeat(17_000) + "\n`#code` \\#escape #real\n```\n#block\n```\nwww.example.com/#url"
        assertEquals(listOf("real"), NoteTagScanner(long).spans().map { it.tag })
        assertEquals(listOf("日记"), NoteTagScanner("左 `\n\n#日记\n\n` 右", lightweight = true).spans().map { it.tag })
        assertEquals(listOf("日记"), NoteTagScanner("左 \\`\n#日记\n结尾`", lightweight = true).spans().map { it.tag })
    }

    @Test
    fun editorSpansFollowMarkdownSyntaxAndKeepRawOffsets() {
        val text = "# 标题 **粗体** *斜体* ~~划去~~\n\n- 列表\n\n1. 列表\n\n1.无空格\n\n> 引用\n\n" +
            "`#行内代码` [链接](https://example.test/#锚点) \\#转义\n\n```md\n# 围栏\n#代码标签\n```\n\n🙂 #日记"
        val spans = markdownHighlightSpans(text)
        fun samples(style: MarkdownHighlightStyle) = spans.filter { it.style == style }.map { text.substring(it.start, it.end) }
        assertEquals(listOf("**粗体**"), samples(MarkdownHighlightStyle.Bold))
        assertEquals(listOf("*斜体*"), samples(MarkdownHighlightStyle.Italic))
        assertEquals(listOf("~~划去~~"), samples(MarkdownHighlightStyle.Strike))
        assertEquals(listOf("- ", "1. "), samples(MarkdownHighlightStyle.ListPrefix))
        assertEquals(listOf("> "), samples(MarkdownHighlightStyle.QuotePrefix))
        assertEquals(listOf("`#行内代码`"), samples(MarkdownHighlightStyle.InlineCode))
        assertEquals(listOf("链接"), samples(MarkdownHighlightStyle.LinkLabel))
        assertEquals(listOf("#日记"), samples(MarkdownHighlightStyle.Tag))
        assertEquals(1, samples(MarkdownHighlightStyle.Heading1).size)
        assertTrue(samples(MarkdownHighlightStyle.CodeFence).single().contains("#代码标签"))
        assertTrue(samples(MarkdownHighlightStyle.SyntaxMarker).containsAll(listOf("#", "(", ")", "`", "```")))
        assertTrue(spans.all { it.start >= 0 && it.end <= text.length && it.start < it.end })
    }

    @Test
    fun editorSpansDegradeAtSixteenKibibytesWithoutLeakingFencedTags() {
        val text = "汉".repeat(6000) + "\n\n###### 标题 ######\r\n\n**普通** `#代码` #有效\n\n~~~md\n# 标题\n#隐藏\n~~~"
        val spans = markdownHighlightSpans(text)
        assertEquals(setOf(MarkdownHighlightStyle.Heading6, MarkdownHighlightStyle.SyntaxMarker,
            MarkdownHighlightStyle.Tag, MarkdownHighlightStyle.CodeFence), spans.map { it.style }.toSet())
        assertEquals("#有效", spans.single { it.style == MarkdownHighlightStyle.Tag }.let { text.substring(it.start, it.end) })
    }

    @Test
    fun knownTagsOnlyAndMetadataOrder() {
        val tags = listOf("阅读", "Work", "阅读", "Work/Child", "Work")
        val original = "#阅读 #未知 #Work/Child #WorkLong #Work-long #Work_suffix #Work/Other"
        val result = notePresentation(original, tags)
        assertEquals(original, result.body)
        assertEquals(listOf("Work"), result.tags)
        assertEquals(listOf("阅读", "Work/Child"), result.inlineTags)
        assertEquals("#阅读 #未知 #Work/Child #WorkLong #Work-long #Work_suffix #Work/Other", original)
        assertEquals(listOf("阅读", "Work", "阅读", "Work/Child", "Work"), tags)
        assertEquals(original, notePresentation(original, emptyList()).body)
    }

    @Test
    fun boundariesAndMarkdownHeadings() {
        val original = "# 阅读\n## 标题\n### Work\n\n#Work 标记，#阅读。 (#Work)；“#阅读” abc#Work #WorkLong #Work/Child\n# Work"
        assertEquals(original, notePresentation(original, listOf("阅读", "Work")).body)
        assertEquals("# 阅读\n## 标题", notePresentation("# 阅读\n## 标题", listOf(" 阅读", "#", "标题")).body)
    }

    @Test
    fun fencedIndentedAndInlineCodeStayLiteral() {
        val original = "````markdown\n#Work\n```\n#阅读\n````\n\n~~~\n#阅读\n~~~\n\n    #Work\n\t#阅读\n\n`#Work` ``a ` #阅读`` \\#Work \\\\#阅读\n\n#Work"
        val expected = original.removeSuffix("\n#Work")
        assertEquals(expected, notePresentation(original, listOf("Work", "阅读")).body)
    }

    @Test
    fun linksImagesReferencesURLsAndHTMLStayLiteral() {
        val original = """
            [#Work 标签](https://example.com/#阅读 "#Work")
            ![#阅读 图片](/file/attachments/a/#Work)
            [#Work][ref]
            ![#阅读][ref]

            [ref]: https://example.com/#阅读 "#Work"

            https://example.com/#Work www.example.com/#阅读 <https://example.com/#Work>
            <span title="#Work"> #阅读 </span>
            <!-- #Work -->

            #Work
        """.trimIndent()
        val expected = original.removeSuffix("\n#Work")
        assertEquals(expected, notePresentation(original, listOf("Work", "阅读")).body)
    }

    @Test
    fun tagOnlyBlocksDisappearWithoutChangingOtherWhitespace() {
        assertEquals("", notePresentation("\n#阅读 #Work\n\n", listOf("阅读", "Work")).body)
        assertEquals("前文\n\n后文", notePresentation("前文\n\n#阅读\n\n#Work\n\n后文", listOf("阅读", "Work")).body)
        assertEquals("前文\n后文", notePresentation("前文\n#阅读\n后文", listOf("阅读")).body)
        assertEquals("正文", notePresentation("#阅读\r\n \r\n正文", listOf("阅读")).body)
        assertEquals("前文\r\n\r\n后文", notePresentation("前文\r\n\r\n#阅读\r\n\r\n后文", listOf("阅读")).body)
        val literal = "```\n\n\n#阅读\n\n```\n\n\n没有标签"
        assertEquals(literal, notePresentation(literal, listOf("阅读")).body)
    }

    @Test
    fun localTagsSkipCodeLinksEscapesAndMarkdownHeadings() {
        val literal = """
            # 一级标题
            ## 二级标题

            `#代码` [#链接](https://example.com/#锚点) \#转义

            ```
            #代码块
            ```

            https://example.com/#网址
        """.trimIndent()
        val result = localNotePresentation("$literal\n\n正文 #阅读 #工作/项目 #阅读")
        assertEquals(listOf("阅读", "工作/项目"), result.inlineTags)
        assertEquals("$literal\n\n正文 #阅读 #工作/项目 #阅读", result.body)
    }

    @Test
    fun localTagsRespectPunctuationAndLiteralHashBoundaries() {
        val result = localNotePresentation("abc#正文 #阅读，(#工作) \\#字面 #未完成#相邻")
        assertEquals(listOf("阅读", "工作"), result.inlineTags)
        assertEquals("abc#正文 #阅读，(#工作) \\#字面 #未完成#相邻", result.body)
    }

    @Test
    fun initialEditorSelectionPlacesCursorBeforeTrailingTags() {
        assertEquals(2, initialEditorSelection("正文 #标签"))
        assertEquals(3, initialEditorSelection("第一行\n\n#tag1 #tag2"))
        assertEquals("多行\n正文".length, initialEditorSelection("多行\n正文 #a #b   "))
        assertEquals("#只有标签".length, initialEditorSelection("#只有标签"))
        assertEquals("没有标签".length, initialEditorSelection("没有标签"))
        assertEquals("正文 `inline #code`".length, initialEditorSelection("正文 `inline #code`"))
    }

    @Test
    fun splitMarkdownBlocksPreservesOpenAndClosedFencesAndIndentedLists() {
        assertEquals(listOf("# 标题", "第一段", "第二段"), splitMarkdownBlocks("# 标题\n\n第一段\n\n第二段"))
        assertEquals(
            listOf("前缀", "```kotlin\nval a = 1\n\nval b = 2", "结尾"),
            splitMarkdownBlocks("前缀\n\n```kotlin\nval a = 1\n\nval b = 2\n\n```\n\n结尾".replace("\n\n```\n\n结尾", ""))
                .plus("结尾"),
        )
        assertEquals(
            listOf("前缀", "```kotlin\nval a = 1\n\nval b = 2"),
            splitMarkdownBlocks("前缀\n\n```kotlin\nval a = 1\n\nval b = 2"),
        )
        assertEquals(
            listOf("前缀", "```kotlin\nval a = 1\n\nval b = 2\n```", "结尾"),
            splitMarkdownBlocks("前缀\n\n```kotlin\nval a = 1\n\nval b = 2\n```\n\n结尾"),
        )
        assertEquals(
            listOf("- 列表项\n\n    - 缩进子项"),
            splitMarkdownBlocks("- 列表项\n\n    - 缩进子项"),
        )
    }

    @Test
    fun listBoundariesPreserveEmptyItemsIndentedContinuationsAndCode() {
        val cases = mapOf(
            "- 无序\n1.有序\n2、普通" to listOf("- 无序", "1.有序\n2、普通"),
            "1.有序\n- 无序\n2、普通" to listOf("1.有序", "- 无序", "2、普通"),
            "1. 甲\n2.\n3. 丙\n正文" to listOf("1. 甲\n2.\n3. 丙", "正文"),
            "- `code\nnext`\n正文" to listOf("- `code\nnext`", "正文"),
            "- ``code\n`literal`\nnext``\n正文" to listOf("- ``code\n`literal`\nnext``", "正文"),
            "- 未闭合 `\n1.有序列表" to listOf("- 未闭合 `", "1.有序列表"),
            "- 未闭合 `\n1.有序列表\n# 标题 `" to listOf("- 未闭合 `", "1.有序列表\n# 标题 `"),
            "- 未闭合 `\n1.有序列表\n> 引用 `" to listOf("- 未闭合 `", "1.有序列表\n> 引用 `"),
            "- `代码\n续行`\n1.有序列表\n# 标题 `" to listOf("- `代码\n续行`", "1.有序列表\n# 标题 `"),
            "- 未闭合 ``\n1.有序列表 `单反引号`" to listOf("- 未闭合 ``", "1.有序列表 `单反引号`"),
            "- 未闭合 `\n\n正文`" to listOf("- 未闭合 `", "正文`"),
            "<pre>\n- 原样文本\n\n1. 原样编号\n</pre>\n\n- 真正列表" to listOf("<pre>\n- 原样文本\n\n1. 原样编号\n</pre>", "- 真正列表"),
            "段落 <pre>\n- 真正列表\n顶格正文" to listOf("段落 <pre>", "- 真正列表", "顶格正文"),
            "- 甲\n  - 乙\n  显式续行\n\t续行\n正文 #日记" to listOf("- 甲\n  - 乙\n  显式续行\n\t续行", "正文 #日记"),
            "```text\n- 字面列表\n顶格正文\n```\n\n1) 列表\n正文" to listOf("```text\n- 字面列表\n顶格正文\n```", "1) 列表", "正文"),
        )
        cases.forEach { (source, blocks) -> assertEquals(blocks, splitMarkdownBlocks(source)) }
    }

    @Test
    fun walkSummariesStripOnlyConfirmedTrailingTagsAndPairedDecoration() {
        val content = "# 标题\n> **正文** __强调__ *斜体* ~~删除~~ snake_case\n- `#日记`\n\n```\n#日记\n```\n\n#日记"
        assertEquals("标题 正文 强调 斜体 删除 snake_case #日记 #日记", walkSummaryBody(content, listOf("日记")))
        assertEquals("猫 #未知", walkSummaryBody("猫\n#未知", emptyList()))
        assertEquals("首行 #日记 后续", walkSummaryBody("首行\n#日记\n后续", listOf("日记")))
        assertEquals("未配对 **星号 和 snake_case", walkSummaryBody("未配对 **星号 和 snake_case", emptyList()))
        assertEquals("😀".repeat(64) + "…", limitWalkSummary("😀".repeat(65)))
        assertEquals("😀".repeat(64), limitWalkSummary("😀".repeat(64)))
    }

    @Test
    fun walkSummaryFoldsUnicodeWhitespace() {
        assertEquals("甲 乙 丙 丁", walkSummaryBody("\u3000甲\u00a0乙\t丙\n丁\u3000", emptyList()))
    }

    @Test
    fun productionRegularExpressionsUseAndroidCompatibleFlags() {
        val mobile = generateSequence(java.io.File(System.getProperty("user.dir"))) { it.parentFile }
            .first { it.name == "mobile" }
        val literal = Regex("""\b(?:Regex|Pattern\s*\.\s*compile)\s*\(\s*("{3}[\s\S]*?"{3}|"(?:\\.|[^"\\])*")""")
        val unsupportedFlag = Regex("""(?<!\\)\(\?[idmsuxU-]*U[idmsuxU-]*(?:[:)])""")
        val matches = mobile.walkTopDown().onEnter { it.name !in setOf("build", ".gradle", ".kotlin", "test", "androidTest") }
            .filter { it.extension == "kt" && "/src/main/" in it.invariantSeparatorsPath }
            .flatMap { file ->
                literal.findAll(file.readText()).filter { unsupportedFlag.containsMatchIn(it.groupValues[1]) }
                    .map { "${file.relativeTo(mobile)}: ${it.value}" }
            }.toList()
        assertTrue(matches.isEmpty(), matches.joinToString("\n"))
    }

    @Test
    fun sharedWalkSummariesPreserveCodeAndLiteralDelimiters() {
        val source = requireNotNull(javaClass.getResourceAsStream("/walk-summary.json"))
        val cases = source.bufferedReader().use { Json.parseToJsonElement(it.readText()).jsonArray }
        for (value in cases) {
            val sample = value.jsonObject
            fun text(key: String) = sample.getValue(key).jsonPrimitive.content
            val tags = sample.getValue("tags").jsonArray.map { it.jsonPrimitive.content }
            assertEquals(text("summary"), walkSummaryBody(text("content"), tags), text("name"))
        }
    }

    @Test
    fun walkSummaryFenceOffsetsAreMeasuredAgainstTheCroppedBody() {
        val input = "```\nx\n```\n#a\n\n"
        val (body, tree) = walkSummarySource(input, listOf("a"))
        assertEquals("```\nx\n```\n\n", body)
        val root = requireNotNull(tree)
        assertEquals(body.length, root.endOffset)
        fun checkOffsets(node: ASTNode) {
            assertTrue(node.startOffset in 0..node.endOffset && node.endOffset <= body.length)
            if (node.type == MarkdownTokenTypes.CODE_FENCE_CONTENT) assertEquals("x", body.substring(node.startOffset, node.endOffset))
            node.children.forEach(::checkOffsets)
        }
        checkOffsets(root)
        assertEquals("x", walkSummaryBody(input, listOf("a")))
    }

    @Test
    fun summaryScansALongLineOfUnclosedDelimitersWithinTheLocalBudget() {
        val body = "**word ".repeat(6900) + "end"
        val elapsed = measureTime { assertEquals(body, walkSummaryBody("$body\n#日记", listOf("日记"))) }
        assertTrue(elapsed < 1.seconds, "48 KB summary took $elapsed")
    }

    @Test
    fun walkStartingPriorityUsesCleanedBodyBeforeAttachmentFallback() {
        val fixture = listOf("", "短句", "# 笔记有足够正文可以作为起点", "#日记", "另外一篇正文也可以作为起点", "")
        val selected = fixture.mapIndexedNotNull { index, raw ->
            walkStartPriority(walkSummaryBody(raw, listOf("日记")), hasAttachment = index < 5)?.let { it to index }
        }.sortedBy { it.first }.map { it.second }
        assertEquals(listOf(2, 4, 1, 0, 3), selected)
    }
}

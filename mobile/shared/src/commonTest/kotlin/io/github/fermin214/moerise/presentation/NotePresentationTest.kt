package io.github.fermin214.moerise.presentation

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class NotePresentationTest {
    @Test
    fun knownTagsOnlyAndMetadataOrder() {
        val tags = listOf("阅读", "Work", "阅读", "Work/Child", "Work")
        val original = "#阅读 #未知 #Work/Child #WorkLong #Work-long #Work_suffix #Work/Other"
        val result = notePresentation(original, tags)
        assertEquals(" #未知  #WorkLong #Work-long #Work_suffix #Work/Other", result.body)
        assertEquals(listOf("阅读", "Work", "Work/Child"), result.tags)
        assertEquals("#阅读 #未知 #Work/Child #WorkLong #Work-long #Work_suffix #Work/Other", original)
        assertEquals(listOf("阅读", "Work", "阅读", "Work/Child", "Work"), tags)
        assertEquals(original, notePresentation(original, emptyList()).body)
    }

    @Test
    fun boundariesAndMarkdownHeadings() {
        val original = "# 阅读\n## 标题\n### Work\n\n#Work 标记，#阅读。 (#Work)；“#阅读” abc#Work #WorkLong #Work/Child\n# Work"
        assertEquals("# 阅读\n## 标题\n### Work\n\n 标记，。 ()；“” abc#Work #WorkLong #Work/Child\n# Work",
            notePresentation(original, listOf("阅读", "Work")).body)
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
        val expected = original.removeSuffix("\n#Work").replace("> #阅读 <", ">  <")
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
        assertEquals(listOf("阅读", "工作/项目"), result.tags)
        assertEquals("$literal\n\n正文   ", result.body)
    }

    @Test
    fun localTagsRespectPunctuationAndLiteralHashBoundaries() {
        val result = localNotePresentation("abc#正文 #阅读，(#工作) \\#字面 #未完成#相邻")
        assertEquals(listOf("阅读", "工作"), result.tags)
        assertEquals("abc#正文 ，() \\#字面 #未完成#相邻", result.body)
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
    fun extractReferenceDefinitionsCollectsCrossBlockLinksAndIgnoresFences() {
        val sample = """
            请看[参考][doc]。

            ```markdown
            [fake]: https://fake.example
            ```

            [doc]: https://notes.example/guide "指南"
        """.trimIndent()
        val defs = extractReferenceDefinitions(
            splitMarkdownBlocks(sample).filter { it.contains("]:") }.joinToString("\n\n"),
        )
        assertTrue(defs.values.contains("https://notes.example/guide"))
        assertFalse(defs.values.contains("https://fake.example"))
    }
}

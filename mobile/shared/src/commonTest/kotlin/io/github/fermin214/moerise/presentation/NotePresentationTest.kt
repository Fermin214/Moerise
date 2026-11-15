package io.github.fermin214.moerise.presentation

import kotlin.test.Test
import kotlin.test.assertEquals

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
}

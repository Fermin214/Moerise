package io.github.fermin214.moerise.presentation

import kotlin.test.*

class MarkdownReferenceTest {
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

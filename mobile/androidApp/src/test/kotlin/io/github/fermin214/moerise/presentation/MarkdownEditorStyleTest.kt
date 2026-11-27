package io.github.fermin214.moerise.presentation

import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.OffsetMapping
import androidx.compose.ui.text.input.TextFieldValue
import kotlin.test.*

class MarkdownEditorStyleTest {
    @Test
    fun visualStylesKeepTextSelectionAndChineseCompositionOffsets() {
        val original = TextFieldValue("## 标题\n**你好**nihao #标签", TextRange(17, 15), TextRange(12, 17))
        val styled = markdownEditorTransformation(original.text).filter(original.annotatedString)
        assertEquals(original.text, styled.text.text)
        assertTrue(styled.text.spanStyles.isNotEmpty())
        assertSame(OffsetMapping.Identity, styled.offsetMapping)
        assertEquals(original.selection, TextRange(styled.offsetMapping.originalToTransformed(original.selection.start),
            styled.offsetMapping.originalToTransformed(original.selection.end)))
        val composing = assertNotNull(original.composition)
        assertEquals(composing, TextRange(styled.offsetMapping.transformedToOriginal(composing.start),
            styled.offsetMapping.transformedToOriginal(composing.end)))
    }
}

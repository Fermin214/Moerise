package io.github.fermin214.moerise.presentation

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.OffsetMapping
import androidx.compose.ui.text.input.TransformedText
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.sp
import io.github.fermin214.moerise.presentation.MarkdownHighlightStyle.*

internal fun markdownEditorTransformation(text: String): VisualTransformation {
    val spans = markdownHighlightSpans(text)
    return VisualTransformation { original ->
        val highlighted = if (original.text == text && spans.isNotEmpty()) buildAnnotatedString {
            append(original)
            for ((start, end, style) in spans) {
                addStyle(when (style) {
                    Heading1, Heading2, Heading3, Heading4, Heading5, Heading6 ->
                        SpanStyle(fontWeight = FontWeight.Bold, fontSize = (22 - style.ordinal).coerceAtLeast(17).sp)
                    SyntaxMarker -> SpanStyle(color = Color(0xFF9299A3))
                    ListPrefix, QuotePrefix -> SpanStyle(color = Color(0xFF657D9F))
                    Bold -> SpanStyle(fontWeight = FontWeight.Bold)
                    Italic -> SpanStyle(fontStyle = FontStyle.Italic)
                    Strike -> SpanStyle(textDecoration = TextDecoration.LineThrough)
                    InlineCode, CodeFence -> SpanStyle(fontFamily = FontFamily.Monospace, color = MoeriseColors.Muted)
                    LinkLabel -> SpanStyle(color = MoeriseColors.Blue, textDecoration = TextDecoration.Underline)
                    LinkMeta -> SpanStyle(color = MoeriseColors.Muted)
                    Tag -> SpanStyle(color = Color(0xFF2A5BB5))
                }, start, end)
            }
        } else original
        TransformedText(highlighted, OffsetMapping.Identity)
    }
}

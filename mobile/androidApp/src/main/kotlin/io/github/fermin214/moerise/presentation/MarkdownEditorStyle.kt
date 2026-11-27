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
                    Heading1 -> SpanStyle(fontWeight = FontWeight.SemiBold, fontSize = 20.sp)
                    Heading2, Heading3, Heading4 -> SpanStyle(fontWeight = FontWeight.SemiBold, fontSize = 17.sp)
                    Heading5, Heading6 -> SpanStyle(fontWeight = FontWeight.Bold, fontSize = 15.sp)
                    SyntaxMarker -> SpanStyle(color = MoeriseColors.Muted)
                    ListPrefix, QuotePrefix -> SpanStyle(color = MoeriseColors.Muted)
                    Bold -> SpanStyle(fontWeight = FontWeight.Bold)
                    Italic -> SpanStyle(fontStyle = FontStyle.Italic)
                    Strike -> SpanStyle(textDecoration = TextDecoration.LineThrough)
                    InlineCode, CodeFence -> SpanStyle(fontFamily = FontFamily.Monospace, color = MoeriseColors.Muted)
                    LinkLabel -> SpanStyle(color = MoeriseColors.Accent, textDecoration = TextDecoration.Underline)
                    LinkMeta -> SpanStyle(color = MoeriseColors.Muted)
                    Tag -> SpanStyle(color = MoeriseColors.Accent)
                }, start, end)
            }
        } else original
        TransformedText(highlighted, OffsetMapping.Identity)
    }
}

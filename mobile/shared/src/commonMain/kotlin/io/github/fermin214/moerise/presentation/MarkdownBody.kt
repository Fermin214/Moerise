package io.github.fermin214.moerise.presentation

import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import com.mikepenz.markdown.m3.Markdown

@Composable
fun MarkdownBody(content: String, modifier: Modifier = Modifier) {
    Markdown(content = content, modifier = modifier)
}

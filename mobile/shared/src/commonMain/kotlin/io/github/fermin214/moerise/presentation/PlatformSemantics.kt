package io.github.fermin214.moerise.presentation

import androidx.compose.ui.Modifier
import androidx.compose.runtime.Composable

expect fun Modifier.platformTestTags(): Modifier

@Composable
expect fun platformImeVisible(): Boolean

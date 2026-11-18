package io.github.fermin214.moerise.presentation

import androidx.compose.ui.Modifier
import androidx.compose.runtime.Composable

actual fun Modifier.platformTestTags(): Modifier = this

@Composable
actual fun platformImeVisible(): Boolean = false

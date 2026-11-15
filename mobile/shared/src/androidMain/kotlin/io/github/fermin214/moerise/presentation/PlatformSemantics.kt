package io.github.fermin214.moerise.presentation

import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.testTagsAsResourceId

actual fun Modifier.platformTestTags(): Modifier = semantics { testTagsAsResourceId = true }

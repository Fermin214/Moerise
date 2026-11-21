package io.github.fermin214.moerise.presentation

import androidx.compose.ui.Modifier
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.isImeVisible
import androidx.compose.runtime.Composable
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.testTagsAsResourceId

fun Modifier.platformTestTags(): Modifier = semantics { testTagsAsResourceId = true }

@OptIn(ExperimentalLayoutApi::class)
@Composable
fun platformImeVisible(): Boolean = WindowInsets.isImeVisible

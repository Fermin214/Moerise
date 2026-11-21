package io.github.fermin214.moerise.presentation

import androidx.activity.compose.BackHandler
import androidx.compose.runtime.Composable

@Composable
fun PlatformBackHandler(enabled: Boolean, onBack: () -> Unit) = BackHandler(enabled, onBack)

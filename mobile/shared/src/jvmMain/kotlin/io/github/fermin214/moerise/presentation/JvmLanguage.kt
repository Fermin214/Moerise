package io.github.fermin214.moerise.presentation

import java.util.Locale

actual fun systemLanguageTag(): String = Locale.getDefault().toLanguageTag()

@androidx.compose.runtime.Composable
actual fun PlatformLanguage(language: UiLanguage, content: @androidx.compose.runtime.Composable () -> Unit) = content()

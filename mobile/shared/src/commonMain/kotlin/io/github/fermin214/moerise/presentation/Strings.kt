package io.github.fermin214.moerise.presentation

enum class UiLanguage { SYSTEM, CHINESE, ENGLISH }

expect fun systemLanguageTag(): String
@androidx.compose.runtime.Composable
expect fun PlatformLanguage(language: UiLanguage, content: @androidx.compose.runtime.Composable () -> Unit)

class Strings(language: UiLanguage) {
    private val chinese = language == UiLanguage.CHINESE ||
        language == UiLanguage.SYSTEM && systemLanguageTag().startsWith("zh", ignoreCase = true)
    fun text(chinese: String, english: String): String = if (this.chinese) chinese else english
}

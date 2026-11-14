package io.github.fermin214.moerise.presentation

enum class UiLanguage { SYSTEM, CHINESE, ENGLISH }

expect fun systemLanguageTag(): String

class Strings(language: UiLanguage) {
    private val chinese = language == UiLanguage.CHINESE ||
        language == UiLanguage.SYSTEM && systemLanguageTag().startsWith("zh", ignoreCase = true)
    fun text(chinese: String, english: String): String = if (this.chinese) chinese else english
}

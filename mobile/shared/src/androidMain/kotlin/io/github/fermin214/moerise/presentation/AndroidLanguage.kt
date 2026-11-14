package io.github.fermin214.moerise.presentation

import java.util.Locale

actual fun systemLanguageTag(): String = Locale.getDefault().toLanguageTag()

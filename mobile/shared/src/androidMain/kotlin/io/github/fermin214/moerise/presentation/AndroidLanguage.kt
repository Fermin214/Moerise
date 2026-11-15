package io.github.fermin214.moerise.presentation

import java.util.Locale
import android.content.ContextWrapper
import android.content.res.Configuration
import android.os.LocaleList
import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext

actual fun systemLanguageTag(): String = Locale.getDefault().toLanguageTag()

@Composable
actual fun PlatformLanguage(language: UiLanguage, content: @Composable () -> Unit) {
    val context = LocalContext.current
    val system = LocalConfiguration.current
    val configuration = remember(system, language) {
        Configuration(system).apply {
            when (language) {
                UiLanguage.CHINESE -> setLocales(LocaleList(Locale.SIMPLIFIED_CHINESE))
                UiLanguage.ENGLISH -> setLocales(LocaleList(Locale.ENGLISH))
                UiLanguage.SYSTEM -> Unit
            }
        }
    }
    val localized = remember(context, configuration) {
        val resources = context.createConfigurationContext(configuration).resources
        object : ContextWrapper(context) { override fun getResources() = resources }
    }
    CompositionLocalProvider(LocalConfiguration provides configuration, LocalContext provides localized, content = content)
}

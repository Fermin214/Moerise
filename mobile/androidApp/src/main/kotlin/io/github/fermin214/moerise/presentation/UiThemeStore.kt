package io.github.fermin214.moerise.presentation

import android.app.UiModeManager
import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import android.os.Build

enum class UiTheme {
    SYSTEM, LIGHT, DARK;

    fun isDark(systemDark: Boolean): Boolean = this == DARK || this == SYSTEM && systemDark
}

interface UiThemeStore {
    fun read(): UiTheme
    fun write(theme: UiTheme)
}

internal class MemoryUiThemeStore(private var theme: UiTheme = UiTheme.SYSTEM) : UiThemeStore {
    override fun read() = theme
    override fun write(theme: UiTheme) { this.theme = theme }
}

internal class AndroidUiThemeStore(context: Context) : UiThemeStore {
    private val preferences = context.getSharedPreferences("ui-preferences", Context.MODE_PRIVATE)

    override fun read() = preferences.getString("ui-theme", null)?.let { saved -> UiTheme.entries.find { it.name == saved } }
        ?: UiTheme.SYSTEM

    override fun write(theme: UiTheme) {
        check(preferences.edit().putString("ui-theme", theme.name).commit())
    }
}

internal fun Configuration.isDarkMode() = uiMode and Configuration.UI_MODE_NIGHT_MASK == Configuration.UI_MODE_NIGHT_YES

internal fun UiTheme.resolveDark(context: Context, configuration: Configuration, useSystemSetting: Boolean = false): Boolean {
    if (this != UiTheme.SYSTEM || !useSystemSetting || Build.VERSION.SDK_INT < 31) return isDark(configuration.isDarkMode())
    return when (context.getSystemService(UiModeManager::class.java).nightMode) {
        UiModeManager.MODE_NIGHT_YES -> true
        UiModeManager.MODE_NIGHT_NO -> false
        else -> Resources.getSystem().configuration.isDarkMode()
    }
}

internal fun applyApplicationNightMode(context: Context, theme: UiTheme) {
    if (Build.VERSION.SDK_INT >= 31) {
        context.getSystemService(UiModeManager::class.java).setApplicationNightMode(when (theme) {
            UiTheme.SYSTEM -> UiModeManager.MODE_NIGHT_AUTO
            UiTheme.LIGHT -> UiModeManager.MODE_NIGHT_NO
            UiTheme.DARK -> UiModeManager.MODE_NIGHT_YES
        })
    }
}

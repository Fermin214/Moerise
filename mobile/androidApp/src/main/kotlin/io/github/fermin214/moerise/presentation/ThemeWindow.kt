package io.github.fermin214.moerise.presentation

import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import android.graphics.drawable.ColorDrawable
import android.view.Window
import androidx.compose.ui.graphics.toArgb
import androidx.core.view.WindowCompat

internal fun Context.activity(): Activity? = when (this) {
    is Activity -> this
    is ContextWrapper -> baseContext.activity()
    else -> null
}

internal fun Window.applyMoeriseTheme(dark: Boolean, dialog: Boolean = false) {
    val palette = if (dark) DarkMoerisePalette else LightMoerisePalette
    val background = if (dialog) android.graphics.Color.TRANSPARENT else palette.Paper.toArgb()
    if ((decorView.background as? ColorDrawable)?.color != background) setBackgroundDrawable(ColorDrawable(background))
    WindowCompat.getInsetsController(this, decorView).apply {
        isAppearanceLightStatusBars = !dark
        isAppearanceLightNavigationBars = !dark
    }
}

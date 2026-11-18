package io.github.fermin214.moerise.presentation

import android.app.Activity
import android.content.ContextWrapper
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.ui.platform.LocalContext
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat

@Composable
actual fun PlatformMapFullscreen(enabled: Boolean) {
    val context = LocalContext.current
    DisposableEffect(context, enabled) {
        var owner = context
        while (owner is ContextWrapper && owner !is Activity) owner = owner.baseContext
        val activity = owner as? Activity
        val controller = activity?.window?.let { WindowCompat.getInsetsController(it, it.decorView) }
        val previousBehavior = controller?.systemBarsBehavior
        if (enabled) {
            controller?.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
            controller?.hide(WindowInsetsCompat.Type.systemBars())
        }
        onDispose {
            if (enabled) {
                controller?.show(WindowInsetsCompat.Type.systemBars())
                if (previousBehavior != null) controller?.systemBarsBehavior = previousBehavior
            }
        }
    }
}

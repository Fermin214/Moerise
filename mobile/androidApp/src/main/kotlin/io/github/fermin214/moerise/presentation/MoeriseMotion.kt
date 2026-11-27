package io.github.fermin214.moerise.presentation

import android.database.ContentObserver
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.view.HapticFeedbackConstants
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.FiniteAnimationSpec
import androidx.compose.animation.core.spring
import androidx.compose.animation.core.tween
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay

internal object MoeriseMotion {
    fun <T> tactile() = spring<T>(dampingRatio = .85f, stiffness = 850f)
    fun <T> responsive() = spring<T>(dampingRatio = .88f, stiffness = 420f)
    fun <T> spatial() = spring<T>(dampingRatio = .92f, stiffness = 260f)
    fun <T> fade() = tween<T>(80)
    val flip = tween<Float>(240, easing = CubicBezierEasing(.22f, 1f, .36f, 1f))
}

internal val LocalReducedMotion = staticCompositionLocalOf { false }
internal val LocalMobileNotice = staticCompositionLocalOf<(String) -> Unit> { {} }

@Composable
internal fun rememberReducedMotion(): Boolean {
    val resolver = LocalView.current.context.contentResolver
    fun reduced() = Settings.Global.getFloat(resolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f
    var value by remember(resolver) { mutableStateOf(reduced()) }
    DisposableEffect(resolver) {
        val observer = object : ContentObserver(Handler(Looper.getMainLooper())) {
            override fun onChange(selfChange: Boolean) { value = reduced() }
        }
        resolver.registerContentObserver(Settings.Global.getUriFor(Settings.Global.ANIMATOR_DURATION_SCALE), false, observer)
        onDispose { resolver.unregisterContentObserver(observer) }
    }
    return value
}

@Composable
internal fun rememberMoeriseHaptic(): () -> Unit {
    val view = LocalView.current
    return remember(view) { { view.performHapticFeedback(HapticFeedbackConstants.CLOCK_TICK); Unit } }
}

@Composable
internal fun Modifier.motionEnter(
    key: Any?, distance: Float = 8f, initialScale: Float = 1f, delayMillis: Long = 0,
    spatial: Boolean = false, animateInitial: Boolean = true, spec: FiniteAnimationSpec<Float>? = null,
): Modifier {
    val reduced = LocalReducedMotion.current
    val appeared = remember { mutableStateOf(false) }
    val progress = remember(key) { Animatable(if (animateInitial || appeared.value) 0f else 1f) }
    SideEffect { appeared.value = true }
    LaunchedEffect(progress, reduced) {
        if (!reduced && delayMillis > 0 && progress.value == 0f) delay(delayMillis)
        progress.animateTo(1f, when {
            reduced -> MoeriseMotion.fade()
            spec != null -> spec
            spatial -> MoeriseMotion.spatial()
            else -> MoeriseMotion.responsive()
        })
    }
    return graphicsLayer {
        alpha = progress.value.coerceIn(0f, 1f)
        if (!reduced) {
            translationY = distance.dp.toPx() * (1 - progress.value)
            scaleX = initialScale + (1 - initialScale) * progress.value
            scaleY = scaleX
        }
    }
}

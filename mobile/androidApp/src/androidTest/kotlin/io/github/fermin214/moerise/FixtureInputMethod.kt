package io.github.fermin214.moerise

import android.app.Activity
import android.app.Application
import android.os.Bundle
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import androidx.activity.compose.setContent
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.platform.InterceptPlatformTextInput
import androidx.compose.ui.platform.PlatformTextInputInterceptor
import androidx.compose.ui.platform.PlatformTextInputMethodRequest
import androidx.compose.ui.platform.PlatformTextInputSession
import androidx.lifecycle.ViewModelProvider
import io.github.fermin214.moerise.presentation.MoeriseApp
import java.util.Collections
import java.util.WeakHashMap
import kotlinx.coroutines.awaitCancellation

/** 保留应用界面和生命周期，仅由测试独占编辑器的输入会话。 */
@OptIn(ExperimentalComposeUiApi::class)
class FixtureInputMethod : Application.ActivityLifecycleCallbacks, PlatformTextInputInterceptor {
    data class Session(val connection: InputConnection, val info: EditorInfo)
    var session: Session? = null
        private set
    private val installed = Collections.newSetFromMap(WeakHashMap<Activity, Boolean>())

    override fun onActivityResumed(activity: Activity) {
        if (activity !is MainActivity || !installed.add(activity)) return
        val model = ViewModelProvider(activity)[AppModel::class.java]
        activity.setContent {
            InterceptPlatformTextInput(this) {
                MoeriseApp(model.mobile)
            }
        }
    }

    override suspend fun interceptStartInputMethod(
        request: PlatformTextInputMethodRequest,
        nextHandler: PlatformTextInputSession,
    ): Nothing {
        val info = EditorInfo()
        val active = Session(request.createInputConnection(info), info)
        session = active
        try {
            awaitCancellation()
        } finally {
            if (session === active) session = null
        }
    }

    override fun onActivityCreated(activity: Activity, state: Bundle?) {}
    override fun onActivityStarted(activity: Activity) {}
    override fun onActivityPaused(activity: Activity) {}
    override fun onActivityStopped(activity: Activity) {}
    override fun onActivitySaveInstanceState(activity: Activity, state: Bundle) {}
    override fun onActivityDestroyed(activity: Activity) { installed.remove(activity) }
}

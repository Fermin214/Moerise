package io.github.fermin214.moerise

import android.app.Activity
import android.app.Application
import android.os.Bundle
import androidx.core.view.doOnPreDraw

class MoeriseApplication : Application() {
    lateinit var runtime: AndroidRuntime
        private set

    override fun onCreate() {
        super.onCreate()
        runtime = AndroidRuntime(this)
        registerActivityLifecycleCallbacks(object : Application.ActivityLifecycleCallbacks {
            private val started = mutableSetOf<Activity>()

            override fun onActivityStarted(activity: Activity) {
                if (started.add(activity) && started.size == 1) activity.window.decorView.doOnPreDraw {
                    activity.window.decorView.post {
                        if (activity in started) runtime.foreground(true)
                    }
                }
            }

            override fun onActivityStopped(activity: Activity) {
                if (started.remove(activity) && started.isEmpty()) runtime.foreground(false)
            }

            override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) {}
            override fun onActivityResumed(activity: Activity) {}
            override fun onActivityPaused(activity: Activity) {}
            override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) {}
            override fun onActivityDestroyed(activity: Activity) {}
        })
    }
}

package io.github.fermin214.moerise

import android.app.ActivityManager
import android.app.ApplicationExitInfo
import android.content.Context
import android.os.Build
import io.github.fermin214.moerise.presentation.InstallPreferences

class AndroidInstallPreferences(private val context: Context) : InstallPreferences {
    private val preferences = context.getSharedPreferences("usage-privacy", Context.MODE_PRIVATE)
    override val clientVersion: String get() = BuildConfig.VERSION_NAME
    override val systemMajor: String get() = Build.VERSION.SDK_INT.toString()
    override fun read(key: String): String? = preferences.getString(key, null)
    override fun write(key: String, value: String?) {
        preferences.edit().putString(key, value).apply()
    }
    override fun processExits(since: Long): List<Pair<Long, String>> {
        if (Build.VERSION.SDK_INT < 30) return emptyList()
        return runCatching {
            context.getSystemService(ActivityManager::class.java)
                ?.getHistoricalProcessExitReasons(context.packageName, 0, 64).orEmpty().mapNotNull { exit ->
                    if (exit.timestamp <= since) null
                    else when (exit.reason) {
                        ApplicationExitInfo.REASON_CRASH, ApplicationExitInfo.REASON_CRASH_NATIVE -> exit.timestamp to "crash"
                        ApplicationExitInfo.REASON_ANR -> exit.timestamp to "anr"
                        else -> null
                    }
                }
        }.getOrDefault(emptyList())
    }
}

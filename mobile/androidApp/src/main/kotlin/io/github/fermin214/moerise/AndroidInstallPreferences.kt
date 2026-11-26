package io.github.fermin214.moerise

import android.app.ActivityManager
import android.app.ApplicationExitInfo
import android.content.Context
import android.os.Build
import io.github.fermin214.moerise.presentation.InstallPreferences
import java.io.File
import java.nio.file.Files

class AndroidInstallPreferences(private val context: Context) : InstallPreferences {
    private val preferences = context.getSharedPreferences("usage-privacy", Context.MODE_PRIVATE)
    private val withdrawal = File(context.noBackupFilesDir, "usage-consent-withdrawn")
    override val clientVersion: String get() = BuildConfig.VERSION_NAME
    override val systemMajor: String get() = Build.VERSION.SDK_INT.toString()
    override val systemVersion: String get() = Build.VERSION.RELEASE
    override fun read(key: String): String? = preferences.getString(key, null)
    override fun write(key: String, value: String?) = preferences.edit().putString(key, value).apply()
    override fun writeBatch(vararg entries: Pair<String, String?>) = edit(entries).apply()
    override fun persist(vararg entries: Pair<String, String?>) {
        check(edit(entries).commit())
    }
    private fun edit(entries: Array<out Pair<String, String?>>) = preferences.edit().also { editor ->
        entries.forEach { (key, value) -> editor.putString(key, value) }
    }
    override fun consentWithdrawn(): Boolean = withdrawal.exists()
    override fun markConsentWithdrawn(withdrawn: Boolean) {
        if (withdrawn) check(withdrawal.createNewFile() || withdrawal.isFile)
        else Files.deleteIfExists(withdrawal.toPath())
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

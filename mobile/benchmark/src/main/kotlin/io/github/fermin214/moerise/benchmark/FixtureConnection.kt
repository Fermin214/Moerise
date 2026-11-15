package io.github.fermin214.moerise.benchmark

import android.content.ComponentName
import android.content.Intent
import androidx.benchmark.macro.MacrobenchmarkScope
import androidx.test.uiautomator.By
import androidx.test.uiautomator.Until
import org.json.JSONObject

const val targetPackage = "io.github.fermin214.moerise.benchmark"

private fun homeIntent() = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
    .setComponent(ComponentName(targetPackage, "io.github.fermin214.moerise.MainActivity"))
    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_RESET_TASK_IF_NEEDED)

fun MacrobenchmarkScope.home() {
    startActivityAndWait(homeIntent())
    check(device.wait(Until.hasObject(By.res("note_editor")), 20_000)) { "编辑器未就绪" }
    check(device.findObject(By.res("note_editor")).isEnabled)
}

fun MacrobenchmarkScope.configure() {
    startActivityAndWait(homeIntent())
    if (device.wait(Until.hasObject(By.res("server_origin")), 3_000)) {
        val config = JSONObject(device.executeShellCommand("cat /data/local/tmp/moerise-android-fixture.json"))
        device.findObject(By.res("server_origin")).text = config.getString("origin")
        device.findObject(By.res("server_username")).text = config.getString("username")
        device.findObject(By.res("server_password")).text = config.getString("password")
        device.findObject(By.res("server_login")).click()
    }
    check(device.wait(Until.hasObject(By.res("note_editor")), 20_000)) { "先为性能包配置隔离账号" }
}

fun MacrobenchmarkScope.scene(points: Int, photo: Boolean) {
    startActivityAndWait(Intent().setComponent(ComponentName(targetPackage, "io.github.fermin214.moerise.StarBenchmarkActivity"))
        .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
        .putExtra("points", points).putExtra("photo", photo))
    check(device.wait(Until.hasObject(By.desc("scene_ready_1")), 20_000)) { "固定星图没有完成绘制" }
}

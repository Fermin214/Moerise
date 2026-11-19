package io.github.fermin214.moerise.benchmark

import android.content.ComponentName
import android.content.Intent
import androidx.benchmark.macro.MacrobenchmarkScope
import androidx.test.uiautomator.By
import androidx.test.uiautomator.Until
import java.util.regex.Pattern
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
    check(device.wait(Until.hasObject(By.res(Pattern.compile("welcome_enter|server_login|note_editor"))), 20_000))
    if (device.hasObject(By.res("welcome_enter"))) {
        check(!device.findObject(By.res("privacy_share")).isChecked)
        device.findObject(By.res("welcome_enter")).click()
    }
    check(device.wait(Until.hasObject(By.res(Pattern.compile("server_login|note_editor"))), 20_000)) {
        "登录表单或顶部新建输入器未就绪"
    }
    if (device.hasObject(By.res("server_login"))) {
        val config = JSONObject(device.executeShellCommand("cat /data/local/tmp/moerise-android-fixture.json"))
        if (!device.hasObject(By.res("server_origin"))) {
            device.findObject(By.res("server_change")).click()
            check(device.wait(Until.hasObject(By.res("server_origin")), 3_000)) { "服务地址输入未展开" }
        }
        device.findObject(By.res("server_origin")).text = config.getString("origin")
        device.findObject(By.res("server_username")).text = config.getString("username")
        device.findObject(By.res("server_password")).text = config.getString("password")
        device.findObject(By.res("server_login")).click()
    }
    check(device.wait(Until.hasObject(By.res("note_editor")), 20_000)) { "先为性能包配置隔离账号" }
    startActivityAndWait(Intent().setComponent(ComponentName(targetPackage, "io.github.fermin214.moerise.PrepareBenchmarkActivity"))
        .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    check(device.wait(Until.hasObject(By.desc(Pattern.compile(
        "benchmark_cache_ready_20_65536_10|benchmark_cache_failed"))), 75_000)) {
        "启动缓存准备超过 75 秒；查看 MoeriseBenchmark 日志"
    }
    check(device.hasObject(By.desc("benchmark_cache_ready_20_65536_10"))) {
        "启动缓存准备失败；查看 MoeriseBenchmark 日志中的 fresh/loading/notes/error"
    }
    // 返回主页，避免后台返回场景重新启动准备页面。
    device.pressBack()
    home()
}

fun MacrobenchmarkScope.scene(points: Int, photo: Boolean) {
    startActivityAndWait(Intent().setComponent(ComponentName(targetPackage, "io.github.fermin214.moerise.StarBenchmarkActivity"))
        .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
        .putExtra("points", points).putExtra("photo", photo))
    check(device.wait(Until.hasObject(By.desc("scene_ready_1")), 20_000)) { "固定星图没有完成绘制" }
}

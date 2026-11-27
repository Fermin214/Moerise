package io.github.fermin214.moerise.benchmark

import androidx.benchmark.macro.junit4.BaselineProfileRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.uiautomator.By
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class BaselineProfileGenerator {
    @get:Rule val profile = BaselineProfileRule()

    @Test fun editorStartup() = profile.collect(
        packageName = targetPackage, includeInStartupProfile = true,
        filterPredicate = { it.contains("Lio/github/fermin214/moerise/") ||
            it.contains("Landroidx/compose/") || it.contains("Landroidx/room/") ||
            it.contains("Lkotlinx/coroutines/") },
    ) {
        configure()
        pressHome()
        home()
        openComposer()
        device.findObject(By.res("quick_editor")).text = "Baseline profile #android"
        device.findObject(By.res("quick_cancel")).click()
    }

    @Test fun starRendering() = profile.collect(
        packageName = targetPackage,
        filterPredicate = { it.contains("Lio/github/fermin214/moerise/") || it.contains("Landroidx/compose/") },
    ) {
        scene(1000, false)
        scene(5000, true)
        device.findObject(By.desc("scene_canvas")).pinchOpen(.4f)
    }
}

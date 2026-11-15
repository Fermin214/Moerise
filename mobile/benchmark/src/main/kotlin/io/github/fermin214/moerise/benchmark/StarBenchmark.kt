package io.github.fermin214.moerise.benchmark

import androidx.benchmark.macro.*
import androidx.benchmark.macro.junit4.MacrobenchmarkRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.uiautomator.By
import androidx.test.uiautomator.Until
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
@OptIn(ExperimentalMetricApi::class)
class StarBenchmark {
    @get:Rule val benchmark = MacrobenchmarkRule()

    private fun firstFrame(points: Int, photo: Boolean) = benchmark.measureRepeated(
        packageName = targetPackage,
        metrics = listOf(StartupTimingMetric(), TraceSectionMetric("star_scene_ready", TraceSectionMetric.Mode.First),
            TraceSectionMetric("star_scene_parse", TraceSectionMetric.Mode.First), FrameTimingMetric()),
        compilationMode = CompilationMode.Partial(BaselineProfileMode.Require),
        startupMode = StartupMode.COLD, iterations = 50,
        setupBlock = { pressHome(); killProcess() },
    ) { scene(points, photo) }

    private fun gestures(points: Int, photo: Boolean) = benchmark.measureRepeated(
        packageName = targetPackage, metrics = listOf(FrameTimingMetric()),
        compilationMode = CompilationMode.Partial(BaselineProfileMode.Require),
        iterations = 10,
        setupBlock = { scene(points, photo) },
    ) {
        val width = device.displayWidth
        val height = device.displayHeight
        repeat(3) {
            device.swipe(width / 4, height / 2, width * 3 / 4, height / 2, 30)
            device.swipe(width * 3 / 4, height / 2, width / 4, height / 2, 30)
            device.findObject(By.desc("scene_canvas")).pinchOpen(.5f)
            device.findObject(By.desc("scene_canvas")).pinchClose(.5f)
        }
        device.findObject(By.desc("scene_closeup")).click()
        device.findObject(By.desc("scene_summaries")).click()
        device.waitForIdle()
        device.findObject(By.desc("scene_refresh")).click()
        check(device.wait(Until.hasObject(By.desc("scene_ready_2")), 20_000))
        device.waitForIdle()
    }

    @Test fun first1000Stars() = firstFrame(1000, false)
    @Test fun first1000Photo() = firstFrame(1000, true)
    @Test fun first5000Stars() = firstFrame(5000, false)
    @Test fun first5000Photo() = firstFrame(5000, true)
    @Test fun gestures1000Stars() = gestures(1000, false)
    @Test fun gestures1000Photo() = gestures(1000, true)
    @Test fun gestures5000Stars() = gestures(5000, false)
    @Test fun gestures5000Photo() = gestures(5000, true)
}

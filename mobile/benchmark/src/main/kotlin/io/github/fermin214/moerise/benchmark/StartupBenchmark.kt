package io.github.fermin214.moerise.benchmark

import androidx.benchmark.macro.*
import androidx.benchmark.macro.junit4.MacrobenchmarkRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.uiautomator.Until
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
@OptIn(ExperimentalMetricApi::class)
class StartupBenchmark {
    @get:Rule val benchmark = MacrobenchmarkRule()

    private fun startup(mode: StartupMode, compilation: CompilationMode) {
        val readyTrace = if (mode == StartupMode.HOT) "note_foreground_ready" else "note_editor_ready"
        var prepared = false
        benchmark.measureRepeated(
            packageName = targetPackage,
            metrics = listOf(StartupTimingMetric(), TraceSectionMetric(readyTrace, TraceSectionMetric.Mode.First)),
            compilationMode = compilation, startupMode = mode, iterations = 50,
            setupBlock = {
                if (!prepared) { configure(); prepared = true } else home()
                pressHome()
                check(device.wait(Until.gone(androidx.test.uiautomator.By.pkg(targetPackage)), 10_000)) {
                    "应用未完成进入后台"
                }
                if (mode == StartupMode.COLD) killProcess()
            },
        ) { home() }
    }

    @Test fun coldWithoutProfile() = startup(StartupMode.COLD, CompilationMode.None())
    @Test fun coldWithProfile() = startup(StartupMode.COLD, CompilationMode.Partial(BaselineProfileMode.Require))
    @Test fun foregroundWithoutProfile() = startup(StartupMode.HOT, CompilationMode.None())
    @Test fun foregroundWithProfile() = startup(StartupMode.HOT, CompilationMode.Partial(BaselineProfileMode.Require))
}

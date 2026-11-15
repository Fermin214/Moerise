package io.github.fermin214.moerise.benchmark

import androidx.benchmark.macro.*
import androidx.benchmark.macro.junit4.MacrobenchmarkRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
@OptIn(ExperimentalMetricApi::class)
class StartupBenchmark {
    @get:Rule val benchmark = MacrobenchmarkRule()

    private fun startup(mode: StartupMode, compilation: CompilationMode) = benchmark.measureRepeated(
        packageName = targetPackage,
        metrics = listOf(StartupTimingMetric(), TraceSectionMetric(
            if (mode == StartupMode.HOT) "note_foreground_ready" else "note_editor_ready",
            TraceSectionMetric.Mode.First)),
        compilationMode = compilation, startupMode = mode, iterations = 50,
        setupBlock = { configure(); pressHome(); if (mode == StartupMode.COLD) killProcess() },
    ) { home() }

    @Test fun coldWithoutProfile() = startup(StartupMode.COLD, CompilationMode.None())
    @Test fun coldWithProfile() = startup(StartupMode.COLD, CompilationMode.Partial(BaselineProfileMode.Require))
    @Test fun foregroundWithoutProfile() = startup(StartupMode.HOT, CompilationMode.None())
    @Test fun foregroundWithProfile() = startup(StartupMode.HOT, CompilationMode.Partial(BaselineProfileMode.Require))
}

package io.github.fermin214.moerise

import androidx.compose.ui.test.*
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assume.assumeNotNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** 由宿主机分阶段断开端口、终止进程和恢复连接，断言用户能观察的结果。 */
@RunWith(AndroidJUnit4::class)
class OfflineQueueTest {
    @get:Rule val compose = fixtureComposeRule()

    private fun waitEditor(expected: String) {
        compose.waitForEditor("note_editor", expected)
    }

    @Test
    fun savedNotesAndUnfinishedInputSurviveOfflineRestart() {
        val arguments = InstrumentationRegistry.getArguments()
        val phase = arguments.getString("recoveryPhase")
        val marker = arguments.getString("markerId")
        assumeNotNull(phase, marker)
        require(InstrumentationRegistry.getInstrumentation().targetContext.packageName ==
            "io.github.fermin214.moerise.fixture")
        require(Regex("[a-f0-9]{32}").matches(requireNotNull(marker)))
        val first = "Saved offline ${marker}_one"
        val second = "Saved offline ${marker}_two"
        val unfinished = "Unsent input $marker"
        compose.waitForHome()
        when (phase) {
            "prepare" -> {
                for (text in listOf(first, second)) {
                    compose.homeNode("note_editor").performTextReplacement(text)
                    compose.homeNode("save_note").performClick()
                    waitEditor("")
                    compose.waitForCard(text, queued = true)
                }
                compose.homeNode("note_editor").performTextReplacement(unfinished)
                // 离开并返回主页，让输入通过正常页面生命周期保存。
                compose.navigate("settings")
                compose.navigate("home")
                waitEditor(unfinished)
            }
            "offlineRestart" -> {
                waitEditor(unfinished)
                for (text in listOf(first, second)) {
                    val key = compose.waitForCard(text, queued = true)
                    compose.cardAction(key, "edit_note")
                    compose.assertSingleEditor(key, text)
                    compose.activityRule.scenario.recreate()
                    compose.assertSingleEditor(key, text)
                    compose.homeNode("cancel_note_$key").performClick()
                    compose.waitForCard(text, queued = true)
                    waitEditor(unfinished)
                }
                waitEditor(unfinished)
            }
            "onlineRestart" -> {
                waitEditor(unfinished)
                compose.waitForCard(first)
                compose.waitForCard(second)
                waitEditor(unfinished)
            }
            else -> error("未知恢复阶段")
        }
    }
}

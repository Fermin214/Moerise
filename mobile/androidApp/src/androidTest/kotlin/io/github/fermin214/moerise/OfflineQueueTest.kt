package io.github.fermin214.moerise

import androidx.compose.ui.test.*
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.fermin214.moerise.data.DraftEntity
import io.github.fermin214.moerise.data.androidDatabase
import kotlinx.coroutines.runBlocking
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

    private fun openRemoval() {
        compose.dismissKeyboard()
        compose.clickTag("open_menu")
        compose.clickTag("account_menu")
        compose.clickTag("account_remove")
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
        if (phase != "afterRemoval") compose.waitForHome()
        when (phase) {
            "privacyUpgrade" -> {
                check(compose.model().mobile.privacy.state.value.enabled)
                compose.onNodeWithTag("welcome_enter").assertDoesNotExist()
                waitEditor(unfinished)
            }
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
                for (text in listOf(first, second)) {
                    compose.waitForCard(text)
                    compose.onAllNodes(readingText(text)).assertCountEquals(1)
                    check(compose.model().mobile.sync.state.value.notes.none { it.content == text })
                }
                waitEditor(unfinished)
            }
            "removeAccount" -> {
                val account = requireNotNull(compose.model().session.state.value.profile).accountKey
                val peer = account.substringBeforeLast('\u0000') + "\u0000users/peer_$marker"
                val db = androidDatabase(InstrumentationRegistry.getInstrumentation().targetContext)
                try {
                    runBlocking { db.dao().putDraft(DraftEntity(marker, peer, "Peer draft $marker", 1)) }
                    val discarded = "Discard local $marker"
                    compose.homeNode("note_editor").performTextReplacement(discarded)
                    compose.homeNode("save_note").performClick()
                    waitEditor("")
                    compose.waitForCard(discarded, queued = true)
                    compose.homeNode("note_editor").performTextReplacement(unfinished)
                    openRemoval()
                    compose.clickTag("account_remove_cancel")
                    waitEditor(unfinished)
                    compose.waitForCard(discarded, queued = true)
                    openRemoval()
                    compose.clickTag("account_remove_confirm")
                    compose.waitForTag("server_login")
                    runBlocking {
                        check(db.dao().drafts(account).isEmpty())
                        check(db.dao().pendingDraftIds(account).isEmpty())
                        check(db.dao().draft(marker)?.accountKey == peer)
                    }
                } finally { db.close() }
            }
            "afterRemoval" -> {
                compose.waitForTag("server_login")
                compose.signIntoFixture()
                waitEditor("")
                compose.waitForCard(first)
                compose.waitForCard(second)
                val account = requireNotNull(compose.model().session.state.value.profile).accountKey
                val db = androidDatabase(InstrumentationRegistry.getInstrumentation().targetContext)
                try {
                    runBlocking {
                        val peer = account.substringBeforeLast('\u0000') + "\u0000users/peer_$marker"
                        check(db.dao().homeDraft(peer)?.content == "Peer draft $marker")
                        check(db.dao().pendingDraftIds(account).isEmpty())
                        db.dao().deleteDraft(marker)
                    }
                } finally { db.close() }
            }
            else -> error("未知恢复阶段")
        }
    }
}

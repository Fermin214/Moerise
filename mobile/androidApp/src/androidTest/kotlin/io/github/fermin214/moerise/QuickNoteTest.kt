package io.github.fermin214.moerise

import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.text.AnnotatedString
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.io.FileInputStream
import org.json.JSONObject
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class QuickNoteTest {
    @get:Rule val compose = createAndroidComposeRule<MainActivity>()

    @Test
    fun loginWriteRecreateSaveAndReadBack() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val descriptor = instrumentation.uiAutomation.executeShellCommand("cat /data/local/tmp/moerise-android-fixture.json")
        val fixture = descriptor.use { JSONObject(FileInputStream(it.fileDescriptor).bufferedReader().readText()) }
        compose.onNodeWithTag("server_origin").performTextReplacement(fixture.getString("origin"))
        compose.onNodeWithTag("server_username").performTextReplacement(fixture.getString("username"))
        compose.onNodeWithTag("server_password").performTextReplacement(fixture.getString("password"))
        compose.onNodeWithTag("server_login").performClick()
        compose.waitUntil(20_000) { compose.onAllNodesWithTag("note_editor").fetchSemanticsNodes().isNotEmpty() }
        val text = "Android quick note ${System.currentTimeMillis()} #android"
        compose.onNodeWithTag("note_editor").performTextInput(text)
        compose.waitForIdle()
        Thread.sleep(500)
        compose.activityRule.scenario.recreate()
        compose.onNodeWithTag("note_editor").assertTextContains(text)
        compose.onNodeWithTag("save_note").performScrollTo().performClick()
        compose.waitUntil(20_000) { compose.onAllNodesWithText(text, substring = false).fetchSemanticsNodes().isNotEmpty() }
        compose.onNodeWithTag("note_editor").assert(SemanticsMatcher.expectValue(SemanticsProperties.EditableText, AnnotatedString("")))
        compose.onNodeWithText(text, substring = false).assertIsDisplayed()
        compose.onNodeWithText(text, substring = false).performClick()
        compose.waitUntil(20_000) { compose.onAllNodesWithTag("note_edit").fetchSemanticsNodes().isNotEmpty() }
        compose.onNodeWithTag("note_edit").performClick()
        compose.waitUntil(10_000) { compose.onAllNodesWithTag("note_editor").fetchSemanticsNodes().isNotEmpty() }
        val edited = "$text edited"
        compose.onNodeWithTag("note_editor").performTextReplacement(edited)
        compose.onNodeWithTag("save_note").performScrollTo().performClick()
        compose.waitUntil(20_000) { compose.onAllNodesWithTag("note_server_saved").fetchSemanticsNodes().isNotEmpty() }
        compose.onNodeWithTag("editor_back").performScrollTo().performClick()
        compose.waitUntil(10_000) { compose.onAllNodesWithTag("note_edit").fetchSemanticsNodes().isNotEmpty() }
        compose.onNodeWithTag("note_archive").performClick()
        compose.waitUntil(20_000) {
            compose.onAllNodesWithText("恢复").fetchSemanticsNodes().isNotEmpty() ||
                compose.onAllNodesWithText("Restore").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("note_archive").performClick()
        compose.waitUntil(20_000) {
            compose.onAllNodesWithText("归档").fetchSemanticsNodes().isNotEmpty() ||
                compose.onAllNodesWithText("Archive").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("note_delete").performClick()
        compose.onNodeWithTag("delete_confirm").performClick()
        compose.waitUntil(20_000) { compose.onAllNodesWithTag("note_edit").fetchSemanticsNodes().isEmpty() }
    }
}

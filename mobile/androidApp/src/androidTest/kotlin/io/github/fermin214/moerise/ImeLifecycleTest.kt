package io.github.fermin214.moerise

import android.content.res.Configuration
import android.content.pm.ActivityInfo
import android.text.InputType
import android.util.Log
import android.view.inputmethod.InputConnection
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.*
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class ImeLifecycleTest {
    private val inputMethod = FixtureInputMethod()
    @get:Rule val compose = fixtureComposeRule(inputMethod)

    @Test
    fun composingCommitCursorEditsAndDraftSurviveRecreation() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        assertEquals("输入法专项测试只能使用隔离流程包", "io.github.fermin214.moerise.fixture",
            instrumentation.targetContext.packageName)
        compose.signIntoFixture()
        compose.onNodeWithTag("home").assertExists()
        assertEquals("手机使用竖屏", ActivityInfo.SCREEN_ORIENTATION_PORTRAIT, compose.activity.requestedOrientation)
        assertEquals("手机编辑窗口为竖屏", Configuration.ORIENTATION_PORTRAIT,
            compose.activity.resources.configuration.orientation)
        compose.homeNode("note_editor")
        val initial = requireNotNull(compose.editorConfiguration("note_editor"))[SemanticsProperties.EditableText].text
        val prefix = if (initial.isEmpty()) "" else "$initial\n"
        var connection = editorConnection()
        edit(connection, "将光标移到草稿末尾", initial) { setSelection(initial.length, initial.length) }
        if (initial.isNotEmpty()) edit(connection, "在已有草稿后插入换行", prefix) { commitText("\n", 1) }
        // 每次组合更新必须替换同一段拼音，中文提交必须替换整段组合文本。
        composeWord(connection, prefix, "", listOf("n", "ni", "nih", "nihao"), "你好")
        edit(connection, "输入法插入换行", "${prefix}你好\n") { commitText("\n", 1) }
        composeWord(connection, "${prefix}你好\n", "", listOf("c", "cao", "caogao"), "草稿")
        val text = "${prefix}你好\n草稿"
        val cursor = prefix.length + 2
        edit(connection, "将光标移到正文中间", text) { setSelection(cursor, cursor) }
        composeWord(connection, "${prefix}你好", "\n草稿", listOf("c", "ce", "ceshi"), "测试")
        edit(connection, "从光标向前删除两个汉字", text) { deleteSurroundingText(2, 0) }
        edit(connection, "从光标向后删除换行", "${prefix}你好草稿") { deleteSurroundingText(0, 1) }
        edit(connection, "在光标处重新插入换行", text) { commitText("\n", 1) }
        edit(connection, "结束输入法组合状态", text) { finishComposingText() }
        assertDraftSaved(text)
        reopenLocalDraft(text)
        val beforeInputRecreation = compose.activity
        compose.activityRule.scenario.recreate()
        assertTrue("输入后重建必须生成新的 Activity", compose.activity !== beforeInputRecreation)
        assertDraftSaved(text)
        // 旧 Activity 的连接不可复用；重建后的编辑器必须能重新建立输入连接。
        connection = editorConnection()
        edit(connection, "重建后将光标移到末尾", text) { setSelection(text.length, text.length) }
        composeWord(connection, text, "", listOf("h", "hui", "huifu"), "恢复")
        val recovered = "${text}恢复"
        edit(connection, "提交后结束组合状态", recovered) { finishComposingText() }
        assertDraftSaved(recovered)
        val beforeRecreation = compose.activity
        compose.activityRule.scenario.recreate()
        assertTrue("显式重建必须生成新的 Activity", compose.activity !== beforeRecreation)
        assertDraftSaved(recovered)
        connection = editorConnection()
        edit(connection, "重建后将光标移到末尾", recovered) { setSelection(recovered.length, recovered.length) }
        val completed = "$recovered\n完成"
        edit(connection, "重建后的输入连接继续提交文本", completed) { commitText("\n完成", 1) }
        edit(connection, "完成后结束组合状态", completed) { finishComposingText() }
        assertDraftSaved(completed)
        reopenLocalDraft(completed)
        compose.onNodeWithTag("home").assertExists()
        compose.onNodeWithTag("open_menu").assertExists()
    }

    private fun editorConnection(): InputConnection {
        compose.homeNode("note_editor").performClick().assertIsFocused()
        compose.waitUntil(10_000) {
            compose.runOnIdle { inputMethod.session != null }
        }
        val session = compose.runOnIdle { requireNotNull(inputMethod.session) }
        val info = session.info
        assertEquals("输入连接必须来自文本编辑器", InputType.TYPE_CLASS_TEXT, info.inputType and InputType.TYPE_MASK_CLASS)
        assertTrue("输入连接必须支持换行", info.inputType and InputType.TYPE_TEXT_FLAG_MULTI_LINE != 0)
        return session.connection
    }

    private fun edit(connection: InputConnection, operation: String, expected: String, action: InputConnection.() -> Boolean) {
        try {
            compose.runOnIdle { assertTrue("输入连接操作失败：$operation", connection.action()) }
        } catch (failure: Throwable) {
            textFailure(operation, expected, failure)
        }
        assertEditorText(expected, operation)
    }

    private fun composeWord(connection: InputConnection, prefix: String, suffix: String, spelling: List<String>, committed: String) {
        spelling.forEach { pinyin ->
            edit(connection, "更新拼音组合：$pinyin", "$prefix$pinyin$suffix") { setComposingText(pinyin, 1) }
        }
        edit(connection, "提交中文：$committed", "$prefix$committed$suffix") { commitText(committed, 1) }
    }

    private fun assertEditorText(expected: String, operation: String = "恢复草稿") {
        // 组合态可附带下划线样式；只比较文本，逐次校验拼音替换和中文提交。
        val exactText = SemanticsMatcher("EditableText.text 等于期望文本") {
            it.config.getOrNull(SemanticsProperties.EditableText)?.text == expected
        }
        try {
            compose.waitForHome()
            compose.waitUntil(10_000) {
                compose.onAllNodes(hasTestTag("note_editor") and exactText).fetchSemanticsNodes().size == 1
            }
            compose.onNodeWithTag("note_editor").assert(exactText)
        } catch (failure: Throwable) {
            textFailure(operation, expected, failure)
        }
    }

    private fun textFailure(operation: String, expected: String, failure: Throwable): Nothing {
        val config = runCatching {
            compose.editorConfiguration("note_editor")
        }.getOrElse { failure.addSuppressed(it); null }
        val actual = config?.getOrNull(SemanticsProperties.EditableText)?.text
        val selection = config?.getOrNull(SemanticsProperties.TextSelectionRange)
        Log.e("MoeriseImeTest", "操作=$operation；期望=${JSONObject.quote(expected)}；" +
            "实际=${actual?.let { JSONObject.quote(it) } ?: "无"}；选区=${selection ?: "无"}")
        val mismatch = runCatching {
            assertEquals("操作「$operation」的 EditableText.text", expected, actual)
        }.exceptionOrNull()
        if (mismatch != null) throw mismatch.apply { addSuppressed(failure) }
        throw failure
    }

    private fun assertDraftSaved(expected: String) {
        assertEditorText(expected)
        val editor = compose.model().mobile.editor
        compose.waitUntil(10_000) {
            val state = editor.state.value
            state.error == null && state.draft?.content == expected &&
                state.savedRevision == state.draft?.revision
        }
    }

    private fun reopenLocalDraft(expected: String) {
        compose.navigate("settings")
        compose.navigate("home")
        compose.waitForEditor("note_editor", expected)
        assertDraftSaved(expected)
    }
}

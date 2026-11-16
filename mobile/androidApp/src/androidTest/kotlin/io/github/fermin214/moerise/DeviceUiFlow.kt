package io.github.fermin214.moerise

import android.view.inputmethod.InputMethodManager
import android.app.Application
import android.graphics.Bitmap
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.AndroidComposeTestRule
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.lifecycle.ViewModelProvider
import androidx.test.ext.junit.rules.ActivityScenarioRule
import androidx.test.platform.app.InstrumentationRegistry
import io.github.fermin214.moerise.data.CORE_REQUEST_TIMEOUT_MILLIS
import java.io.FileInputStream
import java.io.File
import org.json.JSONObject
import org.junit.rules.TestRule
import org.junit.runner.Description
import org.junit.runners.model.Statement

class FixtureActivityRule(private val inputMethod: FixtureInputMethod? = null) : TestRule {
    private val delegate = ActivityScenarioRule(MainActivity::class.java)
    val scenario get() = delegate.scenario
    val activity: MainActivity
        get() {
            var activity: MainActivity? = null
            scenario.onActivity { activity = it }
            return requireNotNull(activity)
        }

    override fun apply(base: Statement, description: Description) = object : Statement() {
        override fun evaluate() {
            val instrumentation = InstrumentationRegistry.getInstrumentation()
            val packageName = instrumentation.targetContext.packageName
            require(packageName == "io.github.fermin214.moerise.fixture")
            val application = instrumentation.targetContext.applicationContext as Application
            inputMethod?.let(application::registerActivityLifecycleCallbacks)
            try {
                // 先由 shell 显式打开隔离包，兼容限制后台 Activity 启动的设备系统。
                instrumentation.uiAutomation.executeShellCommand(
                    "am start -W -n $packageName/${MainActivity::class.java.name}"
                ).use { descriptor ->
                    val result = FileInputStream(descriptor.fileDescriptor).bufferedReader().readText()
                    check("Status: ok" in result) { "隔离包前台启动失败：$result" }
                }
                val guarded = object : Statement() {
                    override fun evaluate() {
                        try {
                            base.evaluate()
                        } catch (failure: Throwable) {
                            runCatching {
                                val directory = File(instrumentation.targetContext.cacheDir,
                                    "acceptance-diagnostics").apply { mkdirs() }
                                val prefix = description.className.substringAfterLast('.') + "-" + description.methodName
                                File(directory, "$prefix-threads.txt").writeText(buildString {
                                    appendLine(failure.stackTraceToString())
                                    Thread.getAllStackTraces().entries.sortedBy { it.key.name }.forEach { (thread, stack) ->
                                        appendLine("\n${thread.name} id=${thread.id} state=${thread.state}")
                                        stack.forEach { appendLine("    at $it") }
                                    }
                                })
                                File(directory, "$prefix-state.txt").writeText(deviceDiagnostics())
                                instrumentation.uiAutomation.takeScreenshot()?.let { bitmap ->
                                    File(directory, "$prefix.png").outputStream().use {
                                        check(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
                                    }
                                    bitmap.recycle()
                                }
                                instrumentation.uiAutomation.executeShellCommand(
                                    "logcat -d -v threadtime -t 400 --pid=${android.os.Process.myPid()}"
                                ).use { descriptor ->
                                    File(directory, "$prefix-logcat.txt").writeText(
                                        FileInputStream(descriptor.fileDescriptor).bufferedReader().readText())
                                }
                            }.exceptionOrNull()?.let(failure::addSuppressed)
                            throw failure
                        }
                    }
                }
                delegate.apply(guarded, description).evaluate()
            } finally {
                inputMethod?.let(application::unregisterActivityLifecycleCallbacks)
            }
        }
    }
}

internal fun fixtureComposeRule(inputMethod: FixtureInputMethod? = null) =
    AndroidComposeTestRule(FixtureActivityRule(inputMethod)) { it.activity }

internal fun AndroidComposeTestRule<FixtureActivityRule, MainActivity>.signIntoFixture() {
    val instrumentation = InstrumentationRegistry.getInstrumentation()
    require(instrumentation.targetContext.packageName == "io.github.fermin214.moerise.fixture")
    waitUntil(20_000) {
        onAllNodesWithTag("server_login").fetchSemanticsNodes().isNotEmpty() ||
            onAllNodesWithTag("home").fetchSemanticsNodes().isNotEmpty()
    }
    if (onAllNodesWithTag("server_login").fetchSemanticsNodes().isNotEmpty()) {
        val fixture = instrumentation.uiAutomation.executeShellCommand(
            "cat /data/local/tmp/moerise-android-fixture.json"
        ).use { JSONObject(FileInputStream(it.fileDescriptor).bufferedReader().readText()) }
        if (onAllNodesWithTag("server_origin").fetchSemanticsNodes().isEmpty()) {
            onNodeWithTag("server_change").performScrollTo().performClick()
        }
        onNodeWithTag("server_origin").performScrollTo().performTextReplacement(fixture.getString("origin"))
        onNodeWithTag("server_username").performScrollTo().performTextReplacement(fixture.getString("username"))
        onNodeWithTag("server_password").performScrollTo().performTextReplacement(fixture.getString("password"))
        dismissKeyboard()
        clickTag("server_login")
    }
    try {
        val session = (instrumentation.targetContext.applicationContext as MoeriseApplication).runtime.session
        waitUntil(5_000) {
            session.state.value.let { it.busy || it.profile != null || it.error != null }
        }
        waitUntil(CORE_REQUEST_TIMEOUT_MILLIS + 5_000) {
            session.state.value.let { it.profile != null || it.error != null }
        }
        check(session.state.value.let { it.profile != null && it.error == null })
        waitForHome()
    } catch (failure: Throwable) {
        val session = (instrumentation.targetContext.applicationContext as MoeriseApplication)
            .runtime.session.state.value
        val transport = runCatching {
            val fixture = instrumentation.uiAutomation.executeShellCommand(
                "cat /data/local/tmp/moerise-android-fixture.json"
            ).use { JSONObject(FileInputStream(it.fileDescriptor).bufferedReader().readText()) }
            val connection = java.net.URL(fixture.getString("origin") + "/api/app/v1/capabilities")
                .openConnection() as java.net.HttpURLConnection
            try {
                connection.connectTimeout = 5_000
                connection.readTimeout = 5_000
                "HTTP ${connection.responseCode}"
            } finally { connection.disconnect() }
        }.fold({ it }, { "${it.javaClass.name}: ${it.message}" })
        val protocol = kotlinx.coroutines.runBlocking(kotlinx.coroutines.Dispatchers.IO) {
            runCatching {
                val fixture = instrumentation.uiAutomation.executeShellCommand(
                    "cat /data/local/tmp/moerise-android-fixture.json"
                ).use { JSONObject(FileInputStream(it.fileDescriptor).bufferedReader().readText()) }
                val origin = fixture.getString("origin")
                val secrets = io.github.fermin214.moerise.data.AndroidSecretStore(instrumentation.targetContext)
                val client = io.github.fermin214.moerise.data.CoreClient(
                    origin, io.github.fermin214.moerise.data.SessionCookies(origin, secrets))
                try {
                    client.capabilities()
                    client.login(fixture.getString("username"), fixture.getString("password"))
                    "OK"
                } finally { client.close() }
            }.fold({ it }, { error ->
                generateSequence(error) { it.cause }.take(5).joinToString(" -> ") { it.javaClass.name }
            })
        }
        throw AssertionError("登录后主页未就绪：loading=${session.loading}, busy=${session.busy}, " +
            "connected=${session.connected}, profile=${session.profile != null}, " +
            "needsLogin=${session.needsLogin}, error=${session.error}, transport=$transport, protocol=$protocol", failure)
    }
    waitUntil(20_000) { runCatching { homeNode("note_editor").assertExists() }.isSuccess }
}

internal fun AndroidComposeTestRule<FixtureActivityRule, MainActivity>.model(): AppModel =
    runOnIdle { ViewModelProvider(activity)[AppModel::class.java] }

internal fun ComposeTestRule.hasTag(tag: String) =
    onAllNodesWithTag(tag).fetchSemanticsNodes().isNotEmpty()

internal fun ComposeTestRule.waitForTag(tag: String) {
    waitUntil(20_000) { hasTag(tag) }
}

/** 只向实际拥有滚动祖先的控件发起滚动；侧栏底部和弹窗按钮直接点击。 */
internal fun ComposeTestRule.clickTag(tag: String) {
    try {
        waitForTag(tag)
        val target = onNodeWithTag(tag)
        waitUntil(20_000) { runCatching { target.assertIsEnabled() }.isSuccess }
        if (onAllNodes(hasTestTag(tag) and hasAnyAncestor(hasScrollAction())).fetchSemanticsNodes().isNotEmpty()) {
            target.performScrollTo()
        }
        waitUntil(20_000) { runCatching { target.assertIsDisplayed() }.isSuccess }
        target.performClick()
    } catch (failure: Throwable) {
        val target = runCatching { onNodeWithTag(tag).printToString() }.getOrDefault("missing")
        throw AssertionError("控件操作失败：tag=$tag；${deviceDiagnostics()}；target=$target", failure)
    }
}

internal fun deviceDiagnostics(): String {
    val mobile = (InstrumentationRegistry.getInstrumentation().targetContext.applicationContext as MoeriseApplication)
        .runtime.mobile
    val session = mobile.session.state.value
    val action = mobile.noteActions.state.value
    val home = mobile.homeNote.state.value
    val picker = mobile.insights.state.value.picker
    val search = mobile.search.state.value
    return "connected=${session.connected}；sessionBusy=${session.busy}；sessionError=${session.error}；" +
        "needsLogin=${session.needsLogin}；sync=${mobile.sync.state.value.status}；" +
        "actionName=${action.name}；actionBusy=${action.busy}；actionPending=${action.pendingAction}；" +
        "actionUnknown=${action.deleteUnknown}；actionError=${action.error}；" +
        "homeBusy=${home.busy}；homeLoading=${home.loading}；homePending=${home.pendingAction}；homeError=${home.error}；" +
        "pickerBusy=${picker.busy}；pickerError=${picker.error}；pickerCandidates=${picker.candidates.map { it.name }}；" +
        "searchLoading=${search.loading}；searchError=${search.error}；searchMode=${search.query.mode}；" +
        "searchResponseMode=${search.responseMode}；searchSemantic=${search.semantic}；searchNames=${search.notes.map { it.name }}"
}

internal fun AndroidComposeTestRule<FixtureActivityRule, MainActivity>.dismissKeyboard() {
    runOnIdle {
        val window = activity.window
        activity.currentFocus?.clearFocus()
        activity.getSystemService(InputMethodManager::class.java)
            .hideSoftInputFromWindow(window.decorView.windowToken, 0)
    }
    waitUntil(10_000) {
        runOnIdle {
            ViewCompat.getRootWindowInsets(activity.window.decorView)
                ?.isVisible(WindowInsetsCompat.Type.ime()) != true
        }
    }
}

internal fun AndroidComposeTestRule<FixtureActivityRule, MainActivity>.clickHomeTag(tag: String) {
    dismissKeyboard()
    homeNode(tag)
    clickTag(tag)
}

private fun homeListMatcher() = hasScrollToIndexAction() and hasAnyAncestor(hasTestTag("home"))

internal fun ComposeTestRule.waitForHome() {
    waitUntil(20_000) {
        hasTag("home") && onAllNodes(homeListMatcher()).fetchSemanticsNodes().size == 1
    }
    onNodeWithTag("detail_back").assertDoesNotExist()
    onNodeWithTag("note_detail").assertDoesNotExist()
}

internal fun ComposeTestRule.homeList(): SemanticsNodeInteraction {
    waitForHome()
    return onNode(homeListMatcher())
}

internal fun readingText(expected: String) = SemanticsMatcher("笔记正文一致") {
    it.config.getOrNull(SemanticsProperties.EditableText) == null &&
        it.config.getOrNull(SemanticsProperties.Text)?.any { value -> value.text.trimEnd() == expected } == true
}

internal fun editorText(expected: String) = SemanticsMatcher("编辑器正文一致") {
    it.config.getOrNull(SemanticsProperties.EditableText)?.text == expected
}

/** 使用 LazyColumn 的滚动语义定位控件，支持手机上暂未组成的卡片。 */
internal fun ComposeTestRule.homeNode(tag: String): SemanticsNodeInteraction {
    val target = onNodeWithTag(tag)
    homeList().performScrollToNode(hasTestTag(tag))
    waitUntil(20_000) { runCatching { target.assertIsDisplayed() }.isSuccess }
    waitForIdle()
    return target
}

internal fun ComposeTestRule.waitForCard(text: String, queued: Boolean = false): String {
    val prefix = if (queued) "queued_note_" else "note_card_"
    val card = SemanticsMatcher("目标笔记卡") {
        it.config.getOrNull(SemanticsProperties.TestTag)?.startsWith(prefix) == true
    } and hasAnyDescendant(readingText(text))
    var key: String? = null
    waitForHome()
    try {
        waitUntil(70_000) {
            key = runCatching {
                if (!onNode(card).isDisplayed()) onNode(homeListMatcher()).performScrollToNode(card)
                onNode(card).fetchSemanticsNode().config[SemanticsProperties.TestTag].removePrefix(prefix)
            }.getOrNull()
            key != null
        }
    } catch (failure: Throwable) {
        throw AssertionError("目标笔记卡片未出现：${deviceDiagnostics()}", failure)
    }
    return requireNotNull(key)
}

internal fun AndroidComposeTestRule<FixtureActivityRule, MainActivity>.cardAction(key: String, action: String) {
    dismissKeyboard()
    homeNode("note_more_$key").performClick()
    onNodeWithTag("menu_open_note_$key").assertDoesNotExist()
    clickTag("${action}_$key")
}

internal fun AndroidComposeTestRule<FixtureActivityRule, MainActivity>.navigate(page: String) {
    dismissKeyboard()
    clickTag("open_menu")
    waitForTag("sidebar_scroll")
    if (page == "archive") {
        clickTag("account_menu")
    }
    clickTag("nav_$page")
    waitUntil(20_000) { runCatching { onNodeWithTag("sidebar_scroll").assertIsNotDisplayed() }.isSuccess }
    try {
        when (page) {
            "home" -> waitForHome()
            "archive" -> { waitForHome(); waitForTag("archive_page") }
            "settings" -> waitForTag("settings_page")
            else -> waitForTag("${page}_page")
        }
    } catch (failure: ComposeTimeoutException) {
        throw AssertionError("导航目标=$page；${pageDiagnostics()}", failure)
    }
}

private fun ComposeTestRule.pageDiagnostics(): String {
    val pages = listOf("home", "review_page", "walk_page", "insights_page", "map_page",
        "settings_page", "settings_tab_0", "sidebar_scroll", "server_login")
    return "页面标记=" + pages.filter { hasTag(it) }.joinToString(",")
}

internal fun AndroidComposeTestRule<FixtureActivityRule, MainActivity>.assertSingleEditor(key: String, text: String) {
    val mobile = model().mobile
    waitUntil(20_000) {
        mobile.noteEditors.value.size == 1 && mobile.getNoteEditor(key)?.state?.value?.draft?.content == text
    }
    homeList().performScrollToIndex(0)
    onNodeWithTag("note_editor").assertDoesNotExist()
    onNodeWithTag("save_note").assertDoesNotExist()
    homeNode("note_editor_$key")
    waitForEditor("note_editor_$key", text)
    val editors = SemanticsMatcher("笔记编辑器") {
        val tag = it.config.getOrNull(SemanticsProperties.TestTag).orEmpty()
        tag == "note_editor" || tag.startsWith("note_editor_")
    }
    onAllNodes(editors).assertCountEquals(1)
}

internal fun ComposeTestRule.waitForEditor(tag: String, text: String) {
    if (tag == "note_editor" && !hasTag(tag)) homeList().performScrollToIndex(0)
    try {
        waitUntil(20_000) {
            onAllNodesWithTag(tag).fetchSemanticsNodes().singleOrNull()
                ?.config?.getOrNull(SemanticsProperties.EditableText)?.text == text
        }
    } catch (failure: ComposeTimeoutException) {
        val actual = onAllNodesWithTag(tag).fetchSemanticsNodes().singleOrNull()
            ?.config?.getOrNull(SemanticsProperties.EditableText)?.text
        throw AssertionError("编辑器=$tag；期望=${JSONObject.quote(text)}；实际=${actual?.let { JSONObject.quote(it) }}；" +
            pageDiagnostics(), failure)
    }
}

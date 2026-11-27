package io.github.fermin214.moerise

import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.*
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.fermin214.moerise.data.CORE_REQUEST_TIMEOUT_MILLIS
import io.github.fermin214.moerise.presentation.SyncStatus
import io.github.fermin214.moerise.presentation.TimelineUiState
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class QuickNoteTest {
    @get:Rule val compose = fixtureComposeRule()

    private fun visualSetup() = InstrumentationRegistry.getArguments().getString("visualSetup") == "true"

    private fun saveNew(text: String): String {
        compose.openComposer().performTextReplacement(text)
        compose.waitForEditor("quick_editor", text)
        saveComposer()
        return compose.waitForCard(text.removeSuffix(" #android"))
    }

    private fun saveComposer() {
        compose.clickTag("quick_save")
        try {
            compose.waitUntil(20_000) { !compose.hasTag("quick_editor_sheet") }
            check(compose.model().mobile.editor.state.value.draft?.content == "")
        } catch (failure: AssertionError) {
            val mobile = compose.model().mobile
            val state = mobile.editor.state.value
            throw AssertionError("新建保存后未清空：busy=${state.busy}；importing=${state.importing}；" +
                "error=${state.error}；revision=${state.draft?.revision}；savedRevision=${state.savedRevision}；" +
                "pending=${state.pending?.status}；编辑目标=${mobile.noteEditors.value.keys}", failure)
        }
    }

    private fun search(text: String) {
        if (!compose.hasTag("search_query")) compose.clickTag("open_search")
        compose.onNodeWithTag("search_query").performTextReplacement(text)
        compose.onNodeWithTag("search_query").performImeAction()
    }

    private fun deleteCard(name: String) {
        compose.cardAction(name, "delete_note")
        compose.homeNode("delete_confirm_$name").performClick()
        val mobile = compose.model().mobile
        try {
            compose.waitUntil(CORE_REQUEST_TIMEOUT_MILLIS + 5_000) {
                !mobile.noteActions.state.value.busy
            }
            val confirmed = mobile.noteActions.state.value
            check(confirmed.error == null && !confirmed.pendingAction && confirmed.note == null)
            // 删除确认后才发起列表读取，两次请求各自使用完整的等待预算。
            compose.waitUntil(CORE_REQUEST_TIMEOUT_MILLIS + 5_000) {
                mobile.timeline.state.value.notes.none { it.name == name }
            }
        } catch (failure: Throwable) {
            val action = mobile.noteActions.state.value
            val timeline = mobile.timeline.state.value
            throw AssertionError("删除未完成：name=$name；busy=${action.busy}；error=${action.error}；" +
                "unknown=${action.deleteUnknown}；pending=${action.pendingAction}；" +
                "timelineLoading=${timeline.loading}；timelineError=${timeline.error}；" +
                "stillListed=${timeline.notes.any { it.name == name }}", failure)
        }
        compose.onNodeWithTag("note_card_$name").assertDoesNotExist()
    }

    private fun changeState(name: String, expected: String) {
        compose.cardAction(name, "archive_note")
        val mobile = compose.model().mobile
        val destination = if (expected == "ARCHIVED") mobile.archive else mobile.timeline
        try {
            compose.waitUntil(CORE_REQUEST_TIMEOUT_MILLIS + 5_000) {
                name !in mobile.stateActions.state.value && destination.state.value.notes.any { it.name == name && it.state == expected }
            }
        } catch (failure: Throwable) {
            throw AssertionError("笔记状态未确认：name=$name；expected=$expected；state=${mobile.stateActions.state.value[name]}；" +
                deviceDiagnostics(), failure)
        }
    }

    @Test
    fun loginWriteRecreateSaveAndReadBack() {
        compose.signIntoFixture()
        if (visualSetup()) return
        val marker = System.currentTimeMillis()
        val text = "Android quick note $marker #android"
        val displayed = text.removeSuffix(" #android")
        compose.openComposer().performTextReplacement(text)
        compose.activityRule.scenario.recreate()
        compose.waitForEditor("quick_editor", text)
        saveComposer()
        val firstName = compose.waitForCard(displayed)
        val peerText = "Android peer note $marker #android"
        val peerName = saveNew(peerText)

        val unfinished = "Unfinished composer $marker"
        compose.openComposer().performTextReplacement(unfinished)
        compose.closeWriting("quick_cancel")
        compose.cardAction(firstName, "edit_note")
        compose.assertSingleEditor(firstName, text)
        val edited = "Android quick note $marker local revision #android"
        compose.onNodeWithTag("note_editor_$firstName").performTextReplacement(edited)

        compose.closeWriting("cancel_note_$firstName")
        compose.cardAction(peerName, "edit_note")
        compose.assertSingleEditor(peerName, peerText)
        val peerEdited = "Android peer note $marker local revision #android"
        compose.onNodeWithTag("note_editor_$peerName").performTextReplacement(peerEdited)
        compose.activityRule.scenario.recreate()
        compose.assertSingleEditor(peerName, peerEdited)
        compose.closeWriting("cancel_note_$peerName")
        compose.navigate("settings")
        compose.navigate("home")
        compose.waitForCard(displayed)
        compose.cardAction(peerName, "edit_note")
        compose.assertSingleEditor(peerName, peerEdited)
        compose.closeWriting("cancel_note_$peerName")

        compose.cardAction(firstName, "edit_note")
        compose.assertSingleEditor(firstName, edited)
        val saved = "Android quick note $marker saved revision #android"
        compose.onNodeWithTag("note_editor_$firstName").performTextReplacement(saved)
        compose.closeWriting("save_note_$firstName")
        compose.waitForCard(saved.removeSuffix(" #android"))
        compose.openComposer()
        compose.waitForEditor("quick_editor", unfinished)
        compose.closeWriting("quick_cancel")

        compose.cardAction(peerName, "edit_note")
        compose.assertSingleEditor(peerName, peerEdited)
        compose.closeWriting("cancel_note_$peerName")
        compose.waitForCard(peerText.removeSuffix(" #android"))
        compose.activityRule.scenario.recreate()
        compose.waitForTag("compose_capsule")
        compose.onNodeWithTag("quick_editor_sheet").assertDoesNotExist()
        compose.cardAction(peerName, "edit_note")
        compose.assertSingleEditor(peerName, peerEdited)
        val peerSaved = "Android peer note $marker saved revision #android"
        compose.onNodeWithTag("note_editor_$peerName").performTextReplacement(peerSaved)
        compose.closeWriting("save_note_$peerName")
        compose.waitForCard(peerSaved.removeSuffix(" #android"))
        compose.cardAction(firstName, "edit_note")
        compose.assertSingleEditor(firstName, saved)
        compose.closeWriting("cancel_note_$firstName")
        compose.openComposer()
        compose.waitForEditor("quick_editor", unfinished)
        compose.closeWriting("quick_cancel")
    }

    @Test
    fun editThirtiethNoteInPlace() {
        compose.signIntoFixture()
        val marker = System.currentTimeMillis()
        val mobile = compose.model().mobile
        fun waitTimeline(label: String, timeout: Long, ready: (TimelineUiState) -> Boolean) {
            acceptanceStep(label)
            try {
                compose.waitUntil(timeout) { ready(mobile.timeline.state.value) }
            } catch (failure: Throwable) {
                val state = mobile.timeline.state.value
                throw AssertionError("$label：notes=${state.notes.size} next=${state.nextPageToken.isNotEmpty()} " +
                    "fresh=${state.fresh} loading=${state.loading} more=${state.loadingMore} error=${state.error} " +
                    "sync=${mobile.sync.state.value.status}", failure)
            }
        }
        waitTimeline("首页首批笔记", CORE_REQUEST_TIMEOUT_MILLIS + 5_000) { it.fresh && it.notes.size >= 20 }
        val first = mobile.timeline.state.value
        check(first.notes.size >= 30 || first.nextPageToken.isNotEmpty()) { "首页只有 ${first.notes.size} 条且无下一页" }
        compose.homeList().performScrollToKey(first.notes.last().name)
        waitTimeline("滚动加载第 30 篇", 2 * CORE_REQUEST_TIMEOUT_MILLIS + 5_000) { it.notes.size >= 30 }
        val notes = mobile.timeline.state.value.notes
        val anchor = "note_card_${notes[28].name}"
        val target = notes[29].name
        compose.homeList().performScrollToKey(notes[28].name)
        val position = compose.onNodeWithTag(anchor).assertIsDisplayed().fetchSemanticsNode().boundsInRoot.top
        compose.onNodeWithTag("note_more_$target").performClick()
        compose.clickTag("edit_note_$target")
        val content = "Timeline edited $marker"
        compose.waitForTag("note_editor_$target")
        compose.onNodeWithTag("note_editor_$target").performTextReplacement(content)
        compose.closeWriting("save_note_$target")
        waitTimeline("保存第 30 篇", CORE_REQUEST_TIMEOUT_MILLIS + 5_000) { state ->
            state.notes.any { it.name == target && it.content == content }
        }
        compose.onNodeWithTag("note_card_$target").assertIsDisplayed()
        compose.navigate("settings")
        compose.clickTag("settings_sync")
        waitTimeline("手动同步开始", CORE_REQUEST_TIMEOUT_MILLIS + 5_000) { mobile.sync.state.value.status == SyncStatus.SYNCING }
        waitTimeline("同步完成与两页笔记", 2 * CORE_REQUEST_TIMEOUT_MILLIS + 5_000) { state ->
            mobile.sync.state.value.status == SyncStatus.CONNECTED && state.fresh && !state.loading && !state.loadingMore &&
                state.notes.getOrNull(29)?.let { it.name == target && it.content == content } == true
        }
        compose.navigate("home")
        compose.onNodeWithTag("note_card_$target").assertIsDisplayed()
        val restored = compose.onNodeWithTag(anchor).assertIsDisplayed().fetchSemanticsNode().boundsInRoot.top
        check(kotlin.math.abs(restored - position) <= 2f) { "主页编辑导致卡片移动 $position → $restored" }
    }

    @Test
    fun searchKeywordHybridAndReadingStayOnHome() {
        compose.signIntoFixture()
        if (visualSetup()) return
        val query = "Android search note ${System.currentTimeMillis()}"
        val name = saveNew("$query #android")
        search(query)
        compose.waitForCard(query)
        compose.onNode(readingText(query)).performClick()
        compose.waitForHome()
        val timestamp = SemanticsMatcher("笔记时间戳") {
            it.config.getOrNull(SemanticsProperties.Text)?.any { value ->
                Regex("\\d{4}-\\d{2}-\\d{2} \\d{2}:\\d{2}").matches(value.text)
            } == true
        } and hasAnyAncestor(hasTestTag("note_card_$name"))
        compose.onNode(timestamp).performTouchInput { click() }
        compose.waitForHome()
        compose.onNodeWithTag("open_note_$name").assertDoesNotExist()
        compose.clickTag("search_close")
        search(query)
        compose.onNode(hasText("关键词") or hasText("Keyword")).performClick()
        compose.onNode(hasText("混合") or hasText("Hybrid")).performClick()
        compose.onNodeWithTag("search_query").performImeAction()
        compose.waitForCard(query)
        compose.clickTag("search_close")
        compose.navigate("home")
        compose.waitForCard(query)
    }

    @Test
    fun archiveRestoreAndDeletePreserveUnfinishedInput() {
        compose.signIntoFixture()
        if (visualSetup()) return
        val marker = System.currentTimeMillis()
        val text = "Android archive note $marker #android"
        val firstName = saveNew(text)
        val peerText = "Android delete peer $marker #android"
        val peerName = saveNew(peerText)
        val unfinished = "Unfinished action input $marker"
        compose.openComposer().performTextReplacement(unfinished)
        compose.closeWriting("quick_cancel")
        changeState(firstName, "ARCHIVED")
        compose.navigate("archive")
        compose.waitForCard(text.removeSuffix(" #android"))
        compose.cardAction(firstName, "edit_note")
        compose.assertSingleEditor(firstName, text)
        compose.closeWriting("cancel_note_$firstName")
        compose.onNodeWithTag("archive_timeline").assertExists()
        changeState(firstName, "NORMAL")
        compose.navigate("home")
        compose.waitForCard(text.removeSuffix(" #android"))
        deleteCard(firstName)
        compose.waitForCard(peerText.removeSuffix(" #android"))
        deleteCard(peerName)
        compose.openComposer()
        compose.waitForEditor("quick_editor", unfinished)
        compose.closeWriting("quick_cancel")
    }

    @Test
    fun navigationPreservesUnfinishedInput() {
        compose.signIntoFixture()
        if (visualSetup()) return
        val unfinished = "Navigation draft ${System.currentTimeMillis()}"
        compose.openComposer().performTextReplacement(unfinished)
        compose.closeWriting("quick_cancel")
        compose.navigate("review")
        compose.navigate("walk")
        acceptanceStep("漫步：等待配置或开始按钮")
        compose.waitUntil(20_000) {
            compose.hasTag("walk_settings") ||
                compose.onAllNodes(hasTestTag("walk_start") and isEnabled()).fetchSemanticsNodes().isNotEmpty()
        }
        if (compose.hasTag("walk_settings")) {
            compose.onNodeWithTag("walk_start").assertIsNotEnabled()
        } else {
            compose.clickTag("walk_start")
            compose.waitForTag("walk_end")
            compose.clickTag("walk_end")
        }
        if (compose.model().mobile.session.state.value.profile?.stargazingEnabled == true) {
            compose.navigate("map")
            compose.clickTag("map_fullscreen")
            compose.clickTag("map_fullscreen")
        }
        compose.navigate("settings")
        for (index in 0..3) compose.clickTag("settings_tab_$index")
        compose.onNodeWithTag("privacy_share").performScrollTo().assertIsOff()
        compose.waitUntil(20_000) { compose.model().mobile.privacy.state.value.allowed == false }
        compose.onNodeWithTag("privacy_share").assertIsNotEnabled()
        compose.navigate("home")
        compose.openComposer()
        compose.waitForEditor("quick_editor", unfinished)
        compose.closeWriting("quick_cancel")
    }
}

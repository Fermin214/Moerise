package io.github.fermin214.moerise.presentation

import android.app.Application
import android.content.ComponentName
import android.view.WindowManager.LayoutParams
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.unit.dp
import io.github.fermin214.moerise.MainActivity
import io.github.fermin214.moerise.data.ConnectionProfile
import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.DraftEntity
import io.github.fermin214.moerise.data.MemorySecrets
import io.github.fermin214.moerise.data.NoteRepository
import io.github.fermin214.moerise.data.SyncStateEntity
import io.github.fermin214.moerise.data.testDatabase
import io.github.fermin214.moerise.domain.Note
import java.nio.file.Files
import kotlin.test.*
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import org.junit.Rule
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.android.controller.ActivityController
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(application = Application::class, sdk = [29], qualifiers = "w360dp-h640dp")
class AndroidUiRegressionTest {
    @get:Rule val compose = createEmptyComposeRule()
    private lateinit var host: ActivityController<ComponentActivity>
    private val strings = Strings(UiLanguage.ENGLISH)

    @BeforeTest
    fun createHost() {
        compose.runOnUiThread { host = Robolectric.buildActivity(ComponentActivity::class.java).setup() }
    }

    @AfterTest
    fun closeHost() {
        if (::host.isInitialized) compose.runOnUiThread { host.pause().stop().destroy() }
    }

    @Test
    fun drawerFollowsAnEligibleDragAndReleasesFocusOnlyAfterSlop() {
        lateinit var drawer: NavigationDrawerState
        lateinit var scroll: ScrollState
        var clicks = 0
        compose.runOnUiThread {
            host.get().setContent {
                val coroutineScope = rememberCoroutineScope()
                drawer = remember { NavigationDrawerState(coroutineScope) }
                scroll = rememberScrollState()
                val focus = LocalFocusManager.current
                val leave = { focus.clearFocus() }
                MoeriseTheme {
                    NavigationDrawer(drawer, true, strings, leave, modifier = Modifier.testTag("drawer_host"),
                        sheet = { Text("Navigation") }) {
                        Column(Modifier.fillMaxSize().timelineFocusExit(leave).verticalScroll(scroll)) {
                            BasicTextField("", {}, modifier = Modifier.fillMaxWidth().height(80.dp)
                                .drawerGestureRegion().testTag("editor"))
                            Button(onClick = { clicks++ }, modifier = Modifier.testTag("body_click")) { Text("Body") }
                            Spacer(Modifier.height(1000.dp))
                        }
                    }
                }
            }
        }
        val editor = compose.onNodeWithTag("editor")
        val root = compose.onNodeWithTag("drawer_host")
        editor.performClick().assertIsFocused()
        compose.onNodeWithTag("body_click").performTouchInput { click() }
        compose.runOnIdle { assertEquals(1, clicks) }
        root.performTouchInput { swipe(Offset(8.dp.toPx(), 180.dp.toPx()), Offset(225.dp.toPx(), 180.dp.toPx())) }
        compose.runOnIdle { assertEquals(0f, drawer.fraction) }
        editor.performClick().assertIsFocused()
        root.performTouchInput { swipe(Offset(55.dp.toPx(), 40.dp.toPx()), Offset(260.dp.toPx(), 40.dp.toPx()), durationMillis = 200) }
        compose.runOnIdle { assertEquals(0f, drawer.fraction) }
        editor.assertIsFocused()

        root.performTouchInput { down(Offset(48.dp.toPx(), 180.dp.toPx())); moveBy(Offset(1f, 0f)) }
        editor.assertIsFocused()
        root.performTouchInput { moveBy(Offset(120.dp.toPx(), 0f)) }
        val reveal = compose.runOnIdle { drawer.fraction }
        assertTrue(reveal in 0.2f..0.49f)
        editor.assertIsNotFocused()
        root.performTouchInput { moveBy(Offset(-85.dp.toPx(), 0f)) }
        compose.runOnIdle { assertTrue(drawer.fraction in 0f..reveal) }
        root.performTouchInput { up() }
        compose.runOnIdle { assertEquals(0f, drawer.fraction) }
        compose.onNodeWithText("Navigation").assertDoesNotExist()

        editor.performClick().assertIsFocused()
        root.performTouchInput { click(Offset(160.dp.toPx(), 180.dp.toPx())) }
        editor.assertIsNotFocused()
        editor.performClick().assertIsFocused()
        root.performTouchInput { swipeUp(startY = 250.dp.toPx(), endY = 160.dp.toPx()) }
        editor.assertIsNotFocused()
        compose.runOnIdle { assertTrue(scroll.value > 0) }
    }

    @Test
    fun stopClearsWritingFocusAndManifestKeepsTheImeHiddenOnEntry() {
        compose.runOnUiThread {
            host.get().setContent {
                val focus = LocalFocusManager.current
                ClearWritingOnStop { focus.clearFocus() }
                BasicTextField("未提交", {}, modifier = Modifier.fillMaxWidth().height(80.dp).testTag("editor"))
            }
        }
        val editor = compose.onNodeWithTag("editor")
        editor.performClick().assertIsFocused()
        compose.runOnUiThread { host.pause().stop().restart().start().resume().visible() }
        editor.assertIsNotFocused()
        editor.assertTextContains("未提交")
        val activity = host.get()
        val mode = activity.packageManager.getActivityInfo(ComponentName(activity, MainActivity::class.java), 0).softInputMode
        assertEquals(LayoutParams.SOFT_INPUT_STATE_ALWAYS_HIDDEN, mode and LayoutParams.SOFT_INPUT_MASK_STATE)
        assertEquals(LayoutParams.SOFT_INPUT_ADJUST_RESIZE, mode and LayoutParams.SOFT_INPUT_MASK_ADJUST)
    }

    @OptIn(ExperimentalTestApi::class, ExperimentalComposeUiApi::class)
    @Test
    fun keyboardCanOpenNavigateAndDismissTheDrawerAndAccessibilityCanCloseIt() {
        var selections = 0
        compose.runOnUiThread {
            host.get().setContent {
                val scope = rememberCoroutineScope()
                val drawer = remember { NavigationDrawerState(scope) }
                MoeriseTheme {
                    NavigationDrawer(drawer, true, strings, {}, modifier = Modifier.testTag("drawer-host"), sheet = {
                        Button(onClick = { selections++ }, modifier = Modifier.testTag("drawer-destination")) { Text("Home") }
                    }) {
                        WebToolbar(strings, onMenu = drawer::open)
                    }
                }
            }
        }
        val root = compose.onNodeWithTag("drawer-host")
        val trigger = compose.onNodeWithTag("open_menu")
        val pane = compose.onNode(SemanticsMatcher.expectValue(SemanticsProperties.PaneTitle, "Navigation"))
        val destination = compose.onNodeWithTag("drawer-destination")
        root.performKeyInput { pressKey(Key.Tab) }
        trigger.assertIsFocused().performKeyInput { pressKey(Key.Enter) }
        pane.assertIsFocused().performKeyInput { pressKey(Key.Tab) }
        destination.assertIsFocused().performKeyInput { pressKey(Key.Enter) }
        compose.runOnIdle { assertEquals(1, selections) }
        destination.performKeyInput { pressKey(Key.Escape) }
        pane.assertDoesNotExist()
        trigger.assertIsFocused().performKeyInput { pressKey(Key.Enter) }
        pane.performSemanticsAction(SemanticsActions.Dismiss)
        pane.assertDoesNotExist()
        trigger.assertIsFocused()
    }

    @Test
    fun compactCardKeepsTapTargetsAndClosingTheMenuDoesNotRefocusTheEditor() {
        compose.runOnUiThread {
            host.get().setContent {
                val focus = LocalFocusManager.current
                CompositionLocalProvider(LocalLeaveWriting provides { focus.clearFocus() }) {
                    MoeriseTheme {
                        Column {
                            BasicTextField("", {}, modifier = Modifier.fillMaxWidth().height(48.dp).testTag("editor"))
                            WebNoteCard(Note("instance", "memos/test", "Remote body", "NORMAL", "hash",
                                createTime = "2026-11-26T10:00:00Z"), "UTC", strings, null, {}, onEdit = {})
                            QueuedNoteCard(DraftEntity("queued", "account", "Local body", 1, conflictJson = "{}"), strings,
                                createdAt = 1_795_620_000_000L, onOpen = {})
                        }
                    }
                }
            }
        }
        val editor = compose.onNodeWithTag("editor")
        val card = hasAnyAncestor(hasTestTag("note_card_memos/test"))
        val action = compose.onNodeWithTag("note_more_memos/test")
        val actionBounds = action.fetchSemanticsNode().boundsInRoot
        val date = compose.onNode(hasTestTag("note_card_date") and card, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val body = compose.onNode(hasTestTag("note_card_body") and card, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertTrue(date.height < actionBounds.height)
        assertTrue(body.top - actionBounds.top <= actionBounds.height + 2f)
        assertEquals(44.dp, action.getUnclippedBoundsInRoot().height)
        assertEquals(44.dp, compose.onNodeWithTag("queued_error_queued").getUnclippedBoundsInRoot().height)

        editor.performClick().assertIsFocused()
        action.performClick()
        compose.onNodeWithTag("edit_note_memos/test").performClick()
        editor.assertIsNotFocused()
    }

    @Test
    fun sidebarHasDirectArchiveAndLogoutAndPrivacyActionStaysEnabled() {
        val scope = CoroutineScope(SupervisorJob())
        val session = SessionController(MemorySecrets(), true, scope)
        try {
            val privacy = PrivacyController(null, session, scope)
            compose.runOnUiThread {
                host.get().setContent {
                    MoeriseTheme {
                        Row {
                            Box(Modifier.weight(1f)) {
                                WebSidebar("home", strings, "Owner", SearchState(), "2026-11",
                                    onClose = {}, onNavigate = {}, onMonth = {}, onDate = {}, onTag = {}, onLogout = {})
                            }
                            Box(Modifier.weight(1f)) { PrivacySettings(privacy, strings) }
                        }
                    }
                }
            }
            compose.onNodeWithTag("nav_archive").assertExists()
            compose.onNodeWithTag("account_logout").assertExists()
            compose.onNodeWithTag("sidebar_sync").assertDoesNotExist()
            compose.onNodeWithTag("account_menu").assertDoesNotExist()
            compose.onNodeWithTag("diagnostic_summary").assertIsEnabled()
        } finally { session.close(); scope.cancel() }
    }

    @Test
    fun savingAndCancellingReturnToAnUnfocusedComposerAndFirstScreenPrewarmsFilters() {
        val folder = Files.createTempDirectory("moerise-composer-focus").toFile()
        val database = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repository = NoteRepository(database)
        val secrets = MemorySecrets()
        val origin = "http://127.0.0.1:1"
        val account = "$origin\u0000instance\u0000users/owner"
        val note = Note("instance", "memos/test", "Original", "NORMAL", "hash", createTime = "2026-11-26T10:00:00Z")
        val job = SupervisorJob()
        val scope = CoroutineScope(job + Dispatchers.Main.immediate)
        runBlocking {
            secrets.write("selected-profile", CoreJson.encodeToString(ConnectionProfile(origin, account, "instance", "users/owner", "Owner", "UTC")))
            database.dao().putSyncState(SyncStateEntity(account, "cursor", "generation", true, 1))
            repository.recordLocalNote(account, note)
        }
        val session = SessionController(secrets, true, scope)
        try {
            val mobile = MobileController(session, { repository }, scope, initialForeground = true)
            compose.runOnUiThread { host.get().setContent { MoeriseApp(mobile) } }
            fun waitForTag(tag: String) {
                compose.waitUntil(10_000) { compose.onAllNodesWithTag(tag).fetchSemanticsNodes().size == 1 }
            }
            val composer = compose.onNodeWithTag("note_editor")
            val inline = compose.onNodeWithTag("note_editor_${note.name}")
            fun edit() {
                compose.runOnIdle { mobile.editNote(note) }
                waitForTag("note_editor_${note.name}")
                inline.performClick().assertIsFocused()
            }
            waitForTag("note_editor")
            compose.waitUntil(10_000) { mobile.filterReads.cached("NORMAL", serviceMonth("UTC")) != null }
            composer.performClick().performTextInput("New note")
            compose.onNodeWithTag("save_note").performClick()
            compose.waitUntil(10_000) { mobile.editor.state.value.draft?.content == "" }
            composer.assertIsNotFocused()

            edit()
            compose.onNodeWithTag("cancel_note_${note.name}").performClick()
            waitForTag("note_editor")
            composer.assertIsNotFocused()

            edit()
            inline.performTextReplacement("Edited")
            compose.onNodeWithTag("save_note_${note.name}").performClick()
            waitForTag("note_editor")
            composer.assertIsNotFocused()
        } finally {
            compose.runOnUiThread { job.cancel(); session.close() }
            compose.waitForIdle()
            database.close()
            folder.deleteRecursively()
        }
    }
}

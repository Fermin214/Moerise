package io.github.fermin214.moerise.presentation

import android.app.Application
import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.content.ComponentName
import android.content.Intent
import android.graphics.Bitmap
import android.net.Uri
import android.os.Looper
import android.view.WindowManager.LayoutParams
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.remember
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.luminance
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.height
import androidx.compose.ui.unit.width
import androidx.test.platform.app.InstrumentationRegistry
import io.github.fermin214.moerise.MainActivity
import io.github.fermin214.moerise.data.ACTIVE_NOTE_EDITOR
import io.github.fermin214.moerise.data.ConnectionProfile
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.data.CoreJson
import io.github.fermin214.moerise.data.DraftEntity
import io.github.fermin214.moerise.data.MemorySecrets
import io.github.fermin214.moerise.data.NoteRepository
import io.github.fermin214.moerise.data.SessionCookies
import io.github.fermin214.moerise.data.StagedAttachment
import io.github.fermin214.moerise.data.SyncStateEntity
import io.github.fermin214.moerise.data.attachmentEdits
import io.github.fermin214.moerise.data.epochMillis
import io.github.fermin214.moerise.data.testDatabase
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.domain.NotePage
import io.github.fermin214.moerise.domain.SyncNotesResponse
import io.github.fermin214.moerise.domain.StarMap
import io.github.fermin214.moerise.domain.StarPoint
import io.github.fermin214.moerise.domain.StarView
import io.ktor.client.engine.mock.MockEngine
import io.ktor.client.engine.mock.respond
import java.io.ByteArrayOutputStream
import java.nio.file.Files
import java.time.Instant
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.test.*
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.junit.Rule
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.android.controller.ActivityController
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(RobolectricTestRunner::class)
@Config(application = Application::class, sdk = [29], qualifiers = "w360dp-h640dp")
class AndroidUiRegressionTest {
    @get:Rule val compose = createEmptyComposeRule()
    private lateinit var host: ActivityController<ComponentActivity>
    private val strings = Strings(UiLanguage.ENGLISH)

    private fun createHost(inTouchMode: Boolean = true) {
        compose.runOnUiThread {
            InstrumentationRegistry.getInstrumentation().setInTouchMode(inTouchMode)
            host = Robolectric.buildActivity(ComponentActivity::class.java).setup()
        }
    }

    private fun waitForMainCondition(description: String, condition: () -> Boolean) {
        compose.waitUntil(conditionDescription = description, timeoutMillis = 10_000) {
            compose.runOnIdle {
                shadowOf(Looper.getMainLooper()).idle()
                condition()
            }
        }
    }

    @AfterTest
    fun closeHost() {
        if (::host.isInitialized) compose.runOnUiThread { host.pause().stop().destroy() }
    }

    @Test
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    fun tappingDuringStarInertiaCommitsTheCameraSoFitCanRestoreTheOverview() {
        createHost()
        val overview = StarView()
        val view = mutableStateOf(overview)
        val point = StarPoint("memos/center", "center-hash", 0.0, 0.0, true, emptyList())
        val map = StarMap("ready", "umap-balanced-v1", "layout", "frame", "vectors", 1,
            points = listOf(point), topics = emptyList(), links = emptyList())
        var selected: String? = null
        compose.runOnUiThread {
            host.get().setContent {
                Column {
                    Button(onClick = { view.value = overview }) { Text("Fit") }
                    Box(Modifier.size(280.dp)) {
                        StarCanvas(StarMapState(snapshot = map, view = view.value, photo = false, allFraming = true),
                            strings, onSize = { _, _ -> }, onTransform = { factor, x, y, panX, panY ->
                                view.value = view.value.transform(factor, x, y, panX, panY)
                            }, onFocus = {}, onPick = { _, _, _ -> }, onExcerpts = {}, onDrawn = {},
                            onSelect = { selected = it })
                    }
                }
            }
        }
        val field = compose.onNodeWithTag("star_canvas").assertIsDisplayed()
        compose.mainClock.autoAdvance = false
        try {
            field.performTouchInput { swipe(centerLeft + Offset(24f, 0f), centerRight - Offset(24f, 0f), durationMillis = 64) }
            compose.mainClock.advanceTimeBy(64)
            compose.runOnIdle { assertEquals(overview, view.value) }
            field.performTouchInput { click(topLeft + Offset(20f, 20f)) }
            compose.runOnIdle { assertNotEquals(overview, view.value) }
            compose.onNodeWithText("Fit").performClick()
            compose.mainClock.advanceTimeBy(500)
            field.performTouchInput { click(center) }
            compose.runOnIdle {
                assertEquals(overview, view.value)
                assertEquals(point.name, selected)
            }
        } finally {
            compose.mainClock.autoAdvance = true
        }
    }

    @Test
    fun drawerFollowsAnEligibleDragAndReleasesFocusOnlyAfterSlop() {
        createHost()
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
                        sheet = { Text("Navigation", modifier = Modifier.fillMaxWidth().testTag("drawer_sheet")) }) {
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
        root.performTouchInput { swipe(Offset(65.dp.toPx(), 180.dp.toPx()), Offset(115.dp.toPx(), 180.dp.toPx()), durationMillis = 48) }
        compose.runOnIdle { assertTrue(drawer.isOpen) }
        val sheet = compose.onNodeWithTag("drawer_sheet").getUnclippedBoundsInRoot().width
        assertTrue(kotlin.math.abs(sheet.value - minOf(root.getUnclippedBoundsInRoot().width.value * .78f, 296f)) <= 1f)
        root.performTouchInput { swipe(Offset(150.dp.toPx(), 180.dp.toPx()), Offset(95.dp.toPx(), 180.dp.toPx()), durationMillis = 48) }
        compose.runOnIdle { assertFalse(drawer.isOpen) }
    }

    @Test
    fun stopClearsWritingFocusAndManifestKeepsTheImeHiddenOnEntry() {
        createHost()
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
        createHost(inTouchMode = false)
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
        createHost()
        compose.runOnUiThread {
            host.get().setContent {
                val focus = LocalFocusManager.current
                CompositionLocalProvider(LocalLeaveWriting provides { focus.clearFocus() }) {
                    MoeriseTheme {
                        Column {
                            BasicTextField("", {}, modifier = Modifier.fillMaxWidth().height(48.dp).testTag("editor"))
                            TimelineNoteCard(TimelineEntry.Remote(Note("instance", "memos/test", "Remote body", "NORMAL", "hash",
                                createTime = "2026-11-26T10:00:00Z")), "UTC", strings, null, {}, {}, onEdit = {})
                            TimelineNoteCard(TimelineEntry.Local(DraftEntity("queued", "account", "Local body", 1, conflictJson = "{}",
                                createdAt = 1_795_620_000_000L)), "UTC", strings, null, {}, {})
                        }
                    }
                }
            }
        }
        val editor = compose.onNodeWithTag("editor")
        val card = hasAnyAncestor(hasTestTag("note_card_memos/test"))
        val action = compose.onNodeWithTag("note_more_memos/test")
        val actionBounds = action.getUnclippedBoundsInRoot()
        val date = compose.onNode(hasTestTag("note_card_date") and card, useUnmergedTree = true).getUnclippedBoundsInRoot()
        val body = compose.onNode(hasTestTag("note_card_body") and card, useUnmergedTree = true).getUnclippedBoundsInRoot()
        assertTrue(date.height < actionBounds.height)
        assertTrue(body.top - actionBounds.bottom <= 6.dp, "Body starts at ${body.top}, action ends at ${actionBounds.bottom}")
        assertEquals(44.dp, actionBounds.height)
        assertEquals(44.dp, compose.onNodeWithTag("queued_error_queued").getUnclippedBoundsInRoot().height)

        editor.performClick().assertIsFocused()
        action.performClick()
        compose.onNodeWithTag("edit_note_memos/test").performClick()
        editor.assertIsNotFocused()
    }

    @Test
    fun copyingBothCardKindsKeepsOriginalMarkdownWhitespaceAndPureTagLine() {
        createHost()
        val original = "  正文 **强调** #日记\n后文\n\n#想法\n "
        val remote = Note("instance", "memos/copy", original, "NORMAL", "hash", tags = listOf("日记", "想法"))
        val entry = mutableStateOf<TimelineEntry>(TimelineEntry.Local(DraftEntity("copy", "account", original, 1)))
        compose.runOnUiThread {
            host.get().setContent {
                MoeriseTheme { TimelineNoteCard(entry.value, "UTC", strings, null, {}, {}) }
            }
        }
        val clipboard = requireNotNull(host.get().getSystemService(ClipboardManager::class.java))
        for (id in listOf("copy", remote.name)) {
            if (id == remote.name) compose.runOnIdle { entry.value = TimelineEntry.Remote(remote) }
            compose.onNodeWithTag("note_more_$id").performClick()
            compose.onNodeWithTag("copy_note_$id").assertTextContains("Copy").performClick()
            compose.runOnIdle { assertEquals(original, clipboard.primaryClip?.getItemAt(0)?.text?.toString()) }
        }
    }

    @Test
    fun queuedTagsAreNotLinksAndBecomeInteractiveAfterConfirmation() {
        createHost()
        val content = "正文 #日记\n\n#想法"
        val remote = Note("instance", "memos/tags", content, "NORMAL", "hash", tags = listOf("日记", "想法"))
        val entry = mutableStateOf<TimelineEntry>(TimelineEntry.Local(DraftEntity("tags", "account", content, 1)))
        val selected = mutableListOf<String>()
        compose.runOnUiThread {
            host.get().setContent {
                MoeriseTheme { TimelineNoteCard(entry.value, "UTC", strings, null, { selected += it }, {}) }
            }
        }
        fun inlineLinks() = compose.onNodeWithText("正文 #日记", useUnmergedTree = true).fetchSemanticsNode()
            .config[SemanticsProperties.Text].flatMap { it.getLinkAnnotations(0, it.length) }
        assertTrue(inlineLinks().isEmpty())
        compose.onNodeWithText("#想法").assertHasNoClickAction()
        compose.runOnIdle { entry.value = TimelineEntry.Remote(remote) }
        val link = inlineLinks().single().item as LinkAnnotation.Clickable
        compose.runOnIdle { requireNotNull(link.linkInteractionListener).onClick(link) }
        compose.onNodeWithText("#想法").assertHasClickAction().performClick()
        compose.runOnIdle { assertEquals(listOf("日记", "想法"), selected) }
    }

    @Test
    fun markdownShowsShortAndLongFirstMountsAndKeepsSingleLineBreaks() {
        createHost()
        val paragraph = "A".repeat(65_531) + " end."
        val text = mutableStateOf(paragraph)
        compose.runOnUiThread {
            host.get().setContent { MoeriseTheme { MarkdownBody(text.value) } }
        }
        compose.onNodeWithText(paragraph).assertExists()
        compose.runOnIdle { text.value = "甲\n乙\n\n> 丙\n> 丁" }
        compose.waitUntil(5_000) { compose.onAllNodesWithText("丙\n丁").fetchSemanticsNodes().isNotEmpty() }
        compose.onNodeWithText("甲\n乙").assertExists()
    }

    @Test
    fun markdownListsKeepTopLevelAlignmentCompactSpacingAndClickableTags() {
        createHost()
        val input = mutableStateOf("前面一段\n- 列表项\n1.有序列表\n2、普通写法 #日记")
        val selected = mutableListOf<String>()
        compose.runOnUiThread {
            host.get().setContent {
                MoeriseTheme {
                    MarkdownBody(input.value, tags = listOf("日记"), onTag = { selected += it })
                }
            }
        }
        val intro = compose.onNodeWithText("前面一段").getUnclippedBoundsInRoot()
        val list = compose.onNodeWithText("列表项").getUnclippedBoundsInRoot()
        val ordinary = compose.onNodeWithText("1.有序列表\n2、普通写法 #日记")
        assertEquals(intro.left, ordinary.getUnclippedBoundsInRoot().left)
        assertTrue(list.left > intro.left)
        assertTrue(list.top - intro.bottom in 6.dp..10.dp)
        val text = ordinary.fetchSemanticsNode().config[SemanticsProperties.Text].single()
        val link = text.getLinkAnnotations(0, text.length).single().item as LinkAnnotation.Clickable
        compose.runOnIdle { requireNotNull(link.linkInteractionListener).onClick(link) }
        assertEquals(listOf("日记"), selected)
        compose.runOnIdle { input.value = "1. 甲\n2.\n3. 丙\n- `code\nnext`\n正文" }
        for (marker in listOf("1.", "2.", "3.")) compose.onNodeWithText(marker, substring = true).assertExists()
        val code = compose.onNode(hasText("code", substring = true) and hasText("next", substring = true))
        val codeText = code.fetchSemanticsNode().config[SemanticsProperties.Text].single().text
        assertFalse('`' in codeText)
        assertTrue(code.getUnclippedBoundsInRoot().left > intro.left)
        assertEquals(intro.left, compose.onNodeWithText("正文").getUnclippedBoundsInRoot().left)
    }

    @Test
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    fun walkRoomDirectoryOpensLongFirstNoteAtTopWithFixedActionsAndDateFooter() {
        createHost()
        val folder = Files.createTempDirectory("moerise-walk-directory").toFile()
        val database = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repository = NoteRepository(database)
        val secrets = MemorySecrets()
        val origin = "http://127.0.0.1:1"
        val account = "$origin\u0000instance\u0000users/owner"
        val attachment = CoreJson.parseToJsonElement("""{"filename":"notes.pdf"}""") as kotlinx.serialization.json.JsonObject
        val longBody = (1..30).joinToString("\n\n") { "Long first note, paragraph $it" }
        val candidate = Note("instance", "memos/long", longBody, "NORMAL", "hash", createTime = "2026-11-14T20:00:00Z")
        val job = SupervisorJob()
        val scope = CoroutineScope(job + Dispatchers.Main.immediate)
        runBlocking {
            secrets.write("selected-profile", CoreJson.encodeToString(
                ConnectionProfile(origin, account, "instance", "users/owner", "Owner", "Pacific/Kiritimati")))
            for (index in 1..6) repository.recordLocalNote(account, candidate.copy(
                name = "memos/file-$index", content = "", createTime = "2026-11-16T00:00:00Z", attachments = listOf(attachment)))
            for (index in 1..3) repository.recordLocalNote(account, candidate.copy(
                name = "memos/body-$index", content = "**Substantive body $index** snake_case", createTime = "2026-11-15T00:00:00Z"))
            repository.recordLocalNote(account, candidate)
        }
        val session = SessionController(secrets, true, scope)
        try {
            val mobile = MobileController(session, { repository }, scope)
            waitForMainCondition("the cached account is restored") { session.state.value.profile != null && !session.state.value.loading }
            compose.runOnIdle { mobile.page("walk") }
            compose.runOnUiThread {
                host.get().setContent {
                    MoeriseTheme {
                        Box(Modifier.width(340.dp).height(320.dp)) {
                            DiscoveryScreen("walk", mobile, strings, {}, {}, {}, {})
                        }
                    }
                }
            }
            waitForMainCondition("Room supplies six priority-ranked candidates") { mobile.walk.state.value.floaters.size == 6 }
            assertEquals(listOf("body-3", "body-2", "body-1", "long", "file-6", "file-5"),
                mobile.walk.state.value.floaters.map { it.name.substringAfter('/') })
            assertEquals(4, compose.onAllNodes(hasTestTagPrefix("walk_floater_")).fetchSemanticsNodes().size)
            compose.onNodeWithTag("walk_end").assertDoesNotExist()
            val start = compose.onNodeWithTag("walk_start")
            val firstRow = compose.onNodeWithTag("walk_floater_memos/body-3")
            val lastRow = compose.onNodeWithTag("walk_floater_memos/long")
            firstRow.assert(hasContentDescription("Start from this note: Substantive body 3 snake_case"))
            assertTrue(firstRow.getUnclippedBoundsInRoot().top >= start.getUnclippedBoundsInRoot().bottom + 20.dp)
            assertEquals(firstRow.getUnclippedBoundsInRoot().left, lastRow.getUnclippedBoundsInRoot().left)
            assertEquals(firstRow.getUnclippedBoundsInRoot().width, lastRow.getUnclippedBoundsInRoot().width)
            lastRow.performScrollTo().performClick()
            waitForMainCondition("the selected long note opens") { mobile.walk.state.value.result?.item?.note?.name == candidate.name }
            val intro = compose.onNodeWithText("Long first note, paragraph 1")
            intro.assertIsDisplayed()
            assertTrue(intro.getUnclippedBoundsInRoot().top < 155.dp)
            val stop = compose.onNodeWithTag("walk_end")
            val fixed = stop.getUnclippedBoundsInRoot()
            assertButtonPalette(stop, MoeriseColors.Surface, MoeriseColors.Line)
            val open = compose.onNodeWithTag("discovery_open")
            open.performScrollTo().assertIsDisplayed().assertTextEquals("View on home")
            assertTrue(open.getUnclippedBoundsInRoot().height >= 44.dp)
            compose.onNodeWithText("2026-11-15").assertIsDisplayed()
            compose.onNodeWithText("2026-11-15 10:00").assertDoesNotExist()
            assertEquals(fixed, stop.getUnclippedBoundsInRoot())
            compose.onNodeWithTag("walk_next").performClick()
            waitForMainCondition("the next note retains the previous note as its trail") { mobile.walk.state.value.previous?.name == candidate.name }
            compose.onNodeWithText("↑").assertIsDisplayed()
            compose.onNodeWithText(walkSummaryBody(longBody, emptyList()).let(::limitWalkSummary)).assertIsDisplayed()
            assertEquals(fixed, stop.getUnclippedBoundsInRoot())
            stop.performClick().assertDoesNotExist()
            runBlocking {
                for (index in 1..20) repository.recordLocalNote(account, candidate.copy(
                    name = "memos/tag-only-$index", content = "#日记", tags = listOf("日记"),
                    createTime = "2026-11-17T00:00:00Z"))
            }
            compose.runOnIdle { mobile.walk.retryFloaters() }
            waitForMainCondition("the latest twenty notes leave the offline directory empty") {
                mobile.walk.state.value.floaters.isEmpty() && mobile.walk.state.value.hasLocalNotes
            }
            compose.onNodeWithText("Start from a note").assertDoesNotExist()
            start.assertIsEnabled().performClick()
            waitForMainCondition("offline random start still reads a local note") { mobile.walk.state.value.result?.path?.size == 1 }
            compose.onNodeWithTag("walk_next").assertIsEnabled().performClick()
            waitForMainCondition("offline continue still reads another local note") { mobile.walk.state.value.result?.path?.size == 2 }
        } finally {
            compose.runOnUiThread { session.close(); job.cancel() }
            compose.waitForIdle()
            database.close()
            folder.deleteRecursively()
        }
    }

    private fun hasTestTagPrefix(prefix: String) = SemanticsMatcher("test tag starts with $prefix") {
        it.config.getOrElse(SemanticsProperties.TestTag) { "" }.startsWith(prefix)
    }

    private fun assertButtonPalette(node: SemanticsNodeInteraction, fill: Color, frame: Color) {
        withNodeBitmap(node) { pixels ->
            assertTrue((0 until pixels.width).any { x -> Color(pixels.getPixel(x, pixels.height / 4)) == fill })
            assertTrue((0 until pixels.width).any { x -> Color(pixels.getPixel(x, pixels.height / 2)) == frame })
        }
    }

    private fun activityBitmap(): Bitmap = compose.runOnIdle {
        val window = host.get().window.decorView
        Bitmap.createBitmap(window.width, window.height, Bitmap.Config.ARGB_8888).apply {
            window.draw(android.graphics.Canvas(this))
        }
    }

    private fun <Result> withNodeBitmap(node: SemanticsNodeInteraction, read: (Bitmap) -> Result): Result {
        val bounds = node.fetchSemanticsNode().boundsInWindow
        val frame = activityBitmap()
        try {
            val pixels = Bitmap.createBitmap(frame, bounds.left.toInt(), bounds.top.toInt(), bounds.width.toInt(), bounds.height.toInt())
            try { return read(pixels) }
            finally { if (pixels !== frame) pixels.recycle() }
        } finally { frame.recycle() }
    }

    @Test
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    fun onlineWalkFallbackAndReviewUseTheSameDirectoryAndActionStyles() {
        createHost()
        val folder = Files.createTempDirectory("moerise-online-directory").toFile()
        val database = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repository = NoteRepository(database)
        val job = SupervisorJob()
        val scope = CoroutineScope(job + Dispatchers.Main.immediate)
        val session = SessionController(MemorySecrets(), true, scope)
        val timezone = "Asia/Hong_Kong"
        val beforeMidnight = "2026-10-29T15:55:00Z"
        val afterMidnight = Instant.parse("2026-10-29T16:05:00Z")
        assertEquals("2026-10-29", displayNoteDay(beforeMidnight, timezone, english = true))
        assertEquals("2026-10-30", displayNoteDay(afterMidnight.toString(), timezone, english = true))
        assertEquals(0L, elapsedNoteDays(beforeMidnight, afterMidnight.toEpochMilli()))
        val recent = Note("instance", "memos/recent", "Review first paragraph", "NORMAL", "hash",
            createTime = Instant.ofEpochMilli(epochMillis() - 600_000).toString())
        val files = (1..6).map { index -> recent.copy(name = "memos/file-$index", content = "",
            attachments = listOf(CoreJson.parseToJsonElement("""{"filename":"file-$index.pdf"}""") as kotlinx.serialization.json.JsonObject)) }
        val candidates = files + listOf(recent.copy(name = "memos/short", content = "短句\n#未知"), recent)
        val review = ReviewBatch("day", "2026-10-30", timezone, listOf(
            ReviewItem(recent.name, recent.sourceHash, recent.createTime, unavailable = false, note = recent),
            ReviewItem("memos/missing", "deleted", recent.createTime, unavailable = true),
        ))
        val syncAvailable = java.util.concurrent.atomic.AtomicBoolean()
        val syncedNotes = listOf(recent, recent.copy(name = "memos/synced", content = "Synced while the walk was open"))
        val server = MockWebServer().apply {
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse = when (request.requestUrl?.encodedPath) {
                    "/api/app/v1/capabilities" -> MockResponse().setBody("""{"login_methods":["password"],"notes":"ready"}""")
                    "/api/app/v1/session" -> MockResponse().setBody("""{"instance_id":"instance","user":{"name":"users/owner"},"timezone":"$timezone","expires_at":"2099-01-01T00:00:00Z","csrf_token":"token"}""")
                    "/api/app/v1/models" -> MockResponse().setBody("""{"embedding":{"enabled":true,"configured":true}}""")
                    "/api/app/v1/notes" -> {
                        assertEquals("20", request.requestUrl?.queryParameter("page_size"))
                        assertEquals("NORMAL", request.requestUrl?.queryParameter("state"))
                        MockResponse().setBody(CoreJson.encodeToString(NotePage(candidates)))
                    }
                    "/api/app/v1/sync/notes" -> if (syncAvailable.get()) {
                        MockResponse().setBody(CoreJson.encodeToString(SyncNotesResponse(
                            mode = "full", items = syncedNotes, nextCursor = "synced", hasMore = false, syncGeneration = "walk-test")))
                    } else MockResponse().setResponseCode(404)
                    "/api/app/v1/reviews/daily" -> MockResponse().setBody(CoreJson.encodeToString(review))
                    else -> MockResponse().setResponseCode(404)
                }
            }
            start()
        }
        try {
            val page = mutableStateOf("walk")
            val mobile = MobileController(session, { repository }, scope)
            compose.runOnIdle { session.login(server.url("/").toString(), "owner", "test-password") }
            waitForMainCondition("the Core test account is connected") { session.state.value.connected }
            compose.runOnUiThread {
                mobile.page("walk")
                host.get().setContent {
                    MoeriseTheme {
                        if (page.value == "settings") SettingsScreen(mobile, strings, {}, {})
                        else DiscoveryScreen(page.value, mobile, strings, {}, {}, {}, {})
                    }
                }
            }
            waitForMainCondition("online candidates load after the empty Room query") { mobile.walk.state.value.floaters.size == 6 }
            assertEquals(listOf("memos/recent", "memos/short") + files.take(4).map { it.name },
                mobile.walk.state.value.floaters.map { it.name })
            compose.onNodeWithTag("walk_floater_memos/short").assert(hasContentDescription("Start from this note: 短句 #未知"))
            compose.onNodeWithTag("walk_floater_memos/file-1").assert(hasContentDescription("Start from this note: file-1.pdf"))
            compose.onNodeWithTag("walk_end").assertDoesNotExist()
            assertFalse(mobile.walk.state.value.hasLocalNotes)
            val account = requireNotNull(session.state.value.profile).accountKey
            syncAvailable.set(true)
            runBlocking { repository.syncNotes(account, requireNotNull(session.core)) }
            assertFalse(mobile.walk.state.value.hasLocalNotes)
            compose.runOnIdle { session.connectionFailed(account) }
            waitForMainCondition("an open walk refreshes the newly synced Room notes on disconnect") {
                val walk = mobile.walk.state.value
                walk.offline && walk.hasLocalNotes && walk.floaters.map { it.name }.toSet() == syncedNotes.map { it.name }.toSet()
            }
            compose.onNodeWithTag("walk_start").assertIsEnabled().performClick()
            waitForMainCondition("offline random start uses the synced notes") { mobile.walk.state.value.result?.path?.size == 1 }
            compose.onNodeWithTag("walk_next").assertIsEnabled().performClick()
            waitForMainCondition("offline continue uses the other synced note") {
                mobile.walk.state.value.result?.path?.toSet() == syncedNotes.map { it.name }.toSet()
            }
            assertTrue(runBlocking { session.checkConnection() })
            compose.runOnIdle { page.value = "review"; mobile.page("review") }
            waitForMainCondition("the review batch loads") { mobile.review.state.value.batch?.id == review.id }
            val paragraph = compose.onNodeWithText("Review first paragraph")
            val date = compose.onNodeWithText("Today")
            assertTrue(paragraph.getUnclippedBoundsInRoot().bottom < date.getUnclippedBoundsInRoot().top)
            compose.onNodeWithTag("discovery_open").assertTextEquals("View on home")
            assertButtonPalette(compose.onNodeWithTag("review_walk"), MoeriseColors.Surface, MoeriseColors.Line)
            compose.onNodeWithTag("review_item_1").performClick()
            compose.onNodeWithTag("discovery_open").assertDoesNotExist()
            compose.runOnIdle { page.value = "settings"; mobile.page("settings") }
            val reload = compose.onNodeWithTag("settings_reload")
            compose.waitUntil(10_000) { reload.isDisplayed() }
            assertButtonPalette(reload, MoeriseColors.Surface, MoeriseColors.Line)
            assertButtonPalette(compose.onNodeWithTag("settings_sync"), MoeriseColors.Surface, MoeriseColors.Line)
            assertButtonPalette(compose.onNodeWithTag("account_logout").performScrollTo(), MoeriseColors.Surface, MoeriseColors.Line)
        } finally {
            compose.runOnUiThread { session.close(); job.cancel() }
            compose.waitForIdle()
            server.shutdown()
            database.close()
            folder.deleteRecursively()
        }
    }

    @Test
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    fun confirmedTagMetadataKeepsTheDecodedInlineImageOnTheFirstFrame() =
        checkImageHandoff(promoteToTail = false)

    @Test
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    fun longImageBlockKeepsTheDecodedImageWhenPromotedToTheTail() =
        checkImageHandoff(promoteToTail = true)

    private fun checkImageHandoff(promoteToTail: Boolean) {
        createHost()
        val origin = "http://127.0.0.1:1"
        val bitmap = Bitmap.createBitmap(20, 20, Bitmap.Config.ARGB_8888)
        bitmap.eraseColor(android.graphics.Color.RED)
        val png = ByteArrayOutputStream().use { stream ->
            assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, stream))
            stream.toByteArray()
        }
        bitmap.recycle()
        val client = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session")) {
                respond("""{"instance_id":"instance","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2099-01-01T00:00:00Z","csrf_token":"token"}""")
            } else {
                respond(png)
            }
        })
        val account = "$origin\u0000instance\u0000users/owner"
        val attachment = CoreJson.parseToJsonElement(
            """{"name":"attachments/pic","filename":"protected.png","type":"image/png","content_url":"/api/app/v1/notes/one/attachments/pic/content"}""",
        ) as kotlinx.serialization.json.JsonObject
        val text = if (promoteToTail) "New text ".repeat(510).trimEnd() else "New text"
        val tagLine = if (promoteToTail) "#work.log" else "#pending"
        val breakBeforeTag = if (promoteToTail) "\n\n" else "\n"
        val body = "![protected](/file/attachments/pic/protected.png)\n$text$breakBeforeTag$tagLine"
        val baseline = Note("instance", "memos/one", "Old text", "NORMAL", "before", attachments = listOf(attachment))
        val confirmed = baseline.copy(content = body, sourceHash = "after", tags = if (promoteToTail) listOf("work.log") else emptyList())
        val draft = DraftEntity("one", account, body, 1, baseline.name, baseline.sourceHash, baselineJson = CoreJson.encodeToString(baseline))
        val entry = mutableStateOf<TimelineEntry>(TimelineEntry.Local(draft))
        val allowTailParse = CompletableDeferred<Unit>()
        val image = compose.onNode(hasTestTag("decoded_attachment_image") and hasContentDescription("protected.png") and
            hasAnyAncestor(hasTestTag("note_markdown")), useUnmergedTree = true)
        val tagInBody = hasText(tagLine, substring = true) and hasAnyAncestor(hasTestTag("note_markdown"))
        val textInBody = hasText(text, substring = true) and hasAnyAncestor(hasTestTag("note_markdown"))
        try {
            compose.runOnUiThread {
                host.get().setContent {
                    val parse = LocalParseMarkdownTail.current
                    CompositionLocalProvider(LocalParseMarkdownTail provides { content, parser, references ->
                        allowTailParse.await()
                        parse(content, parser, references)
                    }) {
                        MoeriseTheme { TimelineNoteCard(entry.value, "UTC", strings, client, {}, {}) }
                    }
                }
            }
            compose.waitUntil(10_000) { image.isDisplayed() }
            if (promoteToTail) {
                compose.onNode(tagInBody, useUnmergedTree = true).assertExists()
                compose.runOnIdle { entry.value = TimelineEntry.Remote(confirmed) }
                compose.waitUntil(10_000) { compose.onAllNodes(tagInBody, useUnmergedTree = true).fetchSemanticsNodes().isEmpty() }
            } else {
                compose.onNode(tagInBody, useUnmergedTree = true).assertDoesNotExist()
                compose.mainClock.autoAdvance = false
                compose.runOnIdle {
                    entry.value = TimelineEntry.Remote(confirmed)
                    Snapshot.sendApplyNotifications()
                    shadowOf(Looper.getMainLooper()).idle()
                }
                compose.mainClock.advanceTimeByFrame()
                compose.onNodeWithTag("note_card_${confirmed.name}", useUnmergedTree = true).assertExists()
                compose.onNode(tagInBody, useUnmergedTree = true).assertExists()
            }
            image.assertIsDisplayed()
            compose.mainClock.autoAdvance = true
            allowTailParse.complete(Unit)
            if (!promoteToTail) {
                compose.waitUntil(10_000) { compose.onAllNodes(tagInBody, useUnmergedTree = true).fetchSemanticsNodes().size == 1 }
            }
            compose.onNode(textInBody, useUnmergedTree = true).assertExists()
            image.assertIsDisplayed()
        } finally {
            allowTailParse.complete(Unit)
            compose.mainClock.autoAdvance = true
            client.close()
        }
    }

    @Test
    fun attachmentResultAfterStopReturnsToTheComposerAndCanSaveWithoutText() =
        checkAttachmentResult(recreate = false)

    @Test
    fun attachmentResultDeliveredAfterRecreationWaitsForDraftRestoration() =
        checkAttachmentResult(recreate = true)

    @Test
    fun failedEditRestorationReleasesThePickerForTheComposer() =
        checkAttachmentResult(recreate = true, inlineRestoreFails = true)

    private fun checkAttachmentResult(recreate: Boolean, inlineRestoreFails: Boolean = false) {
        createHost()
        val folder = Files.createTempDirectory("moerise-picker").toFile()
        val db = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val picked = Uri.fromFile(folder.resolve("only.txt").apply { writeText("attachment only") })
        val context = host.get().applicationContext
        val secrets = MemorySecrets()
        val origin = "http://127.0.0.1:1"
        val account = "$origin\u0000instance\u0000users/owner"
        val library = androidAttachmentFiles(context)
        val repo = NoteRepository(db, library)
        runBlocking { secrets.write("selected-profile", CoreJson.encodeToString(ConnectionProfile(origin, account,
            "instance", "users/owner", "Owner", "UTC"))) }
        var job = SupervisorJob()
        var scope = CoroutineScope(Dispatchers.Main.immediate + job)
        var session = SessionController(secrets, true, scope)
        var mobile = MobileController(session, { repo }, scope, initialForeground = false)
        val restore = CompletableDeferred<Unit>()
        var staged: StagedAttachment? = null
        fun showApp(controller: MobileController) {
            host.get().setContent { MoeriseApp(controller) }
        }
        fun deliver(requestCode: Int) {
            val result = Intent().apply { clipData = ClipData.newRawUri("only.txt", picked) }
            assertTrue(host.get().activityResultRegistry.dispatchResult(requestCode, Activity.RESULT_OK, result))
        }
        fun nextPickerRequest(): Int {
            val launched = requireNotNull(shadowOf(host.get()).nextStartedActivityForResult)
            assertEquals(Intent.ACTION_OPEN_DOCUMENT, launched.intent.action)
            return launched.requestCode
        }
        try {
            compose.runOnUiThread { showApp(mobile) }
            compose.waitUntil(10_000) { compose.onAllNodesWithTag("compose_capsule").fetchSemanticsNodes().size == 1 }
            val field = compose.onNodeWithTag("quick_editor")
            val inline = Note("instance", "memos/inline", "Original", "NORMAL", "hash")
            val target = if (inlineRestoreFails) {
                compose.runOnIdle { mobile.editNote(inline) }
                val tag = "note_editor_${inline.name}"
                compose.waitUntil(10_000) { compose.onAllNodesWithTag(tag).fetchSemanticsNodes().size == 1 }
                compose.waitUntil(10_000) { runBlocking { repo.feature(account, ACTIVE_NOTE_EDITOR)?.contains(inline.name) == true } }
                compose.onNodeWithTag(tag)
            } else {
                compose.onNodeWithTag("compose_capsule").performClick()
                compose.waitUntil(10_000) { compose.onAllNodesWithTag("quick_editor").fetchSemanticsNodes().size == 1 }
                field
            }
            target.performClick().assertIsFocused()
            compose.onNodeWithTag("add_attachment").assertIsEnabled()
                .performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            if (inlineRestoreFails) runBlocking {
                repo.putFeature(account, ACTIVE_NOTE_EDITOR, "[]")
            }
            val requestCode = compose.runOnUiThread { nextPickerRequest() }
            compose.runOnUiThread {
                if (recreate) {
                    host.recreate().visible()
                    job.cancel()
                    session.close()
                    job = SupervisorJob()
                    scope = CoroutineScope(Dispatchers.Main.immediate + job)
                    session = SessionController(secrets, true, scope)
                    val restoredMobile = MobileController(session, { restore.await(); repo }, scope, initialForeground = false)
                    mobile = restoredMobile
                    showApp(restoredMobile)
                } else {
                    host.pause().stop()
                    deliver(requestCode)
                    host.restart().start().resume().visible()
                }
                shadowOf(Looper.getMainLooper()).idle()
            }
            if (recreate) {
                compose.waitForIdle()
                compose.runOnIdle {
                    assertNull(mobile.editor.state.value.draft)
                    deliver(requestCode)
                    restore.complete(Unit)
                }
            }
            if (inlineRestoreFails) {
                waitForMainCondition("inline editor restoration fails with the composer still available") {
                    assertNull(mobile.editor.state.value.error)
                    mobile.localError.value == "LOCAL_READ_FAILED"
                }
                compose.waitForIdle()
                assertTrue(mobile.editor.state.value.draft?.attachmentEdits()?.staged?.isEmpty() == true)
                compose.onNodeWithTag("compose_capsule").performClick()
                compose.waitUntil(10_000) { compose.onAllNodesWithTag("quick_editor").fetchSemanticsNodes().size == 1 }
                field.performClick().assertIsFocused()
                compose.onNodeWithTag("add_attachment").assertIsEnabled()
                    .performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
                compose.runOnUiThread {
                    val next = nextPickerRequest()
                    host.pause().stop()
                    deliver(next)
                    host.restart().start().resume().visible()
                    shadowOf(Looper.getMainLooper()).idle()
                }
            }
            waitForMainCondition("the picked file is staged in the composer") {
                val state = mobile.editor.state.value
                assertNull(state.error)
                state.draft?.attachmentEdits()?.staged?.size == 1 && !state.importing
            }
            staged = mobile.editor.state.value.draft?.attachmentEdits()?.staged?.single()
            compose.waitUntil(10_000) {
                compose.onAllNodes(hasTestTag("quick_save") and isEnabled()).fetchSemanticsNodes().size == 1
            }
            compose.onNodeWithTag("quick_save").assertIsEnabled().performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            waitForMainCondition("the attachment-only note is queued") {
                mobile.sync.state.value.notes.singleOrNull()?.attachmentEdits()?.staged?.size == 1
            }
            val saved = mobile.sync.state.value.notes.single()
            assertEquals("", saved.content)
            assertEquals(assertNotNull(staged).id, saved.attachmentEdits().staged.single().id)
            assertEquals("{}", runBlocking { repo.feature(account, ACTIVE_NOTE_EDITOR) })
        } finally {
            compose.runOnUiThread { job.cancel(); session.close() }
            compose.waitForIdle()
            runBlocking { staged?.let { library.remove(it) } }
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun sidebarHasDirectArchiveAndSingleAccessibleSettingsRow() {
        createHost()
        val scope = CoroutineScope(SupervisorJob())
        val session = SessionController(MemorySecrets(), true, scope)
        try {
            val privacy = PrivacyController(null, session, scope)
            var destination: String? = null
            compose.runOnUiThread {
                host.get().setContent {
                    MoeriseTheme {
                        Row {
                            Box(Modifier.weight(1f)) {
                                WebSidebar("home", strings, "Owner", SearchState(), "2026-11",
                                    onClose = {}, onNavigate = { destination = it }, onMonth = {}, onDate = {}, onTag = {})
                            }
                            Box(Modifier.weight(1f)) { PrivacySettings(privacy, strings) }
                        }
                    }
                }
            }
            compose.onNodeWithTag("nav_archive").assertExists()
            compose.onNodeWithTag("nav_settings").assert(hasContentDescription("Settings") and hasText("Owner")).performClick()
            assertEquals("settings", destination)
            compose.onNodeWithTag("account_logout").assertDoesNotExist()
            compose.onNodeWithTag("sidebar_sync").assertDoesNotExist()
            compose.onNodeWithTag("account_menu").assertDoesNotExist()
            compose.onNodeWithTag("diagnostic_summary").assertIsEnabled()
        } finally { session.close(); scope.cancel() }
    }

    @Test
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    fun sidebarSelectionsAndCalendarMarkersUseWarmPaperColors() {
        createHost()
        val selectedDay = mutableStateOf("")
        val page = mutableStateOf("home")
        val filters = SearchState(tags = listOf(TagCount("selected", 3)),
            calendar = CalendarPage("2026-11", listOf(DayCount("2026-11-16", 1), DayCount("2026-11-17", 2))))
        compose.runOnUiThread {
            host.get().setContent {
                MoeriseTheme {
                    Box(Modifier.width(300.dp).background(MoeriseColors.Paper)) {
                        WebSidebar(page.value, strings, "Owner", filters, "2026-11", {}, { page.value = it },
                            {}, { selectedDay.value = it }, {}, selectedTags = setOf("selected"),
                            selectedDate = selectedDay.value, today = "2026-11-16")
                    }
                }
            }
        }
        fun background(node: SemanticsNodeInteraction) = withNodeBitmap(node) { Color(it.getPixel(it.width / 2, 2)) }
        val today = compose.onNodeWithContentDescription("2026-11-16, 1 notes")
        val medium = compose.onNodeWithContentDescription("2026-11-17, 2 notes")
        val light = background(today)
        val shaded = background(medium)
        assertEquals(Color(0xFFE3E7E3), light)
        assertEquals(Color(0xFFCCD7D3), shaded)
        for (cell in listOf(light, shaded)) {
            assertTrue((cell.luminance() + .05f) / (MoeriseColors.Accent.luminance() + .05f) >= 4.5f)
        }
        fun dot() = withNodeBitmap(today) { Color(it.getPixel(it.width / 2, it.height - 4)) }
        assertEquals(MoeriseColors.Accent, dot())
        today.performClick()
        assertEquals(MoeriseColors.Accent, background(today))
        assertEquals(MoeriseColors.Surface, dot())
        assertEquals(MoeriseColors.SurfaceMuted, background(compose.onNodeWithTag("nav_home").assertIsSelected()))
        val settings = compose.onNodeWithTag("nav_settings")
        settings.performClick().assertIsSelected()
        assertEquals(MoeriseColors.SurfaceMuted, withNodeBitmap(settings) { Color(it.getPixel(it.width / 2, it.height - 2)) })
        val tag = compose.onNodeWithText("#selected").performScrollTo().assertIsSelected()
        assertEquals(MoeriseColors.SurfaceMuted, background(tag))
    }

    @Test
    fun longNoteCardMeasuresTheBodyBeforeClippingIt() {
        createHost()
        val content = (1..22).joinToString("\n\n") { "Paragraph number $it" }
        val note = Note("instance", "memos/long", content, "NORMAL", "hash")
        compose.runOnUiThread {
            host.get().setContent {
                val expanded = remember { mutableStateOf(false) }
                MoeriseTheme {
                    Column(Modifier.verticalScroll(rememberScrollState())) {
                        TimelineNoteCard(TimelineEntry.Remote(note), "UTC", strings, null, {}, {},
                            expanded = expanded.value, onExpand = { expanded.value = it })
                    }
                }
            }
        }
        val body = compose.onNodeWithTag("note_card_body")
        val expand = compose.onNodeWithText("Expand")
        compose.waitUntil(10_000) { expand.isDisplayed() }
        assertTrue(body.getUnclippedBoundsInRoot().height in 316.dp..330.dp)
        expand.performClick()
        compose.waitUntil(10_000) { body.getUnclippedBoundsInRoot().height > 400.dp }
        compose.onNodeWithText("Collapse").performScrollTo().performClick()
        compose.waitUntil(10_000) { body.getUnclippedBoundsInRoot().height <= 350.dp }
    }

    @Test
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    fun reviewKeepsTheIncomingInkCoveredDuringTheOutgoingFade() {
        createHost()
        val position = mutableStateOf(0)
        compose.runOnUiThread {
            host.get().setContent {
                Box(Modifier.size(280.dp, 200.dp).testTag("review_frames")) {
                    ReviewStack("batch", 2, position.value, remember { mutableFloatStateOf(0f) }, { position.value = it }) { index, layer ->
                        Box(layer.fillMaxSize().background(if (index == 0) Color.Red else Color.Blue))
                    }
                }
            }
        }
        val stack = compose.onNodeWithTag("review_frames").assertIsDisplayed()
        compose.mainClock.autoAdvance = false
        try {
            compose.runOnIdle {
                position.value = 1
                Snapshot.sendApplyNotifications()
                shadowOf(Looper.getMainLooper()).idle()
            }
            compose.mainClock.advanceTimeByFrame()
            compose.mainClock.advanceTimeBy(208)
            val bounds = stack.fetchSemanticsNode().boundsInWindow
            val frame = activityBitmap()
            try {
                val overlap = Color(frame.getPixel((bounds.left + bounds.width / 12).toInt(), bounds.center.y.toInt()))
                val exposed = Color(frame.getPixel((bounds.left + bounds.width * 3 / 4).toInt(), bounds.center.y.toInt()))
                assertTrue(overlap.red > .95f && overlap.green in .1f.. .9f, "Overlapping pixel: $overlap, exposed pixel: $exposed")
                assertEquals(overlap.green, overlap.blue, .02f)
                assertTrue(exposed.blue > .9f && exposed.red < .1f, "Exposed pixel: $exposed")
            } finally { frame.recycle() }
        } finally { compose.mainClock.autoAdvance = true }
    }

    @Test
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    fun decodedAttachmentGrowthFoldsTheWholeCard() {
        createHost()
        val bitmap = Bitmap.createBitmap(220, 240, Bitmap.Config.ARGB_8888)
        val png = ByteArrayOutputStream().use { output ->
            assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, output)); output.toByteArray()
        }
        bitmap.recycle()
        val release = CompletableDeferred<Unit>()
        val origin = "https://image-growth.example"
        val client = CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            if (request.url.encodedPath.endsWith("/session"))
                respond("""{"instance_id":"instance","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2099-01-01T00:00:00Z","csrf_token":"token"}""")
            else { release.await(); respond(png) }
        }).also { client -> runBlocking { client.bindAccount("$origin\u0000instance\u0000users/owner") } }
        val item = CoreJson.parseToJsonElement("""{"name":"attachments/square","type":"image/png","filename":"image.png"}""")
            as kotlinx.serialization.json.JsonObject
        val content = (1..8).joinToString("\n\n") { "Paragraph $it" }
        val note = Note("instance", "memos/growth", content, "NORMAL", "before", tags = listOf("end"), attachments = listOf(item))
        try {
            compose.runOnUiThread {
                host.get().setContent {
                    val expanded = remember { mutableStateOf(false) }
                    MoeriseTheme {
                        Column(Modifier.verticalScroll(rememberScrollState())) {
                            TimelineNoteCard(TimelineEntry.Remote(note), "UTC", strings, client, {}, {},
                                expanded = expanded.value, onExpand = { expanded.value = it })
                        }
                    }
                }
            }
            val body = compose.onNodeWithTag("note_card_body")
            compose.onNodeWithText("Expand").assertDoesNotExist()
            release.complete(Unit)
            val expand = compose.onNodeWithText("Expand")
            compose.waitUntil(10_000) { expand.isDisplayed() }
            assertTrue(body.getUnclippedBoundsInRoot().height in 316.dp..330.dp)
            expand.performClick()
            compose.onNodeWithText("#end").performScrollTo().assertIsDisplayed()
            assertTrue(body.getUnclippedBoundsInRoot().height > 400.dp)
        } finally { release.complete(Unit); client.close() }
    }

    @Test
    fun failedLoginKeepsTheMaskedPasswordAndSuccessClearsIt() {
        createHost()
        val release = CountDownLatch(1)
        val attempts = java.util.concurrent.atomic.AtomicInteger()
        val server = MockWebServer().apply {
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse = when (request.path) {
                    "/api/app/v1/capabilities" -> MockResponse().setBody("""{"login_methods":["password"],"notes":"ready"}""")
                    "/api/app/v1/session" -> {
                        val credentials = CoreJson.parseToJsonElement(request.body.readUtf8())
                        assertEquals(CoreJson.parseToJsonElement("""{"username":"owner","password":"fixture-pass"}"""), credentials)
                        if (attempts.incrementAndGet() == 1) {
                            check(release.await(10, TimeUnit.SECONDS))
                            MockResponse().setResponseCode(401).setBody("""{"error":{"code":"INVALID_CREDENTIALS"}}""")
                        } else MockResponse().setBody("""{"instance_id":"instance","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2099-01-01T00:00:00Z","csrf_token":"token"}""")
                    }
                    else -> MockResponse().setResponseCode(404)
                }
            }
            start()
        }
        val job = SupervisorJob()
        val scope = CoroutineScope(job + Dispatchers.Main.immediate)
        val session = SessionController(MemorySecrets(), true, scope)
        try {
            var connected = false
            compose.runOnUiThread { host.get().setContent { MoeriseTheme { ConnectionScreen(session, strings) { connected = true } } } }
            compose.onNodeWithTag("server_origin").performTextReplacement(server.url("/").toString().removeSuffix("/"))
            compose.onNodeWithTag("server_username").performTextReplacement(" owner ")
            val password = compose.onNodeWithTag("server_password")
            password.performTextReplacement("fixture-pass")
            compose.onNodeWithTag("toggle_password").performClick()
            password.assertTextContains("fixture-pass")
            compose.onNodeWithTag("server_login").performScrollTo().performClick()
            waitForMainCondition("login is busy") { session.state.value.busy }
            password.assertIsNotEnabled().assert(SemanticsMatcher.keyIsDefined(SemanticsProperties.Password))
            release.countDown()
            waitForMainCondition("login was rejected") { session.state.value.error == "INVALID_CREDENTIALS" }
            compose.onNodeWithTag("server_password_filled", useUnmergedTree = true).assertExists()
            compose.onNodeWithTag("toggle_password").performClick()
            password.assertTextContains("fixture-pass")
            compose.onNodeWithTag("server_login").performScrollTo().performClick()
            waitForMainCondition("login succeeds") { connected }
            compose.onNodeWithTag("server_password_empty", useUnmergedTree = true).assertExists()
        } finally {
            release.countDown()
            compose.runOnUiThread { session.close(); job.cancel() }
            server.shutdown()
        }
    }

    @Test
    fun coldStartWithAnActiveEditLoadsTheTimelineBeforeReportingTheCapsuleReady() {
        createHost()
        val folder = Files.createTempDirectory("moerise-restored-editor").toFile()
        val database = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repository = NoteRepository(database)
        val secrets = MemorySecrets()
        val origin = "http://127.0.0.1:1"
        val account = "$origin\u0000instance\u0000users/owner"
        val note = Note("instance", "memos/restored", "Restored draft", "NORMAL", "hash")
        val job = SupervisorJob()
        val scope = CoroutineScope(job + Dispatchers.Main.immediate)
        runBlocking {
            secrets.write("selected-profile", CoreJson.encodeToString(ConnectionProfile(origin, account, "instance", "users/owner", "Owner", "UTC")))
            database.dao().putSyncState(SyncStateEntity(account, "cursor", "generation", true, 1))
            repository.recordLocalNote(account, note)
            val draft = repository.restore(account, note).draft
            repository.putFeature(account, ACTIVE_NOTE_EDITOR, """{"kind":"edit","draft_id":"${draft.id}","key":"${note.name}"}""")
        }
        val session = SessionController(secrets, true, scope)
        try {
            var reported = 0
            val mobile = MobileController(session, { repository }, scope, initialForeground = true)
            compose.runOnUiThread { host.get().setContent { MoeriseApp(mobile) { reported++ } } }
            val editorTag = "note_editor_${note.name}"
            compose.waitUntil(10_000) { compose.onAllNodesWithTag(editorTag).fetchSemanticsNodes().size == 1 }
            waitForMainCondition("cached timeline loads behind the restored editor") {
                mobile.timeline.state.value.notes.any { it.name == note.name }
            }
            compose.waitForIdle()
            compose.onNodeWithTag("compose_capsule").assertIsNotEnabled()
            compose.runOnIdle { assertEquals(0, reported) }
            compose.onNodeWithTag("cancel_note_${note.name}").performClick()
            waitForMainCondition("the capsule is reported after the restored editor closes") { reported == 1 }
            compose.onNodeWithTag("compose_capsule").assertIsEnabled()
            compose.onNodeWithTag(editorTag).assertDoesNotExist()
        } finally {
            compose.runOnUiThread { session.close(); job.cancel() }
            database.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun floatingComposerPreservesTheTimelineAndFirstScreenPrewarmsFilters() {
        createHost()
        val folder = Files.createTempDirectory("moerise-composer-focus").toFile()
        val database = testDatabase(folder.resolve("data.sqlite3").absolutePath)
        val repository = NoteRepository(database)
        val secrets = MemorySecrets()
        val origin = "http://127.0.0.1:1"
        val account = "$origin\u0000instance\u0000users/owner"
        val note = Note("instance", "memos/test", "Original", "NORMAL", "hash", createTime = "2026-11-26T10:00:00Z", tags = listOf("writing"))
        val job = SupervisorJob()
        val scope = CoroutineScope(job + Dispatchers.Main.immediate)
        runBlocking {
            secrets.write("selected-profile", CoreJson.encodeToString(ConnectionProfile(origin, account, "instance", "users/owner", "Owner", "UTC")))
            database.dao().putSyncState(SyncStateEntity(account, "cursor", "generation", true, 1))
            repository.recordLocalNote(account, note)
            for (index in 1..12) {
                val older = note.copy(createTime = "2026-11-25T10:00:00Z", tags = emptyList())
                repository.recordLocalNote(account, older.copy(name = "memos/home-$index", content = "Home note $index"))
                repository.recordLocalNote(account, older.copy(name = "memos/archive-$index", content = "Archive note $index", state = "ARCHIVED"))
            }
        }
        val session = SessionController(secrets, true, scope)
        try {
            val mobile = MobileController(session, { repository }, scope, initialForeground = true)
            compose.runOnUiThread { host.get().setContent { MoeriseApp(mobile) } }
            fun waitForTag(tag: String) {
                compose.waitUntil(10_000) { compose.onAllNodesWithTag(tag).fetchSemanticsNodes().size == 1 }
            }
            val composer = compose.onNodeWithTag("quick_editor")
            val inline = compose.onNodeWithTag("note_editor_${note.name}")
            fun edit() {
                compose.runOnIdle { mobile.editNote(note) }
                waitForTag("note_editor_${note.name}")
                inline.performClick().assertIsFocused()
            }
            fun openComposer() {
                waitForTag("compose_capsule")
                compose.onNodeWithTag("compose_capsule").performClick()
                waitForTag("quick_editor")
            }
            fun closed() = waitForMainCondition("the writing sheet closes") {
                !mobile.composerOpen.value && mobile.noteEditors.value.isEmpty()
            }
            waitForTag("compose_capsule")
            waitForMainCondition("first-screen tag and calendar data are available") {
                mobile.filterReads.cached("NORMAL", serviceMonth("UTC")) != null
            }
            fun checkQuickComposerPosition(page: String, firstKey: String) {
                val timeline = compose.onNodeWithTag("${page}_timeline")
                timeline.performScrollToKey(firstKey)
                val anchor = compose.onNodeWithTag("note_card_$firstKey")
                val offset = anchor.getUnclippedBoundsInRoot().top - timeline.getUnclippedBoundsInRoot().top
                assertTrue(anchor.isDisplayed())
                compose.onNodeWithTag(if (page == "home") "compose_capsule" else "quick_compose").assertIsDisplayed().performClick()
                waitForTag("quick_cancel")
                compose.onNodeWithTag("quick_cancel").performClick()
                compose.waitUntil(10_000) { compose.onAllNodesWithTag("quick_editor").fetchSemanticsNodes().isEmpty() }
                assertTrue(anchor.isDisplayed())
                assertEquals(offset, anchor.getUnclippedBoundsInRoot().top - timeline.getUnclippedBoundsInRoot().top)
            }
            compose.onNodeWithTag("quick_compose").assertDoesNotExist()
            waitForMainCondition("home timeline includes the scroll anchor") {
                mobile.timeline.state.value.notes.any { it.name == "memos/home-10" }
            }
            checkQuickComposerPosition("home", "memos/home-10")
            compose.onNodeWithTag("open_menu").performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            compose.onNodeWithTag("nav_archive").performScrollTo().assertIsDisplayed()
                .performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            waitForTag("archive_timeline")
            waitForMainCondition("archive timeline includes the scroll anchor") {
                mobile.archive.state.value.notes.any { it.name == "memos/archive-10" }
            }
            checkQuickComposerPosition("archive", "memos/archive-10")
            compose.onNodeWithTag("open_menu").performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            compose.onNodeWithTag("nav_settings").performClick()
            waitForTag("settings_reload")
            assertFalse(session.state.value.connected)
            compose.onNodeWithTag("account_logout").performScrollTo().assertIsEnabled().performClick()
            waitForTag("logout_confirm")
            compose.onNodeWithText("Cancel").performClick()
            compose.onNodeWithTag("open_menu").performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            compose.onNodeWithTag("nav_home").performScrollTo().assertIsDisplayed()
                .performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            waitForTag("home_timeline")
            openComposer()
            val queuedBody = (1..24).joinToString("\n\n") { "New note $it" }
            composer.performClick().assertIsFocused().performTextReplacement(queuedBody)
            composer.assertTextContains(queuedBody)
            compose.onNodeWithTag("quick_save").assertIsEnabled()
                .performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            waitForMainCondition("the composer clears after the local commit") {
                mobile.editor.state.value.let { it.draft?.content == "" || it.error != null }
            }
            assertNull(mobile.editor.state.value.error)
            val queued = runBlocking { repository.queuedNotes(account).single() }
            assertEquals(queuedBody, queued.content)
            closed()
            val queuedKey = "queued:${queued.id}"
            val timeline = compose.onNodeWithTag("home_timeline")
            timeline.performScrollToKey(queuedKey)
            compose.onNodeWithText("Expand").performClick()
            val confirmed = note.copy(name = "memos/confirmed", content = queuedBody, tags = emptyList(), sourceHash = "confirmed")
            compose.runOnIdle {
                mobile.timeline.onSaved(account, confirmed, displayKey = queuedKey)
                mobile.sync.confirmEnqueued(queued.id, null)
            }
            waitForTag("note_card_${confirmed.name}")
            val confirmedCard = "note_card_${confirmed.name}"
            fun toggle(label: String, card: String) = compose.onNode(hasText(label) and hasAnyAncestor(hasTestTag(card)))
            toggle("Collapse", confirmedCard).performScrollTo().performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            compose.waitUntil(10_000) { toggle("Expand", confirmedCard).isDisplayed() }
            assertTrue(compose.onNodeWithTag(confirmedCard).isDisplayed())
            val edited = queued.copy(id = "edit-confirmed", targetName = confirmed.name, content = "$queuedBody\n\nEdited offline")
            compose.runOnIdle { mobile.sync.publishEnqueued(edited) }
            waitForTag("queued_note_${edited.id}")
            timeline.performScrollToKey(queuedKey)
            compose.waitUntil(10_000) { toggle("Expand", "queued_note_${edited.id}").isDisplayed() }
            compose.runOnIdle {
                mobile.timeline.onSaved(account, confirmed.copy(content = edited.content, sourceHash = "edited"), displayKey = queuedKey)
                mobile.sync.confirmEnqueued(edited.id, null)
            }
            waitForTag("note_card_${confirmed.name}")
            toggle("Expand", confirmedCard).assertExists()

            edit()
            inline.performTextReplacement("Local edit")
            compose.onNodeWithTag("cancel_note_${note.name}").performClick()
            closed()
            openComposer()
            composer.assertIsFocused().performTextReplacement("Floating draft")
            compose.onNodeWithTag("quick_cancel").performClick()
            compose.waitUntil(10_000) { compose.onAllNodesWithTag("quick_editor").fetchSemanticsNodes().isEmpty() }
            assertEquals("Floating draft", runBlocking { repository.restore(account).draft.content })

            openComposer()
            composer.assertIsDisplayed().performClick().assertIsFocused().performTextReplacement("left #writ right")
            composer.performSemanticsAction(SemanticsActions.SetSelection) { select ->
                assertTrue(select("left #wr".length, "left #wr".length, false))
            }
            waitForTag("writing_tags")
            compose.onNode(hasText("#writing") and hasAnyAncestor(hasTestTag("writing_tags"))).performClick()
            composer.assertTextContains("left #writing right")

            composer.performTextReplacement("左 右")
            composer.performSemanticsAction(SemanticsActions.SetSelection) { select ->
                assertTrue(select(2, 2, false))
            }
            compose.onNodeWithTag("insert_tag").performClick()
            composer.assertIsFocused().assertTextContains("左 # 右")
            waitForTag("writing_tags")
            compose.onNode(hasText("#writing") and hasAnyAncestor(hasTestTag("writing_tags"))).performClick()
            composer.assertIsFocused().assertTextContains("左 #writing 右")
            compose.onNodeWithTag("quick_cancel").performClick()
            closed()

            compose.onNodeWithTag("home_timeline").performScrollToKey(note.name)
            compose.onNodeWithText("#writing").performClick()
            waitForMainCondition("the writing tag filter returns the edited note") {
                mobile.search.state.value.notes.map { it.name } == listOf(note.name)
            }
            edit()
            inline.performTextReplacement("Edited")
            inline.assertTextContains("Edited")
            compose.onNodeWithTag("save_note_${note.name}").assertIsEnabled()
                .performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            closed()
            compose.onNodeWithTag("quick_editor").assertDoesNotExist()
            assertEquals(setOf("writing"), mobile.search.state.value.query.tags)
            compose.onNodeWithText("Edited", useUnmergedTree = true).assertExists()
            compose.onNodeWithText("Original", useUnmergedTree = true).assertDoesNotExist()
            val long = note.copy(name = "memos/long", content = "Long text\n".repeat(32))
            compose.runOnIdle { mobile.editNote(long) }
            val longEditor = "note_editor_${long.name}"
            waitForTag(longEditor)
            compose.onNodeWithTag(longEditor).assertIsNotFocused()
                .assert(SemanticsMatcher.expectValue(SemanticsProperties.TextSelectionRange, androidx.compose.ui.text.TextRange.Zero))
            compose.onNodeWithTag("cancel_note_${long.name}").performClick()
            closed()
            assertEquals(setOf("writing"), mobile.search.state.value.query.tags)
        } finally {
            compose.runOnUiThread { job.cancel(); session.close() }
            compose.waitForIdle()
            database.close()
            folder.deleteRecursively()
        }
    }
}

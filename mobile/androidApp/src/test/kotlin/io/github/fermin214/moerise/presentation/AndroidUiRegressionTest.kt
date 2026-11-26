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
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
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
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.height
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
import io.github.fermin214.moerise.data.testDatabase
import io.github.fermin214.moerise.domain.Note
import io.ktor.client.engine.mock.MockEngine
import io.ktor.client.engine.mock.respond
import java.io.ByteArrayOutputStream
import java.nio.file.Files
import kotlin.test.*
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
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
            hasAnyAncestor(hasTestTag("note_card_body")), useUnmergedTree = true)
        val tagInBody = hasText(tagLine, substring = true) and hasAnyAncestor(hasTestTag("note_card_body"))
        val textInBody = hasText(text, substring = true) and hasAnyAncestor(hasTestTag("note_card_body"))
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
                compose.runOnIdle { entry.value = TimelineEntry.Remote(confirmed) }
                compose.mainClock.advanceTimeByFrame()
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
    fun attachmentResultAfterStopReturnsToTheUnfocusedComposerAndCanSaveWithoutText() =
        checkAttachmentResult(recreate = false)

    @Test
    fun attachmentResultDeliveredAfterRecreationWaitsForDraftRestoration() =
        checkAttachmentResult(recreate = true)

    @Test
    fun failedInlineRestorationReleasesThePickerForTheComposer() =
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
            compose.waitUntil(10_000) { compose.onAllNodesWithTag("note_editor").fetchSemanticsNodes().size == 1 }
            val field = compose.onNodeWithTag("note_editor")
            val inline = Note("instance", "memos/inline", "Original", "NORMAL", "hash")
            val target = if (inlineRestoreFails) {
                compose.runOnIdle { mobile.editNote(inline) }
                val tag = "note_editor_${inline.name}"
                compose.waitUntil(10_000) { compose.onAllNodesWithTag(tag).fetchSemanticsNodes().size == 1 }
                compose.waitUntil(10_000) { runBlocking { repo.feature(account, ACTIVE_NOTE_EDITOR)?.contains(inline.name) == true } }
                compose.onNodeWithTag(tag)
            } else field
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
            field.assertIsNotFocused()
            compose.onNodeWithTag("save_note").assertIsEnabled().performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            waitForMainCondition("the attachment-only note is queued") {
                mobile.sync.state.value.notes.singleOrNull()?.attachmentEdits()?.staged?.size == 1
            }
            val saved = mobile.sync.state.value.notes.single()
            assertEquals("", saved.content)
            assertEquals(assertNotNull(staged).id, saved.attachmentEdits().staged.single().id)
        } finally {
            compose.runOnUiThread { job.cancel(); session.close() }
            compose.waitForIdle()
            runBlocking { staged?.let { library.remove(it) } }
            db.close()
            folder.deleteRecursively()
        }
    }

    @Test
    fun sidebarHasDirectArchiveAndLogoutAndPrivacyActionStaysEnabled() {
        createHost()
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
            val composer = compose.onNodeWithTag("note_editor")
            val inline = compose.onNodeWithTag("note_editor_${note.name}")
            fun edit() {
                compose.runOnIdle { mobile.editNote(note) }
                waitForTag("note_editor_${note.name}")
                inline.performClick().assertIsFocused()
            }
            waitForTag("note_editor")
            waitForMainCondition("first-screen tag and calendar data are available") {
                mobile.filterReads.cached("NORMAL", serviceMonth("UTC")) != null
            }
            fun checkQuickComposerPosition(page: String, firstKey: String) {
                val timeline = compose.onNodeWithTag("${page}_timeline")
                timeline.performScrollToKey(firstKey)
                val anchor = compose.onNodeWithTag("note_card_$firstKey")
                val offset = anchor.getUnclippedBoundsInRoot().top - timeline.getUnclippedBoundsInRoot().top
                assertTrue(anchor.isDisplayed())
                compose.onNodeWithTag("quick_compose").assertIsDisplayed().performClick()
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
            compose.onNodeWithTag("nav_home").performScrollTo().assertIsDisplayed()
                .performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            waitForTag("home_timeline")
            compose.onNodeWithTag("home_timeline").performScrollToKey("composer")
            composer.performClick().assertIsFocused().performTextReplacement("New note")
            composer.assertTextContains("New note")
            compose.onNodeWithTag("save_note").assertIsEnabled()
                .performSemanticsAction(SemanticsActions.OnClick) { assertTrue(it()) }
            waitForMainCondition("the composer clears after the local commit") {
                mobile.editor.state.value.let { it.draft?.content == "" || it.error != null }
            }
            assertNull(mobile.editor.state.value.error)
            assertEquals("New note", runBlocking { repository.queuedNotes(account).single().content })
            composer.assertIsNotFocused()

            edit()
            compose.onNodeWithTag("quick_compose").assertIsDisplayed().performClick()
            compose.onNodeWithTag("quick_editor").assertIsFocused().performTextReplacement("Floating draft")
            compose.onNodeWithTag("quick_cancel").performClick()
            compose.waitUntil(10_000) { compose.onAllNodesWithTag("quick_editor").fetchSemanticsNodes().isEmpty() }
            assertEquals("Floating draft", runBlocking { repository.restore(account).draft.content })
            inline.assertIsNotFocused()
            compose.onNodeWithTag("cancel_note_${note.name}").performClick()
            waitForTag("note_editor")
            composer.assertIsNotFocused()

            compose.onNodeWithTag("home_timeline").performScrollToKey("composer")
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
            waitForTag("note_editor")
            composer.assertIsNotFocused()
            assertEquals(setOf("writing"), mobile.search.state.value.query.tags)
            compose.onNodeWithText("Edited", useUnmergedTree = true).assertExists()
            compose.onNodeWithText("Original", useUnmergedTree = true).assertDoesNotExist()
        } finally {
            compose.runOnUiThread { job.cancel(); session.close() }
            compose.waitForIdle()
            database.close()
            folder.deleteRecursively()
        }
    }
}

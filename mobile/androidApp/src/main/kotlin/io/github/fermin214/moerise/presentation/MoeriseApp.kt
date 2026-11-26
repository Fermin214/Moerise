package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.Alignment
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.isCtrlPressed
import androidx.compose.ui.input.key.isMetaPressed
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.input.OffsetMapping
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.input.TransformedText
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.serialization.json.*

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun MoeriseApp(mobile: MobileController, onEditorReady: () -> Unit = {}) {
    val focusManager = LocalFocusManager.current
    val keyboard = LocalSoftwareKeyboardController.current
    val leaveWriting: () -> Unit = {
        mobile.leaveWriting()
        focusManager.clearFocus()
        keyboard?.hide()
    }
    ClearWritingOnStop(leaveWriting)
    val session = mobile.session
    val language by session.language.collectAsState()
    val strings = remember(language) { Strings(language) }
    val connection by session.state.collectAsState()
    val composer by mobile.editor.state.collectAsState()
    val pickAttachments = AttachmentPicker(mobile)
    val noteEditors by mobile.noteEditors.collectAsState()
    val editorFocus by mobile.editorFocus.collectAsState()
    val home by mobile.timeline.state.collectAsState()
    val archive by mobile.archive.state.collectAsState()
    val homeNote by mobile.homeNote.state.collectAsState()
    val actions by mobile.noteActions.state.collectAsState()
    val clearing by mobile.clearing.collectAsState()
    val logout by mobile.logoutState.collectAsState()
    val localError by mobile.localError.collectAsState()
    val review by mobile.review.state.collectAsState()
    val walk by mobile.walk.state.collectAsState()
    val sync by mobile.sync.state.collectAsState()
    val manualSync by mobile.sync.manualSync.collectAsState()
    val search by mobile.search.state.collectAsState()
    val account = connection.profile?.accountKey
    val timezone = connection.profile?.timezone ?: "UTC"
    var page by rememberSaveable(account) { mutableStateOf("home") }
    var searchOpen by rememberSaveable(account) { mutableStateOf(false) }
    var sourceTimeline by rememberSaveable(account) { mutableStateOf("home") }
    var conflictKey by remember(account) { mutableStateOf<String?>(null) }
    var quickCompose by rememberSaveable(account) { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    val timelineActions = remember(account) { mutableStateOf<WritingActions?>(null) }
    val drawer = remember(account, scope) { NavigationDrawerState(scope) }
    val drawerVisible = drawer.visible
    val sidebar = mobile.sidebar
    val sidebarFilters by sidebar.state.collectAsState()
    val filterRevision by mobile.filterReads.revision.collectAsState()
    var month by rememberSaveable(account, timezone) { mutableStateOf(serviceMonth(timezone)) }
    val homeScroll = key(account) { rememberLazyListState() }
    val archiveScroll = key(account) { rememberLazyListState() }
    val reviewScroll = key(account, review.batch?.id, review.position) { rememberScrollState() }
    val walkScroll = key(account, walk.result?.item?.note?.name) { rememberScrollState() }
    val mapNotesScroll = key(account) { rememberLazyListState() }
    val query = search.query
    val filtered = query.filtered
    var searchRefreshing by remember(account, page, filtered) { mutableStateOf(false) }
    LaunchedEffect(search.loading) { if (!search.loading) searchRefreshing = false }
    val timeline = if (page == "archive") archive else home
    val timelineController = if (page == "archive") mobile.archive else mobile.timeline
    val editorStates = noteEditors.mapValues { (_, controller) ->
        key(controller) { controller.state.collectAsState().value }
    }
    val editorsByKey = buildMap<String, EditorController> {
        noteEditors.forEach { (key, controller) ->
            editorStates[key]?.draft?.let { draft ->
                put(key, controller)
                put(draft.id, controller)
                draft.targetName?.let { put(it, controller) }
            }
        }
    }
    val activeDraft = editorStates.values.firstOrNull()?.draft
    val activeBaseline = activeDraft?.baselineJson?.let {
        runCatching { CoreJson.decodeFromString<Note>(it) }.getOrNull()
    }
    val visibleQueue = remember(sync, account, connection.needsLogin, clearing) {
        sync.notesFor(account.takeUnless { connection.needsLogin || clearing })
    }
    val entries = remember(timeline.notes, timeline.displayKeys, timeline.snapshotAt, visibleQueue, search.notes, filtered, page, homeNote.note, homeNote.snapshotAt, activeBaseline) {
        val notes = if (filtered) search.notes else timeline.notes
        val pinned = if (page == "home" && !filtered) listOfNotNull(homeNote.note, activeBaseline)
            .distinctBy { it.name }.filter { candidate -> notes.none { it.name == candidate.name } } else emptyList()
        val selected = notes.map { note ->
            homeNote.note?.takeIf { it.name == note.name && homeNote.snapshotAt >= timeline.snapshotAt } ?: note
        }
        if (filtered || page == "archive") filteredTimelineEntries(selected, visibleQueue, timeline.displayKeys)
        else timelineEntries(pinned + selected, visibleQueue, timeline.displayKeys)
    }
    val entryEditorKeys = entries.map { when (it) { is TimelineEntry.Remote -> it.note.name; is TimelineEntry.Local -> it.draft.id } }.toSet()
    val displayedEditors = entryEditorKeys.mapNotNull(editorsByKey::get).toSet()
    val orphanEditors = if (page == "home") noteEditors.filter { (key, editor) ->
        editorStates[key]?.draft != null && editor !in displayedEditors
    }.keys.toList() else emptyList()
    val homeStatusVisible = homeNote.loading || homeNote.error != null || localError != null
    val focusTarget = homeNote.name ?: activeDraft?.targetName ?: activeDraft?.id
    var targetPositioned by remember(focusTarget, account, page) { mutableStateOf(false) }
    val composerReady = !connection.loading && account != null && composer.accountKey == account && composer.draft != null
    val composerVisible by remember(homeScroll) { derivedStateOf {
        val layout = homeScroll.layoutInfo
        layout.visibleItemsInfo.any { it.key == "composer" && it.offset < layout.viewportEndOffset &&
            it.offset + it.size > layout.viewportStartOffset }
    } }
    fun showComposer() { leaveWriting(); searchOpen = false; quickCompose = true }
    val openComposer: (() -> Unit)? = if (composerReady && !connection.needsLogin && !clearing &&
        (page in setOf("archive", "review", "walk") || page == "home" && (noteEditors.isNotEmpty() || !composerVisible))) {
        ::showComposer
    } else null
    LaunchedEffect(composerReady, page, clearing, account) {
        if (composerReady && page == "home" && !clearing) {
            withFrameNanos { }; withFrameNanos { }
            account?.let(mobile::editorFrameReady)
            onEditorReady()
        }
    }
    fun navigate(next: String) {
        leaveWriting()
        mobile.flush()
        mobile.closeNote()
        page = next
        if (next in setOf("home", "archive")) {
            sourceTimeline = next
            mobile.search.change(SearchQuery(state = if (next == "archive") "ARCHIVED" else "NORMAL"))
        }
        mobile.page(next)
    }
    fun beginDrawer() {
        leaveWriting()
        searchOpen = false
    }
    fun openDrawer() {
        beginDrawer()
        drawer.open()
    }
    fun showFilter(value: SearchQuery, target: String = if (page in setOf("home", "archive")) page else sourceTimeline) {
        mobile.flush()
        val switched = page != target
        page = target
        sourceTimeline = target
        mobile.homeNote.clear()
        mobile.search.change(value.copy(state = if (target == "archive") "ARCHIVED" else "NORMAL"))
        mobile.search.search()
        if (switched) mobile.page(target)
        if (drawer.visible) drawer.close()
    }
    val tagFromReading: (String) -> Unit = { tag -> showFilter(SearchQuery(tags = setOf(tag)), "home") }
    fun startWalk(note: Note) { navigate("walk"); mobile.walk.start(note.name) }
    fun editNote(note: Note) { mobile.editNote(note, ::showComposer) }
    LaunchedEffect(homeNote.name) {
        if (homeNote.name != null) {
            page = "home"
            sourceTimeline = "home"
            searchOpen = false
            mobile.page("home")
        }
    }
    LaunchedEffect(focusTarget, entries.map { it.key }, noteEditors.keys, page, homeStatusVisible, quickCompose) {
        if (page != "home" || targetPositioned || quickCompose) return@LaunchedEffect
        val target = focusTarget ?: return@LaunchedEffect
        val entryIndex = entries.indexOfFirst { entry ->
            when (entry) {
                is TimelineEntry.Remote -> entry.note.name == target
                is TimelineEntry.Local -> entry.draft.id == target || entry.draft.targetName == target
            }
        }
        val prefix = (if (noteEditors.isEmpty()) 1 else 0) +
            (if (query.tags.isNotEmpty() || query.date.isNotEmpty()) 1 else 0) +
            (if (filtered) 1 else 0) + (if (homeStatusVisible) 1 else 0)
        val index = if (entryIndex >= 0) {
            entryIndex + prefix
        } else if (orphanEditors.isNotEmpty()) {
            entries.size + prefix
        } else return@LaunchedEffect
        val targetKey = entries.getOrNull(entryIndex)?.key ?: "editor:$target"
        if (homeScroll.layoutInfo.visibleItemsInfo.none { it.key == targetKey }) homeScroll.scrollToItem(index)
        targetPositioned = true
    }
    LaunchedEffect(query.text, query.mode) { if (searchOpen) { delay(250); if (searchOpen) showFilter(query) } }
    LaunchedEffect(drawerVisible, month, account, connection.connected, connection.needsLogin, sourceTimeline, timezone, filterRevision) {
        if (drawerVisible && account != null && !connection.needsLogin) {
            sidebar.change(query.copy(state = if (sourceTimeline == "archive") "ARCHIVED" else "NORMAL"))
            sidebar.filters(month)
        } else sidebar.leave()
    }
    LaunchedEffect(page, connection.connected) { mobile.page(page) }
    DisposableEffect(sidebar) { onDispose { sidebar.clear() } }
    DisposableEffect(mobile) { onDispose { mobile.page(null) } }
    val modalBack = clearing || drawerVisible || searchOpen || conflictKey != null || quickCompose
    PlatformBackHandler(!modalBack && page != "home") {
        mobile.flush()
        if (page != "map" || !mobile.stars.back()) navigate("home")
    }
    @Composable
    fun renderEditor(id: String, controller: EditorController) {
        val state by controller.state.collectAsState()
        HomeEditor(state, controller, strings, onSave = { mobile.saveNote(id) }, onConflict = { conflictKey = id },
            onCancel = { mobile.finishEditing(id) }, onFocus = { if (it) mobile.focusNoteEditor(id) },
            requestFocus = editorFocus === controller, consumeFocus = { mobile.consumeEditorFocus(controller) },
            editorTag = "note_editor_$id", saveTag = "save_note_$id",
            cancelTag = "cancel_note_$id", actions = timelineActions, pick = pickAttachments,
            filters = mobile.filterReads, filterRevision = filterRevision)
    }
    PlatformLanguage(language) {
    MoeriseTheme {
        WelcomeDialog(mobile.privacy, strings)
        CompositionLocalProvider(
            LocalLeaveWriting provides leaveWriting,
            LocalWebSyncAction provides { if (sync.status == SyncStatus.SIGN_IN) navigate("connect") else mobile.synchronize() },
            LocalWebSearchOpen provides searchOpen, LocalWebSearchPanel provides {
            if (searchOpen) WebSearchPanel(query, strings, onChange = mobile.search::change,
                onClose = { leaveWriting(); searchOpen = false }, onSubmit = { showFilter(query) })
        }) {
            NavigationDrawer(
                drawer, account != null && !clearing && conflictKey == null && logout == null && page != "connect", strings,
                onDragStart = ::beginDrawer,
                modifier = Modifier.platformTestTags().testTag(if (platformImeVisible()) "ime_visible" else "ime_hidden"),
                sheet = {
                    WebSidebar(page, strings, connection.profile?.let { it.displayName.trim().ifBlank { it.userName.removePrefix("users/") } }.orEmpty(),
                        sidebarFilters, month, onClose = drawer::close,
                        onNavigate = { target -> drawer.close(); navigate(target) },
                        onMonth = { month = it }, onDate = { showFilter(query.copy(date = if (query.date == it) "" else it)) },
                        onTag = { tag -> showFilter(query.copy(tags = if (tag in query.tags) query.tags - tag else query.tags + tag)) },
                        onLogout = { drawer.close(); mobile.askLogout() }, selectedTags = query.tags,
                        selectedDate = query.date, today = serviceToday(timezone),
                        stargazingEnabled = connection.profile?.stargazingEnabled != false)
                }
            ) {
                Surface(Modifier.fillMaxSize().platformTestTags(), color = MoeriseColors.Paper) {
                    when {
                        connection.loading -> Box(Modifier.safeDrawingPadding().padding(24.dp)) { CircularProgressIndicator() }
                        account == null || page == "connect" -> Box(Modifier.safeDrawingPadding().imePadding()) {
                            ConnectionScreen(session, strings) { page = "home" }
                        }
                        page == "settings" -> SettingsScreen(mobile, strings, onMenu = ::openDrawer, onSearch = { searchOpen = !searchOpen })
                        page in setOf("review", "walk") -> DiscoveryScreen(page, mobile, strings,
                            if (page == "review") reviewScroll else walkScroll, onMenu = ::openDrawer,
                            onSearch = { searchOpen = !searchOpen }, onWalk = ::startWalk,
                            onSettings = { navigate("settings") }, onCompose = openComposer, onTag = tagFromReading)
                        page == "map" -> StarMapScreen(mobile, strings, mapNotesScroll,
                            onMenu = ::openDrawer, onSearch = { searchOpen = !searchOpen }, onTag = tagFromReading)
                        else -> Column(Modifier.safeDrawingPadding().imePadding().fillMaxSize().testTag("home")) {
                            WebToolbar(strings, onMenu = ::openDrawer, onSearch = { searchOpen = !searchOpen },
                                syncStatus = sync.status, onSync = { if (sync.status == SyncStatus.SIGN_IN) navigate("connect") else mobile.synchronize() },
                                onCompose = openComposer)
                            PullToRefreshBox(isRefreshing = if (filtered) searchRefreshing else timeline.refreshing || manualSync,
                                onRefresh = {
                                    if (filtered) { searchRefreshing = true; mobile.search.search() }
                                    else mobile.synchronize()
                                },
                                modifier = Modifier.weight(1f).fillMaxWidth().timelineFocusExit(leaveWriting),
                                contentAlignment = androidx.compose.ui.Alignment.TopCenter) {
                                LazyColumn(Modifier.fillMaxHeight().widthIn(max = 800.dp).fillMaxWidth().testTag("${page}_timeline"),
                                    state = if (page == "archive") archiveScroll else homeScroll,
                                    contentPadding = PaddingValues(horizontal = 16.dp, vertical = 20.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
                                    if (page == "home" && noteEditors.isEmpty()) item(key = "composer") {
                                        if (composerReady) HomeEditor(composer, mobile.editor, strings, onSave = { mobile.saveComposer() },
                                            onConflict = { conflictKey = composer.draft?.id }, actions = timelineActions, pick = pickAttachments,
                                            filters = mobile.filterReads, filterRevision = filterRevision)
                                        else { LinearProgressIndicator(Modifier.fillMaxWidth()); TextButton(mobile::retryDraft) { Text(strings.text("重新读取", "Read again")) } }
                                    }
                                    if (page == "archive") item { Text(strings.text("归档笔记", "Archived notes"),
                                        style = MaterialTheme.typography.titleLarge, modifier = Modifier.testTag("archive_page")) }
                                    if (query.tags.isNotEmpty() || query.date.isNotEmpty()) item { WebFilterBar(query, strings) { showFilter(it) } }
                                    if (filtered) item { SearchStatus(search, strings) }
                                    if (page == "home" && homeStatusVisible) item(key = "note_status") {
                                        if (homeNote.loading) LinearProgressIndicator(Modifier.fillMaxWidth())
                                        (localError ?: homeNote.error)?.let { code ->
                                            Text(editorError(code, strings), color = MaterialTheme.colorScheme.error)
                                            TextButton(onClick = {
                                                val name = homeNote.name
                                                if (localError != null || name == null) mobile.retryDraft()
                                                else mobile.openNote(name, homeNote.note)
                                            }) { Text(strings.text("重试", "Retry")) }
                                        }
                                    }
                                    items(if (connection.needsLogin || clearing) emptyList() else entries, key = { it.key }) { entry ->
                                        val noteId = when (entry) {
                                            is TimelineEntry.Local -> entry.draft.id
                                            is TimelineEntry.Remote -> entry.note.name
                                        }
                                        val remote = (entry as? TimelineEntry.Remote)?.note
                                        val noteAction = if (remote != null) {
                                            if (actions.name == remote.name && (actions.busy || actions.pendingAction || actions.error != null)) actions
                                            else homeNote.takeIf { it.name == remote.name } ?: NoteDetailState()
                                        } else NoteDetailState()
                                        val controller = editorsByKey[noteId]
                                        if (controller != null) renderEditor(noteId, controller)
                                        else TimelineNoteCard(
                                            entry = entry,
                                            timezone = connection.profile!!.timezone,
                                            strings = strings,
                                            core = session.core,
                                            onTag = { showFilter(query.copy(tags = query.tags + it)) },
                                            onOpenLocal = { local ->
                                                mobile.openDraft(local.id, onComposer = {
                                                    showComposer()
                                                    if (local.conflictJson != null) conflictKey = local.id
                                                }) {
                                                    if (mobile.getNoteEditor(local.id)?.state?.value?.draft?.conflictJson != null) {
                                                        leaveWriting()
                                                        conflictKey = local.id
                                                    }
                                                }
                                            },
                                            onEdit = { editNote(it) },
                                            onState = { mobile.changeNoteState(it) },
                                            onDelete = { mobile.deleteNote(it) },
                                            onWalk = { startWalk(it) },
                                            online = connection.connected,
                                            busy = noteAction.busy,
                                            error = noteAction.error,
                                            pendingAction = noteAction.pendingAction,
                                            deleteUnknown = noteAction.deleteUnknown,
                                            onCheckDeletion = { mobile.checkNoteDeletion(it.name) },
                                        )
                                    }
                                    items(if (connection.needsLogin || clearing) emptyList() else orphanEditors, key = { "editor:$it" }) { id ->
                                        noteEditors[id]?.let { renderEditor(id, it) }
                                    }
                                    item {
                                        val error = if (filtered) search.error else timeline.error
                                        if (error != null) {
                                            Text(searchError(error, strings), color = MaterialTheme.colorScheme.error)
                                            TextButton(onClick = {
                                                if (filtered) mobile.search.search(search.next.isNotEmpty())
                                                else if (timeline.nextPageToken.isNotBlank()) timelineController.more() else timelineController.refresh()
                                            }) { Text(strings.text("重试", "Retry")) }
                                        }
                                        val loading = if (filtered) search.loading else timeline.loading
                                        val next = if (filtered) search.next else timeline.nextPageToken
                                        if (loading && (filtered || entries.isEmpty() || timeline.loadingMore)) LinearProgressIndicator(Modifier.fillMaxWidth())
                                        else if (next.isNotBlank() && error == null) {
                                            LaunchedEffect(next, connection.connected, loading) {
                                                if (connection.connected && !loading) {
                                                    if (filtered) mobile.search.search(true) else timelineController.more()
                                                }
                                            }
                                            TextButton(onClick = { if (filtered) mobile.search.search(true) else timelineController.more() }, enabled = connection.connected) {
                                            Text(strings.text("加载更多", "Load more"))
                                            }
                                        } else if (entries.isEmpty() && (if (filtered) search.responseMode != null else timeline.fresh)) {
                                            Text(strings.text("没有匹配的笔记", "No matching notes"), color = MoeriseColors.Muted)
                                        }
                                    }
                                }
                            }
                            timelineActions.value?.render?.invoke()
                        }
                    }
                }
            }
            PlatformBackHandler(modalBack && !quickCompose) {
                if (clearing) return@PlatformBackHandler
                mobile.flush()
                when {
                    drawerVisible -> drawer.close()
                    conflictKey != null -> conflictKey = null
                    searchOpen -> { leaveWriting(); searchOpen = false }
                }
            }
            logout?.takeIf { it.account == account }?.let {
                LogoutDialog(it, clearing, strings, mobile::confirmLogout, mobile::dismissLogout)
            }
            if (quickCompose && composerReady && !connection.needsLogin && !clearing) {
                QuickComposer(composer, mobile, strings, pickAttachments, filterRevision,
                    onConflict = { conflictKey = composer.draft?.id }) { quickCompose = false }
            }
            conflictKey?.let { id ->
                val target = editorsByKey[id] ?: mobile.editor.takeIf { composer.draft?.id == id }
                target?.let { controller ->
                    val state by controller.state.collectAsState()
                    ConflictDialog(state, controller, strings, onFork = { content, attachments ->
                        mobile.forkConflict(id, content, attachments) { conflictKey = null }
                    }) { conflictKey = null }
                }
            }
            LaunchedEffect(connection.needsLogin, clearing) {
                if (connection.needsLogin || clearing) quickCompose = false
            }
        }
    }
    }
}

private class WritingActions(val render: @Composable () -> Unit)

@Composable
private fun QuickComposer(state: EditorUiState, mobile: MobileController, strings: Strings,
    pick: (DraftEntity) -> Unit, filterRevision: Long, onConflict: () -> Unit, onClose: () -> Unit) {
    val scope = rememberCoroutineScope()
    val actions = remember { mutableStateOf<WritingActions?>(null) }
    var closing by remember { mutableStateOf(false) }
    val dismiss: () -> Unit = {
        if (!closing && !mobile.editor.state.value.busy && !mobile.editor.state.value.importing) {
            closing = true
            scope.launch {
                if (mobile.editor.flushForLifecycle()) onClose()
                else closing = false
            }
        }
    }
    Dialog(onDismissRequest = dismiss, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        val focus = LocalFocusManager.current
        val keyboard = LocalSoftwareKeyboardController.current
        val leave: () -> Unit = { focus.clearFocus(); keyboard?.hide() }
        ClearWritingOnStop(leave)
        CompositionLocalProvider(LocalLeaveWriting provides leave) {
            Box(Modifier.fillMaxSize().imePadding().safeDrawingPadding()) {
                Box(Modifier.matchParentSize().clickable(onClick = dismiss).semantics { contentDescription = strings.text("关闭速记", "Close quick note") })
                Column(Modifier.align(Alignment.BottomCenter).widthIn(max = 800.dp).fillMaxWidth()
                    .padding(horizontal = 16.dp).testTag("quick_editor_sheet")) {
                    Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState())) {
                        HomeEditor(state.copy(busy = state.busy || closing), mobile.editor, strings,
                            onSave = { mobile.saveComposer { if (it == SaveOutcome.COMMITTED) onClose() } },
                            onConflict = onConflict, onCancel = dismiss, requestFocus = true, consumeFocus = { true },
                            actions = actions, pick = pick, filters = mobile.filterReads, filterRevision = filterRevision,
                            editorTag = "quick_editor", saveTag = "quick_save", cancelTag = "quick_cancel")
                    }
                    actions.value?.render?.invoke()
                }
            }
        }
    }
}

@Composable
internal fun ClearWritingOnStop(leaveWriting: () -> Unit) {
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val exitFocus by rememberUpdatedState(leaveWriting)
    DisposableEffect(lifecycle) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_STOP) exitFocus()
        }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer) }
    }
}

@Composable
private fun HomeEditor(
    state: EditorUiState, controller: EditorController, strings: Strings, onSave: () -> Unit, onConflict: () -> Unit,
    onCancel: (() -> Unit)? = null, onFocus: (Boolean) -> Unit = {}, requestFocus: Boolean = false, consumeFocus: () -> Boolean = { false },
    editorTag: String = "note_editor", saveTag: String = "save_note", cancelTag: String? = null,
    actions: MutableState<WritingActions?>, pick: (DraftEntity) -> Unit, filters: FilterReads, filterRevision: Long,
) {
    val draft = state.draft ?: return
    val leaveWriting = LocalLeaveWriting.current
    val editorFocus = remember(draft.id) { FocusRequester() }
    var focused by remember(draft.id) { mutableStateOf(false) }
    LaunchedEffect(draft.id, requestFocus) {
        if (requestFocus && consumeFocus()) editorFocus.requestFocus()
    }
    val edits = draft.attachmentEdits()
    val baseline = remember(draft.baselineJson) {
        draft.baselineJson?.let { runCatching { CoreJson.decodeFromString<Note>(it) }.getOrNull() }
    }
    var field by remember(draft.id) {
        mutableStateOf(TextFieldValue(draft.content, TextRange(initialEditorSelection(draft.content))))
    }
    LaunchedEffect(draft.content) {
        if (field.text != draft.content) {
            field = if (draft.content.isEmpty()) TextFieldValue("", TextRange.Zero)
            else field.copy(text = draft.content, selection = TextRange(initialEditorSelection(draft.content).coerceIn(0, draft.content.length)))
        }
    }
    val canSave = !state.busy && !state.importing && draft.conflictJson == null &&
        (draft.content.isNotBlank() || edits.staged.isNotEmpty() ||
            baseline?.attachments?.any { it["name"]?.jsonPrimitive?.content !in edits.removed } == true || state.pending != null)
    val scanner = remember(field.text) { if ('#' in field.text) NoteTagScanner(field.text) else null }
    val transformation = if (scanner == null) VisualTransformation.None else remember(scanner) {
        val highlighted = buildAnnotatedString {
            append(field.text)
            scanner.spans().forEach { addStyle(SpanStyle(color = Color(0xFF2A5BB5)), it.start, it.end) }
        }
        VisualTransformation { original ->
            TransformedText(if (original.text == highlighted.text) highlighted else original, OffsetMapping.Identity)
        }
    }
    var insertedMarker by remember(draft.id) { mutableStateOf<Int?>(null) }
    val prefix = if (focused && field.composition == null && field.selection.collapsed) {
        val cursor = field.selection.start
        scanner?.prefixAt(cursor) ?: insertedMarker?.takeIf { it + 1 == cursor && field.text.getOrNull(it) == '#' }
            ?.let { NoteTagSpan(it, cursor, "") }
    } else null
    var tags by remember(draft.accountKey, filterRevision) { mutableStateOf<List<TagCount>>(emptyList()) }
    LaunchedEffect(draft.accountKey, prefix != null, filterRevision) {
        if (prefix != null) {
            tags = try { filters.writingTags(draft.accountKey) }
            catch (error: Exception) {
                if (error is kotlinx.coroutines.CancellationException) throw error
                emptyList()
            }
        }
    }
    fun changeField(next: TextFieldValue) {
        if (next.text != field.text) insertedMarker = null
        field = next
        if (next.text != draft.content) controller.change(next.text)
    }
    val actionContent: @Composable () -> Unit = {
        val currentPrefix = prefix
        val matching = if (currentPrefix == null) emptyList() else tags.asSequence()
            .filter { it.tag.startsWith(currentPrefix.tag, ignoreCase = true) }.take(20).toList()
        Column(Modifier.fillMaxWidth()) {
            if (matching.isNotEmpty() && currentPrefix != null) Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState())
                .testTag("writing_tags"), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                matching.forEach { choice -> WebTag(choice.tag, {
                    val next = field.text.getOrNull(currentPrefix.end)
                    val text = "#${choice.tag}" + if (next?.isWhitespace() == true) "" else " "
                    changeField(TextFieldValue(field.text.replaceRange(currentPrefix.start, currentPrefix.end, text),
                        TextRange(currentPrefix.start + text.length + if (next == ' ') 1 else 0)))
                }) }
            }
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp), verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = { pick(draft) }, enabled = !state.busy && !state.importing && state.pending == null && draft.conflictJson == null,
                    modifier = Modifier.size(44.dp).testTag("add_attachment")) {
                    WebIcon(WebIcon.Plus, if (state.importing) strings.text("读取附件…", "Reading files…") else strings.text("添加附件", "Add files"))
                }
                TextButton(enabled = !state.busy && field.composition == null, onClick = {
                    if (field.selection.collapsed && scanner?.prefixAt(field.selection.start) != null) {
                        editorFocus.requestFocus()
                        return@TextButton
                    }
                    val start = field.selection.min
                    val insertion = tagMarkerAt(field.text, start, field.selection.max)
                    val marker = start + insertion.indexOf('#')
                    changeField(TextFieldValue(field.text.replaceRange(start, field.selection.max, insertion),
                        TextRange(marker + 1)))
                    val preview = field.text.replaceRange(marker + 1, marker + 1, "a")
                    insertedMarker = marker.takeIf { NoteTagScanner(preview).prefixAt(marker + 2)?.start == marker }
                    editorFocus.requestFocus()
                }, modifier = Modifier.testTag("insert_tag").semantics { contentDescription = strings.text("添加标签", "Add tag") }) { Text("#") }
                Spacer(Modifier.weight(1f))
                onCancel?.let { TextButton(onClick = { leaveWriting(); it() }, enabled = !state.busy && !state.importing,
                    modifier = cancelTag?.let { tag -> Modifier.testTag(tag) } ?: Modifier) { Text(strings.text("取消", "Cancel")) } }
                if (draft.conflictJson != null) {
                    WebAction(strings.text("处理冲突", "Resolve conflict"), onClick = { leaveWriting(); onConflict() })
                } else WebAction(strings.text("保存", "Save"), onClick = { leaveWriting(); onSave() },
                    modifier = Modifier.testTag(saveTag), enabled = canSave)
            }
        }
    }
    val latestActions by rememberUpdatedState(actionContent)
    val holder = remember { WritingActions { Surface(color = Color.White, shadowElevation = 3.dp) {
        Box(Modifier.padding(horizontal = 16.dp)) { latestActions() }
    } } }
    DisposableEffect(focused, actions, holder) {
        if (focused) actions.value = holder
        onDispose { if (actions.value === holder) actions.value = null }
    }
    Surface(shape = RoundedCornerShape(18.dp), border = BorderStroke(1.dp, MoeriseColors.Line), color = Color.White) {
        Column(Modifier.padding(start = 20.dp, top = 14.dp, end = 20.dp, bottom = 8.dp),
            verticalArrangement = Arrangement.spacedBy(2.dp)) {
            BasicTextField(
                value = field, onValueChange = ::changeField, visualTransformation = transformation,
                modifier = Modifier.fillMaxWidth().heightIn(min = 96.dp).drawerGestureRegion()
                    .testTag(editorTag).onPreviewKeyEvent {
                        if (it.type == KeyEventType.KeyDown && it.key == Key.Escape && onCancel != null) {
                            leaveWriting(); onCancel(); true
                        } else if (it.type == KeyEventType.KeyDown && it.key == Key.Enter &&
                            (it.isCtrlPressed || it.isMetaPressed) && canSave) { leaveWriting(); onSave(); true } else false
                    }.focusRequester(editorFocus).onFocusChanged { focused = it.isFocused; onFocus(it.isFocused) }.semantics {
                        contentDescription = strings.text("写点什么", "Write a note")
                },
                readOnly = state.busy,
                cursorBrush = SolidColor(MoeriseColors.Blue),
                textStyle = TextStyle(color = MoeriseColors.Ink, fontSize = 17.sp, lineHeight = 30.6.sp),
                decorationBox = { input ->
                    Box(Modifier.testTag(if (draft.content.isEmpty()) "${editorTag}_empty" else "${editorTag}_filled")) {
                        if (draft.content.isEmpty()) Text(
                            strings.text("今天想到了什么……", "What came to mind today…"),
                            color = Color(0xFF9299A3), fontSize = 14.sp, lineHeight = 30.6.sp,
                        )
                        input()
                    }
                },
            )
            edits.staged.forEach { file ->
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                    Text(file.filename, modifier = Modifier.weight(1f).padding(top = 12.dp))
                    TextButton(onClick = { controller.removeStaged(file.id) }, enabled = !state.busy && state.pending == null && draft.conflictJson == null) {
                        Text(strings.text("移除", "Remove"))
                    }
                }
            }
            baseline?.attachments?.forEach { item ->
                val name = item["name"]?.jsonPrimitive?.content ?: return@forEach
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                    Text(item["filename"]?.jsonPrimitive?.content ?: name, modifier = Modifier.weight(1f).padding(top = 12.dp))
                    TextButton(onClick = { controller.toggleRemoval(name) }, enabled = !state.busy && state.pending == null && draft.conflictJson == null) {
                        Text(if (name in edits.removed) strings.text("保留", "Keep") else strings.text("移除", "Remove"))
                    }
                }
            }
            state.error?.let { Text(editorError(it, strings), color = MaterialTheme.colorScheme.error) }
            if (!focused) actionContent()
        }
    }
}

@Composable
private fun ConflictDialog(state: EditorUiState, controller: EditorController, strings: Strings,
    onFork: (String, AttachmentEdits) -> Unit, onClose: () -> Unit) {
    val draft = state.draft ?: return
    val body = draft.conflictJson?.let { runCatching { CoreJson.parseToJsonElement(it).jsonObject }.getOrNull() } ?: return
    val remote = body["remote"]?.let { runCatching { CoreJson.decodeFromJsonElement<Note>(it) }.getOrNull() }
    val submitted = body["submitted"]?.jsonObject?.get("content")?.jsonPrimitive?.content.orEmpty()
    var merged by remember(draft.id, draft.conflictJson) { mutableStateOf(draft.content) }
    val attachments = draft.attachmentEdits()
    var selectedFiles by remember(draft.id, draft.conflictJson) { mutableStateOf(attachments.staged.map { it.id }.toSet()) }
    var selectedRemovals by remember(draft.id, draft.conflictJson) { mutableStateOf(attachments.removed.toSet()) }
    AlertDialog(
        onDismissRequest = onClose, title = { Text(strings.text("合并笔记", "Merge note")) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text(strings.text("提交的版本", "Submitted version"), style = MaterialTheme.typography.titleSmall)
                SelectionContainer { Text(submitted) }
                if (submitted != draft.content) {
                    Text(strings.text("本机版本", "Local version"), style = MaterialTheme.typography.titleSmall)
                    SelectionContainer { Text(draft.content) }
                }
                Text(strings.text("服务器版本", "Server version"), style = MaterialTheme.typography.titleSmall)
                SelectionContainer { Text(remote?.content ?: strings.text("无法读取服务器版本。", "Could not read the server version.")) }
                if (remote == null && draft.targetName != null) TextButton(onClick = controller::refreshConflict) {
                    Text(strings.text("重新读取", "Read again"))
                }
                OutlinedTextField(merged, { merged = it }, label = { Text(strings.text("合并后的正文", "Merged content")) }, modifier = Modifier.fillMaxWidth())
                attachments.staged.forEach { file ->
                    Row(Modifier.fillMaxWidth().clickable { selectedFiles = if (file.id in selectedFiles) selectedFiles - file.id else selectedFiles + file.id }) {
                        Checkbox(file.id in selectedFiles, onCheckedChange = { selectedFiles = if (it) selectedFiles + file.id else selectedFiles - file.id })
                        Text(strings.text("重新添加：", "Add again: ") + file.filename, modifier = Modifier.padding(top = 12.dp))
                    }
                }
                remote?.attachments?.forEach { item ->
                    val name = item["name"]?.jsonPrimitive?.content ?: return@forEach
                    Row(Modifier.fillMaxWidth().clickable { selectedRemovals = if (name in selectedRemovals) selectedRemovals - name else selectedRemovals + name }) {
                        Checkbox(name in selectedRemovals, onCheckedChange = { selectedRemovals = if (it) selectedRemovals + name else selectedRemovals - name })
                        Text(strings.text("移除：", "Remove: ") + (item["filename"]?.jsonPrimitive?.content ?: name), modifier = Modifier.padding(top = 12.dp))
                    }
                }
            }
        },
        confirmButton = {
            TextButton(enabled = remote != null && draft.targetName == remote.name, onClick = {
                if (remote != null) {
                    val remoteNames = remote.attachments.mapNotNull { it["name"]?.jsonPrimitive?.content }.toSet()
                    controller.merge(merged, remote, AttachmentEdits(attachments.staged.filter { it.id in selectedFiles },
                        selectedRemovals.filter { it in remoteNames }))
                    onClose()
                }
            }) { Text(strings.text("应用合并", "Apply merge")) }
        },
        dismissButton = {
            Column {
                TextButton(enabled = !state.busy, onClick = {
                    onFork(merged, AttachmentEdits(staged = attachments.staged.filter { it.id in selectedFiles }))
                }, modifier = Modifier.testTag("conflict_fork")) { Text(strings.text("作为新笔记编辑", "Edit as a new note")) }
                TextButton(onClick = onClose) { Text(strings.text("返回", "Back")) }
            }
        },
    )
}

fun editorError(code: String, strings: Strings): String = when (code) {
    "LOCAL_REMOVE_FAILED" -> strings.text("本机数据未能全部删除，请重试。", "Could not delete all local data. Try again.")
    "LOCAL_READ_FAILED" -> strings.text("无法读取本机笔记，请重试。", "Could not read local notes. Try again.")
    "LOCAL_WRITE_FAILED" -> strings.text("本机保存失败，请释放存储空间后重试。", "Could not save on this device. Free storage and retry.")
    "CONTENT_TOO_LARGE" -> strings.text("正文不能超过 64 KiB。", "Content must fit within 64 KiB.")
    "CONTENT_EMPTY" -> strings.text("请先写下笔记。", "Write a note first.")
    "SOURCE_CHANGED" -> strings.text("服务器笔记已变化，请合并后再保存。", "The server note changed. Merge before saving.")
    "IDEMPOTENCY_CONFLICT" -> strings.text("保存请求发生冲突，请编辑笔记并合并。", "The save request conflicts. Edit the note to merge.")
    "ATTACHMENTS_TOO_LARGE" -> strings.text("最多添加 10 个附件，合计不超过 8 MiB。", "Add up to 10 files, totaling no more than 8 MiB.")
    "ATTACHMENT_READ_FAILED", "ATTACHMENT_MISSING", "ATTACHMENT_CHANGED" -> strings.text("附件无法读取，请重新选择文件。", "Could not read the file. Select it again.")
    "ATTACHMENT_REFERENCED", "ATTACHMENT_DELETE_REJECTED" -> strings.text("正文仍引用要移除的图片，请修改引用后再保存。", "Content still references a removed image. Edit the reference before saving.")
    else -> connectionError(code, strings)
}

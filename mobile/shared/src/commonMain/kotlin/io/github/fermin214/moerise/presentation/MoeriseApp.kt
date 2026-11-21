package io.github.fermin214.moerise.presentation

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.geometry.Rect
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
import androidx.compose.ui.layout.boundsInRoot
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import kotlinx.coroutines.launch
import kotlinx.coroutines.delay
import kotlinx.serialization.json.*

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun MoeriseApp(mobile: MobileController, onEditorReady: () -> Unit = {}) {
    val focusManager = LocalFocusManager.current
    val keyboard = LocalSoftwareKeyboardController.current
    val session = mobile.session
    val language by session.language.collectAsState()
    val strings = remember(language) { Strings(language) }
    val connection by session.state.collectAsState()
    val composer by mobile.editor.state.collectAsState()
    val noteEditors by mobile.noteEditors.collectAsState()
    val home by mobile.timeline.state.collectAsState()
    val archive by mobile.archive.state.collectAsState()
    val homeNote by mobile.homeNote.state.collectAsState()
    val actions by mobile.noteActions.state.collectAsState()
    val clearing by mobile.clearing.collectAsState()
    val localError by mobile.localError.collectAsState()
    val review by mobile.review.state.collectAsState()
    val walk by mobile.walk.state.collectAsState()
    val sync by mobile.sync.state.collectAsState()
    val search by mobile.search.state.collectAsState()
    val account = connection.profile?.accountKey
    var page by rememberSaveable(account) { mutableStateOf("home") }
    var searchOpen by rememberSaveable(account) { mutableStateOf(false) }
    var sourceTimeline by rememberSaveable(account) { mutableStateOf("home") }
    var conflictKey by remember(account) { mutableStateOf<String?>(null) }
    var confirmRemoval by remember(account) { mutableStateOf(false) }
    val drawer = rememberDrawerState(DrawerValue.Closed)
    val scope = rememberCoroutineScope()
    val sidebar = mobile.sidebar
    val sidebarFilters by sidebar.state.collectAsState()
    var month by rememberSaveable(account) { mutableStateOf(serviceMonth(connection.profile?.timezone ?: "UTC")) }
    val homeScroll = key(account) { rememberLazyListState() }
    val archiveScroll = key(account) { rememberLazyListState() }
    val reviewScroll = key(account, review.batch?.id, review.position) { rememberScrollState() }
    val walkScroll = key(account, walk.result?.item?.note?.name) { rememberScrollState() }
    val insightScroll = key(account) { List(3) { rememberScrollState() } }
    val mapNotesScroll = key(account) { rememberLazyListState() }
    val bounds = remember(account) { mutableStateMapOf<String, Rect>() }
    val focused = remember(account) { mutableStateMapOf<String, Boolean>() }
    val query = search.query
    val filtered = query.text.isNotBlank() || query.tags.isNotEmpty() || query.date.isNotEmpty()
    var searchRefreshing by remember(account, page, filtered) { mutableStateOf(false) }
    LaunchedEffect(search.loading) { if (!search.loading) searchRefreshing = false }
    val timeline = if (page == "archive") archive else home
    val timelineController = if (page == "archive") mobile.archive else mobile.timeline
    val editorStates = noteEditors.mapValues { (_, controller) ->
        key(controller) { controller.state.collectAsState().value }
    }
    val editorsByKey = buildMap<String, EditorController> {
        noteEditors.forEach { (key, controller) ->
            put(key, controller)
            editorStates[key]?.draft?.let { draft ->
                put(draft.id, controller)
                draft.targetName?.let { put(it, controller) }
            }
        }
    }
    val activeDraft = editorStates.values.firstOrNull()?.draft
    val activeBaseline = activeDraft?.baselineJson?.let {
        runCatching { CoreJson.decodeFromString<Note>(it) }.getOrNull()
    }
    val entries = remember(timeline.notes, timeline.displayKeys, timeline.snapshotAt, sync.notes, search.notes, filtered, page, homeNote.note, homeNote.snapshotAt, activeBaseline) {
        val notes = if (filtered) search.notes else timeline.notes
        val pinned = if (page == "home" && !filtered) listOfNotNull(homeNote.note, activeBaseline)
            .distinctBy { it.name }.filter { candidate -> notes.none { it.name == candidate.name } } else emptyList()
        val selected = notes.map { note ->
            homeNote.note?.takeIf { it.name == note.name && homeNote.snapshotAt >= timeline.snapshotAt } ?: note
        }
        if (filtered) selected.map { TimelineEntry.Remote(it) }
        else timelineEntries(pinned + selected, if (page == "archive") emptyList() else sync.notes, timeline.displayKeys)
    }
    val entryEditorKeys = entries.map { when (it) { is TimelineEntry.Remote -> it.note.name; is TimelineEntry.Local -> it.draft.id } }.toSet()
    val displayedEditors = entryEditorKeys.mapNotNull(editorsByKey::get).toSet()
    val orphanEditors = if (page == "home") noteEditors.filterValues { it !in displayedEditors }.keys.toList() else emptyList()
    val homeStatusVisible = homeNote.loading || homeNote.error != null || localError != null
    val focusTarget = homeNote.name ?: activeDraft?.targetName ?: activeDraft?.id
    var targetPositioned by remember(focusTarget, account, page) { mutableStateOf(false) }
    val composerReady = !connection.loading && account != null && composer.accountKey == account && composer.draft != null
    LaunchedEffect(composerReady, page, clearing, account) {
        if (composerReady && page == "home" && !clearing) {
            withFrameNanos { }; withFrameNanos { }
            account?.let(mobile::editorFrameReady)
            onEditorReady()
        }
    }
    fun navigate(next: String) {
        mobile.flush()
        mobile.closeNote()
        page = next
        if (next in setOf("home", "archive")) {
            sourceTimeline = next
            mobile.search.change(SearchQuery(state = if (next == "archive") "ARCHIVED" else "NORMAL"))
        }
        mobile.page(next)
    }
    fun openDrawer() {
        focusManager.clearFocus()
        keyboard?.hide()
        searchOpen = false
        scope.launch { drawer.open() }
    }
    fun showFilter(value: SearchQuery) {
        val target = if (page in setOf("home", "archive")) page else sourceTimeline
        mobile.flush()
        if (page != target) { page = target; mobile.page(target) }
        mobile.homeNote.clear()
        mobile.search.change(value.copy(state = if (target == "archive") "ARCHIVED" else "NORMAL"))
        mobile.search.search()
        scope.launch { drawer.close() }
    }
    fun startWalk(note: Note) { navigate("walk"); mobile.walk.start(note.name) }
    fun startInsight(note: Note) { navigate("insights"); mobile.insights.seed(note.name) }
    fun editNote(note: Note) { mobile.editNote(note) }
    LaunchedEffect(homeNote.name) {
        if (homeNote.name != null) {
            page = "home"
            sourceTimeline = "home"
            searchOpen = false
            mobile.page("home")
        }
    }
    LaunchedEffect(focusTarget, entries.map { it.key }, noteEditors.keys, page, homeStatusVisible) {
        if (page != "home" || targetPositioned) return@LaunchedEffect
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
    LaunchedEffect(query.text, query.mode) { if (searchOpen) { delay(250); showFilter(query) } }
    LaunchedEffect(drawer.isOpen, month, account, connection.connected, sourceTimeline, connection.profile?.timezone) {
        if (drawer.isOpen && account != null && !connection.needsLogin) {
            sidebar.change(query.copy(state = if (sourceTimeline == "archive") "ARCHIVED" else "NORMAL"))
            sidebar.filters(month)
        } else sidebar.leave()
    }
    LaunchedEffect(page, connection.connected) { mobile.page(page) }
    DisposableEffect(sidebar) { onDispose { sidebar.clear() } }
    DisposableEffect(mobile) { onDispose { mobile.page(null) } }
    PlatformBackHandler(clearing || drawer.isOpen || searchOpen || conflictKey != null || page != "home") {
        if (clearing) return@PlatformBackHandler
        mobile.flush()
        when {
            drawer.isOpen -> scope.launch { drawer.close() }
            conflictKey != null -> conflictKey = null
            searchOpen -> searchOpen = false
            page == "map" && mobile.stars.back() -> Unit
            else -> navigate("home")
        }
    }
    @Composable
    fun renderEditor(id: String, controller: EditorController) {
        val state by controller.state.collectAsState()
        HomeEditor(state, controller, strings, onSave = { mobile.saveNote(id) }, onConflict = { conflictKey = id },
            onCancel = { mobile.finishEditing(id) }, onFocus = { focused[id] = it; if (it) mobile.focusNoteEditor(id) }, onBounds = { bounds[id] = it },
            onRemoved = { bounds.remove(id); focused.remove(id) }, editorTag = "note_editor_$id", saveTag = "save_note_$id",
            cancelTag = "cancel_note_$id")
    }
    PlatformLanguage(language) {
    MoeriseTheme {
        WelcomeDialog(mobile.privacy, strings)
        CompositionLocalProvider(
            LocalWebSyncAction provides { if (sync.status == SyncStatus.SIGN_IN) navigate("connect") else mobile.synchronize() },
            LocalWebSearchOpen provides searchOpen, LocalWebSearchPanel provides {
            if (searchOpen) WebSearchPanel(query, strings, onChange = mobile.search::change,
                onClose = { searchOpen = false }, onSubmit = { showFilter(query) })
        }) {
            ModalNavigationDrawer(modifier = Modifier.platformTestTags()
                .testTag(if (platformImeVisible()) "ime_visible" else "ime_hidden"),
                drawerState = drawer, gesturesEnabled = drawer.isOpen, drawerContent = {
                ModalDrawerSheet(Modifier.width(320.dp), drawerShape = androidx.compose.ui.graphics.RectangleShape,
                    drawerContainerColor = MoeriseColors.Paper) {
                    WebSidebar(page, strings, connection.profile?.let { it.displayName.trim().ifBlank { it.userName.removePrefix("users/") } }.orEmpty(),
                        sidebarFilters, month, drawer.isOpen, onClose = { scope.launch { drawer.close() } },
                        onNavigate = { target -> scope.launch { drawer.close() }; navigate(target) },
                        onMonth = { month = it }, onDate = { showFilter(query.copy(date = if (query.date == it) "" else it)) },
                        onTag = { tag -> showFilter(query.copy(tags = if (tag in query.tags) query.tags - tag else query.tags + tag)) },
                        onArchive = { scope.launch { drawer.close() }; navigate("archive"); mobile.archive.refresh() },
                        onLogout = { scope.launch { drawer.close() }; mobile.logout() },
                        onRemoveAccount = { scope.launch { drawer.close() }; confirmRemoval = true }, selectedTags = query.tags,
                        selectedDate = query.date, today = serviceToday(connection.profile?.timezone ?: "UTC"),
                        stargazingEnabled = connection.profile?.stargazingEnabled != false,
                        onSync = { scope.launch { drawer.close() }; if (sync.status == SyncStatus.SIGN_IN) navigate("connect") else mobile.synchronize() })
                }
            }) {
                Surface(Modifier.fillMaxSize().platformTestTags().webNavigationGesture(
                    enabled = account != null && !clearing && !drawer.isOpen && conflictKey == null &&
                        focused.values.none { it } && page !in setOf("map", "connect"),
                    editorBounds = bounds.values.toList(), onOpen = ::openDrawer), color = MoeriseColors.Paper) {
                    when {
                        connection.loading -> Box(Modifier.safeDrawingPadding().padding(24.dp)) { CircularProgressIndicator() }
                        account == null || page == "connect" -> Box(Modifier.safeDrawingPadding().imePadding()) {
                            ConnectionScreen(session, strings) { page = "home" }
                        }
                        page == "settings" -> SettingsScreen(mobile, strings, onMenu = ::openDrawer, onSearch = { searchOpen = !searchOpen })
                        page in setOf("review", "walk") -> DiscoveryScreen(page, mobile, strings,
                            if (page == "review") reviewScroll else walkScroll, onMenu = ::openDrawer,
                            onSearch = { searchOpen = !searchOpen }, onInsight = ::startInsight, onWalk = ::startWalk,
                            onSettings = { navigate("settings") })
                        page == "insights" -> InsightScreen(mobile, strings, insightScroll,
                            onMenu = ::openDrawer, onSearch = { searchOpen = !searchOpen }, onSettings = { navigate("settings") })
                        page == "map" -> StarMapScreen(mobile, strings, mapNotesScroll,
                            onMenu = ::openDrawer, onSearch = { searchOpen = !searchOpen })
                        else -> Column(Modifier.safeDrawingPadding().imePadding().fillMaxSize().testTag("home")) {
                            WebToolbar(strings, onMenu = ::openDrawer, onSearch = { searchOpen = !searchOpen },
                                syncStatus = sync.status, onSync = { if (sync.status == SyncStatus.SIGN_IN) navigate("connect") else mobile.synchronize() })
                            PullToRefreshBox(isRefreshing = if (filtered) searchRefreshing else timeline.refreshing,
                                onRefresh = {
                                    if (filtered) { searchRefreshing = true; mobile.search.search() }
                                    else {
                                        if (page == "home") mobile.synchronize() else timelineController.refresh(userInitiated = true)
                                    }
                                },
                                modifier = Modifier.weight(1f).fillMaxWidth(), contentAlignment = androidx.compose.ui.Alignment.TopCenter) {
                                LazyColumn(Modifier.fillMaxHeight().widthIn(max = 800.dp).fillMaxWidth(),
                                    state = if (page == "archive") archiveScroll else homeScroll,
                                    contentPadding = PaddingValues(horizontal = 16.dp, vertical = 20.dp), verticalArrangement = Arrangement.spacedBy(14.dp)) {
                                    if (page == "home" && noteEditors.isEmpty()) item(key = "composer") {
                                        if (composerReady) HomeEditor(composer, mobile.editor, strings, onSave = mobile::saveComposer,
                                            onConflict = {}, onFocus = { focused["composer"] = it }, onBounds = { bounds["composer"] = it },
                                            onRemoved = { bounds.remove("composer"); focused.remove("composer") })
                                        else { LinearProgressIndicator(Modifier.fillMaxWidth()); TextButton(mobile::retryDraft) { Text(strings.text("重新读取", "Read again")) } }
                                    }
                                    if (page == "archive") item { Text(strings.text("归档笔记", "Archived notes"),
                                        style = MaterialTheme.typography.titleLarge, modifier = Modifier.testTag("archive_page")) }
                                    if (query.tags.isNotEmpty() || query.date.isNotEmpty()) item { WebFilterBar(query, strings, ::showFilter) }
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
                                    items(entries, key = { it.key }) { entry ->
                                        val noteId = when (entry) {
                                            is TimelineEntry.Local -> entry.draft.id
                                            is TimelineEntry.Remote -> entry.note.name
                                        }
                                        val remote = (entry as? TimelineEntry.Remote)?.note
                                        val noteAction = if (remote != null) {
                                            if (actions.name == remote.name && (actions.busy || actions.pendingAction || actions.error != null)) actions
                                            else homeNote.takeIf { it.name == remote.name } ?: NoteDetailState()
                                        } else NoteDetailState()
                                        val controller = editorsByKey[noteId].takeIf { entry is TimelineEntry.Local || page == "home" }
                                        if (controller != null) renderEditor(noteId, controller)
                                        else TimelineNoteCard(
                                            entry = entry,
                                            timezone = connection.profile!!.timezone,
                                            strings = strings,
                                            core = session.core,
                                            onTag = { showFilter(query.copy(tags = query.tags + it)) },
                                            onOpenLocal = { local ->
                                                mobile.openDraft(local.id) {
                                                    if (mobile.getNoteEditor(local.id)?.state?.value?.draft?.conflictJson != null) conflictKey = local.id
                                                }
                                            },
                                            onEdit = { editNote(it) },
                                            onState = { mobile.changeNoteState(it) },
                                            onDelete = { mobile.deleteNote(it) },
                                            onWalk = { startWalk(it) },
                                            onInsight = { startInsight(it) },
                                            online = connection.connected,
                                            busy = noteAction.busy,
                                            error = noteAction.error,
                                            pendingAction = noteAction.pendingAction,
                                            deleteUnknown = noteAction.deleteUnknown,
                                            onCheckDeletion = { mobile.checkNoteDeletion(it.name) },
                                        )
                                    }
                                    items(orphanEditors, key = { "editor:$it" }) { id -> noteEditors[id]?.let { renderEditor(id, it) } }
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
                        }
                    }
                }
            }
            if (confirmRemoval && account != null) AlertDialog(
                onDismissRequest = { confirmRemoval = false },
                title = { Text(strings.text("删除本机账号数据？", "Delete account data on this device?")) },
                text = { Text(strings.text("此账号的草稿、待发请求和缓存将被删除，并退出登录。服务端笔记不受影响。",
                    "This deletes this account’s drafts, queued changes and cache, then signs you out. Notes on the server are kept.")) },
                confirmButton = {
                    TextButton(modifier = Modifier.testTag("account_remove_confirm"), onClick = {
                        confirmRemoval = false
                        page = "home"
                        mobile.clearLocalData(account)
                    }) { Text(strings.text("删除并退出", "Delete and sign out"), color = MaterialTheme.colorScheme.error) }
                },
                dismissButton = {
                    TextButton(modifier = Modifier.testTag("account_remove_cancel"), onClick = { confirmRemoval = false }) {
                        Text(strings.text("取消", "Cancel"))
                    }
                },
            )
            conflictKey?.let { id -> editorsByKey[id]?.let { controller ->
                val state by controller.state.collectAsState()
                ConflictDialog(state, controller, strings, onFork = { content, attachments ->
                    mobile.forkConflict(id, content, attachments) { conflictKey = null }
                }) { conflictKey = null }
            } }
        }
    }
    }
}
@Composable
private fun HomeEditor(
    state: EditorUiState, controller: EditorController, strings: Strings, onSave: () -> Unit, onConflict: () -> Unit,
    onCancel: (() -> Unit)? = null, onFocus: (Boolean) -> Unit = {}, onBounds: (Rect) -> Unit = {},
    onRemoved: () -> Unit = {}, editorTag: String = "note_editor", saveTag: String = "save_note", cancelTag: String? = null,
) {
    val draft = state.draft ?: return
    val editorFocus = remember(draft.id) { FocusRequester() }
    LaunchedEffect(draft.id, onCancel != null) {
        if (onCancel != null) editorFocus.requestFocus()
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
    DisposableEffect(draft.id) { onDispose { onFocus(false); onRemoved() } }
    Surface(modifier = Modifier.onGloballyPositioned { onBounds(it.boundsInRoot()) },
        shape = RoundedCornerShape(18.dp), border = BorderStroke(1.dp, MoeriseColors.Line), color = Color.White) {
        Column(Modifier.padding(start = 20.dp, top = 14.dp, end = 20.dp, bottom = 8.dp),
            verticalArrangement = Arrangement.spacedBy(2.dp)) {
            BasicTextField(
                value = field, onValueChange = { next ->
                    field = next
                    if (next.text != draft.content) controller.change(next.text)
                },
                modifier = Modifier.fillMaxWidth().heightIn(min = 96.dp, max = 320.dp)
                    .verticalScroll(rememberScrollState()).testTag(editorTag).onPreviewKeyEvent {
                        if (it.type == KeyEventType.KeyDown && it.key == Key.Escape && onCancel != null) {
                            onCancel(); true
                        } else if (it.type == KeyEventType.KeyDown && it.key == Key.Enter &&
                            (it.isCtrlPressed || it.isMetaPressed) && canSave) { onSave(); true } else false
                    }.focusRequester(editorFocus).onFocusChanged { onFocus(it.isFocused) }.semantics {
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
            Row(Modifier.fillMaxWidth().heightIn(min = 44.dp),
                verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                key(draft.accountKey, draft.id) {
                    AttachmentPicker(
                        enabled = !state.busy && !state.importing && state.pending == null && draft.conflictJson == null,
                        importScope = controller.attachmentScope,
                        strings = strings, onPicked = { controller.attach(draft.accountKey, draft.id, it) },
                        onError = { controller.attachmentError(it, draft.id) }, onBusy = { controller.importing(draft.id, it) },
                    )
                }
                Spacer(Modifier.weight(1f))
                onCancel?.let { TextButton(onClick = it, enabled = !state.busy,
                    modifier = cancelTag?.let { tag -> Modifier.testTag(tag) } ?: Modifier) { Text(strings.text("取消", "Cancel")) } }
                if (draft.conflictJson != null) {
                    WebAction(strings.text("处理冲突", "Resolve conflict"), onClick = onConflict)
                } else WebAction(
                    text = strings.text("保存", "Save"),
                    onClick = onSave, modifier = Modifier.testTag(saveTag),
                    enabled = canSave,
                )
            }
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

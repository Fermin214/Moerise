package io.github.fermin214.moerise.presentation

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.animateContentSize
import androidx.compose.animation.core.tween
import androidx.compose.animation.core.updateTransition
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
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
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.isCtrlPressed
import androidx.compose.ui.input.key.isMetaPressed
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.DialogWindowProvider
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import kotlinx.coroutines.delay
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.selects.select
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.*

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun MoeriseApp(mobile: MobileController, onHomeReady: () -> Unit = {}) {
    val focusManager = LocalFocusManager.current
    val keyboard = LocalSoftwareKeyboardController.current
    val leaveWriting: () -> Unit = {
        mobile.leaveWriting()
        focusManager.clearFocus()
        keyboard?.hide()
    }
    ClearWritingOnStop(leaveWriting)
    val session = mobile.session
    val theme by session.theme.collectAsState()
    val themeError by session.themeError.collectAsState()
    val darkTheme = theme.resolveDark(LocalContext.current, LocalConfiguration.current, useSystemSetting = themeError != null)
    val window = LocalView.current.context.activity()?.window
    SideEffect { window?.applyMoeriseTheme(darkTheme) }
    val language by session.language.collectAsState()
    val strings = remember(language) { Strings(language) }
    val connection by session.state.collectAsState()
    val composer by mobile.editor.state.collectAsState()
    val pickAttachments = AttachmentPicker(mobile)
    val noteEditors by mobile.noteEditors.collectAsState()
    val composerOpen by mobile.composerOpen.collectAsState()
    val restoredEditors by mobile.restoredEditors.collectAsState()
    val home by mobile.timeline.state.collectAsState()
    val archive by mobile.archive.state.collectAsState()
    val homeNote by mobile.homeNote.state.collectAsState()
    val actions by mobile.noteActions.state.collectAsState()
    val stateActions by mobile.stateActions.state.collectAsState()
    val clearing by mobile.clearing.collectAsState()
    val logout by mobile.logoutState.collectAsState()
    val localError by mobile.localError.collectAsState()
    val sync by mobile.sync.state.collectAsState()
    val manualSync by mobile.sync.manualSync.collectAsState()
    val search by mobile.search.state.collectAsState()
    val account = connection.profile?.accountKey
    val newTimelineKey by mobile.newTimelineKey.collectAsState()
    val reducedMotion = rememberReducedMotion()
    val snackbar = remember(account) { SnackbarHostState() }
    val currentStrings by rememberUpdatedState(strings)
    LaunchedEffect(mobile, account, snackbar) {
        mobile.notices.collect(account) { notice ->
            val labels = currentStrings
            val message = when (notice.code) {
                "NOTE_ARCHIVED" -> labels.text("笔记已归档。", "Note archived.")
                "NOTE_UNDO_CONFLICT" -> labels.text("笔记已在服务器上变化，无法撤销归档。", "The note changed on the server. Archiving could not be undone.")
                "NOTE_STATE_CONFLICT" -> labels.text("笔记已在服务器上变化，请刷新后重试。", "The note changed on the server. Refresh and try again.")
                "NOTE_STATE_REJECTED" -> labels.text("服务器拒绝了笔记状态变更。", "The server rejected the note state change.")
                "NOTE_UNDO_FAILED" -> labels.text("撤销归档失败，请刷新笔记后重试。", "Undo failed. Refresh the note and try again.")
                "COPIED" -> labels.text("已复制", "Copied")
                "COPY_FAILED" -> labels.text("复制失败", "Copy failed")
                else -> settingsMessage(notice.code, labels)
            }
            snackbar.showMobileNotice(message, labels.text("撤销", "Undo"), notice.undo)
        }
    }
    val timezone = connection.profile?.timezone ?: "UTC"
    val serviceDay = rememberServiceDay(timezone)
    var page by rememberSaveable(account) { mutableStateOf("home") }
    var searchOpen by rememberSaveable(account) { mutableStateOf(false) }
    var sourceTimeline by rememberSaveable(account) { mutableStateOf("home") }
    val sidebarTagState = if (page == "archive") "ARCHIVED" else "NORMAL"
    var conflictKey by remember(account) { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    val drawer = remember(account, scope) { NavigationDrawerState(scope) }
    val drawerVisible = drawer.visible
    val sidebar = mobile.sidebar
    val sidebarFilters by sidebar.state.collectAsState()
    val filterRevision by mobile.filterReads.revision.collectAsState()
    var month by rememberSaveable(account, timezone) { mutableStateOf(serviceDay.take(7)) }
    var currentMonth by remember(account, timezone) { mutableStateOf(serviceDay.take(7)) }
    LaunchedEffect(serviceDay) {
        val nextMonth = serviceDay.take(7)
        if (month == currentMonth) month = nextMonth
        currentMonth = nextMonth
    }
    val homeScroll = key(account) { rememberLazyListState() }
    val archiveScroll = key(account) { rememberLazyListState() }
    val mapNotesScroll = key(account) { rememberLazyListState() }
    val query = search.query
    val filtered = query.filtered
    var searchRefreshing by remember(account, page, filtered) { mutableStateOf(false) }
    LaunchedEffect(search.loading) { if (!search.loading) searchRefreshing = false }
    val timeline = if (page == "archive") archive else home
    val timelineController = if (page == "archive") mobile.archive else mobile.timeline
    val timelineScroll = if (page == "archive") archiveScroll else homeScroll
    val expandedNotes = remember(account, page) { mutableStateMapOf<String, Boolean>() }
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
    val visibleQueue = remember(sync, account, connection.needsLogin, clearing) {
        sync.notesFor(account.takeUnless { connection.needsLogin || clearing })
    }
    val entries = remember(timeline, visibleQueue, search.notes, query, page, homeNote, stateActions, timezone) {
        val notes = (if (filtered) search.notes else timeline.notes).toMutableList()
        val listState = if (page == "archive") "ARCHIVED" else "NORMAL"
        if (listState == "NORMAL" && !filtered) homeNote.note?.takeIf { note ->
            notes.none { it.name == note.name }
        }?.let { notes.add(0, it) }
        val selected = applyStateActions(notes.map { note ->
            homeNote.note?.takeIf { it.name == note.name && homeNote.snapshotAt >= timeline.snapshotAt } ?: note
        }, stateActions, listState, query, timezone)
        if (filtered || page == "archive") filteredTimelineEntries(selected, visibleQueue, timeline.displayKeys)
        else timelineEntries(selected, visibleQueue, timeline.displayKeys)
    }
    if (page in setOf("home", "archive") && query.text.isNotBlank() && query.mode != "keyword") {
        val cardNames by rememberUpdatedState(remember(entries) {
            entries.associate<TimelineEntry, Any, String?> { entry ->
                entry.key to when (entry) {
                    is TimelineEntry.Remote -> entry.note.name
                    is TimelineEntry.Local -> entry.draft.targetName
                }
            }
        })
        LaunchedEffect(account, query, page, timelineScroll) {
            snapshotFlow { timelineScroll.layoutInfo.visibleItemsInfo.mapNotNull { cardNames[it.key] } }
                .collect { mobile.search.visibleCards(query, it) }
        }
    }
    val homeStatusVisible = homeNote.loading || homeNote.error != null || localError != null
    val focusTarget = homeNote.name
    var targetPositioned by remember(focusTarget, account, page) { mutableStateOf(false) }
    val composerReady = !connection.loading && account != null && restoredEditors == account &&
        composer.accountKey == account && composer.draft != null && !connection.needsLogin && !clearing
    val editingEntry = noteEditors.entries.firstOrNull { editorStates[it.key]?.draft != null }
    val writing = composerReady && (composerOpen || editingEntry != null)
    val capsuleVisible = page == "home" && composerReady
    val capsuleEnabled = capsuleVisible && !writing && !drawerVisible && conflictKey == null && logout == null
    val bottomAction = updateTransition(snackbar.currentSnackbarData, label = "bottom action")
    val capsuleInteractive = capsuleEnabled && bottomAction.currentState == null &&
        bottomAction.targetState == null && !bottomAction.isRunning
    val latestCapsuleInteractive by rememberUpdatedState(capsuleInteractive)
    var homeReported by remember(account) { mutableStateOf(false) }
    LaunchedEffect(account, composerReady) {
        if (composerReady) {
            withFrameNanos { }; withFrameNanos { }
            mobile.timelineFrameReady(account)
        }
    }
    LaunchedEffect(account, capsuleInteractive) {
        if (account != null && capsuleInteractive && !homeReported) {
            withFrameNanos { }; withFrameNanos { }
            if (latestCapsuleInteractive && mobile.isHomeCapsuleAvailable(account)) {
                homeReported = true
                onHomeReady()
            }
        }
    }
    val openComposer: (() -> Unit)? = if (composerReady && page in setOf("archive", "review", "walk")) {
        mobile::openComposer
    } else null
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
    LaunchedEffect(homeNote.name) {
        if (homeNote.name != null) {
            page = "home"
            sourceTimeline = "home"
            searchOpen = false
            mobile.page("home")
        }
    }
    LaunchedEffect(focusTarget, entries.map { it.key }, page, homeStatusVisible, writing) {
        if (page != "home" || targetPositioned || writing) return@LaunchedEffect
        val target = focusTarget ?: return@LaunchedEffect
        val entryIndex = entries.indexOfFirst { entry ->
            when (entry) {
                is TimelineEntry.Remote -> entry.note.name == target
                is TimelineEntry.Local -> entry.draft.id == target || entry.draft.targetName == target
            }
        }
        val prefix = (if (query.tags.isNotEmpty() || query.date.isNotEmpty()) 1 else 0) +
            (if (filtered) 1 else 0) + (if (homeStatusVisible) 1 else 0)
        val index = if (entryIndex >= 0) {
            entryIndex + prefix
        } else return@LaunchedEffect
        val targetKey = entries[entryIndex].key
        if (homeScroll.layoutInfo.visibleItemsInfo.none { it.key == targetKey }) homeScroll.scrollToItem(index)
        targetPositioned = true
    }
    LaunchedEffect(query.text, query.mode) { if (searchOpen) { delay(250); if (searchOpen) showFilter(query) } }
    LaunchedEffect(drawerVisible, month, account, connection.connected, connection.needsLogin, sidebarTagState, timezone, filterRevision) {
        if (drawerVisible && account != null && !connection.needsLogin) {
            sidebar.change(SearchQuery(state = sidebarTagState))
            sidebar.filters(month)
        } else sidebar.leave()
    }
    LaunchedEffect(page, connection.connected) { mobile.page(page) }
    DisposableEffect(sidebar) { onDispose { sidebar.clear() } }
    DisposableEffect(mobile) { onDispose { mobile.page(null) } }
    val modalBack = clearing || drawerVisible || searchOpen || conflictKey != null || writing
    PlatformBackHandler(!modalBack && page != "home") {
        mobile.flush()
        if (page != "map" || !mobile.stars.back()) navigate("home")
    }
    PlatformLanguage(language) {
    MoeriseTheme(darkTheme) {
        WelcomeDialog(mobile.privacy, strings)
        CompositionLocalProvider(
            LocalReducedMotion provides reducedMotion,
            LocalMobileNotice provides { mobile.notice(it) },
            LocalLeaveWriting provides leaveWriting,
            LocalWebSyncAction provides { if (sync.status == SyncStatus.SIGN_IN) navigate("connect") else mobile.synchronize() },
            LocalWebSearchOpen provides searchOpen, LocalWebSearchPanel provides {
            if (searchOpen) Box(Modifier.motionEnter("search", distance = -8f)) {
                WebSearchPanel(query, strings, onChange = mobile.search::change,
                    onClose = { leaveWriting(); searchOpen = false }, onSubmit = { showFilter(query) })
            }
        }) {
            NavigationDrawer(
                drawer, account != null && !clearing && !writing && conflictKey == null && logout == null && page != "connect", strings,
                onDragStart = ::beginDrawer,
                modifier = Modifier.platformTestTags().testTag(if (platformImeVisible()) "ime_visible" else "ime_hidden"),
                sheet = {
                    WebSidebar(page, strings, connection.profile?.let { it.displayName.trim().ifBlank { it.userName.removePrefix("users/") } }.orEmpty(),
                        sidebarFilters.let { if (it.query.state == sidebarTagState) it else it.copy(tags = emptyList()) },
                        month, onClose = drawer::close,
                        onNavigate = { target -> drawer.close(); navigate(target) },
                        onMonth = { month = it }, onDate = { date ->
                            val next = if (page == "home") query.copy(date = if (query.date == date) "" else date)
                                else SearchQuery(date = date)
                            showFilter(next, "home")
                        },
                        onTag = { tag ->
                            val value = if (page in setOf("home", "archive")) query else SearchQuery(state = sidebarTagState)
                            showFilter(value.copy(tags = if (tag in value.tags) value.tags - tag else value.tags + tag),
                                if (sidebarTagState == "ARCHIVED") "archive" else "home")
                        },
                        selectedTags = if (page in setOf("home", "archive") && query.state == sidebarTagState) query.tags else emptySet(),
                        selectedDate = if (page == "home") query.date else "", today = serviceDay,
                        stargazingEnabled = connection.profile?.stargazingEnabled != false)
                }
            ) {
                Surface(Modifier.fillMaxSize().platformTestTags(), color = MoeriseColors.Paper) {
                    Box(Modifier.fillMaxSize()) {
                    when {
                        connection.loading -> Box(Modifier.safeDrawingPadding().padding(24.dp)) { CircularProgressIndicator() }
                        account == null || page == "connect" -> ConnectionScreen(session, strings) { page = "home" }
                        page == "settings" -> SettingsScreen(mobile, strings, onMenu = ::openDrawer, onSearch = { searchOpen = !searchOpen })
                        page in setOf("review", "walk") -> DiscoveryScreen(page, mobile, strings, onMenu = ::openDrawer,
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
                                    state = timelineScroll,
                                    contentPadding = PaddingValues(start = 20.dp, end = 20.dp, top = 6.dp,
                                        bottom = if (page == "home") 92.dp else 24.dp), verticalArrangement = Arrangement.spacedBy(0.dp)) {
                                    if (page == "archive") item { Text(strings.text("归档", "Archive"),
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
                                    itemsIndexed(if (connection.needsLogin || clearing) emptyList() else entries, key = { _, entry -> entry.key }) { index, entry ->
                                        Box(if (reducedMotion) Modifier else Modifier.animateItem(
                                            fadeInSpec = MoeriseMotion.tactile(),
                                            placementSpec = null,
                                            fadeOutSpec = MoeriseMotion.tactile(),
                                        )) {
                                        val remote = (entry as? TimelineEntry.Remote)?.note
                                        val stateAction = remote?.name?.let(stateActions::get)
                                        val noteAction = if (remote != null) {
                                            if (actions.name == remote.name && (actions.busy || actions.pendingAction || actions.error != null)) actions
                                            else homeNote.takeIf { it.name == remote.name } ?: NoteDetailState()
                                        } else NoteDetailState()
                                        val recent = entry.key == newTimelineKey
                                        Box(if (recent) Modifier.motionEnter(entry.key, distance = -18f, initialScale = .985f, spatial = true)
                                            else Modifier.motionEnter(entry.key, initialScale = .992f,
                                                distance = 0f, animateInitial = false)) {
                                        TimelineNoteCard(
                                            entry = entry,
                                            timezone = connection.profile!!.timezone,
                                            today = serviceDay,
                                            first = index == 0,
                                            strings = strings,
                                            core = session.core,
                                            onTag = { showFilter(query.copy(tags = query.tags + it)) },
                                            onOpenLocal = { local ->
                                                mobile.openDraft(local.id, onComposer = {
                                                    if (local.conflictJson != null) conflictKey = local.id
                                                }) {
                                                    if (mobile.getNoteEditor(local.id)?.state?.value?.draft?.conflictJson != null) {
                                                        leaveWriting()
                                                        conflictKey = local.id
                                                    }
                                                }
                                            },
                                            onEdit = { mobile.editNote(it) },
                                            onState = { mobile.changeNoteState(it) },
                                            onDelete = { mobile.deleteNote(it) },
                                            onWalk = { startWalk(it) },
                                            online = connection.connected,
                                            busy = noteAction.busy,
                                            error = if (stateAction?.unknown == true) "WRITE_OUTCOME_UNKNOWN" else noteAction.error,
                                            pendingAction = noteAction.pendingAction || stateAction != null,
                                            stateActionSending = stateAction != null && !stateAction.unknown,
                                            deleteUnknown = noteAction.deleteUnknown,
                                            onCheckDeletion = { mobile.checkNoteDeletion(it.name) },
                                            expanded = if (query.text.isBlank() && stateAction == null && !noteAction.pendingAction)
                                                expandedNotes[entry.key] == true else null,
                                            onExpand = { expanded ->
                                                scope.launch(start = CoroutineStart.UNDISPATCHED) {
                                                    val layout = timelineScroll.layoutInfo
                                                    val item = layout.visibleItemsInfo.firstOrNull { it.key == entry.key }
                                                    expandedNotes[entry.key] = expanded
                                                    if (!expanded && item != null && item.offset < layout.viewportStartOffset) {
                                                        timelineScroll.scrollToItem(item.index)
                                                    }
                                                }
                                            },
                                        )
                                        }
                                        }
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
                        }
                    }
                    Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().safeDrawingPadding().imePadding(),
                        contentAlignment = Alignment.BottomCenter) {
                        if (capsuleVisible && (bottomAction.currentState == null || bottomAction.targetState == null)) {
                            Box(Modifier.fillMaxWidth().height(88.dp).testTag("compose_feather")
                                .background(Brush.verticalGradient(0f to Color.Transparent, .65f to MoeriseColors.ScrollPaper)))
                        }
                        Box(Modifier.padding(horizontal = 16.dp, vertical = 12.dp).widthIn(max = 560.dp).fillMaxWidth()) {
                            bottomAction.AnimatedContent(transitionSpec = {
                                if (reducedMotion) fadeIn(tween(0)) togetherWith fadeOut(tween(0))
                                else fadeIn(tween(120, delayMillis = 90)) togetherWith fadeOut(tween(90))
                            }) { data ->
                                when {
                                    data != null -> MobileSnackbar(data)
                                    capsuleVisible -> ComposerCapsule(composer.draft?.content.orEmpty(), strings, capsuleEnabled, mobile::openComposer)
                                }
                            }
                        }
                    }
                    }
                }
            }
            PlatformBackHandler(modalBack && !writing) {
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
            if (writing) {
                val editing = editingEntry?.takeUnless { composerOpen }
                val active = editing?.value ?: mobile.editor
                val editorId = editing?.key
                val state by active.state.collectAsState()
                state.draft?.let { draft ->
                    key(draft.id) {
                        NoteComposerSheet(state.copy(error = state.error ?: localError), active, mobile.filterReads, strings, pickAttachments, filterRevision,
                            editorId, onConflict = { conflictKey = editorId ?: draft.id },
                            onSave = { if (editorId == null) mobile.saveComposer() else mobile.saveNote(editorId) },
                            onClose = { if (editorId == null) mobile.closeComposer() else mobile.finishEditing(editorId) })
                    }
                }
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
        }
    }
    }
}

@Composable
private fun ComposerCapsule(draft: String, strings: Strings, enabled: Boolean, onClick: () -> Unit) {
    val preview = draft.lineSequence().firstOrNull { it.isNotBlank() }
    Surface(onClick = onClick, enabled = enabled, modifier = Modifier.fillMaxWidth().height(48.dp).testTag("compose_capsule"),
        shape = RoundedCornerShape(24.dp), color = MoeriseColors.Surface, border = BorderStroke(1.dp, MoeriseColors.Line),
        shadowElevation = 2.dp) {
        Box(Modifier.padding(horizontal = 15.dp), contentAlignment = Alignment.CenterStart) {
            Text(preview ?: strings.text("今天想到了什么……", "What came to mind today…"),
                color = if (preview == null) MoeriseColors.InkColophon else MoeriseColors.Ink,
                maxLines = 1, overflow = TextOverflow.Ellipsis, fontSize = 15.sp)
        }
    }
}

internal suspend fun SnackbarHostState.showMobileNotice(message: String, undoLabel: String, undo: (() -> Unit)?) {
    val archiveUndo = undo as? ArchiveUndo
    if (archiveUndo?.failed == true) return
    val result = withTimeoutOrNull(3000) {
        if (archiveUndo == null) {
            showSnackbar(message, undo?.let { undoLabel }, duration = SnackbarDuration.Indefinite)
        } else coroutineScope {
            val shown = async { showSnackbar(message, undoLabel, duration = SnackbarDuration.Indefinite) }
            val rejected = async { archiveUndo.awaitFailure() }
            try {
                select {
                    shown.onAwait { it }
                    rejected.onAwait { SnackbarResult.Dismissed }
                }
            } finally {
                shown.cancel()
                rejected.cancel()
            }
        }
    }
    if (result == SnackbarResult.ActionPerformed) undo?.invoke()
}

@Composable
private fun MobileSnackbar(data: SnackbarData) {
    Surface(Modifier.fillMaxWidth().heightIn(min = 48.dp).semantics { liveRegion = LiveRegionMode.Polite }, shape = RoundedCornerShape(14.dp),
        color = MoeriseColors.SheetSurface, border = BorderStroke(1.dp, MoeriseColors.CardBorder), shadowElevation = 4.dp) {
        Row(Modifier.padding(horizontal = 14.dp, vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(data.visuals.message, Modifier.weight(1f), color = MoeriseColors.Ink, fontSize = 15.sp)
            data.visuals.actionLabel?.let {
                TextButton(onClick = data::performAction, colors = ButtonDefaults.textButtonColors(contentColor = MoeriseColors.Accent)) {
                    Text(it)
                }
            }
        }
    }
}

@Composable
private fun NoteComposerSheet(state: EditorUiState, controller: EditorController, filters: FilterReads, strings: Strings,
    pick: (DraftEntity) -> Unit, filterRevision: Long, editorId: String?, onConflict: () -> Unit, onSave: () -> Unit,
    onClose: () -> Unit) {
    val dismiss: () -> Unit = {
        if (!state.busy && !state.importing) onClose()
    }
    Dialog(onDismissRequest = dismiss, properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false)) {
        val window = (LocalView.current.parent as? DialogWindowProvider)?.window
        val palette = MoeriseColors
        SideEffect {
            window?.setDimAmount(0f)
            window?.applyMoeriseTheme(palette.dark, dialog = true)
        }
        val focus = LocalFocusManager.current
        val keyboard = LocalSoftwareKeyboardController.current
        val leave: () -> Unit = { focus.clearFocus(); keyboard?.hide() }
        ClearWritingOnStop(leave)
        CompositionLocalProvider(LocalLeaveWriting provides leave) {
            Box(Modifier.fillMaxSize()) {
                Box(Modifier.matchParentSize().background(palette.Scrim)
                    .clickable(interactionSource = null, indication = null, onClick = dismiss)
                    .semantics { contentDescription = strings.text("关闭写作", "Close writing") })
                Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().statusBarsPadding().displayCutoutPadding().imePadding(),
                    contentAlignment = Alignment.BottomCenter) {
                    SheetEditor(state, controller, strings, onSave, onConflict, dismiss, editorId,
                        pick, filters, filterRevision)
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
private fun SheetEditor(
    state: EditorUiState, controller: EditorController, strings: Strings, onSave: () -> Unit, onConflict: () -> Unit,
    onCancel: () -> Unit, editorId: String?,
    pick: (DraftEntity) -> Unit, filters: FilterReads, filterRevision: Long,
) {
    val haptic = rememberMoeriseHaptic()
    val draft = state.draft ?: return
    val leaveWriting = LocalLeaveWriting.current
    val editorFocus = remember(draft.id) { FocusRequester() }
    val keyboard = LocalSoftwareKeyboardController.current
    val requestFocus = remember(draft.id) {
        editorId == null || draft.content.length <= 280 && draft.content.count { it == '\n' } <= 8
    }
    val editorTag = editorId?.let { "note_editor_$it" } ?: "quick_editor"
    val saveTag = editorId?.let { "save_note_$it" } ?: "quick_save"
    val cancelTag = editorId?.let { "cancel_note_$it" } ?: "quick_cancel"
    var focused by remember(draft.id) { mutableStateOf(false) }
    LaunchedEffect(draft.id) {
        if (requestFocus) {
            withFrameNanos { }
            editorFocus.requestFocus()
            keyboard?.show()
        }
    }
    val edits = draft.attachmentEdits()
    val baseline = remember(draft.baselineJson) {
        draft.baselineJson?.let { runCatching { CoreJson.decodeFromString<Note>(it) }.getOrNull() }
    }
    var field by remember(draft.id) {
        mutableStateOf(TextFieldValue(draft.content, TextRange(if (requestFocus) initialEditorSelection(draft.content) else 0)))
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
    val palette = MoeriseColors
    val transformation = remember(field.text, palette) { markdownEditorTransformation(field.text, palette) }
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
        val current = controller.state.value
        if (current.busy || current.draft?.id != draft.id) return
        if (next.text != current.draft.content && !controller.change(next.text)) return
        if (next.text != field.text) insertedMarker = null
        field = next
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
                IconButton(enabled = !state.busy && field.composition == null, onClick = {
                    if (field.selection.collapsed && scanner?.prefixAt(field.selection.start) != null) {
                        editorFocus.requestFocus()
                        return@IconButton
                    }
                    val start = field.selection.min
                    val insertion = tagMarkerAt(field.text, start, field.selection.max)
                    val marker = start + insertion.indexOf('#')
                    changeField(TextFieldValue(field.text.replaceRange(start, field.selection.max, insertion),
                        TextRange(marker + 1)))
                    val preview = field.text.replaceRange(marker + 1, marker + 1, "a")
                    insertedMarker = marker.takeIf { NoteTagScanner(preview).prefixAt(marker + 2)?.start == marker }
                    editorFocus.requestFocus()
                }, modifier = Modifier.size(44.dp).testTag("insert_tag")) {
                    WebIcon(WebIcon.Hash, strings.text("添加标签", "Add tag"), tint = MoeriseColors.Muted)
                }
                Spacer(Modifier.weight(1f))
                WebSecondaryAction(strings.text("取消", "Cancel"), onClick = onCancel, enabled = !state.busy && !state.importing,
                    modifier = Modifier.testTag(cancelTag))
                Spacer(Modifier.width(8.dp))
                if (draft.conflictJson != null) {
                    WebAction(strings.text("处理冲突", "Resolve conflict"), onClick = { leaveWriting(); onConflict() })
                } else WebAction(strings.text("保存", "Save"), onClick = { haptic(); onSave() },
                    modifier = Modifier.testTag(saveTag), enabled = canSave)
            }
        }
    }
    Surface(modifier = Modifier.widthIn(max = 800.dp).fillMaxWidth().testTag("quick_editor_sheet")
        .then(if (LocalReducedMotion.current) Modifier else Modifier.animateContentSize(tween(140))),
        shape = RoundedCornerShape(topStart = 18.dp, topEnd = 18.dp, bottomStart = 0.dp, bottomEnd = 0.dp),
        color = MoeriseColors.SheetSurface, shadowElevation = 3.dp) {
        Column(Modifier.drawWithContent {
            drawContent()
            val edge = 18.dp.toPx()
            val line = 1.dp.toPx()
            drawLine(palette.CardBorder, Offset(edge, line / 2), Offset(size.width - edge, line / 2), strokeWidth = line)
        }.navigationBarsPadding().padding(start = 20.dp, top = 14.dp, end = 20.dp, bottom = 8.dp),
            verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState())) {
            BasicTextField(
                value = field, onValueChange = ::changeField, visualTransformation = transformation,
                modifier = Modifier.fillMaxWidth().heightIn(min = 64.dp)
                    .testTag(editorTag).onPreviewKeyEvent {
                        if (it.type == KeyEventType.KeyDown && it.key == Key.Escape) {
                            onCancel(); true
                        } else if (it.type == KeyEventType.KeyDown && it.key == Key.Enter &&
                            (it.isCtrlPressed || it.isMetaPressed) && canSave) { haptic(); onSave(); true } else false
                    }.focusRequester(editorFocus).onFocusChanged { focused = it.isFocused }.semantics {
                        contentDescription = strings.text("写点什么", "Write a note")
                },
                readOnly = state.busy,
                cursorBrush = SolidColor(MoeriseColors.Accent),
                textStyle = MaterialTheme.typography.bodyLarge.copy(color = MoeriseColors.Ink),
                decorationBox = { input ->
                    Box(Modifier.testTag(if (draft.content.isEmpty()) "${editorTag}_empty" else "${editorTag}_filled")) {
                        if (draft.content.isEmpty()) Text(
                            strings.text("今天想到了什么……", "What came to mind today…"),
                            color = MoeriseColors.InkColophon, style = MaterialTheme.typography.bodyLarge,
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
            }
            actionContent()
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
    "WRITE_OUTCOME_UNKNOWN" -> strings.text("操作结果待核对。请重试操作。", "The result is unconfirmed. Retry the action.")
    "IDEMPOTENCY_CONFLICT" -> strings.text("保存请求发生冲突，请编辑笔记并合并。", "The save request conflicts. Edit the note to merge.")
    "ATTACHMENTS_TOO_LARGE" -> strings.text("最多添加 10 个附件，合计不超过 8 MiB。", "Add up to 10 files, totaling no more than 8 MiB.")
    "ATTACHMENT_READ_FAILED", "ATTACHMENT_MISSING", "ATTACHMENT_CHANGED" -> strings.text("附件无法读取，请重新选择文件。", "Could not read the file. Select it again.")
    "ATTACHMENT_REFERENCED", "ATTACHMENT_DELETE_REJECTED" -> strings.text("正文仍引用要移除的图片，请修改引用后再保存。", "Content still references a removed image. Edit the reference before saving.")
    else -> connectionError(code, strings)
}

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
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import kotlinx.coroutines.launch
import kotlinx.serialization.json.*

object MoeriseColors {
    val Paper = Color(0xFFF7F9FC)
    val Ink = Color(0xFF1F2328)
    val Muted = Color(0xFF5F6672)
    val Line = Color(0xFFE3E8EF)
    val Blue = Color(0xFF1E4A9A)
    val BlueSurface = Color(0xFFDCE8FB)
}

@Composable
fun MoeriseApp(mobile: MobileController, onEditorReady: () -> Unit = {}) {
    val session = mobile.session
    val language by session.language.collectAsState()
    val strings = remember(language) { Strings(language) }
    val connection by session.state.collectAsState()
    val editor by mobile.editor.state.collectAsState()
    val homeTimeline by mobile.timeline.state.collectAsState()
    val archivedTimeline by mobile.archive.state.collectAsState()
    val detail by mobile.detail.state.collectAsState()
    val editing by mobile.editing.collectAsState()
    val review by mobile.review.state.collectAsState()
    val walk by mobile.walk.state.collectAsState()
    val stars by mobile.stars.state.collectAsState()
    var page by rememberSaveable(connection.profile?.accountKey) { mutableStateOf("home") }
    val drawer = rememberDrawerState(DrawerValue.Closed)
    val uiScope = rememberCoroutineScope()
    val homeScroll = key(connection.profile?.accountKey) { rememberLazyListState() }
    val archiveScroll = key(connection.profile?.accountKey) { rememberLazyListState() }
    val searchScroll = key(connection.profile?.accountKey) { rememberLazyListState() }
    var searchFrom by rememberSaveable(connection.profile?.accountKey) { mutableStateOf("home") }
    val detailScroll = key(connection.profile?.accountKey, detail.name) { rememberScrollState() }
    val reviewScroll = key(connection.profile?.accountKey, review.batch?.id, review.position) { rememberScrollState() }
    val walkScroll = key(connection.profile?.accountKey, walk.result?.item?.note?.name) { rememberScrollState() }
    val insightScroll = key(connection.profile?.accountKey) { rememberScrollState() }
    val mapTopicsScroll = key(connection.profile?.accountKey) { rememberLazyListState() }
    val mapNotesScroll = key(connection.profile?.accountKey) { rememberLazyListState() }
    val mapReaderScroll = key(connection.profile?.accountKey, stars.selected) { rememberScrollState() }
    val timeline = if (page == "archive") archivedTimeline else homeTimeline
    val timelineController = if (page == "archive") mobile.archive else mobile.timeline
    var conflict by remember { mutableStateOf(false) }
    val ready = !connection.loading && connection.profile != null &&
        editor.accountKey == connection.profile?.accountKey && editor.draft != null
    LaunchedEffect(ready) {
        if (ready) { withFrameNanos { }; onEditorReady() }
    }
    fun navigate(next: String) { mobile.flush(); if (page == "search") mobile.search.leave(); page = next; mobile.page(next) }
    fun startWalk(note: Note) { mobile.closeNote(); navigate("walk"); mobile.walk.start(note.name) }
    fun startInsight(note: Note) { mobile.closeNote(); navigate("insights"); mobile.insights.seed(note.name) }
    LaunchedEffect(page, detail.name, editing, connection.connected) {
        mobile.page(page.takeIf { detail.name == null && !editing })
    }
    DisposableEffect(mobile) { onDispose { mobile.page(null) } }
    PlatformBackHandler(drawer.isOpen || page != "home" || detail.name != null || editing || conflict) {
        mobile.flush()
        when {
            drawer.isOpen -> uiScope.launch { drawer.close() }
            conflict -> conflict = false
            editing -> mobile.finishEditing()
            detail.name != null -> mobile.closeNote()
            page == "map" && mobile.stars.back() -> Unit
            else -> navigate(if (page == "search") searchFrom else "home")
        }
    }
    MaterialTheme(colorScheme = lightColorScheme(
        primary = MoeriseColors.Blue, background = MoeriseColors.Paper,
        surface = Color.White, onSurface = MoeriseColors.Ink,
    )) {
        ModalNavigationDrawer(drawerState = drawer, gesturesEnabled = drawer.isOpen,
            drawerContent = {
                ModalDrawerSheet {
                    Text("Moerise", style = MaterialTheme.typography.headlineSmall, modifier = Modifier.padding(24.dp))
                    listOf("home" to strings.text("主页", "Home"), "review" to strings.text("回顾", "Review"),
                        "walk" to strings.text("漫步", "Walk"), "insights" to strings.text("洞察", "Insights"),
                        "map" to strings.text("观星", "Stargazing"),
                        "settings" to strings.text("设置", "Settings")).forEach { (target, label) ->
                        NavigationDrawerItem(label = { Text(label) }, selected = page == target,
                            modifier = Modifier.padding(horizontal = 12.dp).testTag("nav_$target"), onClick = {
                                uiScope.launch { drawer.close() }
                                navigate(target)
                            })
                    }
                }
            },
        ) {
        Surface(Modifier.fillMaxSize(), color = MoeriseColors.Paper) {
            if (connection.loading) {
                Box(Modifier.safeDrawingPadding().padding(24.dp)) { CircularProgressIndicator() }
                return@Surface
            }
            if (connection.profile == null || page == "connect") {
                Box(Modifier.safeDrawingPadding().imePadding()) { ConnectionScreen(session, strings) { page = "home" } }
                return@Surface
            }
            if (page == "settings") {
                Column(Modifier.safeDrawingPadding().verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    TextButton(onClick = { navigate("home") }) { Text(strings.text("返回主页", "Back to home")) }
                    Text(strings.text("设置", "Settings"), style = MaterialTheme.typography.headlineSmall)
                    Text(connection.profile!!.origin)
                    Text(connection.profile!!.displayName.ifBlank { connection.profile!!.userName })
                    Text(strings.text("语言", "Language"))
                    UiLanguage.entries.forEach { choice ->
                        Row(Modifier.fillMaxWidth().clickable { session.language(choice) }.heightIn(min = 48.dp)) {
                            RadioButton(language == choice, onClick = { session.language(choice) })
                            Text(when (choice) {
                                UiLanguage.SYSTEM -> strings.text("跟随系统", "System")
                                UiLanguage.CHINESE -> "简体中文"
                                UiLanguage.ENGLISH -> "English"
                            }, modifier = Modifier.padding(top = 12.dp))
                        }
                    }
                    Button(onClick = { navigate("connect") }) { Text(strings.text("服务连接", "Server connection")) }
                    OutlinedButton(onClick = { navigate("archive"); mobile.archive.refresh() }) { Text(strings.text("归档笔记", "Archived notes")) }
                    OutlinedButton(onClick = mobile::logout) { Text(strings.text("退出登录", "Sign out")) }
                    Text("Moerise 0.1.0", color = MoeriseColors.Muted)
                }
                return@Surface
            }
            if (detail.name != null && !editing) {
                NoteDetailScreen(detail, mobile, strings, detailScroll, onWalk = ::startWalk, onInsight = ::startInsight) { tag, note ->
                    mobile.search.change(SearchQuery(tags = setOf(tag), state = note.state))
                    mobile.search.tab(0)
                    mobile.search.search()
                    mobile.closeNote()
                    if (page != "search") searchFrom = page
                    page = "search"
                }
                return@Surface
            }
            if (page == "search") {
                SearchScreen(mobile, strings, searchScroll) { navigate(searchFrom) }
                return@Surface
            }
            if (page in setOf("review", "walk")) {
                DiscoveryScreen(page, mobile, strings, if (page == "review") reviewScroll else walkScroll,
                    onMenu = { uiScope.launch { drawer.open() } }, onInsight = ::startInsight, onWalk = ::startWalk)
                return@Surface
            }
            if (page == "insights") {
                InsightScreen(mobile, strings, insightScroll) { uiScope.launch { drawer.open() } }
                return@Surface
            }
            if (page == "map") {
                StarMapScreen(mobile, strings, mapTopicsScroll, mapNotesScroll, mapReaderScroll,
                    onMenu = { uiScope.launch { drawer.open() } }, onWalk = ::startWalk, onInsight = ::startInsight)
                return@Surface
            }
            LazyColumn(
                Modifier.safeDrawingPadding().imePadding().fillMaxSize().testTag("home"),
                state = if (page == "archive") archiveScroll else homeScroll,
                contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(14.dp),
            ) {
                item {
                    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                        if (editing) TextButton(onClick = mobile::finishEditing, modifier = Modifier.testTag("editor_back")) { Text(strings.text("返回笔记", "Back to note")) }
                        else if (page == "archive") TextButton(onClick = { navigate("home") }) { Text(strings.text("返回主页", "Back to home")) }
                        else Row {
                            TextButton(onClick = { uiScope.launch { drawer.open() } }, modifier = Modifier.testTag("open_menu")) { Text(strings.text("导航", "Menu")) }
                            Text("Moerise", fontSize = 22.sp, modifier = Modifier.padding(top = 8.dp))
                        }
                        if (!editing) Row {
                            TextButton(onClick = { searchFrom = page; navigate("search") }, modifier = Modifier.testTag("open_search")) { Text(strings.text("搜索", "Search")) }
                            TextButton(onClick = { navigate("settings") }) { Text(strings.text("设置", "Settings")) }
                        }
                    }
                }
                if (connection.needsLogin || connection.error != null) item {
                    Text(strings.text("连接中断，草稿仍会保存在本机。", "Connection interrupted. Drafts are saved on this device."), color = MoeriseColors.Muted)
                    TextButton(onClick = { if (connection.needsLogin) navigate("connect") else session.retrySession() }) {
                        Text(if (connection.needsLogin) strings.text("重新登录", "Sign in again") else strings.text("重试连接", "Retry connection"))
                    }
                }
                if (page != "archive" || editing) item {
                    if (ready) HomeEditor(editor, mobile.editor, strings, connection.connected, onConflict = { conflict = true })
                    else {
                        LinearProgressIndicator(Modifier.fillMaxWidth())
                        editor.error?.let { Text(editorError(it, strings), color = MaterialTheme.colorScheme.error) }
                        TextButton(onClick = mobile::retryDraft) { Text(strings.text("重试读取草稿", "Retry opening draft")) }
                    }
                }
                item {
                    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                        Text(if (page == "archive") strings.text("归档笔记", "Archived notes") else strings.text("笔记", "Notes"), modifier = Modifier.padding(top = 12.dp))
                        TextButton(onClick = timelineController::refresh, enabled = connection.connected && !timeline.loading) {
                            Text(strings.text("刷新", "Refresh"))
                        }
                    }
                    if (!timeline.fresh && timeline.notes.isNotEmpty()) Text(strings.text("本机快照", "Saved snapshot"), color = MoeriseColors.Muted)
                    timeline.error?.let { Text(connectionError(it, strings), color = MaterialTheme.colorScheme.error) }
                }
                items(if (editing) emptyList() else timeline.notes, key = { it.name }) { note ->
                    Surface(
                        shape = RoundedCornerShape(16.dp), border = BorderStroke(1.dp, MoeriseColors.Line),
                        modifier = Modifier.fillMaxWidth().clickable { mobile.openNote(note.name) },
                    ) {
                        Column(Modifier.padding(18.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Text(note.content, maxLines = 7, lineHeight = 25.sp)
                            Text(displayServiceTime(note.createTime, connection.profile!!.timezone), style = MaterialTheme.typography.labelSmall, color = MoeriseColors.Muted)
                        }
                    }
                }
                item {
                    if (timeline.loading) LinearProgressIndicator(Modifier.fillMaxWidth())
                    else if (timeline.nextPageToken.isNotBlank()) {
                        TextButton(onClick = timelineController::more, enabled = connection.connected) { Text(strings.text("加载更多", "Load more")) }
                    } else if (timeline.notes.isEmpty() && timeline.fresh) {
                        Text(strings.text("从第一篇笔记开始。", "Start with your first note."), color = MoeriseColors.Muted)
                    }
                }
            }
            if (conflict && ready) ConflictDialog(editor, mobile.editor, strings) { conflict = false }
        }
        }
    }
}

@Composable
private fun HomeEditor(state: EditorUiState, controller: EditorController, strings: Strings, connected: Boolean, onConflict: () -> Unit) {
    val draft = state.draft ?: return
    Surface(shape = RoundedCornerShape(18.dp), border = BorderStroke(1.dp, MoeriseColors.Line)) {
        Column(Modifier.padding(18.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            BasicTextField(
                value = draft.content, onValueChange = controller::change,
                modifier = Modifier.fillMaxWidth().heightIn(min = 140.dp, max = 320.dp)
                    .verticalScroll(rememberScrollState()).testTag("note_editor"),
                textStyle = TextStyle(color = MoeriseColors.Ink, fontSize = 17.sp, lineHeight = 29.sp),
                decorationBox = { inner ->
                    Box {
                        if (draft.content.isEmpty()) Text(strings.text("写下此刻的想法…", "Write what’s on your mind…"), color = MoeriseColors.Muted, fontSize = 17.sp)
                        inner()
                    }
                },
            )
            val edits = draft.attachmentEdits()
            key(draft.accountKey, draft.id) {
                AttachmentPicker(
                    enabled = !state.busy && !state.importing && state.pending == null && draft.conflictJson == null,
                    strings = strings, onPicked = { controller.attach(draft.accountKey, draft.id, it) },
                    onError = { controller.attachmentError(it, draft.id) }, onBusy = { controller.importing(draft.id, it) },
                )
            }
            edits.staged.forEach { file ->
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                    Text(file.filename, modifier = Modifier.weight(1f).padding(top = 12.dp))
                    TextButton(onClick = { controller.removeStaged(file.id) }, enabled = !state.busy && state.pending == null && draft.conflictJson == null) {
                        Text(strings.text("移除", "Remove"))
                    }
                }
            }
            val baseline = draft.baselineJson?.let { CoreJson.decodeFromString<Note>(it) }
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
            if (state.serverSaved) Text(strings.text("提交内容已存服务器", "Submitted content saved to server"), color = MoeriseColors.Blue, modifier = Modifier.testTag("note_server_saved"))
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                Text(
                    if (draft.revision == state.savedRevision) strings.text("草稿已存本机", "Draft saved on device")
                    else strings.text("正在保存草稿…", "Saving draft…"),
                    style = MaterialTheme.typography.labelSmall, color = MoeriseColors.Muted,
                    modifier = Modifier.padding(top = 14.dp),
                )
                if (draft.conflictJson != null) {
                    Button(onClick = onConflict) { Text(strings.text("处理冲突", "Resolve conflict")) }
                } else Button(
                    onClick = controller::save, modifier = Modifier.testTag("save_note"),
                    enabled = !state.busy && !state.importing && connected && (draft.content.isNotBlank() || edits.staged.isNotEmpty() ||
                        baseline?.attachments?.any { it["name"]?.jsonPrimitive?.content !in edits.removed } == true || state.pending != null),
                ) {
                    Text(when {
                        state.busy -> strings.text("保存中…", "Saving…")
                        state.pending != null -> strings.text("重试保存", "Retry save")
                        else -> strings.text("保存", "Save")
                    })
                }
            }
        }
    }
}

@Composable
private fun ConflictDialog(state: EditorUiState, controller: EditorController, strings: Strings, onClose: () -> Unit) {
    val draft = state.draft ?: return
    val body = draft.conflictJson?.let { CoreJson.parseToJsonElement(it).jsonObject } ?: return
    val remote = body["remote"]?.let { CoreJson.decodeFromJsonElement<Note>(it) }
    var merged by remember(draft.id, draft.conflictJson) { mutableStateOf(draft.content) }
    val attachments = draft.attachmentEdits()
    var selectedFiles by remember(draft.conflictJson) { mutableStateOf(emptySet<String>()) }
    var selectedRemovals by remember(draft.conflictJson) { mutableStateOf(attachments.removed.toSet()) }
    AlertDialog(
        onDismissRequest = onClose, title = { Text(strings.text("合并笔记", "Merge note")) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text(strings.text("提交的版本", "Submitted version"), style = MaterialTheme.typography.titleSmall)
                Text(body["submitted"]?.jsonObject?.get("content")?.jsonPrimitive?.content ?: "")
                Text(strings.text("服务器版本", "Server version"), style = MaterialTheme.typography.titleSmall)
                Text(remote?.content ?: strings.text("无法读取服务器版本。", "Could not read the server version."))
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
            }) { Text(strings.text("保存为草稿", "Keep merged draft")) }
        },
        dismissButton = { TextButton(onClick = onClose) { Text(strings.text("返回", "Back")) } },
    )
}

fun editorError(code: String, strings: Strings): String = when (code) {
    "LOCAL_WRITE_FAILED" -> strings.text("草稿落盘失败，请保留此页面并释放存储空间后重试。", "Draft could not be saved on device. Free storage and retry.")
    "CONTENT_TOO_LARGE" -> strings.text("正文不能超过 64 KiB。", "Content must fit within 64 KiB.")
    "CONTENT_EMPTY" -> strings.text("请先写下笔记。", "Write a note first.")
    "SOURCE_CHANGED" -> strings.text("服务器笔记已变化，请合并后再保存。", "The server note changed. Merge before saving.")
    "IDEMPOTENCY_CONFLICT" -> strings.text("保存请求发生冲突，已保留正文和原始请求。", "The save request conflicts. Your draft and original request have been kept.")
    "ATTACHMENTS_TOO_LARGE" -> strings.text("最多添加 10 个附件，合计不超过 8 MiB。", "Add up to 10 files, totaling no more than 8 MiB.")
    "ATTACHMENT_READ_FAILED", "ATTACHMENT_MISSING", "ATTACHMENT_CHANGED" -> strings.text("附件无法读取，请重新选择文件。", "Could not read the file. Select it again.")
    "ATTACHMENT_REFERENCED", "ATTACHMENT_DELETE_REJECTED" -> strings.text("正文仍引用要移除的图片，请修改引用后再保存。", "Content still references a removed image. Edit the reference before saving.")
    else -> connectionError(code, strings)
}

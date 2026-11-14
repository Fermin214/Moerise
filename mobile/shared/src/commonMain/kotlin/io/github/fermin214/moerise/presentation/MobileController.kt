package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*

class MobileController(
    val session: SessionController,
    private val repository: suspend () -> NoteRepository,
    private val scope: CoroutineScope,
    private val clearFiles: suspend () -> Unit = {},
) {
    val timeline = TimelineController(repository, { session.core }, scope)
    val archive = TimelineController(repository, { session.core }, scope, "ARCHIVED")
    val search = SearchController({ session.core }, { session.state.value.profile?.accountKey }, scope)
    val review = ReviewController({ session.core }, { session.state.value.profile?.accountKey }, scope)
    val walk = WalkController({ session.core }, { session.state.value.profile?.accountKey }, scope)
    val insights = InsightController(repository, { session.core }, { session.state.value.profile?.accountKey }, scope,
        { Strings(session.language.value).text("zh-CN", "en") }) { key, note -> timeline.onSaved(key, note) }
    val stars = StarMapController(repository, { session.core }, { session.state.value.profile?.accountKey }, scope)
    val localDrafts = LocalDraftController(repository, { session.state.value.profile?.accountKey }, scope)
    val detail = NoteDetailController(repository, { session.state.value.profile?.accountKey }, { session.core }, scope) {
        timeline.refresh()
        if (archive.state.value.fresh) archive.refresh()
    }
    val editor = EditorController(repository, { session.core }, scope) { key, note ->
        timeline.onSaved(key, note)
        detail.acceptSaved(key, note)
        scope.launch { runCatching { repository().collectFiles() } }
    }
    private val mutableEditing = MutableStateFlow(false)
    val editing = mutableEditing.asStateFlow()
    private val editorFrameAccount = MutableStateFlow<String?>(null)
    private val mutableClearing = MutableStateFlow(false)
    val clearing = mutableClearing.asStateFlow()
    private val mutableLocalError = MutableStateFlow<String?>(null)
    val localError = mutableLocalError.asStateFlow()
    private var visiblePage: String? = null
    private var inForeground = true

    fun page(value: String?) {
        visiblePage = value
        updateDiscovery()
        if (value == "drafts" && !clearing.value) scope.launch {
            if (editor.flushForLifecycle() && !clearing.value && visiblePage == "drafts") localDrafts.refresh()
        }
    }
    fun foreground(value: Boolean) {
        inForeground = value
        if (!value) search.leave()
        updateDiscovery()
    }
    private fun updateDiscovery() {
        val connected = session.state.value.connected && inForeground && !clearing.value
        review.visible(connected && visiblePage == "review")
        walk.visible(connected && visiblePage == "walk")
        insights.visible(connected && visiblePage == "insights")
        stars.visible(connected && visiblePage == "map")
    }

    init {
        scope.launch {
            session.state.map { it.profile?.accountKey }.distinctUntilChanged().collectLatest { key ->
                editorFrameAccount.value = null
                timeline.reset(key)
                archive.reset(key)
                search.clear()
                review.clear()
                walk.clear()
                insights.clear()
                stars.clear()
                detail.clear()
                localDrafts.clear()
                mutableEditing.value = false
                editor.open(key)
            }
        }
        scope.launch {
            editorFrameAccount.filterNotNull().collectLatest { key ->
                if (key == session.state.value.profile?.accountKey && !clearing.value) runCatching { timeline.readCache() }
            }
        }
        scope.launch {
            combine(
                session.state.map { Triple(it.profile?.accountKey, it.connected, it.needsLogin) }.distinctUntilChanged(),
                combine(editor.state, editorFrameAccount) { editor, frame ->
                    editor.accountKey.takeIf { editor.draft != null && it == frame }
                }.distinctUntilChanged(),
            ) { connection, ready -> connection to ready }.collect { (connection, ready) ->
                if (connection.third) { timeline.hideAccountData(); archive.hideAccountData(); search.clear(); review.clear(); walk.clear(); insights.clear(); stars.clear(); detail.clear(); clearFiles() }
                else if (!clearing.value && connection.second && connection.first == ready && ready != null) timeline.refresh()
                updateDiscovery()
            }
        }
    }

    fun logout() {
        if (clearing.value) return
        scope.launch {
            try { if (editor.flushForLifecycle()) { clearFiles(); session.logout() } }
            catch (error: Exception) { if (error is CancellationException) throw error }
        }
    }

    fun flush() {
        if (clearing.value) return
        scope.launch {
            if (clearing.value) return@launch
            editor.flushForLifecycle()
            if (!clearing.value) runCatching { insights.flush() }
        }
    }
    fun retryDraft() { scope.launch { if (!clearing.value) editor.open(session.state.value.profile?.accountKey) } }
    fun editorFrameReady(account: String) {
        if (account == session.state.value.profile?.accountKey && editor.state.value.accountKey == account && !clearing.value)
            editorFrameAccount.value = account
    }

    fun openNote(name: String) {
        if (clearing.value) return
        val fallback = (timeline.state.value.notes + archive.state.value.notes + search.state.value.notes +
            review.state.value.batch?.items.orEmpty().mapNotNull { it.note } +
            listOfNotNull(walk.state.value.result?.item?.note, stars.state.value.reader)).firstOrNull { it.name == name }
        val at = if (timeline.state.value.notes.any { it.name == name }) timeline.state.value.snapshotAt else 0
        flush(); search.leave(); page(null)
        detail.open(name, fallback, at, online = session.state.value.connected)
    }
    fun closeNote() { flush(); detail.clear() }
    fun editNote(note: Note) {
        scope.launch {
            if (clearing.value) return@launch
            val key = session.state.value.profile?.accountKey ?: return@launch
            detail.pauseRead()
            editor.open(key, note)
            mutableEditing.value = editor.state.value.draft?.slot == note.name
        }
    }
    fun finishEditing() {
        scope.launch {
            if (clearing.value) return@launch
            editor.open(session.state.value.profile?.accountKey)
            if (editor.state.value.draft?.slot == "home") {
                mutableEditing.value = false
                detail.state.value.name?.let { detail.open(it, detail.state.value.note, online = session.state.value.connected) }
            }
        }
    }

    fun openDraft(id: String, onOpened: () -> Unit) {
        scope.launch {
            if (clearing.value) return@launch
            val key = session.state.value.profile?.accountKey ?: return@launch
            detail.clear()
            editor.openStored(key, id)
            if (editor.state.value.draft?.id == id && key == session.state.value.profile?.accountKey) {
                mutableEditing.value = editor.state.value.draft?.slot != "home"
                onOpened()
            }
        }
    }
    fun forkConflict(content: String, attachments: AttachmentEdits, onCreated: () -> Unit) {
        scope.launch {
            if (!clearing.value && editor.forkConflict(content, attachments)) {
                mutableEditing.value = false; detail.clear(); onCreated()
            }
        }
    }
    fun clearLocalData() {
        if (clearing.value) return
        val account = session.state.value.profile?.accountKey ?: return
        mutableClearing.value = true
        mutableLocalError.value = null
        visiblePage = null
        editorFrameAccount.value = null
        updateDiscovery()
        scope.launch {
            try {
                search.clear(); review.clear(); walk.clear()
                editor.stopForRemoval(); detail.stopForRemoval()
                insights.stopForRemoval(); stars.stopForRemoval()
                timeline.stopForRemoval(); archive.stopForRemoval(); localDrafts.stopForRemoval()
                mutableEditing.value = false
                repository().clearAccount(account)
                clearFiles()
                session.logout()
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                mutableLocalError.value = "LOCAL_WRITE_FAILED"
                if (account == session.state.value.profile?.accountKey) {
                    timeline.reset(account); archive.reset(account); editor.open(account)
                }
            } finally { mutableClearing.value = false }
        }
    }
}

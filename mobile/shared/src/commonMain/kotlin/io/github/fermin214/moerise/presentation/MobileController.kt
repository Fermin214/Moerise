package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.NoteRepository
import io.github.fermin214.moerise.domain.Note
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*

class MobileController(
    val session: SessionController,
    repository: suspend () -> NoteRepository,
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
    private var visiblePage: String? = null
    private var inForeground = true

    fun page(value: String?) { visiblePage = value; updateDiscovery() }
    fun foreground(value: Boolean) {
        inForeground = value
        if (!value) search.leave()
        updateDiscovery()
    }
    private fun updateDiscovery() {
        val connected = session.state.value.connected && inForeground
        review.visible(connected && visiblePage == "review")
        walk.visible(connected && visiblePage == "walk")
        insights.visible(connected && visiblePage == "insights")
        stars.visible(connected && visiblePage == "map")
    }

    init {
        scope.launch {
            session.state.map { it.profile?.accountKey }.distinctUntilChanged().collectLatest { key ->
                timeline.reset(key)
                archive.reset(key)
                search.clear()
                review.clear()
                walk.clear()
                insights.clear()
                stars.clear()
                detail.clear()
                mutableEditing.value = false
                editor.open(key)
                if (key != null) runCatching { timeline.readCache() }
            }
        }
        scope.launch {
            combine(
                session.state.map { Triple(it.profile?.accountKey, it.connected, it.needsLogin) }.distinctUntilChanged(),
                editor.state.map { it.accountKey.takeIf { _ -> it.draft != null } }.distinctUntilChanged(),
            ) { connection, ready -> connection to ready }.collect { (connection, ready) ->
                if (connection.third) { timeline.hideAccountData(); archive.hideAccountData(); search.clear(); review.clear(); walk.clear(); insights.clear(); stars.clear(); detail.clear(); clearFiles() }
                else if (connection.second && connection.first == ready && ready != null) timeline.refresh()
                updateDiscovery()
            }
        }
    }

    fun logout() {
        scope.launch {
            try { if (editor.flushForLifecycle()) { clearFiles(); session.logout() } }
            catch (error: Exception) { if (error is CancellationException) throw error }
        }
    }

    fun flush() { scope.launch { editor.flushForLifecycle(); runCatching { insights.flush() } } }
    fun retryDraft() { scope.launch { editor.open(session.state.value.profile?.accountKey) } }

    fun openNote(name: String) { flush(); search.leave(); page(null); detail.open(name) }
    fun closeNote() { flush(); detail.clear() }
    fun editNote(note: Note) {
        scope.launch {
            val key = session.state.value.profile?.accountKey ?: return@launch
            editor.open(key, note)
            mutableEditing.value = editor.state.value.draft?.slot == note.name
        }
    }
    fun finishEditing() {
        scope.launch {
            editor.open(session.state.value.profile?.accountKey)
            if (editor.state.value.draft?.slot == "home") mutableEditing.value = false
        }
    }
}

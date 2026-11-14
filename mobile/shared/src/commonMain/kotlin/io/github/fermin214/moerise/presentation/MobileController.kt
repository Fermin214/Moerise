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

    init {
        scope.launch {
            session.state.map { it.profile?.accountKey }.distinctUntilChanged().collectLatest { key ->
                timeline.reset(key)
                archive.reset(key)
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
                if (connection.third) { timeline.hideAccountData(); archive.hideAccountData(); detail.clear(); clearFiles() }
                else if (connection.second && connection.first == ready && ready != null) timeline.refresh()
            }
        }
    }

    fun logout() {
        scope.launch {
            try { if (editor.flushForLifecycle()) { clearFiles(); session.logout() } }
            catch (error: Exception) { if (error is CancellationException) throw error }
        }
    }

    fun flush() { scope.launch { editor.flushForLifecycle() } }
    fun retryDraft() { scope.launch { editor.open(session.state.value.profile?.accountKey) } }

    fun openNote(name: String) { flush(); detail.open(name) }
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

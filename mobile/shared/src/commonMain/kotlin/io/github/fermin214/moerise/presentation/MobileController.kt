package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.NoteRepository
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*

class MobileController(
    val session: SessionController,
    repository: suspend () -> NoteRepository,
    private val scope: CoroutineScope,
) {
    val timeline = TimelineController(repository, { session.core }, scope)
    val editor = EditorController(repository, { session.core }, scope, timeline::onSaved)

    init {
        scope.launch {
            session.state.map { it.profile?.accountKey }.distinctUntilChanged().collectLatest { key ->
                timeline.reset(key)
                editor.open(key)
                if (key != null) runCatching { timeline.readCache() }
            }
        }
        scope.launch {
            combine(
                session.state.map { Triple(it.profile?.accountKey, it.connected, it.needsLogin) }.distinctUntilChanged(),
                editor.state.map { it.accountKey.takeIf { _ -> it.draft != null } }.distinctUntilChanged(),
            ) { connection, ready -> connection to ready }.collect { (connection, ready) ->
                if (connection.third) timeline.hideAccountData()
                else if (connection.second && connection.first == ready && ready != null) timeline.refresh()
            }
        }
    }

    fun logout() {
        scope.launch {
            try { if (editor.flushForLifecycle()) session.logout() }
            catch (error: Exception) { if (error is CancellationException) throw error }
        }
    }

    fun flush() { scope.launch { editor.flushForLifecycle() } }
    fun retryDraft() { scope.launch { editor.open(session.state.value.profile?.accountKey) } }
}

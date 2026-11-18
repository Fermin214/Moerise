package io.github.fermin214.moerise.data

import io.github.fermin214.moerise.presentation.EditorController
import io.github.fermin214.moerise.presentation.NoteWriteQueue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.sync.Mutex

internal class DraftQueueFixture(
    private val repository: () -> NoteRepository, private val client: CoreClient, private val scope: CoroutineScope,
) {
    private val lock = Mutex()
    val composer = newEditor()
    val otherEditors = MutableStateFlow<List<EditorController>>(emptyList())
    val queue = NoteWriteQueue({ repository() }, lock, { listOf(composer) + otherEditors.value },
        { composer.state.value.accountKey })

    fun newEditor() = EditorController({ repository() }, { client }, scope, lock)
}

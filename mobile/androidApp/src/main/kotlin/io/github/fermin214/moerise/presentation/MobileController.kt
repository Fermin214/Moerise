package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*

class MobileController(
    val session: SessionController,
    private val repository: suspend () -> NoteRepository,
    private val scope: CoroutineScope,
    private val clearFiles: suspend () -> Unit = {},
    private val onQueueChanged: () -> Unit = {},
    initialForeground: Boolean = true,
    installPreferences: InstallPreferences? = null,
) {
    val privacy = PrivacyController(installPreferences, session, scope)
    private val editorLock = Mutex()
    private val registryLock = Mutex()
    private val mutableNoteEditors = MutableStateFlow<Map<String, EditorController>>(emptyMap())
    val noteEditors = combine(mutableNoteEditors, session.state.map { it.profile?.accountKey }.distinctUntilChanged()) { editors, key ->
        editors.filterValues { it.state.value.accountKey == key && key != null }
    }.stateIn(scope, SharingStarted.Eagerly, emptyMap())
    private var focusedEditor: String? = null
    val noteEditor: EditorController get() = focusedEditor?.let(::getNoteEditor) ?: editor
    val timeline = TimelineController(repository, { session.core }, scope)
    val archive = TimelineController(repository, { session.core }, scope, "ARCHIVED")
    val filterReads = FilterReads(repository, { session.core?.takeIf { session.state.value.connected } }, { session.state.value.profile?.takeUnless { session.state.value.needsLogin }?.accountKey },
        { session.state.value.profile?.timezone ?: "UTC" }, scope)
    val search = SearchController({ session.core }, { session.state.value.profile?.accountKey }, scope, filterReads, privacy, repository) {
        session.state.value.profile?.timezone ?: "UTC"
    }
    val sidebar = SearchController({ session.core }, { session.state.value.profile?.accountKey }, scope, filterReads, repository = repository) {
        session.state.value.profile?.timezone ?: "UTC"
    }
    val review = ReviewController({ session.core }, { session.state.value.profile?.accountKey }, scope, privacy, repository)
    val walk = WalkController({ session.core }, { session.state.value.profile?.accountKey }, scope, privacy, repository, enablePrefetch = true, online = { session.state.value.connected })
    val stars = StarMapController(repository, { session.core }, { session.state.value.profile?.accountKey }, scope, privacy, online = { session.state.value.connected })
    val settings = SettingsController({ session.core }, { session.state.value.profile?.accountKey }, scope)
    val homeNote = NoteDetailController(repository, { session.state.value.profile?.accountKey }, { session.core }, scope, ::noteChanged)
    val noteActions = NoteDetailController(repository, { session.state.value.profile?.accountKey }, { session.core }, scope, ::noteChanged)
    private fun noteChanged(name: String, previous: Note?, note: Note?) {
        val key = session.state.value.profile?.accountKey ?: return
        filterReads.changed(previous ?: (timeline.state.value.notes + archive.state.value.notes).find { it.name == name }, note)
        if (note?.state == "NORMAL") homeNote.acceptSaved(session.state.value.profile?.accountKey.orEmpty(), note)
        else if (homeNote.state.value.name == name) homeNote.clear()
        for (list in listOf(timeline, archive)) {
            if (note == null) list.remove(name) else list.onSaved(key, note)
        }
    }
    private fun newEditor() = EditorController(repository, { session.core }, scope, editorLock)
    val editor = newEditor()
    private val writes = NoteWriteQueue(repository, editorLock, { listOf(editor) + mutableNoteEditors.value.values },
        { session.state.value.profile?.accountKey?.takeIf { key ->
            editor.state.value.let { it.accountKey == key && it.draft != null }
        } }, privacy) { key, note, draftId ->
        filterReads.changed(timeline.state.value.notes.firstOrNull { it.name == note.name }, note)
        timeline.onSaved(key, note, draftId); homeNote.acceptSaved(key, note)
    }
    val sync = SyncController(session, repository, writes, scope, {
        scope.launch {
            val account = session.state.value.profile?.accountKey ?: return@launch
            val synced = runCatching { repository().isNotesSynced(account) }.getOrDefault(false)
            if (synced) {
                timeline.reloadLocal()
                archive.reloadLocal()
            } else {
                timeline.refresh()
                if (archive.state.value.fresh) archive.refresh()
            }
            runCatching { repository().collectFiles() }
        }
    }, initialForeground, privacy)
    private val mutableEditing = MutableStateFlow(false)
    val editing = mutableEditing.asStateFlow()
    private val editorFrameAccount = MutableStateFlow<String?>(null)
    private val mutableClearing = MutableStateFlow(false)
    val clearing = mutableClearing.asStateFlow()
    private val mutableLocalError = MutableStateFlow<String?>(null)
    val localError = mutableLocalError.asStateFlow()
    private var visiblePage: String? = null
    private var inForeground = initialForeground
    private val prefetchAllowed = MutableStateFlow(initialForeground)

    fun page(value: String?) {
        visiblePage = value
        prefetchAllowed.value = inForeground && (value == null || value == "home")
        updateDiscovery()
        if (value == "drafts" && !clearing.value) scope.launch {
            editor.flushForLifecycle()
        }
    }
    fun foreground(value: Boolean) {
        inForeground = value
        privacy.foreground(value)
        prefetchAllowed.value = value && (visiblePage == null || visiblePage == "home")
        sync.foreground(value)
        if (!value) search.leave()
        if (!value) onQueueChanged()
        updateDiscovery()
    }
    private fun updateDiscovery() {
        val available = session.state.value.profile != null && !session.state.value.needsLogin && inForeground && !clearing.value
        val connected = available && session.state.value.connected
        review.visible(available && visiblePage == "review")
        walk.connectivityChanged()
        walk.visible(available && visiblePage == "walk")
        stars.connectivityChanged()
        stars.visible(available && visiblePage == "map" && session.state.value.profile?.stargazingEnabled != false)
    }

    init {
        session.onRequestFailure = { started, error ->
            privacy.record("request_error", usageResult(error), started, if (inForeground) "user" else "background", error)
        }
        scope.launch {
            var saved: Pair<String, Long>? = null
            settings.state.collect { value ->
                if (value.status != "SETTINGS_SAVED") return@collect
                val key = session.state.value.profile?.accountKey ?: return@collect
                val snapshot = value.snapshot ?: return@collect
                val version = snapshot["version"]?.jsonPrimitive?.longOrNull ?: return@collect
                if (saved == (key to version)) return@collect
                saved = key to version
                val values = snapshot["settings"]?.jsonObject ?: return@collect
                val timezone = values["timezone"]?.jsonPrimitive?.content ?: return@collect
                val stargazingEnabled = values["stargazing"]?.jsonObject?.get("enabled")?.jsonPrimitive?.booleanOrNull
                    ?: return@collect
                session.acceptSettings(key, timezone, stargazingEnabled)
                if (!stargazingEnabled) stars.clear()
                timeline.refresh()
                if (archive.state.value.fresh) archive.refresh()
                review.clear()
                walk.configurationChanged()
                if (search.state.value.query != SearchQuery()) search.search()
                updateDiscovery()
            }
        }
        scope.launch {
            stars.state.map { it.disabledReason }.distinctUntilChanged().collect { reason ->
                if (reason == "stargazing") session.state.value.profile?.let {
                    session.acceptSettings(it.accountKey, it.timezone, false)
                }
            }
        }
        scope.launch {
            var previousAccount: String? = null
            session.state.map { it.profile?.accountKey }.distinctUntilChanged().collectLatest { key ->
                val switched = previousAccount != null && key != null && previousAccount != key
                previousAccount = key
                filterReads.clear(); sidebar.clear()
                editorFrameAccount.value = null
                timeline.reset(key)
                archive.reset(key)
                search.clear()
                review.clear()
                walk.clear()
                stars.clear()
                settings.clear()
                homeNote.clear()
                noteActions.clear()
                closeEditors()
                if (switched) clearFiles()
                editor.open(key)
                if (key != null) restoreEditing(key)
            }
        }
        scope.launch {
            combine(editorFrameAccount, prefetchAllowed, timeline.state.map { it.fresh }.distinctUntilChanged(),
                session.state.map { it.profile?.takeIf { _ -> it.connected && !it.needsLogin } }.distinctUntilChanged()) { frame, allowed, fresh, profile ->
                profile?.takeIf { allowed && fresh && frame == it.accountKey }
            }.distinctUntilChanged().collectLatest { profile ->
                if (profile == null) return@collectLatest
                delay(250)
                try {
                    if (!repository().isNotesSynced(profile.accountKey)) {
                        filterReads.tags("NORMAL")
                        filterReads.calendar("NORMAL", serviceMonth(profile.timezone))
                    }
                    review.prefetch()
                    if (profile.stargazingEnabled) stars.prefetch()
                } catch (error: Exception) { if (error is CancellationException) throw error }
            }
        }
        scope.launch {
            editorFrameAccount.filterNotNull().collectLatest { key ->
                if (key == session.state.value.profile?.accountKey && !clearing.value) runCatching { timeline.readCache() }
            }
        }
        scope.launch {
            combine(
                session.state.map { Triple(it.profile, it.connected, it.needsLogin) }.distinctUntilChanged(),
                combine(editor.state, editorFrameAccount) { editor, frame ->
                    editor.accountKey.takeIf { editor.draft != null && it == frame }
                }.distinctUntilChanged(),
            ) { connection, ready -> connection to ready }.collect { (connection, ready) ->
                if (connection.third) { filterReads.clear(); sidebar.clear(); timeline.hideAccountData(); archive.hideAccountData(); search.clear(); review.clear(); walk.clear(); stars.clear(); settings.clear(); homeNote.clear(); clearFiles() }
                else if (!clearing.value && connection.second && connection.first?.accountKey == ready && ready != null) timeline.refresh()
                if (connection.first?.stargazingEnabled == false) stars.clear()
                updateDiscovery()
            }
        }
    }

    fun logout() {
        if (clearing.value) return
        scope.launch {
            try { if (flushEditors()) { sync.stop(); clearFiles(); session.logout() } }
            catch (error: Exception) { if (error is CancellationException) throw error }
        }
    }

    fun flush() {
        if (clearing.value) return
        scope.launch {
            if (clearing.value) return@launch
            flushEditors()
        }
    }
    fun retryDraft() {
        scope.launch {
            if (clearing.value) return@launch
            mutableLocalError.value = null
            val account = session.state.value.profile?.accountKey
            editor.open(account)
            if (account != null && mutableNoteEditors.value.isEmpty()) restoreEditing(account)
        }
    }
    fun save() {
        val key = focusedEditor
        if (key != null && getNoteEditor(key) != null) saveNote(key) else saveComposer()
    }
    fun saveComposer() {
        scope.launch {
            if (clearing.value) return@launch
            registryLock.withLock {
                if (mutableNoteEditors.value.isNotEmpty()) return@withLock
                if (enqueue(editor, true)) {
                    sync.changed()
                    onQueueChanged()
                }
            }
        }
    }
    fun saveNote(key: String) {
        scope.launch {
            if (clearing.value) return@launch
            registryLock.withLock {
                val active = getNoteEditor(key) ?: return@withLock
                if (enqueue(active, false)) {
                    if (!forgetEditing(active, persist = false)) return@withLock
                    sync.changed()
                    onQueueChanged()
                }
            }
        }
    }
    fun synchronize() {
        if (!clearing.value) sync.userRequest()
    }
    private suspend fun enqueue(editor: EditorController, returnHome: Boolean): Boolean {
        val draft = editor.state.value.draft ?: return false
        val started = epochMillis()
        val queued = editor.enqueue(returnHome) { frozen -> privacy.beginWrite("${frozen.id}:${frozen.revision}", started) }
        val result = if (queued) "success" else "failure"
        privacy.record(if (draft.targetName == null) "create" else "edit", result, started)
        privacy.record("enqueue", result, started, code = editor.state.value.error)
        return queued
    }
    fun changeNoteState(note: Note) {
        if (!clearing.value) {
            if (homeNote.state.value.name == note.name && homeNote.state.value.pendingAction)
                homeNote.changeState()
            else noteActions.changeState(note)
        }
    }
    fun deleteNote(note: Note) {
        if (!clearing.value) noteActions.delete(note)
    }
    fun checkNoteDeletion(name: String) {
        if (clearing.value) return
        if (noteActions.state.value.name == name) noteActions.checkDeletion()
        else if (homeNote.state.value.name == name) homeNote.checkDeletion()
    }
    fun editorFrameReady(account: String) {
        if (account == session.state.value.profile?.accountKey && editor.state.value.accountKey == account && !clearing.value)
            editorFrameAccount.value = account
    }

    fun openNote(name: String, cached: Note? = null) {
        if (clearing.value) return
        val fallback = cached ?: (timeline.state.value.notes + archive.state.value.notes + search.state.value.notes +
            review.state.value.batch?.items.orEmpty().mapNotNull { it.note } +
            listOfNotNull(walk.state.value.result?.item?.note, stars.state.value.reader)).firstOrNull { it.name == name }
        val at = if (timeline.state.value.notes.any { it.name == name }) timeline.state.value.snapshotAt else 0
        flush(); search.clear(); page("home")
        homeNote.open(name, fallback, at, online = session.state.value.connected)
    }
    fun closeNote() { flush(); homeNote.clear() }
    fun editNote(note: Note) {
        openEditing(note.name, { controller, account, onRestored ->
            val queued = repository().queuedNotes(account).firstOrNull { it.targetName == note.name }
            if (queued != null) controller.openStored(account, queued.id, onRestored)
            else controller.open(account, note, onRestored)
        }) {
            openNote(note.name, note)
        }
    }
    fun finishEditing() {
        focusedEditor?.let(::finishEditing)
    }
    fun finishEditing(key: String) {
        scope.launch {
            if (clearing.value) return@launch
            registryLock.withLock {
                getNoteEditor(key)?.let { forgetEditing(it) }
            }
        }
    }

    fun openDraft(id: String, onOpened: () -> Unit) {
        openEditing(id, { controller, account, onRestored -> controller.openStored(account, id, onRestored) }) {
            homeNote.clear(); page("home"); onOpened()
        }
    }

    fun getNoteEditor(key: String): EditorController? {
        val account = session.state.value.profile?.accountKey ?: return null
        return mutableNoteEditors.value[key]?.takeIf { it.state.value.accountKey == account } ?:
            mutableNoteEditors.value.values.firstOrNull {
                it.state.value.accountKey == account && (it.state.value.draft?.id == key || it.state.value.draft?.targetName == key)
            }
    }

    fun focusNoteEditor(key: String) {
        getNoteEditor(key)?.let { active ->
            focusedEditor = mutableNoteEditors.value.entries.firstOrNull { it.value === active }?.key
        }
    }

    private fun openEditing(
        key: String,
        restore: suspend (EditorController, String, () -> Unit) -> Unit,
        onOpened: () -> Unit,
    ) {
        scope.launch {
            if (clearing.value) return@launch
            registryLock.withLock {
                val account = session.state.value.profile?.accountKey ?: return@withLock
                try {
                    mutableLocalError.value = null
                    if (!editor.flushForLifecycle()) return@withLock
                    val existing = getNoteEditor(key)
                    for (previous in mutableNoteEditors.value.values.distinct()) {
                        if (previous !== existing && !removeEditor(previous)) return@withLock
                    }
                    val active = existing ?: newEditor()
                    if (existing == null) restore(active, account) {
                        // 恢复与登记共享本机锁，避免同步确认遗漏编辑器。
                        mutableNoteEditors.value = mapOf(key to active)
                    }
                    if (clearing.value || account != session.state.value.profile?.accountKey || active.state.value.draft == null) {
                        if (!clearing.value && account == session.state.value.profile?.accountKey)
                            mutableLocalError.value = active.state.value.error ?: "LOCAL_READ_FAILED"
                        if (existing == null) removeEditor(active)
                        return@withLock
                    }
                    rememberEditing(account, key, requireNotNull(active.state.value.draft).id)
                    focusNoteEditor(key)
                    mutableEditing.value = true
                    onOpened()
                } catch (error: Exception) {
                    if (error is CancellationException) throw error
                    mutableLocalError.value = "LOCAL_WRITE_FAILED"
                }
            }
        }
    }

    private suspend fun rememberEditing(account: String, key: String? = null, id: String? = null) {
        repository().putFeature(account, "active-note-editor", buildJsonObject {
            key?.let { put("key", it) }
            id?.let { put("draft_id", it) }
        }.toString())
    }

    private suspend fun restoreEditing(account: String) = registryLock.withLock {
        try {
            val saved = repository().feature(account, "active-note-editor")
                ?.let { CoreJson.parseToJsonElement(it).jsonObject } ?: return@withLock
            val id = saved["draft_id"]?.jsonPrimitive?.content ?: return@withLock
            val key = saved["key"]?.jsonPrimitive?.content ?: return@withLock
            if (clearing.value || account != session.state.value.profile?.accountKey) return@withLock
            val active = newEditor()
            active.openStored(account, id) { mutableNoteEditors.value = mapOf(key to active) }
            if (active.state.value.draft != null) {
                focusedEditor = key
                mutableEditing.value = true
            } else mutableLocalError.value = "LOCAL_READ_FAILED"
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            mutableLocalError.value = "LOCAL_READ_FAILED"
        }
    }

    private suspend fun forgetEditing(active: EditorController, persist: Boolean = true): Boolean {
        val account = active.state.value.accountKey ?: return false
        return try {
            if (!removeEditor(active, persist)) return false
            rememberEditing(account)
            mutableLocalError.value = null
            true
        } catch (error: Exception) {
            if (error is CancellationException) throw error
            mutableLocalError.value = "LOCAL_WRITE_FAILED"
            false
        }
    }

    private suspend fun removeEditor(active: EditorController, persist: Boolean = true): Boolean {
        if (!active.close(persist)) return false
        mutableNoteEditors.update { entries -> entries.filterValues { it !== active } }
        if (focusedEditor !in mutableNoteEditors.value) focusedEditor = mutableNoteEditors.value.keys.lastOrNull()
        mutableEditing.value = mutableNoteEditors.value.isNotEmpty()
        return true
    }

    private suspend fun flushEditors(): Boolean {
        if (!editor.flushForLifecycle()) return false
        for (active in mutableNoteEditors.value.values.distinct()) if (!active.flushForLifecycle()) return false
        return true
    }

    private suspend fun closeEditors(): Boolean = registryLock.withLock {
        var success = true
        for (active in mutableNoteEditors.value.values.distinct()) if (!removeEditor(active)) success = false
        success
    }

    fun forkConflict(content: String, attachments: AttachmentEdits, onCreated: () -> Unit) {
        val key = focusedEditor ?: editor.state.value.draft?.id ?: return
        forkConflict(key, content, attachments, onCreated)
    }
    fun forkConflict(key: String, content: String, attachments: AttachmentEdits, onCreated: () -> Unit) {
        scope.launch {
            if (clearing.value) return@launch
            registryLock.withLock {
                val active = getNoteEditor(key) ?: editor.takeIf { it.state.value.draft?.id == key } ?: return@withLock
                if (active === editor) {
                    if (active.forkConflict(content, attachments)) {
                        homeNote.clear(); onCreated(); sync.changed(); onQueueChanged()
                    }
                    return@withLock
                }
                if (editor.flushForLifecycle() && active.forkConflict(content, attachments)) {
                    val restored = active.state.value
                    if (editor.adoptDraft(RestoredDraft(requireNotNull(restored.draft), restored.pending), preservePrevious = true)) {
                        if (!forgetEditing(active, persist = false)) return@withLock
                        homeNote.clear(); onCreated(); sync.changed(); onQueueChanged()
                    }
                }
            }
        }
    }
    fun clearLocalData(account: String) {
        if (clearing.value || account != session.state.value.profile?.accountKey) return
        mutableClearing.value = true
        mutableLocalError.value = null
        visiblePage = null
        editorFrameAccount.value = null
        updateDiscovery()
        scope.launch {
            try {
                session.stopForRemoval()
                sync.stop()
                search.clear(); review.clear(); walk.clear()
                settings.clear()
                if (!closeEditors()) error("LOCAL_WRITE_FAILED")
                editor.stopForRemoval(); homeNote.stopForRemoval(); noteActions.stopForRemoval()
                stars.stopForRemoval()
                timeline.stopForRemoval(); archive.stopForRemoval()
                mutableEditing.value = false
                repository().clearAccount(account)
                clearFiles()
                session.logout()
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                mutableLocalError.value = "LOCAL_REMOVE_FAILED"
                if (account == session.state.value.profile?.accountKey) {
                    session.recoverAfterRemovalFailure()
                    timeline.reset(account); archive.reset(account); editor.open(account)
                }
            } finally { mutableClearing.value = false }
        }
    }
}

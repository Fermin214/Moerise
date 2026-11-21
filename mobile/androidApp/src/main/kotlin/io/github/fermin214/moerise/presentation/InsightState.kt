package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.domain.*

data class InsightState(
    val recovery: InsightRecovery = InsightRecovery(), val chat: ChatSession? = null,
    val materials: ChatMaterials? = null,
    val history: List<ChatSummary> = emptyList(), val historyNext: String = "", val tab: Int = 0,
    val busy: Boolean = false, val streaming: Boolean = false,
    val error: String? = null,
    val conflicted: Boolean = false, val stopping: Boolean = false, val deleting: Boolean = false,
    val capabilities: ChatCapabilities = ChatCapabilities(materialSelection = true, saveSelection = true), val starting: String = "",
    val confirmed: List<ChatTurn> = emptyList(), val failedGeneration: Boolean = false,
    val saving: Boolean = false, val pendingSave: ChatSaveIntent? = null, val saveError: String? = null,
    val savedOperation: String = "",
) {
    val running get() = chat?.turns?.any { it.assistant.status == "running" } == true
    val canStop get() = streaming || running || recovery.partial?.assistant?.status == "running"
    val locked get() = busy || running || conflicted || stopping || deleting ||
        recovery.generation != null || recovery.mutation != null || recovery.creation != null
    val canDelete get() = chat != null && pendingSave == null && !saving && !stopping && !deleting &&
        (recovery.mutation?.method in setOf(null, "DELETE")) &&
        recovery.creation == null && (!busy || streaming || running)
}


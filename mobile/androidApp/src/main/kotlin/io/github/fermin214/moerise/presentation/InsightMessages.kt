package io.github.fermin214.moerise.presentation

import io.github.fermin214.moerise.domain.*

data class SelectableMessage(val ref: ChatMessageRef, val content: String, val status: String, val eligible: Boolean)

internal fun selectableMessages(turns: List<ChatTurn>): List<SelectableMessage> = buildList {
    for (turn in turns) {
        if (turn.kind == "question") add(SelectableMessage(ChatMessageRef(turn.requestId, "user", turn.user.version),
            turn.user.content, turn.user.status, turn.user.content.isNotBlank()))
        add(SelectableMessage(ChatMessageRef(turn.requestId, "assistant", turn.assistant.version), turn.assistant.content,
            turn.assistant.status, turn.assistant.content.isNotBlank() && turn.assistant.status in setOf("completed", "canceled", "truncated")))
    }
}

internal fun selectionBody(messages: List<SelectableMessage>, english: Boolean): String {
    if (messages.size == 1) return messages.single().content
    return messages.joinToString("\n\n") { message ->
        val role = if (message.ref.role == "assistant") "Moerise" else if (english) "Me" else "我"
        "**$role**\n\n${message.content}"
    }
}

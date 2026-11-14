package io.github.fermin214.moerise.presentation

import androidx.compose.runtime.Composable
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.data.StagedAttachment
import io.github.fermin214.moerise.domain.Note
import kotlinx.serialization.json.JsonObject

@Composable
expect fun AttachmentPicker(enabled: Boolean, strings: Strings, onPicked: (List<StagedAttachment>) -> Boolean, onError: (String) -> Unit, onBusy: (Boolean) -> Unit)

@Composable
expect fun OpenAttachmentButton(note: Note, item: JsonObject, core: CoreClient, strings: Strings)

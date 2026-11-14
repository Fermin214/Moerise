package io.github.fermin214.moerise.presentation

import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.ImageBitmap
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.data.StagedAttachment
import io.github.fermin214.moerise.domain.Note
import kotlinx.serialization.json.JsonObject

@Composable
actual fun AttachmentPicker(enabled: Boolean, strings: Strings, onPicked: (List<StagedAttachment>) -> Boolean, onError: (String) -> Unit) = Unit

@Composable
actual fun OpenAttachmentButton(note: Note, item: JsonObject, core: CoreClient, strings: Strings) = Unit

actual suspend fun decodeProtectedImage(bytes: ByteArray): ImageBitmap? = null

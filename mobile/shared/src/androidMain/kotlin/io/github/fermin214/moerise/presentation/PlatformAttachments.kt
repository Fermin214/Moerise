package io.github.fermin214.moerise.presentation

import android.content.ClipData
import android.content.Intent
import android.graphics.BitmapFactory
import android.provider.OpenableColumns
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.*
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.core.content.FileProvider
import io.github.fermin214.moerise.data.*
import io.github.fermin214.moerise.domain.Note
import java.io.ByteArrayOutputStream
import java.io.File
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*

fun androidAttachmentFiles(context: android.content.Context) =
    AttachmentFiles(File(context.applicationContext.noBackupFilesDir, "attachments").absolutePath)

private object OpenFileCache {
    val lock = Mutex()
    var epoch = 0L
    const val maximum = 256L * 1024 * 1024
    fun prune(parent: File, keep: File? = null) {
        val cutoff = System.currentTimeMillis() - 24 * 60 * 60 * 1000L
        val folders = parent.listFiles().orEmpty().filter { it != keep }.sortedBy { it.lastModified() }
        folders.filter { it.lastModified() < cutoff }.forEach { it.deleteRecursively() }
        var total = parent.walkTopDown().filter { it.isFile }.sumOf { it.length() }
        for (folder in folders.filter { it.exists() }) {
            if (total <= maximum) break
            val bytes = folder.walkTopDown().filter { it.isFile }.sumOf { it.length() }
            if (folder.deleteRecursively()) total -= bytes
        }
    }
}

suspend fun clearAttachmentCache(context: android.content.Context) = withContext(Dispatchers.IO) {
    OpenFileCache.lock.withLock {
        ++OpenFileCache.epoch
        File(context.applicationContext.cacheDir, "attachments").deleteRecursively()
        Unit
    }
}

@Composable
actual fun AttachmentPicker(enabled: Boolean, importScope: CoroutineScope, strings: Strings, onPicked: (List<StagedAttachment>) -> Boolean, onError: (String) -> Unit, onBusy: (Boolean) -> Unit) {
    val context = LocalContext.current.applicationContext
    var busy by remember { mutableStateOf(false) }
    val accept by rememberUpdatedState(onPicked)
    val fail by rememberUpdatedState(onError)
    val pending by rememberUpdatedState(onBusy)
    val launcher = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
        if (uris.isNotEmpty()) importScope.launch {
            busy = true
            pending(true)
            val files = androidAttachmentFiles(context)
            val staged = mutableListOf<StagedAttachment>()
            var accepted = false
            try {
                require(uris.size <= 10) { "ATTACHMENTS_TOO_LARGE" }
                withContext(Dispatchers.IO) {
                    var total = 0
                    for (uri in uris) {
                        val resolver = context.contentResolver
                        val reported = resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { cursor ->
                            if (cursor.moveToFirst()) cursor.getString(0) else null
                        }
                        val filename = reported?.takeIf(::validFilename) ?: "attachment"
                        val type = resolver.getType(uri)?.lowercase()?.takeIf {
                            Regex("^[a-z0-9!#&^_.+-]+/[a-z0-9!#&^_.+-]+$").matches(it)
                        } ?: "application/octet-stream"
                        val bytes = resolver.openInputStream(uri)?.use { input ->
                            val output = ByteArrayOutputStream()
                            val buffer = ByteArray(16 * 1024)
                            while (true) {
                                ensureActive()
                                val count = input.read(buffer)
                                if (count < 0) break
                                total += count
                                require(total <= 8 * 1024 * 1024) { "ATTACHMENTS_TOO_LARGE" }
                                output.write(buffer, 0, count)
                            }
                            output.toByteArray()
                        } ?: error("ATTACHMENT_READ_FAILED")
                        staged += files.stage(filename, type, bytes)
                    }
                }
                accepted = accept(staged)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                fail(if (error is IllegalArgumentException) error.message ?: "ATTACHMENT_READ_FAILED" else "ATTACHMENT_READ_FAILED")
            } finally {
                if (!accepted) withContext(NonCancellable) { staged.forEach { runCatching { files.remove(it) } } }
                busy = false
                pending(false)
            }
        }
    }
    TextButton(onClick = { launcher.launch(arrayOf("*/*")) }, enabled = enabled && !busy) {
        Text(if (busy) strings.text("读取附件…", "Reading files…") else strings.text("添加附件", "Add files"))
    }
}

@Composable
actual fun OpenAttachmentButton(note: Note, item: JsonObject, core: CoreClient, strings: Strings) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var busy by remember(note.name, note.sourceHash, item) { mutableStateOf(false) }
    var error by remember(note.name, note.sourceHash, item) { mutableStateOf<String?>(null) }
    val filename = item["filename"]?.jsonPrimitive?.content?.takeIf(::validFilename) ?: "attachment"
    TextButton(onClick = {
        if (!busy) scope.launch {
            busy = true
            error = null
            val cacheEpoch = OpenFileCache.epoch
            try {
                val file = withContext(Dispatchers.IO) {
                    OpenFileCache.lock.withLock {
                    if (cacheEpoch != OpenFileCache.epoch) throw CancellationException("Account changed")
                    val parent = File(context.cacheDir, "attachments").apply { mkdirs() }
                    OpenFileCache.prune(parent)
                    val identity = "${core.origin}\u0000${note.instanceId}\u0000${note.name}\u0000${note.sourceHash}\u0000${item["name"]}"
                    val folder = File(parent, sha256(identity.encodeToByteArray())).apply { mkdirs() }
                    val destination = File(folder, filename)
                    if (!destination.exists()) {
                        val temporary = File(folder, ".download")
                        try {
                            temporary.outputStream().use { output ->
                                core.downloadAttachment(note, item) { bytes, count -> output.write(bytes, 0, count) }
                            }
                            check(temporary.renameTo(destination))
                        } finally { temporary.delete() }
                    }
                    folder.setLastModified(System.currentTimeMillis())
                    OpenFileCache.prune(parent, folder)
                    destination
                    }
                }
                val uri = FileProvider.getUriForFile(context, "${context.packageName}.files", file)
                context.startActivity(Intent(Intent.ACTION_VIEW).apply {
                    setDataAndType(uri, item["type"]?.jsonPrimitive?.content ?: "application/octet-stream")
                    addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                    clipData = ClipData.newRawUri(filename, uri)
                })
            } catch (failure: Exception) {
                if (failure is CancellationException) throw failure
                error = strings.text("附件无法打开，请重试。", "Could not open the file. Try again.")
            } finally { busy = false }
        }
    }, enabled = !busy) { Text(if (busy) strings.text("下载中…", "Downloading…") else filename) }
    error?.let { Text(it) }
}

actual suspend fun decodeProtectedImage(bytes: ByteArray): ImageBitmap? = withContext(Dispatchers.IO) {
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
    if (bounds.outWidth <= 0 || bounds.outHeight <= 0 || bounds.outWidth > 65_535 || bounds.outHeight > 65_535) return@withContext null
    val options = BitmapFactory.Options().apply { inSampleSize = 1 }
    while (bounds.outWidth / options.inSampleSize > 1024 || bounds.outHeight / options.inSampleSize > 1024) options.inSampleSize *= 2
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, options)?.asImageBitmap()
}

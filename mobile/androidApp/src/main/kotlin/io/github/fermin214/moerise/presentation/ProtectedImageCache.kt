package io.github.fermin214.moerise.presentation

import android.content.Context
import android.util.LruCache
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asAndroidBitmap
import io.github.fermin214.moerise.data.CoreClient
import io.github.fermin214.moerise.data.sha256
import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.Note
import java.io.File
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.sync.withPermit
import kotlinx.serialization.json.*

internal sealed interface ProtectedImageResult {
    data class Loaded(val bitmap: ImageBitmap) : ProtectedImageResult
    data class Failed(val retryable: Boolean) : ProtectedImageResult
}

internal object ProtectedImageCache {
    private const val inlineLimit = 8 * 1024 * 1024
    private const val diskLimit = 128L * 1024 * 1024
    private val memory = object : LruCache<String, ImageBitmap>(32 * 1024 * 1024) {
        override fun sizeOf(key: String, value: ImageBitmap) = value.asAndroidBitmap().allocationByteCount
    }
    private val lock = Mutex()
    private val workers = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val decoders = Semaphore(2)
    private val loading = mutableMapOf<String, Deferred<ProtectedImageResult>>()
    private val mutableEpoch = MutableStateFlow(0L)
    val epoch = mutableEpoch.asStateFlow()

    private fun key(account: String, item: JsonObject) =
        "$account\u0000${item["name"]?.jsonPrimitive?.content}\u0000${item["content_url"]?.jsonPrimitive?.content}"

    fun peek(core: CoreClient, item: JsonObject, stamp: Long): ImageBitmap? =
        core.attachmentAccountKey?.let { memory.get(key(it, item)) }?.takeIf { stamp == epoch.value }

    suspend fun clear(context: Context) = withContext(Dispatchers.IO) {
        lock.withLock {
            mutableEpoch.value++
            loading.values.forEach { it.cancel() }
            loading.clear()
            memory.evictAll()
            check(File(context.cacheDir, "image_cache").deleteRecursively()) { "LOCAL_REMOVE_FAILED" }
        }
    }

    suspend fun load(context: Context, core: CoreClient, note: Note, item: JsonObject, stamp: Long): ProtectedImageResult {
        if ((item["size"]?.jsonPrimitive?.longOrNull ?: 0L) > inlineLimit) return ProtectedImageResult.Failed(false)
        val account = try { core.attachmentAccount() }
        catch (error: Exception) {
            if (error is CancellationException && (error !is TimeoutCancellationException || !currentCoroutineContext().isActive)) throw error
            return failure(error)
        }
        if (account.split('\u0000').getOrNull(1) != note.instanceId) return ProtectedImageResult.Failed(false)
        val id = key(account, item)
        val task = lock.withLock {
            checkEpoch(stamp)
            memory.get(id)?.let { return ProtectedImageResult.Loaded(it) }
            loading[id] ?: workers.async(start = CoroutineStart.LAZY) {
                val job = coroutineContext[Job]
                try {
                    fetch(context, core, note, item, account, id, stamp)
                } finally {
                    withContext(NonCancellable) {
                        lock.withLock { if (loading[id] === job) loading.remove(id) }
                    }
                }
            }.also { loading[id] = it; it.start() }
        }
        return task.await().also { checkEpoch(stamp, core, account) }
    }

    private suspend fun fetch(context: Context, core: CoreClient, note: Note, item: JsonObject, account: String,
        id: String, stamp: Long): ProtectedImageResult = decoders.withPermit {
        val root = File(context.cacheDir, "image_cache")
        val destination = File(File(root, sha256(account.encodeToByteArray())), sha256(id.encodeToByteArray()))
        try {
            val cached = lock.withLock {
                checkEpoch(stamp, core, account)
                runCatching { destination.takeIf { it.length() in 1..inlineLimit.toLong() }?.readBytes() }.getOrNull()
            }
            val decoded = cached?.let { decodeProtectedImage(it) }
            val bytes = if (decoded == null) core.attachmentBytes(note, item, inlineLimit) else cached
            val bitmap = decoded ?: decodeProtectedImage(requireNotNull(bytes)) ?: return@withPermit ProtectedImageResult.Failed(false)
            lock.withLock {
                checkEpoch(stamp, core, account)
                if (decoded == null) runCatching {
                    destination.parentFile?.mkdirs()
                    val temporary = File(destination.parentFile, "${destination.name}.part")
                    try {
                        val image = requireNotNull(bytes)
                        prune(root, destination, image.size)
                        temporary.writeBytes(image)
                        check(temporary.renameTo(destination))
                    } finally { temporary.delete() }
                }
                destination.setLastModified(System.currentTimeMillis())
                memory.put(id, bitmap)
            }
            ProtectedImageResult.Loaded(bitmap)
        } catch (error: Exception) {
            if (error is CancellationException && (error !is TimeoutCancellationException || !currentCoroutineContext().isActive)) throw error
            failure(error)
        }
    }

    private fun checkEpoch(stamp: Long) {
        if (stamp != epoch.value) throw CancellationException("Image cache cleared")
    }

    private fun checkEpoch(stamp: Long, core: CoreClient, account: String) {
        checkEpoch(stamp)
        if (core.attachmentAccountKey != account) throw CancellationException("Image account changed")
    }

    private fun failure(error: Exception) = ProtectedImageResult.Failed(when (error) {
        is CoreException -> error.status == 408 || error.status == 429 || error.status >= 500
        is IllegalArgumentException -> error.message == "ATTACHMENT_DOWNLOAD_INCOMPLETE"
        else -> true
    })

    private fun prune(root: File, keep: File, incoming: Int) {
        val files = root.walkTopDown().filter { it.isFile }.toList()
        var total = files.sumOf { it.length() }
        for (file in files.filter { it != keep }.sortedBy { it.lastModified() }) {
            if (total + incoming <= diskLimit) break
            val length = file.length()
            if (file.delete()) total -= length
        }
        check(total + incoming <= diskLimit)
    }
}

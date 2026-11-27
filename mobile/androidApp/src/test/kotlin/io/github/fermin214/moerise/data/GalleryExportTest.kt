package io.github.fermin214.moerise.data

import android.content.ContentProvider
import android.content.ContentUris
import android.content.ContentValues
import android.content.Context
import android.content.pm.ProviderInfo
import android.database.MatrixCursor
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.provider.MediaStore.Images.Media
import androidx.test.core.app.ApplicationProvider
import io.github.fermin214.moerise.domain.CoreException
import io.github.fermin214.moerise.domain.Note
import io.github.fermin214.moerise.presentation.clearAttachmentCache
import io.github.fermin214.moerise.presentation.recoverGalleryExports
import io.github.fermin214.moerise.presentation.saveImageToGallery
import io.ktor.client.engine.mock.MockEngine
import io.ktor.client.engine.mock.respond
import io.ktor.http.HttpStatusCode
import io.ktor.utils.io.ByteChannel
import io.ktor.utils.io.writeFully
import java.io.File
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.test.*
import kotlinx.coroutines.*
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowContentResolver

@RunWith(RobolectricTestRunner::class)
@Config(application = android.app.Application::class, sdk = [29])
class GalleryExportTest {
    private val origin = "https://gallery.example"
    private val owner = "$origin\u0000instance\u0000users/owner"
    private val item = buildJsonObject {
        put("name", "attachments/photo"); put("filename", "photo.png"); put("type", "image/png")
    }
    private val note = Note("instance", "memos/note", "", "NORMAL", "hash", attachments = listOf(item))
    private val bytes = byteArrayOf(6, 7, 8)

    @Test
    fun pendingRowsRecoverAcrossTheInsertAndPublishBoundaries() = runBlocking {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val photos = galleryProvider(context)
        val preferences = context.getSharedPreferences("pending-gallery-exports", Context.MODE_PRIVATE)
        val orphan = "moerise-pending-orphan.png"
        photos.insert(Media.EXTERNAL_CONTENT_URI, photo(orphan, 1))
        val pending = photos.insert(Media.EXTERNAL_CONTENT_URI, photo("pending.png", 1))
        val published = photos.insert(Media.EXTERNAL_CONTENT_URI, photo("published.png", 0))
        assertTrue(preferences.edit().putString(orphan, "").putString("pending", pending.toString())
            .putString("published", published.toString()).commit())
        try {
            recoverGalleryExports(context)
            assertEquals(listOf("published.png"), photos.names())
            assertTrue(preferences.all.isEmpty())
        } finally { photos.closeFiles() }
    }

    @Test
    fun publicationSuspendsLogoutWithoutBlockingTheMainLooper() = runBlocking<Unit> {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val photos = galleryProvider(context)
        val updating = CountDownLatch(1)
        val release = CountDownLatch(1)
        photos.beforePublish = { updating.countDown(); check(release.await(10, TimeUnit.SECONDS)) }
        val core = client()
        try {
            assertFailsWith<IllegalArgumentException> { saveImageToGallery(context, note.copy(instanceId = "other"), item, core) }
            assertFailsWith<IllegalArgumentException> { saveImageToGallery(context, note.copy(attachments = emptyList()), item, core) }
            assertTrue(photos.names().isEmpty())
            val saving = async { saveImageToGallery(context, note, item, core) }
            val main = Looper.getMainLooper()
            supervisorScope {
                assertTrue(withContext(Dispatchers.IO) { updating.await(5, TimeUnit.SECONDS) })
                val logout = async(Dispatchers.Main) { core.clearLocalSession() }
                var mainResponsive = false
                Handler(main).post { mainResponsive = true }
                try {
                    shadowOf(main).idle()
                    assertTrue(mainResponsive)
                    assertFalse(logout.isCompleted)
                } finally { release.countDown() }
                saving.await()
                withTimeout(5_000) {
                    while (!logout.isCompleted) { shadowOf(main).idle(); delay(10) }
                    logout.await()
                }
            }
            assertEquals(listOf("photo.png"), photos.names())
            assertContentEquals(bytes, photos.bytes("photo.png"))
            clearAttachmentCache(context)
            assertEquals(listOf("photo.png"), photos.names())
            assertEquals(1, photos.publications)
        } finally { release.countDown(); core.close(); photos.closeFiles() }
    }

    @Test fun canceledExportImmediatelyDiscardsThePendingRow() = checkInterruptedExport(Interruption.Cancel)
    @Test fun unauthorizedSessionImmediatelyDiscardsThePendingRow() = checkInterruptedExport(Interruption.Unauthorized)
    @Test fun changingAccountDiscardsThePendingRow() = checkInterruptedExport(Interruption.AccountChange)
    @Test fun cacheClearDiscardsThePendingRow() = checkInterruptedExport(Interruption.CacheClear)

    private enum class Interruption { Cancel, Unauthorized, AccountChange, CacheClear }

    private fun checkInterruptedExport(interruption: Interruption) = runBlocking<Unit> {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val photos = galleryProvider(context)
        val ledger = context.getSharedPreferences("pending-gallery-exports", Context.MODE_PRIVATE)
        val stream = ByteChannel(autoFlush = true)
        val core = client(stream)
        try {
            supervisorScope {
                val saving = async { saveImageToGallery(context, note, item, core) }
                try {
                    stream.writeFully(bytes)
                    withTimeout(5_000) {
                        while (!photos.hasWritten(bytes.size)) delay(10)
                    }
                    assertEquals(1, photos.names().size)
                    assertEquals(1, ledger.all.size)
                    when (interruption) {
                        Interruption.Cancel -> saving.cancel()
                        Interruption.Unauthorized -> assertEquals(401, assertFailsWith<CoreException> { core.capabilities() }.status)
                        Interruption.AccountChange -> core.bindAccount("$origin\u0000instance\u0000users/other")
                        Interruption.CacheClear -> clearAttachmentCache(context)
                    }
                    stream.flushAndClose()
                    assertFailsWith<CancellationException> { withTimeout(5_000) { saving.await() } }
                    assertTrue(saving.isCompleted)
                    assertTrue(photos.names().isEmpty())
                    assertTrue(ledger.all.isEmpty())
                    assertEquals(0, photos.publications)
                } finally {
                    stream.cancel(null)
                    saving.cancelAndJoin()
                }
            }
        } finally { core.close(); photos.closeFiles() }
    }

    private suspend fun client(stream: ByteChannel? = null) =
        CoreClient(origin, SessionCookies(origin, MemorySecrets()), MockEngine { request ->
            when {
                request.url.encodedPath.endsWith("/capabilities") -> respond("{}", HttpStatusCode.Unauthorized)
                request.url.encodedPath.endsWith("/session") ->
                    respond("""{"instance_id":"instance","user":{"name":"users/owner"},"timezone":"UTC","expires_at":"2099-01-01T00:00:00Z","csrf_token":"token"}""")
                stream != null -> respond(stream)
                else -> respond(bytes)
            }
        }).also { it.bindAccount(owner) }

    private fun photo(name: String, pending: Int) = ContentValues().apply {
        put(Media.RELATIVE_PATH, "Pictures/Moerise/"); put(Media.DISPLAY_NAME, name); put(Media.IS_PENDING, pending)
    }

    private fun galleryProvider(context: Context) = GalleryProvider(context.cacheDir).also { provider ->
        provider.attachInfo(context, ProviderInfo().apply { authority = "media"; exported = true })
        ShadowContentResolver.registerProviderInternal("media", provider)
    }
}

private class GalleryProvider(private val directory: File) : ContentProvider() {
    private val rows = ConcurrentHashMap<Long, ContentValues>()
    private val files = ConcurrentHashMap<Long, File>()
    private var nextId = 0L
    var beforePublish: () -> Unit = {}
    @Volatile var publications = 0
        private set

    fun names() = rows.values.map { it.getAsString(Media.DISPLAY_NAME) }
    fun bytes(name: String) = files.getValue(rows.entries.single { it.value.getAsString(Media.DISPLAY_NAME) == name }.key).readBytes()
    fun hasWritten(size: Int) = files.values.any { it.length() == size.toLong() }
    fun closeFiles() { files.values.forEach { it.delete() } }
    override fun onCreate() = true
    override fun getType(uri: Uri) = "image/png"

    override fun insert(uri: Uri, values: ContentValues?): Uri {
        val id = ++nextId
        rows[id] = ContentValues(requireNotNull(values))
        return ContentUris.withAppendedId(uri, id)
    }

    override fun openFile(uri: Uri, mode: String): ParcelFileDescriptor {
        val file = files.getOrPut(ContentUris.parseId(uri)) { File.createTempFile("gallery-test", ".png", directory) }
        return ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_WRITE_ONLY or ParcelFileDescriptor.MODE_TRUNCATE)
    }

    override fun query(uri: Uri, projection: Array<out String>?, selection: String?, selectionArgs: Array<out String>?, sortOrder: String?): MatrixCursor {
        val id = uri.lastPathSegment?.toLongOrNull()
        return MatrixCursor(arrayOf(Media._ID)).apply {
            for ((key, values) in rows) {
                if (id != null && key != id || values.getAsInteger(Media.IS_PENDING) != 1) continue
                if (selectionArgs != null && (values.getAsString(Media.RELATIVE_PATH) != selectionArgs[0] ||
                        values.getAsString(Media.DISPLAY_NAME) != selectionArgs[1])) continue
                addRow(arrayOf(key))
            }
        }
    }

    override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?): Int {
        val id = ContentUris.parseId(uri)
        check(rows[id]?.getAsInteger(Media.IS_PENDING) == 1)
        files.remove(id)?.delete()
        rows.remove(id)
        return 1
    }

    override fun update(uri: Uri, values: ContentValues?, selection: String?, selectionArgs: Array<out String>?): Int {
        val row = rows[ContentUris.parseId(uri)] ?: return 0
        assertEquals(1, row.getAsInteger(Media.IS_PENDING))
        assertEquals(0, requireNotNull(values).getAsInteger(Media.IS_PENDING))
        beforePublish()
        row.putAll(values)
        publications++
        return 1
    }
}

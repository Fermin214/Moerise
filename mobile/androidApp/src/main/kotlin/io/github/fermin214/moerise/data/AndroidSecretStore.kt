package io.github.fermin214.moerise.data

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.AtomicFile
import java.io.File
import java.security.KeyStore
import java.security.MessageDigest
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

class AndroidSecretStore(context: Context) : SecretStore {
    private val directory = File(context.applicationContext.noBackupFilesDir, "credentials")
    private val lock = Mutex()
    private val alias = "moerise-session-v1"
    @Synchronized private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(alias, null) as? SecretKey)?.let { return it }
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").apply {
            init(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build())
        }.generateKey()
    }
    private fun file(name: String): AtomicFile {
        val digest = MessageDigest.getInstance("SHA-256").digest(name.toByteArray()).joinToString("") { "%02x".format(it) }
        return AtomicFile(File(directory, digest))
    }

    override suspend fun read(key: String): String? = lock.withLock { withContext(Dispatchers.IO) {
        val file = file(key)
        if (!file.baseFile.exists()) return@withContext null
        try {
            val bytes = file.openRead().use { it.readBytes() }
            require(bytes.size >= 29 && bytes[0] == 1.toByte())
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, bytes.copyOfRange(1, 13)))
            cipher.updateAAD(key.toByteArray())
            cipher.doFinal(bytes.copyOfRange(13, bytes.size)).decodeToString()
        } catch (_: java.security.GeneralSecurityException) {
            file.delete()
            null
        } catch (_: IllegalArgumentException) {
            file.delete()
            null
        }
    } }

    override suspend fun write(key: String, value: String?) = lock.withLock { withContext(Dispatchers.IO) {
        check(directory.isDirectory || directory.mkdirs())
        val file = file(key)
        if (value == null) { file.delete(); return@withContext }
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key())
        cipher.updateAAD(key.toByteArray())
        val payload = byteArrayOf(1) + cipher.iv + cipher.doFinal(value.encodeToByteArray())
        val output = file.startWrite()
        try { output.write(payload); file.finishWrite(output) }
        catch (error: Exception) { file.failWrite(output); throw error }
    } }
}

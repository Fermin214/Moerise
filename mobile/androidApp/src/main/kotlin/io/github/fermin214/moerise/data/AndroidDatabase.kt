package io.github.fermin214.moerise.data

import android.content.Context
import androidx.room.Room
import java.io.File

fun androidDatabase(context: Context): LocalDatabase {
    val app = context.applicationContext
    val folder = File(app.noBackupFilesDir, "data").apply { mkdirs() }
    return Room.databaseBuilder<LocalDatabase>(app, File(folder, "moerise.sqlite3").absolutePath)
        .configureDatabase()
}

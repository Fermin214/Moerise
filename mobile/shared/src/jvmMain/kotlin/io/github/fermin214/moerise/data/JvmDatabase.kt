package io.github.fermin214.moerise.data

import androidx.room.Room

fun jvmDatabase(path: String): LocalDatabase =
    Room.databaseBuilder<LocalDatabase>(name = path).configureDatabase()

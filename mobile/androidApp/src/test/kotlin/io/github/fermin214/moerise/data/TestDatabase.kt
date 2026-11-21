package io.github.fermin214.moerise.data

import androidx.room.Room
import androidx.test.core.app.ApplicationProvider

fun testDatabase(path: String): LocalDatabase =
    Room.databaseBuilder<LocalDatabase>(ApplicationProvider.getApplicationContext(), path).configureDatabase()

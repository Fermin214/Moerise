package io.github.fermin214.moerise.data

import androidx.room.ConstructedBy
import androidx.room.Dao
import androidx.room.Database
import androidx.room.Entity
import androidx.room.PrimaryKey
import androidx.room.Query
import androidx.room.RoomDatabase
import androidx.room.RoomDatabaseConstructor
import androidx.room.Upsert
import androidx.sqlite.driver.bundled.BundledSQLiteDriver
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.IO

@Entity(tableName = "profiles")
data class ProfileEntity(
    @PrimaryKey val accountKey: String,
    val origin: String,
    val instanceId: String,
    val userName: String,
    val displayName: String,
    val timezone: String,
)

@Entity(tableName = "drafts")
data class DraftEntity(
    @PrimaryKey val id: String,
    val accountKey: String,
    val content: String,
    val revision: Long,
    val targetName: String? = null,
    val sourceHash: String? = null,
    val attachmentsJson: String = "[]",
    val conflictJson: String? = null,
)

@Entity(tableName = "pending_writes")
data class PendingWriteEntity(
    @PrimaryKey val id: String,
    val draftId: String,
    val accountKey: String,
    val idempotencyKey: String,
    val method: String,
    val path: String,
    val body: String,
    val draftRevision: Long,
    val status: String = "prepared",
)

@Entity(tableName = "snapshots", primaryKeys = ["accountKey", "filterKey"])
data class SnapshotEntity(val accountKey: String, val filterKey: String, val body: String, val fetchedAt: Long)

@Dao
interface LocalDao {
    @Upsert suspend fun putProfile(profile: ProfileEntity)
    @Query("SELECT * FROM profiles WHERE accountKey = :key") suspend fun profile(key: String): ProfileEntity?
    @Upsert suspend fun putDraft(draft: DraftEntity)
    @Query("SELECT * FROM drafts WHERE accountKey = :key ORDER BY revision DESC LIMIT 1")
    suspend fun homeDraft(key: String): DraftEntity?
    @Query("SELECT * FROM drafts WHERE id = :id") suspend fun draft(id: String): DraftEntity?
    @Query("DELETE FROM drafts WHERE id = :id") suspend fun deleteDraft(id: String)
    @Upsert suspend fun putWrite(write: PendingWriteEntity)
    @Query("SELECT * FROM pending_writes WHERE draftId = :draftId AND status != 'conflict' LIMIT 1")
    suspend fun pending(draftId: String): PendingWriteEntity?
    @Query("DELETE FROM pending_writes WHERE id = :id") suspend fun deleteWrite(id: String)
    @Query("UPDATE pending_writes SET status = 'unknown' WHERE accountKey = :key AND status = 'sending'")
    suspend fun recoverSending(key: String)
    @Upsert suspend fun putSnapshot(snapshot: SnapshotEntity)
    @Query("SELECT * FROM snapshots WHERE accountKey = :key AND filterKey = :filter")
    suspend fun snapshot(key: String, filter: String): SnapshotEntity?
    @Query("DELETE FROM snapshots WHERE accountKey = :key") suspend fun deleteSnapshots(key: String)
}

@Database(entities = [ProfileEntity::class, DraftEntity::class, PendingWriteEntity::class, SnapshotEntity::class], version = 1)
@ConstructedBy(LocalDatabaseConstructor::class)
abstract class LocalDatabase : RoomDatabase() {
    abstract fun dao(): LocalDao
}

@Suppress("NO_ACTUAL_FOR_EXPECT")
expect object LocalDatabaseConstructor : RoomDatabaseConstructor<LocalDatabase> {
    override fun initialize(): LocalDatabase
}

fun RoomDatabase.Builder<LocalDatabase>.configureDatabase(): LocalDatabase =
    setDriver(BundledSQLiteDriver()).setQueryCoroutineContext(Dispatchers.IO).build()

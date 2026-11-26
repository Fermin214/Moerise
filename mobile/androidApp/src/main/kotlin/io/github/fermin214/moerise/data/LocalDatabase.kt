package io.github.fermin214.moerise.data

import androidx.room.ColumnInfo
import androidx.room.Dao
import androidx.room.Database
import androidx.room.Entity
import androidx.room.Index
import androidx.room.PrimaryKey
import androidx.room.Query
import androidx.room.RoomDatabase
import androidx.room.Upsert
import androidx.room.migration.Migration
import androidx.sqlite.SQLiteConnection
import androidx.sqlite.driver.AndroidSQLiteDriver
import androidx.sqlite.execSQL
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

@kotlinx.serialization.Serializable
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
    @ColumnInfo(defaultValue = "NULL") val baselineJson: String? = null,
    @ColumnInfo(defaultValue = "'home'") val slot: String = "home",
    @ColumnInfo(defaultValue = "0") val createdAt: Long = 0,
    val queuedRevisionJson: String? = null,
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
    @ColumnInfo(defaultValue = "NULL") val submittedDraftJson: String? = null,
)

@Entity(tableName = "snapshots", primaryKeys = ["accountKey", "filterKey"])
data class SnapshotEntity(val accountKey: String, val filterKey: String, val body: String, val fetchedAt: Long)

@Entity(tableName = "feature_states", primaryKeys = ["accountKey", "feature"])
data class FeatureStateEntity(val accountKey: String, val feature: String, val body: String)

@Entity(
    tableName = "notes",
    primaryKeys = ["accountKey", "name"],
    indices = [
        Index(value = ["accountKey", "state", "createTime"], name = "idx_notes_state_time"),
        Index(value = ["accountKey", "createTime"], name = "idx_notes_time"),
    ],
)
data class NoteEntity(
    val accountKey: String,
    val name: String,
    val instanceId: String,
    val content: String,
    val state: String,
    val sourceHash: String,
    val createTime: String,
    val updateTime: String,
    val noteJson: String,
)

@Entity(
    tableName = "note_tags",
    primaryKeys = ["accountKey", "name", "tag"],
    indices = [Index(value = ["accountKey", "tag"], name = "idx_note_tags_account_tag")],
)
data class NoteTagEntity(val accountKey: String, val name: String, val tag: String)

@Entity(tableName = "sync_state")
data class SyncStateEntity(
    @PrimaryKey val accountKey: String,
    val cursor: String,
    val syncGeneration: String,
    val initialized: Boolean,
    val updatedAt: Long,
)

@Entity(tableName = "cached_snapshots", primaryKeys = ["accountKey", "kind"])
data class CachedSnapshotEntity(val accountKey: String, val kind: String, val body: String, val fetchedAt: Long)

data class LocalTagRow(val tag: String, val count: Int)

@Dao
interface LocalDao {
    @Upsert suspend fun putProfile(profile: ProfileEntity)
    @Query("SELECT * FROM profiles WHERE accountKey = :key") suspend fun profile(key: String): ProfileEntity?
    @Upsert suspend fun putDraft(draft: DraftEntity)
    @Query("SELECT * FROM drafts WHERE accountKey = :key AND slot = 'home' ORDER BY revision DESC LIMIT 1")
    suspend fun homeDraft(key: String): DraftEntity?
    @Query("SELECT * FROM drafts WHERE accountKey = :key AND slot = :slot ORDER BY revision DESC LIMIT 1")
    suspend fun draftForSlot(key: String, slot: String): DraftEntity?
    @Query("SELECT * FROM drafts WHERE accountKey = :key ORDER BY revision DESC")
    suspend fun drafts(key: String): List<DraftEntity>
    @Query("SELECT draftId FROM pending_writes WHERE accountKey = :key AND status != 'conflict'")
    suspend fun pendingDraftIds(key: String): List<String>
    @Query("SELECT EXISTS(SELECT 1 FROM pending_writes WHERE accountKey = :key AND method = 'POST' AND path = '/notes' AND status != 'conflict')")
    suspend fun hasPendingCreation(key: String): Boolean
    @Query("SELECT pending_writes.* FROM pending_writes INNER JOIN drafts ON drafts.id = pending_writes.draftId WHERE pending_writes.accountKey = :key AND pending_writes.status IN ('prepared', 'unknown') ORDER BY pending_writes.rowid")
    suspend fun queuedWrites(key: String): List<PendingWriteEntity>
    @Query("SELECT * FROM drafts WHERE id = :id") suspend fun draft(id: String): DraftEntity?
    @Query("DELETE FROM drafts WHERE id = :id") suspend fun deleteDraft(id: String)
    @Query("SELECT attachmentsJson FROM drafts") suspend fun attachmentReferences(): List<String>
    @Query("SELECT queuedRevisionJson FROM drafts WHERE queuedRevisionJson IS NOT NULL")
    suspend fun queuedRevisions(): List<String>
    @Query("SELECT submittedDraftJson FROM pending_writes WHERE submittedDraftJson IS NOT NULL")
    suspend fun submittedRevisions(): List<String>
    @Upsert suspend fun putWrite(write: PendingWriteEntity)
    @Query("UPDATE pending_writes SET status = :status WHERE id = :id AND accountKey = :key AND status != 'conflict'")
    suspend fun writeStatus(id: String, key: String, status: String)
    @Query("SELECT * FROM pending_writes WHERE draftId = :draftId AND status != 'conflict' LIMIT 1")
    suspend fun pending(draftId: String): PendingWriteEntity?
    @Query("DELETE FROM pending_writes WHERE id = :id") suspend fun deleteWrite(id: String)
    @Query("UPDATE pending_writes SET status = 'unknown' WHERE accountKey = :key AND status = 'sending'")
    suspend fun recoverSending(key: String)
    @Upsert suspend fun putSnapshot(snapshot: SnapshotEntity)
    @Query("SELECT * FROM snapshots WHERE accountKey = :key AND filterKey = :filter")
    suspend fun snapshot(key: String, filter: String): SnapshotEntity?
    @Query("DELETE FROM snapshots WHERE accountKey = :key") suspend fun deleteSnapshots(key: String)
    @Query("DELETE FROM snapshots WHERE accountKey = :key AND (filterKey GLOB '*:tags:*' OR filterKey GLOB '*:calendar:*')")
    suspend fun deleteFilterSnapshots(key: String)
    @Upsert suspend fun putFeature(state: FeatureStateEntity)
    @Query("SELECT body FROM feature_states WHERE accountKey = :key AND feature = :feature")
    suspend fun feature(key: String, feature: String): String?
    @Query("SELECT accountKey FROM feature_states WHERE feature = :feature")
    suspend fun featureAccounts(feature: String): List<String>
    @Query("DELETE FROM feature_states WHERE accountKey = :key AND feature = :feature")
    suspend fun deleteFeature(key: String, feature: String)
    @Query("DELETE FROM drafts WHERE accountKey = :key") suspend fun deleteDrafts(key: String)
    @Query("DELETE FROM pending_writes WHERE accountKey = :key") suspend fun deleteWrites(key: String)
    @Query("DELETE FROM feature_states WHERE accountKey = :key") suspend fun deleteFeatures(key: String)
    @Query("DELETE FROM profiles WHERE accountKey = :key") suspend fun deleteProfile(key: String)
    @Upsert suspend fun putNotes(notes: List<NoteEntity>)
    @Upsert suspend fun putNote(note: NoteEntity)
    @Query("DELETE FROM notes WHERE accountKey = :key AND name IN (:names)") suspend fun deleteNotesByName(key: String, names: List<String>)
    @Query("DELETE FROM notes WHERE accountKey = :key") suspend fun deleteAllNotes(key: String)
    @Upsert suspend fun putNoteTags(tags: List<NoteTagEntity>)
    @Query("DELETE FROM note_tags WHERE accountKey = :key AND name IN (:names)") suspend fun deleteNoteTagsByName(key: String, names: List<String>)
    @Query("DELETE FROM note_tags WHERE accountKey = :key") suspend fun deleteAllNoteTags(key: String)
    @Query("SELECT * FROM notes WHERE accountKey = :key AND name = :name LIMIT 1") suspend fun note(key: String, name: String): NoteEntity?
    @Query("SELECT * FROM notes WHERE accountKey = :key AND state = :state ORDER BY createTime DESC, name DESC LIMIT :limit OFFSET :offset")
    suspend fun notesPage(key: String, state: String, limit: Int, offset: Int): List<NoteEntity>
    @Query("SELECT * FROM notes WHERE accountKey = :key AND state = 'NORMAL' AND name NOT IN (:visited) ORDER BY RANDOM() LIMIT 1")
    suspend fun walkNote(key: String, visited: List<String>): NoteEntity?
    @Query("SELECT COUNT(*) FROM notes WHERE accountKey = :key AND state = :state") suspend fun countNotes(key: String, state: String): Int
    @Query("SELECT * FROM notes WHERE accountKey = :key AND (:state = 'ALL' OR state = :state) ORDER BY createTime DESC, name DESC")
    suspend fun notesByState(key: String, state: String): List<NoteEntity>
    @Query("SELECT createTime FROM notes WHERE accountKey = :key AND state = :state AND createTime >= :start AND createTime < :end")
    suspend fun noteTimes(key: String, state: String, start: String, end: String): List<String>
    @Query("SELECT createTime FROM notes WHERE accountKey = :key AND createTime >= :start AND createTime < :end")
    suspend fun noteTimes(key: String, start: String, end: String): List<String>
    @Query("""
        SELECT createTime FROM notes
        WHERE accountKey = :key AND (:state = 'ALL' OR state = :state) AND substr(createTime, 21, 1) = '.'
          AND CAST(ltrim(substr(createTime, 1, 20), '0') AS INTEGER) >= :start
          AND CAST(ltrim(substr(createTime, 1, 20), '0') AS INTEGER) < :end
    """)
    suspend fun historicNoteTimes(key: String, state: String, start: Long, end: Long): List<String>
    @Query("SELECT * FROM pending_writes WHERE accountKey = :key")
    suspend fun accountWrites(key: String): List<PendingWriteEntity>
    @Query("SELECT note_tags.tag AS tag, COUNT(*) AS count FROM note_tags INNER JOIN notes ON notes.accountKey = note_tags.accountKey AND notes.name = note_tags.name WHERE notes.accountKey = :key AND (:state = 'ALL' OR notes.state = :state) GROUP BY note_tags.tag ORDER BY count DESC, note_tags.tag ASC")
    suspend fun tagCounts(key: String, state: String): List<LocalTagRow>
    @Query("SELECT * FROM sync_state WHERE accountKey = :key") suspend fun syncState(key: String): SyncStateEntity?
    @Upsert suspend fun putSyncState(state: SyncStateEntity)
    @Query("DELETE FROM sync_state WHERE accountKey = :key") suspend fun deleteSyncState(key: String)
    @Upsert suspend fun putCachedSnapshot(snapshot: CachedSnapshotEntity)
    @Query("SELECT * FROM cached_snapshots WHERE accountKey = :key AND kind = :kind")
    suspend fun cachedSnapshot(key: String, kind: String): CachedSnapshotEntity?
    @Query("DELETE FROM cached_snapshots WHERE accountKey = :key") suspend fun deleteCachedSnapshots(key: String)
}

@Database(
    entities = [
        ProfileEntity::class, DraftEntity::class, PendingWriteEntity::class,
        SnapshotEntity::class, FeatureStateEntity::class, NoteEntity::class,
        NoteTagEntity::class, SyncStateEntity::class, CachedSnapshotEntity::class,
    ],
    version = 2,
)
abstract class LocalDatabase : RoomDatabase() {
    abstract fun dao(): LocalDao
}


val LOCAL_DB_MIGRATION_1_2 = object : Migration(1, 2) {
    override fun migrate(connection: SQLiteConnection) {
        connection.execSQL("CREATE TABLE IF NOT EXISTS `notes` (`accountKey` TEXT NOT NULL, `name` TEXT NOT NULL, `instanceId` TEXT NOT NULL, `content` TEXT NOT NULL, `state` TEXT NOT NULL, `sourceHash` TEXT NOT NULL, `createTime` TEXT NOT NULL, `updateTime` TEXT NOT NULL, `noteJson` TEXT NOT NULL, PRIMARY KEY(`accountKey`, `name`))")
        connection.execSQL("CREATE INDEX IF NOT EXISTS `idx_notes_state_time` ON `notes` (`accountKey`, `state`, `createTime`)")
        connection.execSQL("CREATE INDEX IF NOT EXISTS `idx_notes_time` ON `notes` (`accountKey`, `createTime`)")
        connection.execSQL("CREATE TABLE IF NOT EXISTS `note_tags` (`accountKey` TEXT NOT NULL, `name` TEXT NOT NULL, `tag` TEXT NOT NULL, PRIMARY KEY(`accountKey`, `name`, `tag`))")
        connection.execSQL("CREATE INDEX IF NOT EXISTS `idx_note_tags_account_tag` ON `note_tags` (`accountKey`, `tag`)")
        connection.execSQL("CREATE TABLE IF NOT EXISTS `sync_state` (`accountKey` TEXT NOT NULL, `cursor` TEXT NOT NULL, `syncGeneration` TEXT NOT NULL, `initialized` INTEGER NOT NULL, `updatedAt` INTEGER NOT NULL, PRIMARY KEY(`accountKey`))")
        connection.execSQL("CREATE TABLE IF NOT EXISTS `cached_snapshots` (`accountKey` TEXT NOT NULL, `kind` TEXT NOT NULL, `body` TEXT NOT NULL, `fetchedAt` INTEGER NOT NULL, PRIMARY KEY(`accountKey`, `kind`))")
    }
}

fun RoomDatabase.Builder<LocalDatabase>.configureDatabase(): LocalDatabase =
    setDriver(AndroidSQLiteDriver())
        .addMigrations(LOCAL_DB_MIGRATION_1_2)
        .setQueryCoroutineContext(Dispatchers.IO).build()

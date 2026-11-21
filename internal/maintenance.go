package moerise

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

func AcquireWriter(directory string) (*flock.Flock, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(directory, ".writer.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		lock.Close()
		return nil, errors.New("Another Moerise writer is using this data directory")
	}
	return lock, nil
}

type Manifest struct {
	Format      int    `json:"format"`
	AppVersion  string `json:"app_version"`
	CreatedAt   string `json:"created_at"`
	InstanceID  string `json:"instance_id"`
	User        string `json:"user"`
	StateSHA256 string `json:"state_sha256"`
	Projection  string `json:"projection"`
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func checkDatabase(path string) error {
	db, err := openDB(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return errors.New("backup database failed the integrity check")
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("backup database contains invalid references")
	}
	return rows.Err()
}

func verifySnapshot(settings Settings, snapshot string) error {
	if err := checkDatabase(snapshot); err != nil {
		return err
	}
	db, err := openDB(snapshot, true)
	if err != nil {
		return err
	}
	defer db.Close()
	var address, user string
	if err := db.QueryRow("SELECT base_url, user_name FROM bindings WHERE instance_id = ?", settings.Memos.InstanceID).Scan(&address, &user); err != nil {
		return errors.New("backup has no matching Memos binding")
	}
	if address != settings.Memos.BaseURL || user != settings.Memos.User {
		return errors.New("backup source URL or account differs from the configured binding")
	}
	applied, err := appliedMigrations(db)
	if err != nil {
		return err
	}
	for version, checksum := range applied {
		body, err := migrationSQL("state", version)
		if err != nil || hashBytes(body) != checksum {
			return errors.New("backup schema is incompatible with this Moerise version")
		}
	}
	return nil
}

func Backup(settings Settings, output string) (Manifest, error) {
	var manifest Manifest
	source := filepath.Join(settings.StateDir, "state.sqlite3")
	if _, err := os.Stat(source); err != nil {
		return manifest, errors.New("no state database exists")
	}
	destination, err := filepath.Abs(output)
	if err != nil {
		return manifest, err
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return manifest, errors.New("backup destination already exists or cannot be accessed")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return manifest, err
	}
	temp, err := os.MkdirTemp(filepath.Dir(destination), filepath.Base(destination)+".partial-")
	if err != nil {
		return manifest, err
	}
	defer os.RemoveAll(temp)
	snapshot := filepath.Join(temp, "state.sqlite3")
	db, err := openDB(source, false)
	if err != nil {
		return manifest, err
	}
	_, vacuumErr := db.Exec("VACUUM INTO ?", snapshot)
	closeErr := db.Close()
	if err := errors.Join(vacuumErr, closeErr); err != nil {
		return manifest, err
	}
	if err := os.Chmod(snapshot, 0600); err != nil {
		return manifest, err
	}
	if err := verifySnapshot(settings, snapshot); err != nil {
		return manifest, err
	}
	checksum, err := fileHash(snapshot)
	if err != nil {
		return manifest, err
	}
	manifest = Manifest{1, Version, nowText(), settings.Memos.InstanceID, settings.Memos.User, checksum, "rebuild-from-memos"}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	if err := os.WriteFile(filepath.Join(temp, "manifest.json"), append(data, '\n'), 0600); err != nil {
		return manifest, err
	}
	return manifest, os.Rename(temp, destination)
}

func removeDatabase(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

const (
	restoreCoreResetSQL = `UPDATE sync_state SET watermark = NULL, last_full = NULL, last_incremental = NULL,
			source_ok = 0, index_generation = NULL;
		DELETE FROM jobs WHERE kind = 'sync';
		DELETE FROM client_sessions;`
	restoreBotAndDeliveryResetSQL = `DELETE FROM bot_cursors WHERE kind='qq';
		INSERT OR REPLACE INTO bot_restore SELECT instance_id,CAST(round(unixepoch('subsec')*1000) AS INTEGER) FROM bindings;
		UPDATE bot_inbox SET phase='restored',body=NULL,error_code='STATE_RESTORED'
			WHERE phase IN ('pending','reply','sending');
		WITH interrupted AS (
			SELECT p.root_id, MIN(p.position) AS first FROM review_delivery_parts p
			JOIN notifications n ON n.id=p.notification_id
			WHERE n.status IN ('pending','running','unknown','failed') GROUP BY p.root_id
		)
		UPDATE notifications SET status='skipped', last_error='PART_INTERRUPTED'
			WHERE status='pending' AND id IN (
				SELECT p.notification_id FROM review_delivery_parts p JOIN interrupted batch
				ON batch.root_id=p.root_id AND p.position>batch.first
			);
		UPDATE notifications SET status = 'unknown', last_error = 'STATE_RESTORED'
			WHERE status IN ('pending', 'running');
		UPDATE notification_attempts SET outcome = 'unknown' WHERE outcome = 'running';
		UPDATE channel_events SET status = 'processed', payload = NULL,
			reply_status = CASE WHEN result_kind IS NULL THEN 'skipped' ELSE 'unknown' END, last_error = 'STATE_RESTORED'
			WHERE status IN ('pending', 'processing', 'processed');
		UPDATE notifications SET status = 'skipped', last_error = 'STATE_RESTORED' WHERE status = 'unknown'
			AND source_event IN (SELECT message_id FROM channel_events WHERE reply_status = 'skipped');`
)

func resetRestoredState(db *sql.DB, full bool) error {
	query := "BEGIN IMMEDIATE;\n" + restoreCoreResetSQL + "\n"
	if full {
		var compact bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='delivery_logs')`).Scan(&compact); err != nil {
			return err
		}
		if compact {
			query += `DELETE FROM bot_cursors WHERE kind='qq';
            INSERT OR REPLACE INTO bot_restore SELECT instance_id,CAST(round(unixepoch('subsec')*1000) AS INTEGER) FROM bindings;
            UPDATE bot_inbox SET phase='restored',body=NULL,error_code='STATE_RESTORED' WHERE phase IN ('pending','reply','sending');
            UPDATE delivery_logs SET status='failed',last_error='STATE_RESTORED' WHERE status IN ('pending','running');` + "\n"
		} else {
			query += restoreBotAndDeliveryResetSQL + "\n"
		}
	}
	query += "PRAGMA user_version = 0;\nCOMMIT;\nPRAGMA wal_checkpoint(TRUNCATE);\nPRAGMA journal_mode = DELETE;"
	_, err := db.Exec(query)
	return err
}

func Restore(settings Settings, source, safety string) error {
	data, err := os.ReadFile(filepath.Join(source, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return errors.New("invalid backup manifest")
	}
	snapshot := filepath.Join(source, "state.sqlite3")
	checksum, err := fileHash(snapshot)
	if err != nil || manifest.Format != 1 || checksum != manifest.StateSHA256 {
		return errors.New("backup manifest or checksum is invalid")
	}
	if manifest.InstanceID != settings.Memos.InstanceID || manifest.User != settings.Memos.User {
		return errors.New("backup belongs to a different Memos binding")
	}
	if err := verifySnapshot(settings, snapshot); err != nil {
		return err
	}
	lock, err := AcquireWriter(settings.StateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	state := filepath.Join(settings.StateDir, "state.sqlite3")
	if _, err := os.Stat(state); err == nil {
		if _, err := Backup(settings, safety); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	input, err := os.Open(snapshot)
	if err != nil {
		return err
	}
	defer input.Close()
	file, err := os.CreateTemp(settings.StateDir, "state.restore-*.sqlite3")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer removeDatabase(temp)
	_, copyErr := io.Copy(file, input)
	if err := errors.Join(copyErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	db, err := openDB(temp, false)
	if err != nil {
		return err
	}
	if err := migrate(db, "state"); err != nil {
		db.Close()
		return err
	}
	updateErr := resetRestoredState(db, true)
	closeErr := db.Close()
	if err := errors.Join(updateErr, closeErr); err != nil {
		return err
	}
	if err := checkDatabase(temp); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(state + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(temp, state); err != nil {
		return err
	}
	return removeDatabase(filepath.Join(settings.IndexDir, "index.sqlite3"))
}

func RebuildIndex(settings Settings) error {
	lock, err := AcquireWriter(settings.StateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	state := filepath.Join(settings.StateDir, "state.sqlite3")
	if _, err := os.Stat(state); err == nil {
		db, err := openDB(state, false)
		if err != nil {
			return err
		}
		_, updateErr := db.Exec("UPDATE sync_state SET watermark = NULL, last_full = NULL, source_ok = 0, index_generation = NULL WHERE instance_id = ?", settings.Memos.InstanceID)
		if err := errors.Join(updateErr, db.Close()); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return removeDatabase(filepath.Join(settings.IndexDir, "index.sqlite3"))
}

const maxAutoMigrationBackups = 5

type RollbackResult struct {
	BackupPath            string
	SafetyPath            string
	CrossVersion          bool
	DowngradeVersion      string
	MaxSnapshotMigration  string
	MaxCurrentMigration   string
	CurrentKnownMigration string
}

type backupEntry struct {
	path    string
	name    string
	modTime time.Time
}

func autoBackupDir(settings Settings) string {
	return filepath.Join(settings.StateDir, "backups")
}

func listAutoBackups(dir string) ([]backupEntry, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := []backupEntry{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "auto-pre-") || !strings.HasSuffix(name, ".sqlite3") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		result = append(result, backupEntry{
			path:    filepath.Join(dir, name),
			name:    name,
			modTime: info.ModTime(),
		})
	}
	slices.SortFunc(result, func(a, b backupEntry) int {
		if !a.modTime.Equal(b.modTime) {
			if a.modTime.After(b.modTime) {
				return -1
			}
			return 1
		}
		return strings.Compare(b.name, a.name)
	})
	return result, nil
}

func pruneAutoBackups(dir string, keep int) error {
	backups, err := listAutoBackups(dir)
	if err != nil || len(backups) <= keep {
		return err
	}
	for _, entry := range backups[keep:] {
		if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func maybeAutoBackupState(db *sql.DB, settings Settings) error {
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='schema_migrations')`).Scan(&exists); err != nil || !exists {
		return err
	}
	versions, _, applied, err := inspectMigrations(db, "state")
	if err != nil || len(applied) == 0 {
		return nil
	}
	hasPending := false
	for _, version := range versions {
		if applied[version] == "" {
			hasPending = true
			break
		}
	}
	if !hasPending {
		return nil
	}
	dir := autoBackupDir(settings)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tempPath := filepath.Join(dir, ".partial-auto-pre-"+newID()+".sqlite3")
	defer os.Remove(tempPath)
	if _, err := db.Exec("VACUUM INTO ?", tempPath); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0600); err != nil {
		return err
	}
	if err := checkDatabase(tempPath); err != nil {
		return err
	}
	dest := filepath.Join(dir, fmt.Sprintf("auto-pre-%s-%s.sqlite3", Version, time.Now().UTC().Format("20060102T150405.000000000Z")))
	if err := os.Rename(tempPath, dest); err != nil {
		return err
	}
	return pruneAutoBackups(dir, maxAutoMigrationBackups)
}

func resolveRollbackBackup(settings Settings, backup string) (string, error) {
	dir := autoBackupDir(settings)
	if backup == "" {
		entries, err := listAutoBackups(dir)
		if err != nil {
			return "", err
		}
		if len(entries) == 0 {
			return "", errors.New("no automatic pre-migration backup found")
		}
		return entries[0].path, nil
	}
	candidate := backup
	if !filepath.IsAbs(candidate) && !strings.ContainsRune(candidate, os.PathSeparator) {
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			candidate = filepath.Join(dir, candidate)
		}
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("rollback backup path must be a file")
	}
	return abs, nil
}

func maxMigrationKey(applied map[string]string) string {
	maxVersion := ""
	for version := range applied {
		if version > maxVersion {
			maxVersion = version
		}
	}
	return maxVersion
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".partial-copy-*.sqlite3")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	_, copyErr := io.Copy(tmp, in)
	if err := errors.Join(copyErr, tmp.Chmod(0600), tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}

func reserveRollbackSafetyCopy(settings Settings, liveState string) (string, error) {
	dir := autoBackupDir(settings)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, fmt.Sprintf("pre-rollback-%s.sqlite3", time.Now().UTC().Format("20060102T150405.000000000Z")))
	tmpPath := filepath.Join(dir, ".partial-safety-"+newID()+".sqlite3")
	defer os.Remove(tmpPath)
	db, err := openDB(liveState, false)
	if err == nil {
		_, vacuumErr := db.Exec("VACUUM INTO ?", tmpPath)
		closeErr := db.Close()
		if errors.Join(vacuumErr, closeErr) == nil && os.Chmod(tmpPath, 0600) == nil {
			if err := os.Rename(tmpPath, dest); err != nil {
				return "", err
			}
			return dest, nil
		}
	}
	if err := copyFileAtomic(liveState, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func Rollback(settings Settings, backup string) (RollbackResult, error) {
	var result RollbackResult
	lock, err := AcquireWriter(settings.StateDir)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	snapshot, err := resolveRollbackBackup(settings, backup)
	if err != nil {
		return result, err
	}
	if err := verifySnapshot(settings, snapshot); err != nil {
		return result, err
	}
	snapDB, err := openDB(snapshot, true)
	if err != nil {
		return result, err
	}
	snapApplied, snapErr := appliedMigrations(snapDB)
	snapCloseErr := snapDB.Close()
	if err := errors.Join(snapErr, snapCloseErr); err != nil {
		return result, err
	}
	if len(snapApplied) == 0 {
		return result, errors.New("rollback snapshot contains no applied migrations")
	}
	entries, err := migrations.ReadDir("migrations/state")
	if err != nil {
		return result, err
	}
	knownVersions := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			v := strings.TrimSuffix(entry.Name(), ".sql")
			knownVersions[v] = v
		}
	}
	result.BackupPath = snapshot
	result.MaxSnapshotMigration = maxMigrationKey(snapApplied)
	result.CurrentKnownMigration = maxMigrationKey(knownVersions)

	statePath := filepath.Join(settings.StateDir, "state.sqlite3")
	if _, statErr := os.Stat(statePath); statErr == nil {
		if curDB, openErr := openDB(statePath, true); openErr == nil {
			if curApplied, err := appliedMigrations(curDB); err == nil {
				result.MaxCurrentMigration = maxMigrationKey(curApplied)
			}
			_ = curDB.Close()
		}
		safetyPath, err := reserveRollbackSafetyCopy(settings, statePath)
		if err != nil {
			return result, err
		}
		result.SafetyPath = safetyPath
	} else if !os.IsNotExist(statErr) {
		return result, statErr
	}
	if result.MaxSnapshotMigration < result.CurrentKnownMigration ||
		(result.MaxCurrentMigration != "" && result.MaxSnapshotMigration < result.MaxCurrentMigration) {
		result.CrossVersion = true
		if result.MaxSnapshotMigration <= "007" {
			result.DowngradeVersion = "0.2.0"
		}
	}

	input, err := os.Open(snapshot)
	if err != nil {
		return result, err
	}
	defer input.Close()
	tmpFile, err := os.CreateTemp(settings.StateDir, "state.rollback-*.sqlite3")
	if err != nil {
		return result, err
	}
	tempPath := tmpFile.Name()
	defer removeDatabase(tempPath)
	_, copyErr := io.Copy(tmpFile, input)
	if err := errors.Join(copyErr, tmpFile.Chmod(0600), tmpFile.Sync(), tmpFile.Close()); err != nil {
		return result, err
	}
	db, err := openDB(tempPath, false)
	if err != nil {
		return result, err
	}
	resetErr := resetRestoredState(db, result.MaxSnapshotMigration >= "003")
	closeErr := db.Close()
	if err := errors.Join(resetErr, closeErr); err != nil {
		return result, err
	}
	if err := checkDatabase(tempPath); err != nil {
		return result, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(statePath + suffix); err != nil && !os.IsNotExist(err) {
			return result, err
		}
	}
	if err := os.Rename(tempPath, statePath); err != nil {
		return result, err
	}
	if err := removeDatabase(filepath.Join(settings.IndexDir, "index.sqlite3")); err != nil {
		return result, err
	}
	return result, nil
}

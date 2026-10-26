package moerise

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

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
	_, updateErr := db.Exec(`BEGIN IMMEDIATE;
		UPDATE sync_state SET watermark = NULL, last_full = NULL, last_incremental = NULL,
			source_ok = 0, index_generation = NULL;
		DELETE FROM jobs WHERE kind = 'sync';
		UPDATE notifications SET status = 'unknown', last_error = 'STATE_RESTORED'
			WHERE status IN ('pending', 'running');
		UPDATE notification_attempts SET outcome = 'unknown' WHERE outcome = 'running';
		UPDATE channel_events SET reply_status = 'unknown', last_error = 'STATE_RESTORED'
			WHERE status IN ('pending', 'processing', 'processed');
		PRAGMA user_version = 0;
		COMMIT;
		PRAGMA wal_checkpoint(TRUNCATE);
		PRAGMA journal_mode = DELETE;`)
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

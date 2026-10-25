package moerise

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*/*.sql
var migrations embed.FS

type Store struct {
	Settings Settings
	state    *sql.DB
	index    *sql.DB
}

func openDB(path string, readonly bool) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	slash := filepath.ToSlash(absolute)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	address := url.URL{Scheme: "file", Path: slash}
	options := url.Values{}
	if readonly {
		options.Set("mode", "ro")
		options.Set("immutable", "1")
	} else {
		options.Add("_pragma", "foreign_keys(1)")
		options.Add("_pragma", "busy_timeout(15000)")
		options.Add("_pragma", "synchronous(FULL)")
		options.Set("_txlock", "immediate")
	}
	address.RawQuery = options.Encode()
	db, err := sql.Open("sqlite", address.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrationSQL(kind, version string) ([]byte, error) {
	data, err := migrations.ReadFile("migrations/" + kind + "/" + version + ".sql")
	return []byte(strings.ReplaceAll(string(data), "\r\n", "\n")), err
}

func migrate(db *sql.DB, kind string) error {
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;
		CREATE TABLE IF NOT EXISTS schema_migrations
		(version TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations/" + kind)
	if err != nil {
		return err
	}
	known := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version := strings.TrimSuffix(entry.Name(), ".sql")
		body, err := migrationSQL(kind, version)
		if err != nil {
			return err
		}
		known[version] = hashBytes(body)
	}
	applied, err := appliedMigrations(db)
	if err != nil {
		return err
	}
	for version, checksum := range applied {
		if known[version] == "" {
			return fmt.Errorf("%s database requires a newer Moerise version", kind)
		}
		if known[version] != checksum {
			return fmt.Errorf("changed %s migration: %s", kind, version)
		}
	}
	for _, entry := range entries {
		version := strings.TrimSuffix(entry.Name(), ".sql")
		if known[version] == "" || applied[version] != "" {
			continue
		}
		body, _ := migrationSQL(kind, version)
		if err := transaction(db, func(tx *sql.Tx) error {
			if _, err := tx.Exec(string(body)); err != nil {
				return err
			}
			_, err := tx.Exec("INSERT INTO schema_migrations VALUES (?, ?, ?)", version, known[version], nowText())
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

func appliedMigrations(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query("SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		result[version] = checksum
	}
	return result, rows.Err()
}

func transaction(db *sql.DB, action func(*sql.Tx) error) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := action(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func NewStore(settings Settings) (*Store, error) {
	for _, directory := range []string{settings.StateDir, settings.IndexDir} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, err
		}
	}
	stateInfo, err := os.Stat(settings.StateDir)
	if err != nil {
		return nil, err
	}
	indexInfo, err := os.Stat(settings.IndexDir)
	if err != nil {
		return nil, err
	}
	if os.SameFile(stateInfo, indexInfo) {
		return nil, errors.New("state and index directories must be separate")
	}
	state, err := openDB(filepath.Join(settings.StateDir, "state.sqlite3"), false)
	if err != nil {
		return nil, err
	}
	index, err := openDB(filepath.Join(settings.IndexDir, "index.sqlite3"), false)
	if err != nil {
		state.Close()
		return nil, err
	}
	store := &Store{settings, state, index}
	for _, migration := range []struct {
		db   *sql.DB
		kind string
	}{{state, "state"}, {index, "index"}} {
		if err := migrate(migration.db, migration.kind); err != nil {
			store.Close()
			return nil, err
		}
	}
	if err := store.bind(); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return errors.Join(s.state.Close(), s.index.Close()) }

func (s *Store) bind() error {
	instance, user := s.Settings.Memos.InstanceID, s.Settings.Memos.User
	if err := transaction(s.state, func(tx *sql.Tx) error {
		var address, boundUser string
		err := tx.QueryRow("SELECT base_url, user_name FROM bindings WHERE instance_id = ?", instance).Scan(&address, &boundUser)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && (address != s.Settings.Memos.BaseURL || boundUser != user) {
			return errors.New("source binding changed; use a new instance_id and data directories")
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO bindings VALUES (?, ?, ?, ?)", instance, s.Settings.Memos.BaseURL, user, nowText()); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT OR IGNORE INTO sync_state (instance_id, updated_at) VALUES (?, ?)", instance, nowText()); err != nil {
			return err
		}
		rows, err := tx.Query("SELECT id FROM jobs WHERE status = 'running' AND instance_id = ?", instance)
		if err != nil {
			return err
		}
		running := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			running = append(running, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range running {
			var pending string
			err := tx.QueryRow("SELECT id FROM jobs WHERE instance_id = ? AND status = 'pending'", instance).Scan(&pending)
			if err == nil {
				if _, err := tx.Exec("UPDATE jobs SET mode = 'full' WHERE id = ?", pending); err != nil {
					return err
				}
				result, _ := json.Marshal(map[string]string{"merged_into": pending})
				if _, err := tx.Exec("UPDATE jobs SET status = 'succeeded', completed_at = ?, last_error = 'SUPERSEDED_AFTER_RESTART', result_json = ? WHERE id = ?", nowText(), string(result), id); err != nil {
					return err
				}
			} else if errors.Is(err, sql.ErrNoRows) {
				if _, err := tx.Exec("UPDATE jobs SET status = 'pending', mode = 'full', available_at = ?, lease_until = NULL WHERE id = ?", nowSeconds(), id); err != nil {
					return err
				}
			} else {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := transaction(s.index, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM scan_memos"); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT OR IGNORE INTO projection_meta VALUES (?, ?)", instance, newID())
		return err
	}); err != nil {
		return err
	}
	generation, err := s.Generation()
	if err != nil {
		return err
	}
	_, err = s.state.Exec(`UPDATE sync_state SET
		watermark = CASE WHEN index_generation = ? THEN watermark ELSE NULL END,
		last_full = CASE WHEN index_generation = ? THEN last_full ELSE NULL END,
		source_ok = 0, index_generation = ? WHERE instance_id = ?`, generation, generation, generation, instance)
	return err
}

func (s *Store) Status() (SyncStatus, error) {
	result := SyncStatus{Counts: map[string]int{"NORMAL": 0, "ARCHIVED": 0}, Jobs: []JobCount{}}
	var generation *string
	err := s.state.QueryRow(`SELECT instance_id, watermark, last_incremental, last_full,
		source_ok, index_generation, upstream_version, last_error, updated_at FROM sync_state WHERE instance_id = ?`,
		s.Settings.Memos.InstanceID).Scan(&result.InstanceID, &result.Watermark, &result.LastIncremental,
		&result.LastFull, &result.SourceOK, &generation, &result.UpstreamVersion, &result.LastError, &result.UpdatedAt)
	if err != nil {
		return result, err
	}
	if generation != nil {
		result.IndexGeneration = *generation
	}
	rows, err := s.state.Query("SELECT status, COUNT(*) FROM jobs WHERE instance_id = ? GROUP BY status", result.InstanceID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var count JobCount
		if err := rows.Scan(&count.Status, &count.Count); err != nil {
			rows.Close()
			return result, err
		}
		result.Jobs = append(result.Jobs, count)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = s.index.Query("SELECT state, COUNT(*) FROM memos WHERE instance_id = ? AND user_name = ? GROUP BY state", result.InstanceID, s.Settings.Memos.User)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return result, err
		}
		result.Counts[state] = count
	}
	return result, rows.Err()
}

func (s *Store) SourceHealth(ok bool, code, version string) error {
	var errorValue, versionValue any
	if code != "" {
		errorValue = code
	}
	if version != "" {
		versionValue = version
	}
	_, err := s.state.Exec("UPDATE sync_state SET source_ok = ?, last_error = ?, upstream_version = COALESCE(?, upstream_version), updated_at = ? WHERE instance_id = ?", ok, errorValue, versionValue, nowText(), s.Settings.Memos.InstanceID)
	return err
}

func (s *Store) Generation() (string, error) {
	var result string
	err := s.index.QueryRow("SELECT generation FROM projection_meta WHERE instance_id = ?", s.Settings.Memos.InstanceID).Scan(&result)
	return result, err
}

func (s *Store) Enqueue(mode string, immediate bool) (Job, error) {
	id := ""
	err := transaction(s.state, func(tx *sql.Tx) error {
		err := tx.QueryRow("SELECT id FROM jobs WHERE instance_id = ? AND status = 'pending'", s.Settings.Memos.InstanceID).Scan(&id)
		if err == nil {
			if mode == "full" {
				if _, err := tx.Exec("UPDATE jobs SET mode = 'full' WHERE id = ?", id); err != nil {
					return err
				}
			}
			if immediate {
				if _, err := tx.Exec("UPDATE jobs SET available_at = ? WHERE id = ?", nowSeconds(), id); err != nil {
					return err
				}
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id = newID()
		_, err = tx.Exec("INSERT INTO jobs (id, instance_id, kind, mode, status, available_at, created_at) VALUES (?, ?, 'sync', ?, 'pending', ?, ?)", id, s.Settings.Memos.InstanceID, mode, nowSeconds(), nowText())
		return err
	})
	if err != nil {
		return Job{}, err
	}
	return s.Job(id)
}

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var job Job
	var result *string
	err := row.Scan(&job.ID, &job.InstanceID, &job.Kind, &job.Mode, &job.Status, &job.Attempts, &job.AvailableAt,
		&job.LeaseUntil, &job.CreatedAt, &job.StartedAt, &job.CompletedAt, &job.LastError, &result)
	if err == nil && result != nil {
		err = json.Unmarshal([]byte(*result), &job.Result)
	}
	return job, err
}

func (s *Store) Job(id string) (Job, error) {
	job, err := scanJob(s.state.QueryRow("SELECT * FROM jobs WHERE id = ? AND instance_id = ?", id, s.Settings.Memos.InstanceID))
	if errors.Is(err, sql.ErrNoRows) {
		err = failure("JOB_NOT_FOUND", "Job was not found", 404, false)
	}
	return job, err
}

func (s *Store) Claim() (*Job, error) {
	var id string
	err := transaction(s.state, func(tx *sql.Tx) error {
		err := tx.QueryRow("SELECT id FROM jobs WHERE status = 'pending' AND instance_id = ? AND available_at <= ? ORDER BY created_at LIMIT 1", s.Settings.Memos.InstanceID, nowSeconds()).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE jobs SET status = 'running', attempts = attempts + 1, started_at = ?, lease_until = ? WHERE id = ?", nowText(), nowSeconds()+float64(s.Settings.Sync.LeaseSeconds), id)
		return err
	})
	if err != nil || id == "" {
		return nil, err
	}
	job, err := s.Job(id)
	return &job, err
}

func (s *Store) Heartbeat(id string) error {
	_, err := s.state.Exec("UPDATE jobs SET lease_until = ? WHERE id = ? AND status = 'running'", nowSeconds()+float64(s.Settings.Sync.LeaseSeconds), id)
	return err
}

func (s *Store) Complete(id string, result map[string]any) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return transaction(s.state, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE jobs SET status = 'succeeded', completed_at = ?, lease_until = NULL, last_error = NULL, result_json = ? WHERE id = ?", nowText(), string(payload), id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM jobs WHERE instance_id = ? AND status = 'succeeded' AND id NOT IN
			(SELECT id FROM jobs WHERE instance_id = ? AND status = 'succeeded' ORDER BY completed_at DESC LIMIT 500)`, s.Settings.Memos.InstanceID, s.Settings.Memos.InstanceID)
		return err
	})
}

func (s *Store) Retry(job Job, code string) error {
	delay := min(s.Settings.Sync.RetrySeconds*(1<<min(max(job.Attempts-1, 0), 8)), 3600)
	return transaction(s.state, func(tx *sql.Tx) error {
		var pendingID, mode string
		err := tx.QueryRow("SELECT id, mode FROM jobs WHERE instance_id = ? AND status = 'pending'", s.Settings.Memos.InstanceID).Scan(&pendingID, &mode)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if pendingID != "" {
			payload, _ := json.Marshal(map[string]string{"merged_into": job.ID})
			if _, err := tx.Exec("UPDATE jobs SET status = 'succeeded', completed_at = ?, last_error = 'MERGED_WITH_RETRY', result_json = ? WHERE id = ?", nowText(), string(payload), pendingID); err != nil {
				return err
			}
		}
		if mode == "full" || job.Mode == "full" {
			mode = "full"
		} else {
			mode = "incremental"
		}
		_, err = tx.Exec("UPDATE jobs SET status = 'pending', mode = ?, available_at = ?, lease_until = NULL, last_error = ? WHERE id = ?", mode, nowSeconds()+float64(delay), code, job.ID)
		return err
	})
}

func (s *Store) Stage(run string, memos []Memo) error {
	return transaction(s.index, func(tx *sql.Tx) error {
		for _, memo := range memos {
			payload, err := json.Marshal(memo)
			if err != nil {
				return err
			}
			if _, err := tx.Exec("INSERT INTO scan_memos VALUES (?, ?, ?) ON CONFLICT(run_id, name) DO UPDATE SET payload_json = excluded.payload_json", run, memo.Name, string(payload)); err != nil {
				return err
			}
		}
		return nil
	})
}

func namesQuery(db *sql.DB, query string, args ...any) (map[string]bool, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}
func (s *Store) Names() (map[string]bool, error) {
	return namesQuery(s.index, "SELECT name FROM memos WHERE instance_id = ? AND user_name = ?", s.Settings.Memos.InstanceID, s.Settings.Memos.User)
}
func (s *Store) StagedNames(run string) (map[string]bool, error) {
	return namesQuery(s.index, "SELECT name FROM scan_memos WHERE run_id = ?", run)
}

func (s *Store) put(tx *sql.Tx, memo Memo) (int, error) {
	payload, err := json.Marshal(memo)
	if err != nil {
		return 0, err
	}
	result, err := tx.Exec(`INSERT INTO memos VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id, user_name, name) DO UPDATE SET state = excluded.state,
		update_time = excluded.update_time, source_hash = excluded.source_hash, content_hash = excluded.content_hash,
		payload_json = excluded.payload_json, observed_at = excluded.observed_at WHERE memos.source_hash != excluded.source_hash`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User, memo.Name, memo.State, memo.UpdateTime, memo.SourceHash, memo.ContentHash, string(payload), nowText())
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}

func (s *Store) Publish(run string, absent map[string]bool) (map[string]any, error) {
	memos := []Memo{}
	changed := 0
	err := transaction(s.index, func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT payload_json FROM scan_memos WHERE run_id = ?", run)
		if err != nil {
			return err
		}
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				rows.Close()
				return err
			}
			memo, err := decodeMemo(data)
			if err != nil {
				rows.Close()
				return err
			}
			memos = append(memos, memo)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, memo := range memos {
			count, err := s.put(tx, memo)
			if err != nil {
				return err
			}
			changed += count
		}
		for name := range absent {
			if _, err := tx.Exec("DELETE FROM memos WHERE instance_id = ? AND user_name = ? AND name = ?", s.Settings.Memos.InstanceID, s.Settings.Memos.User, name); err != nil {
				return err
			}
		}
		_, err = tx.Exec("DELETE FROM scan_memos WHERE run_id = ?", run)
		return err
	})
	return map[string]any{"observed": len(memos), "changed": changed, "removed": len(absent)}, err
}

func (s *Store) Refresh(name string, memo *Memo) error {
	return transaction(s.index, func(tx *sql.Tx) error {
		if memo == nil {
			_, err := tx.Exec("DELETE FROM memos WHERE instance_id = ? AND user_name = ? AND name = ?", s.Settings.Memos.InstanceID, s.Settings.Memos.User, name)
			return err
		}
		_, err := s.put(tx, *memo)
		return err
	})
}

func (s *Store) Advance(watermark string, full bool) error {
	_, err := s.state.Exec(`UPDATE sync_state SET watermark = ?, last_incremental = ?,
		last_full = CASE WHEN ? THEN ? ELSE last_full END, source_ok = 1, last_error = NULL,
		updated_at = ? WHERE instance_id = ?`, watermark, nowText(), full, nowText(), nowText(), s.Settings.Memos.InstanceID)
	return err
}
func (s *Store) DiscardScan(run string) error {
	_, err := s.index.Exec("DELETE FROM scan_memos WHERE run_id = ?", run)
	return err
}

func (s *Store) Page(state string, size int, cursor []string) ([]Memo, error) {
	where := "instance_id = ? AND user_name = ? AND state = ?"
	args := []any{s.Settings.Memos.InstanceID, s.Settings.Memos.User, state}
	if len(cursor) == 2 {
		where += " AND (update_time, name) < (?, ?)"
		args = append(args, cursor[0], cursor[1])
	}
	args = append(args, size)
	rows, err := s.index.Query("SELECT payload_json FROM memos WHERE "+where+" ORDER BY update_time DESC, name DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Memo{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		memo, err := decodeMemo(data)
		if err != nil {
			return nil, err
		}
		result = append(result, memo)
	}
	return result, rows.Err()
}

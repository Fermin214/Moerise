//go:build integration

package acceptance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestV020UpgradeRollbackAcceptance(t *testing.T) {
	baseline := os.Getenv("MOERISE_V020_BINARY")
	if baseline == "" {
		t.Skip("MOERISE_V020_BINARY points to the released v0.2.0 binary")
	}
	baseline, err := filepath.Abs(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := exec.Command(baseline, "version").Output(); err != nil || strings.TrimSpace(string(version)) != "0.2.0" {
		t.Fatalf("invalid baseline: %s %v", version, err)
	}
	h := newHarnessWithBinary(t, nil, baseline)
	note := h.call(h.memos, "/api/v1/memos", "POST", h.upstream,
		map[string]string{"content": "upgrade source #升级", "visibility": "PRIVATE"}, 200)
	name := note["name"].(string)
	projected := func() {
		t.Helper()
		wait(t, "note projected by the running binary", func() bool {
			return h.projection()[name]["content"] == "upgrade source #升级"
		})
	}
	projected()
	h.stop()
	state := h.database("state")
	if _, err := state.Exec(`INSERT INTO review_batches VALUES ('upgrade-history', ?, ?, '2026-01-01', 'UTC', '2026-01-01T00:00:00Z')`,
		h.settings.Memos.InstanceID, h.settings.Memos.User); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(`INSERT INTO review_items VALUES ('upgrade-history', 0, ?, 'upgrade-source-hash', '2026-01-01T00:00:00Z')`, name); err != nil {
		t.Fatal(err)
	}
	state.Close()
	preservedHistory := func() {
		t.Helper()
		state := h.database("state")
		defer state.Close()
		var memoName, sourceHash string
		if err := state.QueryRow("SELECT memo_name, source_hash FROM review_items WHERE batch_id='upgrade-history'").Scan(&memoName, &sourceHash); err != nil || memoName != name || sourceHash != "upgrade-source-hash" {
			t.Fatalf("product review history changed: %s %s %v", memoName, sourceHash, err)
		}
	}
	backup := filepath.Join(h.root, "v020-backup")
	h.command(true, "backup", "--output", backup)
	if _, err := os.Stat(filepath.Join(backup, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	before := h.database("state")
	var oldMigrations int
	if err := before.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&oldMigrations); err != nil {
		t.Fatal(err)
	}
	before.Close()
	latest := buildHarnessBinary(t, filepath.Join(h.root, "moerise-new"))
	h.binary = latest
	h.start()
	h.ready()
	projected()
	preservedHistory()
	if h.projection()[name]["content"] != "upgrade source #升级" {
		t.Fatal("upgrade lost indexed note")
	}
	h.stop()
	backups, err := filepath.Glob(filepath.Join(h.settings.StateDir, "backups", "auto-pre-*.sqlite3"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one automatic pre-upgrade backup: %v %v", backups, err)
	}
	state = h.database("state")
	var newMigrations int
	if err := state.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&newMigrations); err != nil {
		t.Fatal(err)
	}
	state.Close()
	if newMigrations <= oldMigrations {
		t.Fatal("upgrade did not apply new state migrations")
	}
	state = h.database("state")
	if _, err := state.Exec("UPDATE review_items SET source_hash='post-upgrade-revision' WHERE batch_id='upgrade-history'"); err != nil {
		t.Fatal(err)
	}
	state.Close()
	h.command(true, "rollback", "--backup", backups[0])
	if _, err := os.Stat(filepath.Join(h.settings.IndexDir, "index.sqlite3")); !os.IsNotExist(err) {
		t.Fatalf("rollback did not discard the rebuildable index: %v", err)
	}
	h.binary = baseline
	h.start()
	h.ready()
	projected()
	preservedHistory()
	if h.projection()[name]["content"] != "upgrade source #升级" {
		t.Fatal("v0.2.0 did not rebuild notes after rollback")
	}
	h.stop()
	h.command(true, "backup", "--output", filepath.Join(h.root, "rolled-back-v020"))
	h.binary = latest
	h.start()
	h.ready()
	projected()
	preservedHistory()
	if h.projection()[name]["content"] != "upgrade source #升级" {
		t.Fatal("second upgrade lost the note")
	}
	data, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
	var manifest map[string]any
	if err != nil || json.Unmarshal(data, &manifest) != nil {
		t.Fatal("original v0.2.0 backup is unreadable", err)
	}
	t.Log("PASS released v0.2.0 backup, upgrade, automatic hot backup, v0.2.0 rollback/reindex and second upgrade")
}

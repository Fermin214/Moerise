//go:build integration

package acceptance

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	moerise "moerise/internal"
)

func upgradeClient(h *harness) *clientHarness {
	client := &clientHarness{h: h}
	client.call("POST", "/api/app/v1/session", map[string]string{"username": "acceptance-owner", "password": h.password}, 200)
	return client
}

func upgradeSettings(t *testing.T, h *harness, edit func(map[string]any)) map[string]any {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(h.settings.Web.StateEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := hkdf.Key(sha256.New, key, nil, "moerise/state/product_settings/v1", 32)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	state := h.database("state")
	defer state.Close()
	var version int64
	var ciphertext []byte
	if err := state.QueryRow("SELECT version, ciphertext FROM product_settings").Scan(&version, &ciphertext); err != nil {
		t.Fatal(err)
	}
	aad := fmt.Appendf(nil, "%s\x00%s\x00%d", h.settings.Memos.InstanceID, h.settings.Memos.User, version)
	nonceSize := aead.NonceSize()
	plain, err := aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], aad)
	if err != nil {
		t.Fatal(err)
	}
	var product map[string]any
	if err := json.Unmarshal(plain, &product); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(product)
		plain, err = json.Marshal(product)
		if err != nil {
			t.Fatal(err)
		}
		nonce := make([]byte, nonceSize)
		if _, err := rand.Read(nonce); err != nil {
			t.Fatal(err)
		}
		ciphertext = aead.Seal(nonce, nonce, plain, aad)
		if _, err := state.Exec("UPDATE product_settings SET ciphertext=?", ciphertext); err != nil {
			t.Fatal(err)
		}
	}
	return product
}

func TestUpgradeV030RollbackAcceptance(t *testing.T) {
	baseline := os.Getenv("MOERISE_V030_BINARY")
	if baseline == "" {
		t.Fatal("MOERISE_V030_BINARY must point to the released v0.3.0 binary")
	}
	baseline, err := filepath.Abs(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := exec.Command(baseline, "version").Output(); err != nil || strings.TrimSpace(string(version)) != "0.3.0" {
		t.Fatalf("invalid baseline: %s %v", version, err)
	}
	h := newHarnessWithBinary(t, nil, baseline)
	h.stop()
	unkeyedBackup := filepath.Join(h.root, "v030-unkeyed")
	h.command(true, "backup", "--output", unkeyedBackup)
	template, err := os.ReadFile(filepath.Join(filepath.Dir(baseline), "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(template, &config); err != nil {
		t.Fatal(err)
	}
	h.settings.Web = moerise.WebConfig{Enabled: true, PublicOrigin: h.base, StateEncryptionKey: randomStateKey()}
	config["memos"], config["web"], config["sync"] = h.settings.Memos, h.settings.Web, h.settings.Sync
	config["state_dir"], config["index_dir"] = h.settings.StateDir, h.settings.IndexDir
	config["access_tokens"], config["cursor_secret"] = h.settings.AccessTokens, h.settings.CursorSecret
	config["review"] = moerise.ReviewConfig{Count: 3, RecentDays: 0}
	config["llm"].(map[string]any)["api_key"] = "v030-private-llm-key"
	config["insights"].(map[string]any)["default_prompt"] = "保留旧提示词"
	releasedConfig, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.config, releasedConfig, 0600); err != nil {
		t.Fatal(err)
	}
	h.command(true, "check-config")
	h.start()
	client := upgradeClient(h)
	originalSettings := client.call("GET", "/api/app/v1/settings", nil, 200)
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
	chat := client.call("POST", "/api/app/v1/chats", map[string]any{
		"request_id": "v030-upgrade-chat", "title": "升级前的洞察", "names": []string{name},
	}, 201)
	chatPath := "/api/app/v1/chats/" + chat["id"].(string)
	h.stop()
	state := h.database("state")
	if _, err := state.Exec(`INSERT INTO chat_turns
		(id, chat_id, position, request_id, request_hash, kind, user_content, assistant_content, status,
		 settings_version, prompt, model, materials_json, created_at, updated_at)
		VALUES ('upgrade-turn', ?, 0, 'v030-question', 'upgrade-hash', 'question', '问题正文', '升级前的完整回答',
		        'completed', 1, '', 'old-model', '[]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, chat["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(`INSERT INTO review_batches (id, instance_id, user_name, local_date, timezone, created_at)
		VALUES ('upgrade-history', ?, ?, '2026-01-01', 'UTC', '2026-01-01T00:00:00Z')`,
		h.settings.Memos.InstanceID, h.settings.Memos.User); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(`INSERT INTO review_items VALUES ('upgrade-history', 0, ?, 'upgrade-source-hash', '2026-01-01T00:00:00Z')`, name); err != nil {
		t.Fatal(err)
	}
	state.Close()
	h.start()
	history := client.call("GET", chatPath, nil, 200)
	h.stop()
	oldProduct := upgradeSettings(t, h, func(product map[string]any) {
		delete(product["stargazing"].(map[string]any), "enabled")
	})
	preservedHistory := func() {
		t.Helper()
		state := h.database("state")
		defer state.Close()
		var memoName, sourceHash string
		if err := state.QueryRow("SELECT memo_name, source_hash FROM review_items WHERE batch_id='upgrade-history'").Scan(&memoName, &sourceHash); err != nil || memoName != name || sourceHash != "upgrade-source-hash" {
			t.Fatalf("product review history changed: %s %s %v", memoName, sourceHash, err)
		}
		var answer string
		if err := state.QueryRow("SELECT assistant_content FROM chat_turns WHERE id='upgrade-turn'").Scan(&answer); err != nil || answer != "升级前的完整回答" {
			t.Fatalf("legacy chat history changed: %s %v", answer, err)
		}
	}
	backup := filepath.Join(h.root, "v030-backup")
	h.command(true, "backup", "--output", backup)
	if _, err := os.Stat(filepath.Join(backup, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	before := h.database("state")
	var oldMigration string
	if err := before.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&oldMigration); err != nil || oldMigration != "009" {
		t.Fatalf("expected released migration 009, got %s: %v", oldMigration, err)
	}
	before.Close()
	latest := buildHarnessBinary(t, filepath.Join(h.root, "moerise-new"))
	h.binary = latest
	h.start()
	h.ready()
	projected()
	preservedHistory()
	if identity := client.call("GET", "/api/app/v1/session", nil, 200); identity["stargazing_enabled"] != true {
		t.Fatal("legacy ciphertext without stargazing.enabled should keep stargazing enabled")
	}
	client.call("GET", chatPath, nil, 404)
	client.call("GET", "/api/app/v1/chats?page_size=20", nil, 404)
	client.call("GET", "/api/app/v1/chat-materials?page_size=20", nil, 404)
	if capabilities := client.call("GET", "/api/app/v1/capabilities", nil, 200); capabilities["chat"] != nil {
		t.Fatal("capabilities expose chat")
	}
	view := client.call("GET", "/api/app/v1/settings", nil, 200)
	updated := client.call("PATCH", "/api/app/v1/settings",
		map[string]any{"version": view["version"], "settings": map[string]any{"timezone": "UTC"}}, 200)
	public, _ := json.Marshal(updated)
	for _, field := range []string{`"llm"`, `"insights"`, `"llm_naming"`, `"llm_api_key"`, `"default_insight_prompt"`, `v030-private-llm-key`} {
		if bytes.Contains(public, []byte(field)) {
			t.Fatalf("settings response exposes retired field %s", field)
		}
	}
	compatibilityNote := client.call("POST", "/api/app/v1/notes", map[string]string{"content": "upgrade keyword note"}, 201)
	client.call("GET", clientNotePath(compatibilityNote), nil, 200)
	client.call("GET", "/api/app/v1/sync/notes", nil, 200)
	if search := client.call("POST", "/api/app/v1/search", map[string]string{"query": "keyword", "mode": "keyword"}, 200); len(search["notes"].([]any)) == 0 {
		t.Fatal("legacy client keyword search did not find the new note")
	}
	client.call("GET", "/api/app/v1/reviews/daily", nil, 200)
	if stars := client.call("GET", "/api/app/v1/map", nil, 200); stars["disabled_reason"] != "embedding" {
		t.Fatal("legacy client could not read stargazing state")
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		vectors := make([]map[string]any, len(request.Input))
		for index, input := range request.Input {
			vectors[index] = map[string]any{"index": index, "embedding": []float32{1, float32(len(input)) / 100, 0.1}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": vectors})
	}))
	t.Cleanup(model.Close)
	client.call("PATCH", "/api/app/v1/settings", map[string]any{
		"version": updated["version"],
		"settings": map[string]any{"embedding": map[string]any{
			"enabled": true, "provider": "openai-compatible", "protocol": "openai-compatible",
			"base_url": model.URL + "/v1", "model": "upgrade-fixture", "dimensions": 3,
		}},
		"secrets": map[string]any{"embedding_api_key": map[string]string{"action": "replace", "value": "upgrade-fixture-key"}},
	}, 200)
	h.vectorsReady()
	client.call("POST", "/api/app/v1/walks/start", map[string]any{"name": name}, 200)
	acceptanceMap(t, client)
	h.stop()
	after := upgradeSettings(t, h, nil)
	for _, key := range []string{"llm", "insights"} {
		if !reflect.DeepEqual(after[key], oldProduct[key]) {
			t.Fatalf("encrypted %s changed after an unrelated patch", key)
		}
	}
	if after["llm"].(map[string]any)["api_key"] != "v030-private-llm-key" ||
		after["stargazing"].(map[string]any)["llm_naming"] != oldProduct["stargazing"].(map[string]any)["llm_naming"] {
		t.Fatal("encrypted legacy credential or naming preference was lost")
	}
	backups, err := filepath.Glob(filepath.Join(h.settings.StateDir, "backups", "auto-pre-"+moerise.Version+"-*.sqlite3"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one automatic pre-upgrade backup: %v %v", backups, err)
	}
	state = h.database("state")
	var newMigration string
	if err := state.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&newMigration); err != nil {
		t.Fatal(err)
	}
	state.Close()
	if newMigration != "010" {
		t.Fatal("upgrade did not apply new state migrations")
	}
	state = h.database("state")
	if _, err := state.Exec("UPDATE review_items SET source_hash='post-upgrade-revision' WHERE batch_id='upgrade-history'"); err != nil {
		t.Fatal(err)
	}
	state.Close()
	h.binary = baseline
	h.command(false, "backup", "--output", filepath.Join(h.root, "incompatible-v030"))
	h.binary = latest
	h.command(false, "rollback", "--backup", backup)
	for _, snapshot := range []string{backups[0], filepath.Join(backup, "state.sqlite3")} {
		h.binary = latest
		t.Logf("rollback using %s", snapshot)
		h.command(true, "rollback", "--backup", snapshot)
		if _, err := os.Stat(filepath.Join(h.settings.IndexDir, "index.sqlite3")); !os.IsNotExist(err) {
			t.Fatalf("rollback did not discard the rebuildable index: %v", err)
		}
		h.binary = baseline
		h.start()
		h.ready()
		projected()
		preservedHistory()
		restored := upgradeClient(h)
		if !reflect.DeepEqual(restored.call("GET", "/api/app/v1/settings", nil, 200), originalSettings) ||
			!reflect.DeepEqual(restored.call("GET", chatPath, nil, 200), history) {
			t.Fatal("released v0.3.0 did not recover original settings and complete chat history")
		}
		h.stop()
		h.binary = latest
		h.start()
		h.ready()
		preservedHistory()
		h.stop()
	}
	data, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
	var manifest map[string]any
	if err != nil || json.Unmarshal(data, &manifest) != nil {
		t.Fatal("original v0.3.0 backup is unreadable", err)
	}
	if onDisk, err := os.ReadFile(h.config); err != nil || !bytes.Equal(onDisk, releasedConfig) {
		t.Fatal("upgrade rewrote the released deployment configuration", err)
	}
	t.Run("deployment precedence", func(t *testing.T) {
		config["web"] = moerise.WebConfig{}
		delete(config["stargazing"].(map[string]any), "enabled")
		h.settings.StateDir = filepath.Join(h.root, "unkeyed-state")
		h.settings.IndexDir = filepath.Join(h.root, "unkeyed-index")
		config["state_dir"], config["index_dir"] = h.settings.StateDir, h.settings.IndexDir
		writeConfig := func() {
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(h.config, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		checkBoot := func(enabled bool) {
			check := func() {
				t.Helper()
				settings, err := moerise.LoadSettings(h.config)
				if err != nil || settings.Stargazing.Enabled != enabled {
					t.Fatalf("expected stargazing.enabled=%t, got %v: %v", enabled, settings.Stargazing.Enabled, err)
				}
			}
			check()
			h.start()
			defer h.stop()
			check()
		}
		writeConfig()
		checkBoot(false)
		checkBoot(false)
		snapshot, err := os.ReadFile(filepath.Join(unkeyedBackup, "state.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		h.settings.StateDir = filepath.Join(h.root, "legacy-unkeyed-state")
		h.settings.IndexDir = filepath.Join(h.root, "legacy-unkeyed-index")
		config["state_dir"], config["index_dir"] = h.settings.StateDir, h.settings.IndexDir
		if err := os.Mkdir(h.settings.StateDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.settings.StateDir, "state.sqlite3"), snapshot, 0600); err != nil {
			t.Fatal(err)
		}
		config["stargazing"].(map[string]any)["Enabled"] = nil
		writeConfig()
		checkBoot(true)
		checkBoot(true)
		config["stargazing"].(map[string]any)["enabled"] = false
		writeConfig()
		checkBoot(false)
		t.Setenv("MOERISE_STARGAZING_ENABLED", "true")
		checkBoot(true)
	})
	t.Run("persisted disabled outranks deployment", func(t *testing.T) {
		h.settings.StateDir = filepath.Join(h.root, "state")
		h.settings.IndexDir = filepath.Join(h.root, "index")
		if err := os.WriteFile(h.config, releasedConfig, 0600); err != nil {
			t.Fatal(err)
		}
		h.binary = latest
		upgradeSettings(t, h, func(product map[string]any) {
			product["stargazing"].(map[string]any)["enabled"] = false
		})
		h.start()
		defer h.stop()
		if identity := upgradeClient(h).call("GET", "/api/app/v1/session", nil, 200); identity["stargazing_enabled"] != false {
			t.Fatal("persisted false was replaced by the deployment setting")
		}
	})
	t.Log("PASS released v0.3.0 state and config upgrade, retired ciphertext round-trip, automatic and manual rollback")
}

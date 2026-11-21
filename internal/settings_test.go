package moerise

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func settingsFixture(t *testing.T) (Settings, *Store) {
	t.Helper()
	settings := testSettings(t)
	settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	return settings, testStore(t, settings)
}

func settingsChange(version int64, fields string) SettingsUpdate {
	var update SettingsUpdate
	_ = json.Unmarshal([]byte(fields), &update.Settings)
	update.Version = version
	return update
}

func TestProductSettingsWithoutStargazingKeepsEnabledDefault(t *testing.T) {
	settings, _ := settingsFixture(t)
	body, err := json.Marshal(productSettings(settings))
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(body, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "stargazing")
	body, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := newStateCipher(settings, "product_settings")
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	settings.Stargazing.Enabled = false
	loaded, err := decryptProduct(settings, 1, aead.Seal(nonce, nonce, body, settingsAAD(settings, 1)))
	if err != nil || !loaded.Stargazing.Enabled {
		t.Fatalf("legacy settings disabled stargazing: %+v %v", loaded.Stargazing, err)
	}
}

func TestProductSettingsNotificationTimesPatch(t *testing.T) {
	deployment, store := settingsFixture(t)
	next, err := store.prepareSettings(settingsChange(1, `{"notifications":{"times":["20:00","08:00"]}}`))
	if err != nil || !slices.Equal(next.Notifications.Clocks(), []string{"08:00", "20:00"}) {
		t.Fatalf("clock patch: %+v %v", next.Notifications, err)
	}
	if !slices.Equal(store.Settings.Notifications.Times, []string{"09:00"}) {
		t.Fatal("patch mutated saved settings")
	}
	if err := store.persistSettings(next); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadPersistedProduct(deployment)
	if err != nil || !slices.Equal(loaded.Notifications.Clocks(), []string{"08:00", "20:00"}) {
		t.Fatalf("restart clocks: %+v %v", loaded.Notifications, err)
	}
	for _, times := range []string{`[]`, `[""]`, `["25:00"]`, `["9:00"]`} {
		if _, err := store.prepareSettings(settingsChange(2, `{"notifications":{"times":`+times+`}}`)); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("invalid clocks: %s %v", times, err)
		}
	}
}

func TestProductSettingsPersistenceAndSecretBoundaries(t *testing.T) {
	settings, store := settingsFixture(t)
	key := "private-model-key-that-must-not-leak"
	update := settingsChange(1, `{"review":{"count":8,"min_age_hours":36},"insights":{"default_prompt":"探索材料中的关联"}}`)
	update.Secrets = map[string]SecretUpdate{"llm_api_key": {Action: "replace", Value: &key}}
	next, err := store.prepareSettings(update)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.persistSettings(next); err != nil {
		t.Fatal(err)
	}
	store.Settings = next
	view, _ := json.Marshal(store.settingsView())
	if bytes.Contains(view, []byte(key)) || bytes.Contains(view, []byte(`"api_key"`)) ||
		!store.settingsView().Secrets["llm_api_key"].Configured {
		t.Fatal("settings view exposes credentials or loses their status")
	}
	var ciphertext []byte
	if err := store.state.QueryRow("SELECT ciphertext FROM product_settings").Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(key)) || bytes.Contains(ciphertext, []byte("review")) {
		t.Fatal("product settings are stored in plaintext")
	}
	// 写连接未关闭时读取 WAL，覆盖进程中断后的恢复路径。
	loaded, err := loadPersistedProduct(settings)
	if err != nil || loaded.Review.Count != 8 || configured(loaded.LLM.APIKey) != key {
		t.Fatalf("persisted settings did not override deployment values: %v, %+v", err, loaded.Review)
	}
	if _, err := store.prepareSettings(update); errorCode(err) != "SETTINGS_CONFLICT" {
		t.Fatalf("stale version accepted: %v", err)
	}
	settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	if _, err := loadPersistedProduct(settings); err == nil {
		t.Fatal("wrong encryption key accepted")
	}
}

func TestProductSettingsSecretOperationsAndValidation(t *testing.T) {
	_, store := settingsFixture(t)
	key := "a-private-key"
	store.Settings.LLM.APIKey = &key
	store.Settings.LLM.Model = new("original-model")
	for _, body := range []string{
		`{"memos":{"token":"bad"}}`, `{"llm":{"api_key":"bypass"}}`,
		`{"review":{"count":0}}`, `{"notifications":{"times":["09:00","09:00"]}}`,
		`{"timezone":null}`, `{"insights":{"default_prompt":""}}`,
	} {
		if _, err := store.prepareSettings(settingsChange(1, body)); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("accepted invalid settings %s: %v", body, err)
		}
	}
	if _, err := store.prepareSettings(settingsChange(1, `{"llm":{"model":"changed-model"},"review":{"count":0}}`)); errorCode(err) != "INVALID_ARGUMENT" ||
		configured(store.Settings.LLM.Model) != "original-model" {
		t.Fatal("rejected settings mutated a pointer in the published version")
	}
	prepared, err := store.prepareSettings(settingsChange(1, `{"llm":{"model":"changed-model"}}`))
	if err != nil || configured(prepared.LLM.Model) != "changed-model" || configured(store.Settings.LLM.Model) != "original-model" {
		t.Fatal("preparing a new model changed the existing model")
	}
	for _, operation := range []SecretUpdate{
		{Action: "replace"}, {Action: "replace", Value: new("*****")},
		{Action: "clear", Value: &key}, {Action: "keep", Value: &key}, {Action: "invalid"},
	} {
		if _, err := store.prepareSettings(SettingsUpdate{Version: 1, Secrets: map[string]SecretUpdate{"llm_api_key": operation}}); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("accepted invalid secret operation: %v", err)
		}
	}
	for _, action := range []string{"keep", "clear", "replace"} {
		operation := SecretUpdate{Action: action}
		if action == "replace" {
			operation.Value = new("replacement")
		}
		settings, err := store.prepareSettings(SettingsUpdate{Version: 1, Secrets: map[string]SecretUpdate{"llm_api_key": operation}})
		if err != nil {
			t.Fatal(err)
		}
		expected := map[string]string{"keep": key, "clear": "", "replace": "replacement"}[action]
		if configured(settings.LLM.APIKey) != expected || configured(store.Settings.LLM.APIKey) != key {
			t.Fatal("secret operation mutated the published settings")
		}
	}
}

func TestProductSettingsInitializationAndEnvironmentPrecedence(t *testing.T) {
	settings := testSettings(t)
	settings.Review.MinAgeHours, settings.Review.RecentDays = 29, 11
	settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	store := testStore(t, settings)
	if store.productVersion != 1 || store.Settings.Review.MinAgeHours != 29 || store.Settings.Review.RecentDays != 11 {
		t.Fatal("initialization replaced product values")
	}
	next, err := store.prepareSettings(settingsChange(1, `{"review":{"min_age_hours":12,"recent_days":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.persistSettings(next); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.json")
	body, _ := json.Marshal(settings)
	if err := os.WriteFile(config, body, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOERISE_REVIEW_MIN_AGE_HOURS", "99999999")
	t.Setenv("MOERISE_TIMEZONE", "invalid/timezone")
	loaded, err := LoadSettings(config)
	if err != nil || loaded.Review.MinAgeHours != 12 || loaded.Review.RecentDays != 2 {
		t.Fatalf("deployment product values replaced saved settings: %v", err)
	}
	if strings.Contains(string(body), "ciphertext") {
		t.Fatal("fixture unexpectedly uses stored data")
	}
}

func TestProductSettingsToleratesAndPreservesUnknownFields(t *testing.T) {
	settings, store := settingsFixture(t)
	aead, err := newStateCipher(settings, "product_settings")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	rawKnown, _ := json.Marshal(productSettings(settings))
	if err := json.Unmarshal(rawKnown, &payload); err != nil {
		t.Fatal(err)
	}
	payload["future_top_level"] = map[string]any{"enabled": true, "ratio": 0.75}
	sg := payload["stargazing"].(map[string]any)
	sg["future_layout_mode"] = "galaxy-v2"
	futureBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	cipherBody := aead.Seal(nonce, nonce, futureBytes, settingsAAD(settings, 2))
	if _, err := store.state.Exec(`UPDATE product_settings SET version = 2, ciphertext = ? WHERE instance_id = ?`, cipherBody, settings.Memos.InstanceID); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(settings)
	if err != nil {
		t.Fatalf("failed opening store with future settings keys: %v", err)
	}
	defer reopened.Close()
	next, err := reopened.prepareSettings(settingsChange(2, `{"stargazing":{"enabled":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.persistSettings(next); err != nil {
		t.Fatal(err)
	}
	var version int64
	var savedCipher []byte
	if err := reopened.state.QueryRow(`SELECT version, ciphertext FROM product_settings WHERE instance_id = ?`, settings.Memos.InstanceID).Scan(&version, &savedCipher); err != nil {
		t.Fatal(err)
	}
	plain, err := aead.Open(nil, savedCipher[:aead.NonceSize()], savedCipher[aead.NonceSize():], settingsAAD(settings, version))
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(plain, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if roundTripped["future_top_level"] == nil {
		t.Fatalf("lost top-level unknown field on round-trip: %s", plain)
	}
	if got := roundTripped["stargazing"].(map[string]any); got["enabled"] != false || got["future_layout_mode"] != "galaxy-v2" {
		t.Fatalf("unexpected stargazing payload after round-trip: %s", plain)
	}
}

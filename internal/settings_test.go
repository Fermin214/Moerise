package moerise

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
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
	// Read while the writer is open to include the WAL, as after an interrupted process.
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
	for _, body := range []string{
		`{"memos":{"token":"bad"}}`, `{"llm":{"api_key":"bypass"}}`,
		`{"review":{"count":0}}`, `{"notifications":{"times":["09:00","09:00"]}}`,
		`{"timezone":null}`, `{"insights":{"default_prompt":""}}`,
	} {
		if _, err := store.prepareSettings(settingsChange(1, body)); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("accepted invalid settings %s: %v", body, err)
		}
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

func TestProductSettingsUpgradeInitializationAndEnvironmentPrecedence(t *testing.T) {
	settings, store := settingsFixture(t)
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

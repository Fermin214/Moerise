package moerise

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWebConfigurationEnvironmentAndBoundaries(t *testing.T) {
	settings := testSettings(t)
	data, _ := json.Marshal(settings)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	t.Setenv("MOERISE_WEB_ENABLED", "true")
	t.Setenv("MOERISE_PUBLIC_ORIGIN", "https://notes.example.com/")
	t.Setenv("MOERISE_STATE_ENCRYPTION_KEY", key)
	loaded, err := LoadSettings(path)
	if err != nil || !loaded.Web.Enabled || loaded.Web.PublicOrigin != "https://notes.example.com" || loaded.Web.StateEncryptionKey != key {
		t.Fatalf("web environment: %+v %v", loaded.Web.Enabled, err)
	}
	for _, origin := range []string{"https://notes.example.com", "http://localhost:8787", "http://127.0.0.1:8787", "http://[::1]:8787"} {
		loaded.Web.PublicOrigin = origin
		if err := loaded.Validate(); err != nil {
			t.Fatalf("valid origin %q: %v", origin, err)
		}
	}
	for _, origin := range []string{"", "null", "http://notes.example.com", "https://notes.example.com/path", "https://notes.example.com//", "https://user@notes.example.com", "https://notes.example.com?x=1"} {
		loaded.Web.PublicOrigin = origin
		if err := loaded.Validate(); err == nil {
			t.Fatalf("invalid origin %q accepted", origin)
		}
	}
	loaded.Web.PublicOrigin = "https://notes.example.com"
	for _, badKey := range []string{"", "not-base64", base64.StdEncoding.EncodeToString([]byte("short"))} {
		loaded.Web.StateEncryptionKey = badKey
		if err := loaded.Validate(); err == nil {
			t.Fatal("invalid encryption key accepted")
		}
	}
	loaded.Web.StateEncryptionKey = key
	loaded.CursorSecret = key
	if err := loaded.Validate(); err == nil {
		t.Fatal("state encryption reused the cursor key")
	}
	loaded.Web.Enabled = false
	loaded.Web.PublicOrigin, loaded.Web.StateEncryptionKey = "", ""
	if err := loaded.Validate(); err != nil {
		t.Fatal("disabled web requires session configuration")
	}
}

func TestEnvironmentOverridesAndExplicitReviewMigration(t *testing.T) {
	settings := testSettings(t)
	settings.Review.MinAgeHours, settings.Review.RecentDays, settings.Review.TopicLimit = 48, 14, 1
	data, _ := json.Marshal(settings)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSettings(path)
	if err != nil || loaded.Review.MinAgeHours != 48 || loaded.Review.RecentDays != 14 {
		t.Fatalf("explicit legacy durations were replaced: %+v %v", loaded.Review, err)
	}
	t.Setenv("MOERISE_REVIEW_COUNT", "5")
	t.Setenv("MOERISE_NOTIFICATIONS_TIMES", "18:00, 09:00")
	loaded, err = LoadSettings(path)
	if err != nil || loaded.Review.Count != 5 || !slices.Equal(loaded.Notifications.Clocks(), []string{"09:00", "18:00"}) {
		t.Fatalf("environment overlay failed: %+v %v", loaded.Notifications, err)
	}
	t.Setenv("MOERISE_NOTIFICATIONS_TIMES", "09:00,09:00")
	if _, err := LoadSettings(path); err == nil {
		t.Fatal("duplicate slots accepted")
	}
}

func TestEnvironmentOnlyConfiguration(t *testing.T) {
	settings := testSettings(t)
	for name, value := range map[string]string{
		"MOERISE_MEMOS_INSTANCE_ID": settings.Memos.InstanceID,
		"MOERISE_MEMOS_BASE_URL":    settings.Memos.BaseURL,
		"MOERISE_MEMOS_PUBLIC_URL":  settings.Memos.PublicURL,
		"MOERISE_MEMOS_USER":        settings.Memos.User,
		"MOERISE_MEMOS_TOKEN":       settings.Memos.Token,
		"MOERISE_ACCESS_TOKEN":      settings.AccessTokens[0].Token,
		"MOERISE_CURSOR_SECRET":     settings.CursorSecret,
		"MOERISE_STATE_DIR":         settings.StateDir,
		"MOERISE_INDEX_DIR":         settings.IndexDir,
	} {
		t.Setenv(name, value)
	}
	loaded, err := LoadSettings(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || loaded.Review.MinAgeHours != 72 || loaded.Review.RecentDays != 7 {
		t.Fatalf("environment-only defaults: %+v %v", loaded.Review, err)
	}
}

func TestEnvironmentWebhookWithoutApplication(t *testing.T) {
	settings := testSettings(t)
	data, _ := json.Marshal(settings)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOERISE_CHANNEL_KIND", "feishu-webhook")
	t.Setenv("MOERISE_NOTIFICATIONS_ENABLED", "true")
	t.Setenv("MOERISE_FEISHU_WEBHOOK_URL", "https://open.feishu.cn/open-apis/bot/v2/hook/example")
	loaded, err := LoadSettings(path)
	if err != nil || configured(loaded.Channel.AppID) != "" || !loaded.Notifications.Enabled {
		t.Fatalf("Webhook configuration requires no application credentials: %v", err)
	}
}

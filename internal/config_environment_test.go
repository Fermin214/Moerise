package moerise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

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

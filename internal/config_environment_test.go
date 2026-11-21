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

func TestEnvironmentOverridesAndReviewConfiguration(t *testing.T) {
	settings := testSettings(t)
	settings.Review.MinAgeHours, settings.Review.RecentDays = 48, 14
	data, _ := json.Marshal(settings)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSettings(path)
	if err != nil || loaded.Review.MinAgeHours != 48 || loaded.Review.RecentDays != 14 {
		t.Fatalf("explicit review durations were replaced: %+v %v", loaded.Review, err)
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
		"MOERISE_MEMOS_PAGE_SIZE":   "500",
		"MOERISE_SYNC_POLL_SECONDS": "120",
		"MOERISE_ACCESS_TOKEN":      settings.AccessTokens[0].Token,
		"MOERISE_CURSOR_SECRET":     settings.CursorSecret,
		"MOERISE_STATE_DIR":         settings.StateDir,
		"MOERISE_INDEX_DIR":         settings.IndexDir,
	} {
		t.Setenv(name, value)
	}
	loaded, err := LoadSettings(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || loaded.Review.MinAgeHours != 72 || loaded.Review.RecentDays != 7 ||
		loaded.Memos.PageSize != 500 || loaded.Sync.PollSeconds != 120 || !loaded.TelemetryForwarding {
		t.Fatalf("environment-only defaults: %+v %v", loaded.Review, err)
	}
	t.Setenv("MOERISE_TELEMETRY_FORWARDING", "false")
	loaded, err = LoadSettings(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || loaded.TelemetryForwarding {
		t.Fatalf("forwarding environment opt-out: %v %v", loaded.TelemetryForwarding, err)
	}
	t.Setenv("MOERISE_FEISHU_MODE", "")
	t.Setenv("MOERISE_FEISHU_APP_ID", "cli_private")
	t.Setenv("MOERISE_FEISHU_APP_SECRET", strings.Repeat("s", 32))
	t.Setenv("MOERISE_STATE_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	for _, kind := range []string{"none", "telegram"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("MOERISE_CHANNEL_KIND", kind)
			t.Setenv("MOERISE_STATE_DIR", t.TempDir())
			t.Setenv("MOERISE_INDEX_DIR", t.TempDir())
			if kind == "telegram" {
				t.Setenv("MOERISE_TELEGRAM_BOT_TOKEN", telegramTestToken)
				t.Setenv("MOERISE_TELEGRAM_CHAT_ID", "-123")
				t.Setenv("MOERISE_NOTIFICATIONS_ENABLED", "true")
			}
			path := filepath.Join(t.TempDir(), "absent.json")
			for range 2 {
				settings, err := LoadSettings(path)
				if err != nil {
					t.Fatal(err)
				}
				store := testStore(t, settings)
				app := NewApp(settings, store, nil)
				if app.Feishu == nil || store.Settings.Channel.FeishuMode != "websocket" ||
					!store.settingsView().Secrets["app_secret"].Configured || store.Settings.Channel.Kind != kind {
					t.Fatal("initial or persisted deployment lost the private Feishu app")
				}
				app.Close()
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				t.Setenv("MOERISE_FEISHU_APP_SECRET", "")
			}
		})
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

func TestDeploymentOnlyLegacyQueueSurvivesUpgradeAndRestart(t *testing.T) {
	settings := testSettings(t)
	settings.Channel = ChannelConfig{Kind: "feishu-webhook", WebhookURL: new("https://legacy.example/hook"), UserOpenID: new("ou_old")}
	settings.Notifications.Enabled = true
	if err := os.MkdirAll(settings.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := openDB(filepath.Join(settings.StateDir, "state.sqlite3"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := migrationSQL("state", "001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, checksum TEXT, applied_at TEXT);
		INSERT INTO schema_migrations VALUES ('001',?,?)`, hashBytes(schema), nowText()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bindings VALUES (?,?,?,?)`, settings.Memos.InstanceID,
		settings.Memos.BaseURL, settings.Memos.User, nowText()); err != nil {
		t.Fatal(err)
	}
	itemID := newID()
	oldKey := hashBytes(canonicalJSON([]string{"feishu-webhook", "", "ou_old", "", *settings.Channel.WebhookURL}))
	if _, err := db.Exec(`INSERT INTO notifications (id,instance_id,user_name,channel_key,logical_key,kind,local_date,
		status,platform_uuid,available_at,created_at,updated_at) VALUES (?,?,?,?,'review','review','2026-10-26','unknown',?,0,?,?)`,
		itemID, settings.Memos.InstanceID, settings.Memos.User, oldKey, newID(), nowText(), nowText()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	body, _ := json.Marshal(settings)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	for start := range 2 {
		loaded, err := LoadSettings(path)
		if err != nil {
			t.Fatal(err)
		}
		store := testStore(t, loaded)
		queue := NewNotificationService(store, nil, nil)
		platform := &notificationPlatform{}
		queue.channelTransport = func(channel ChannelConfig) (NotificationTransport, func()) {
			if channel.Kind != "feishu-webhook" || configured(channel.WebhookURL) != *settings.Channel.WebhookURL || channel.UserOpenID != nil {
				t.Fatal("old queue item would use a different destination")
			}
			return platform, func() {}
		}
		if start == 0 {
			if _, err := queue.Retry(t.Context(), itemID); err != nil {
				t.Fatal(err)
			}
		} else if progress, err := queue.Deliver(t.Context()); err != nil || !progress || platform.calls != 1 {
			t.Fatalf("legacy delivery after restart: progress=%v calls=%d err=%v", progress, platform.calls, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

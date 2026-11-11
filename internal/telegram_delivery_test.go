package moerise

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

const telegramTestToken = "123456:abcdefghijklmnopqrstuvwxyz_0123456789"

func telegramTestChannel() ChannelConfig {
	return ChannelConfig{Kind: "telegram", BotToken: new(telegramTestToken), Destination: new("789")}
}

func telegramTestResponse(id int, document bool) map[string]any {
	message := map[string]any{"message_id": id, "chat": map[string]int{"id": 789},
		"from": map[string]any{"id": 123456, "is_bot": true}}
	if document {
		message["document"] = map[string]string{"file_id": "telegram_image"}
	}
	return map[string]any{"ok": true, "result": message}
}

func TestTelegramProtocolAndPrivateErrors(t *testing.T) {
	for _, test := range []struct {
		name, response, code string
		status               int
	}{
		{"success", `{"ok":true,"result":{"message_id":42,"chat":{"id":789},"from":{"id":123456,"is_bot":true}}}`, "", 200},
		{"channel", `{"ok":true,"result":{"message_id":42,"chat":{"id":-100789,"type":"channel"}}}`, "", 200},
		{"wrong-chat", `{"ok":true,"result":{"message_id":42,"chat":{"id":790},"from":{"id":123456,"is_bot":true}}}`, "SEND_OUTCOME_UNKNOWN", 200},
		{"wrong-bot", `{"ok":true,"result":{"message_id":42,"chat":{"id":789},"from":{"id":1,"is_bot":true}}}`, "SEND_OUTCOME_UNKNOWN", 200},
		{"missing-message", `{"ok":true}`, "SEND_OUTCOME_UNKNOWN", 200},
		{"missing-ack", `{}`, "SEND_OUTCOME_UNKNOWN", 200},
		{"invalid-json", `{"ok":true`, "SEND_OUTCOME_UNKNOWN", 200},
		{"trailing-json", `{"ok":true} {}`, "SEND_OUTCOME_UNKNOWN", 200},
		{"denied", `{"ok":false,"error_code":403,"description":"PRIVATE_UPSTREAM"}`, "CHANNEL_REJECTED", 403},
		{"api-denied", `{"ok":false,"error_code":400}`, "CHANNEL_REJECTED", 200},
		{"server-error", `{"ok":false,"error_code":500}`, "SEND_OUTCOME_UNKNOWN", 500},
		{"untrusted-rate-limit", `{}`, "SEND_OUTCOME_UNKNOWN", 429},
		{"redirect", `{}`, "SEND_OUTCOME_UNKNOWN", 307},
		{"rate-limit", `{"ok":false,"error_code":429,"parameters":{"retry_after":90}}`, "CHANNEL_RATE_LIMITED", 429},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			destination := "789"
			if test.name == "channel" {
				destination = "-100789"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/bot"+telegramTestToken+"/sendMessage" {
					t.Error("unexpected Telegram endpoint")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["chat_id"] != destination ||
					body["text"] != "literal **Markdown** <&> 中文🙂\nhttps://example.com" ||
					body["parse_mode"] != nil || body["link_preview_options"].(map[string]any)["is_disabled"] != true {
					t.Error("Telegram changed note text, formatted it or enabled previews")
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			channel := telegramTestChannel()
			channel.Destination = &destination
			delivery := newTelegramDelivery(channel, nil, nil).(*TelegramDelivery)
			defer delivery.http.CloseIdleConnections()
			delivery.baseURL = server.URL
			receipt, err := delivery.sendReviewText(t.Context(), Notification{}, "literal **Markdown** <&> 中文🙂\nhttps://example.com")
			if test.code == "" && (err != nil || receipt != "telegram:"+configured(channel.Destination)+":42") ||
				test.code != "" && (err == nil || errorCode(err) != test.code) || calls != 1 {
				t.Fatalf("unexpected outcome: receipt=%q err=%v calls=%d", receipt, err, calls)
			}
			if err != nil && (strings.Contains(err.Error(), telegramTestToken) ||
				strings.Contains(err.Error(), "PRIVATE_UPSTREAM") || strings.Contains(err.Error(), "literal")) {
				t.Fatal("Telegram error exposed credentials, note content or upstream details")
			}
		})
	}
	t.Run("network-error", func(t *testing.T) {
		delivery := newTelegramDelivery(telegramTestChannel(), nil, nil).(*TelegramDelivery)
		defer delivery.http.CloseIdleConnections()
		delivery.baseURL = "http://127.0.0.1:0"
		_, err := delivery.sendReviewText(t.Context(), Notification{}, "private note")
		if errorCode(err) != "SEND_OUTCOME_UNKNOWN" || strings.Contains(err.Error(), telegramTestToken) {
			t.Fatalf("unsafe network error: %v", err)
		}
	})
}

type telegramReviewSource struct {
	Source
}

func (s telegramReviewSource) DownloadAttachment(context.Context, map[string]any) (CreateAttachment, error) {
	return CreateAttachment{Filename: "原图.png", Type: "image/png", Content: []byte("original-image-bytes")}, nil
}

func TestTelegramFullReviewRateLimitAndChannelRecovery(t *testing.T) {
	reviews, memos := reviewFixture(t, 2)
	store := reviews.Store
	source := telegramReviewSource{reviews.Source}
	content := strings.Repeat("原文🙂 <&> **Markdown**\\\n", 600) + "\nhttps://example.com/end"
	for _, memo := range memos {
		memo["content"] = content
		memo["tags"] = []string{"回忆"}
		memo["createTime"] = "2019-12-31T18:30:00Z"
		memo["attachments"] = []any{
			map[string]any{"name": "attachments/image1", "filename": "原图.png", "type": "image/png"},
			map[string]any{"name": "attachments/file1", "filename": "file.txt", "type": "text/plain"},
		}
	}
	store.Settings.Channel = telegramTestChannel()
	store.Settings.Notifications.Enabled = true
	store.Settings.Review.RecentDays = 0
	store.Settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	if err := store.initializeProductSettings(); err != nil {
		t.Fatal(err)
	}
	deployment := store.Settings
	var texts []string
	uploads, calls, webhookCalls := 0, 0, 0
	fail := "rate-limit"
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 && fail == "rate-limit" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":90}}`)
			return
		}
		image := strings.HasSuffix(r.URL.Path, "/sendDocument")
		if image {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, header, err := r.FormFile("document")
			if err != nil {
				t.Error(err)
				return
			}
			data, _ := io.ReadAll(file)
			file.Close()
			if !bytes.Equal(data, []byte("original-image-bytes")) || header.Filename != "原图.png" || r.FormValue("chat_id") != "789" {
				t.Error("Telegram did not receive the original Memos image and destination")
			}
			uploads++
		} else {
			var body struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" ||
				!utf8.ValidString(body.Text) || len(utf16.Encode([]rune(body.Text))) > 4096 {
				t.Error("invalid or oversized Telegram text")
			}
			texts = append(texts, body.Text)
		}
		if fail == "unknown" {
			fail = ""
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(telegramTestResponse(calls, image))
	}))
	defer telegram.Close()
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		webhookCalls++
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer webhook.Close()
	makeApp := func() *App {
		app := NewApp(deployment, store, source)
		if delivery, ok := app.Notifications.Transport.(*TelegramDelivery); ok {
			delivery.baseURL = telegram.URL
		}
		app.Notifications.channelTransport = func(channel ChannelConfig) (NotificationTransport, func()) {
			delivery, closeDelivery := app.notificationTransport(channel)
			if telegramDelivery, ok := delivery.(*TelegramDelivery); ok {
				telegramDelivery.baseURL = telegram.URL
			}
			return delivery, closeDelivery
		}
		return app
	}
	app := makeApp()
	defer func() { app.Close() }()
	at := time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC)
	batch, err := app.Reviews.Daily(t.Context(), at)
	if err != nil || len(batch.Items) != 2 {
		t.Fatalf("daily review: %v", err)
	}
	// Confirm an image-only note without a platform receipt before continuing to long text.
	memos[batch.Items[0].Name]["content"] = ""
	batch, err = app.Reviews.Get(t.Context(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := app.Notifications.Schedule(t.Context(), at); err != nil || n != 1 {
		t.Fatalf("schedule: %d %v", n, err)
	}
	deliver := func() bool {
		t.Helper()
		worked, err := app.Notifications.Deliver(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return worked
	}
	if !deliver() || !deliver() {
		t.Fatal("header or rate-limited attempt was not processed")
	}
	var delayedID string
	var available float64
	if err := store.state.QueryRow(`SELECT id, available_at FROM notifications
		WHERE last_error = 'CHANNEL_RATE_LIMITED'`).Scan(&delayedID, &available); err != nil ||
		available < nowSeconds()+85 || deliver() {
		t.Fatalf("Telegram retry_after was not respected: %v", err)
	}
	if _, err := store.state.Exec("UPDATE notifications SET available_at = 0 WHERE id = ?", delayedID); err != nil {
		t.Fatal(err)
	}
	fail = "unknown"
	if !deliver() || deliver() {
		t.Fatal("unknown part was resent or released its successor")
	}
	unknown, err := app.Notifications.List("unknown")
	if err != nil || len(unknown) != 1 {
		t.Fatalf("missing unknown part: %v", err)
	}
	update := SettingsUpdate{Version: 1, Settings: map[string]json.RawMessage{
		"channel": json.RawMessage(`{"kind":"none"}`), "notifications": json.RawMessage(`{"enabled":false}`),
	}}
	view, err := app.UpdateSettings(t.Context(), update)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(view)
	var ciphertext []byte
	if err := store.state.QueryRow("SELECT ciphertext FROM product_settings").Scan(&ciphertext); err != nil ||
		bytes.Contains(ciphertext, []byte(telegramTestToken)) || bytes.Contains(public, []byte(telegramTestToken)) ||
		bytes.Contains(public, []byte("notification_channels")) {
		t.Fatal("Telegram credentials escaped encrypted settings")
	}
	app.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = testStore(t, deployment)
	app = makeApp()
	if app.Notifications.Transport != nil || store.Settings.Channel.Kind != "none" || deliver() {
		t.Fatal("disabled channel sent a notification after restart")
	}
	update = SettingsUpdate{Version: 2, Settings: map[string]json.RawMessage{
		"channel": json.RawMessage(`{"kind":"feishu-webhook"}`), "notifications": json.RawMessage(`{"enabled":true}`),
	}, Secrets: map[string]SecretUpdate{"webhook_url": {Action: "replace", Value: &webhook.URL}}}
	if _, err := app.UpdateSettings(t.Context(), update); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Notifications.Resolve(t.Context(), unknown[0].ID, "sent", "unverifiable-receipt", "owner"); errorCode(err) != "CHANNEL_RECEIPT_UNSUPPORTED" {
		t.Fatalf("Telegram accepted an unqueryable receipt: %v", err)
	}
	if _, err := app.Notifications.Resolve(t.Context(), unknown[0].ID, "sent", "", "owner"); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if !deliver() {
			break
		}
	}
	if webhookCalls != 0 || len(texts) < 4 || texts[0] != "Daily review" || uploads != 2 {
		t.Fatalf("old queue changed target, repeated its title or lost images: texts=%d uploads=%d webhook=%d",
			len(texts), uploads, webhookCalls)
	}
	remaining := texts[1:]
	for _, entry := range batch.Items {
		end := 0
		for end < len(remaining) && remaining[end] != "#回忆  Created on 2020-01-01" {
			end++
		}
		if end == len(remaining) || strings.Join(remaining[:end], "") != entry.Note.Content {
			t.Fatalf("Telegram note %s: footer=%v parts=%d actual bytes=%d expected bytes=%d",
				entry.Name, end < len(remaining), end, len(strings.Join(remaining[:end], "")), len(entry.Note.Content))
		}
		remaining = remaining[end+1:]
	}
	if len(remaining) != 0 {
		t.Fatal("Telegram emitted unexpected text")
	}
	if n, err := app.Notifications.Schedule(t.Context(), at.Add(time.Hour)); err != nil || n != 0 {
		t.Fatal("channel switch duplicated a processed review slot")
	}
	if n, err := app.Notifications.Schedule(t.Context(), at.AddDate(0, 0, 1)); err != nil || n != 1 {
		t.Fatalf("new channel not scheduled: %d %v", n, err)
	}
	for range 100 {
		if !deliver() {
			break
		}
	}
	if webhookCalls == 0 {
		t.Fatal("new reviews did not use the selected Feishu webhook")
	}
}

func TestNotificationChannelDefaultsAndTelegramEnvironment(t *testing.T) {
	settings := testSettings(t)
	if settings.Channel.Kind != "none" || settings.Notifications.Enabled {
		t.Fatal("a messaging provider is selected by default")
	}
	store := testStore(t, settings)
	app := NewApp(settings, store, nil)
	defer app.Close()
	if app.Feishu != nil || app.Notifications.Transport != nil || store.settingsView().ChannelCapabilities.Text {
		t.Fatal("default configuration activated a messaging provider")
	}
	data, _ := json.Marshal(settings)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOERISE_CHANNEL_KIND", "telegram")
	t.Setenv("MOERISE_TELEGRAM_BOT_TOKEN", telegramTestToken)
	t.Setenv("MOERISE_TELEGRAM_CHAT_ID", "-1001234567890")
	t.Setenv("MOERISE_NOTIFICATIONS_ENABLED", "true")
	loaded, err := LoadSettings(path)
	if err != nil || configured(loaded.Channel.BotToken) != telegramTestToken ||
		configured(loaded.Channel.Destination) != "-1001234567890" {
		t.Fatalf("Telegram environment configuration: %v", err)
	}
	for _, value := range []string{"", "0", "@username", "1/other", "9223372036854775808"} {
		candidate := loaded
		candidate.Channel.Destination = &value
		if candidate.Validate() == nil {
			t.Fatalf("invalid Telegram chat ID %q accepted", value)
		}
	}
	for _, value := range []string{"", "no-token", "123456:short", "123456:abcdefghijklmnopqrstuvwxyz/other",
		"9223372036854775808:abcdefghijklmnopqrstuvwxyz"} {
		candidate := loaded
		candidate.Channel.BotToken = &value
		if candidate.Validate() == nil {
			t.Fatal("invalid Telegram bot token accepted")
		}
	}
	original := notificationChannelKey(loaded.Channel)
	rotated := loaded.Channel
	rotated.BotToken = new("123456:rotated_abcdefghijklmnopqrstuvwxyz")
	if notificationChannelKey(rotated) != original {
		t.Fatal("rotating a token for the same bot changed its channel identity")
	}
	rotated.BotToken = new("654321:rotated_abcdefghijklmnopqrstuvwxyz")
	if notificationChannelKey(rotated) == original {
		t.Fatal("changing the bot retained the old channel identity")
	}
}

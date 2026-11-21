package moerise

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSettingsChannelChangePreservesNotificationRecovery(t *testing.T) {
	for _, mode := range []string{"sent", "pending", "failed"} {
		t.Run(mode, func(t *testing.T) {
			var oldCalls, newCalls atomic.Int32
			unknown := mode != "sent" && mode != "pending"
			var fail atomic.Bool
			fail.Store(unknown)
			oldChannel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				oldCalls.Add(1)
				if fail.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = io.WriteString(w, `{"code":0}`)
			}))
			defer oldChannel.Close()
			newChannel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				newCalls.Add(1)
				_, _ = io.WriteString(w, `{"code":0}`)
			}))
			defer newChannel.Close()
			reviews, _ := reviewFixture(t, 12)
			store, source := reviews.Store, reviews.Source
			store.Settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
			store.Settings.Channel.Kind = "feishu-webhook"
			store.Settings.Channel.WebhookURL = &oldChannel.URL
			store.Settings.Notifications.Enabled = true
			if err := store.initializeProductSettings(); err != nil {
				t.Fatal(err)
			}
			deployment := store.Settings
			app := NewApp(deployment, store, source)
			defer app.Close()
			at := time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC)
			if count, err := app.Notifications.Schedule(t.Context(), at); err != nil || count != 1 {
				t.Fatalf("initial schedule: %d %v", count, err)
			}
			items, err := app.Notifications.List("")
			if err != nil || len(items) != 1 {
				t.Fatalf("initial notification: %v %v", items, err)
			}
			root := items[0]
			drain := func() {
				t.Helper()
				for range 30 {
					worked, err := app.Notifications.Deliver(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if !worked {
						return
					}
				}
				t.Fatal("notification queue did not settle")
			}
			if mode != "pending" {
				drain()
			}
			beforeCalls := oldCalls.Load()
			update := SettingsUpdate{Version: 1, Secrets: map[string]SecretUpdate{
				"webhook_url": {Action: "replace", Value: &newChannel.URL},
			}}
			view, err := app.UpdateSettings(t.Context(), update)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(view)
			var ciphertext []byte
			if err := store.state.QueryRow("SELECT ciphertext FROM product_settings").Scan(&ciphertext); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(body, []byte(oldChannel.URL)) || bytes.Contains(body, []byte("notification_channels")) ||
				bytes.Contains(ciphertext, []byte(oldChannel.URL)) {
				t.Fatal("old channel credentials escaped encrypted settings")
			}
			app.Close()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = testStore(t, deployment)
			app = NewApp(deployment, store, source)
			defer app.Close()
			if count, err := app.Notifications.Schedule(t.Context(), at.Add(time.Hour)); err != nil || count != 0 {
				t.Fatalf("channel change duplicated a processed slot: %d %v", count, err)
			}
			detail, err := app.Notifications.Detail(root.ID)
			if err != nil || detail.Notification.ID != root.ID {
				t.Fatalf("old notification disappeared: %v", err)
			}
			visible, err := app.Notifications.List(detail.Notification.Status)
			if err != nil || len(visible) == 0 {
				t.Fatalf("old notification missing from management list: %v", err)
			}
			if unknown {
				if detail.Notification.Status != "failed" || detail.Notification.Attempts != 1 {
					t.Fatalf("channel change altered unknown outcome: %+v", detail.Notification)
				}
				drain()
				if oldCalls.Load() != beforeCalls || newCalls.Load() != 0 {
					t.Fatal("unknown notification was automatically resent after changing channel")
				}
				fail.Store(false)
				if _, err := app.Notifications.Retry(t.Context(), root.ID); err != nil {
					t.Fatal(err)
				}
			}
			drain()
			detail, err = app.Notifications.Detail(root.ID)
			if err != nil || detail.Notification.Status != "sent" || newCalls.Load() != 0 {
				t.Fatalf("old notification recovery changed destination: %+v %v, new calls=%d",
					detail.Notification, err, newCalls.Load())
			}
			if mode == "sent" && oldCalls.Load() != beforeCalls || mode != "sent" && oldCalls.Load() <= beforeCalls {
				t.Fatal("old channel lost its send state or remaining parts")
			}
			if count, err := app.Notifications.Schedule(t.Context(), at.AddDate(0, 0, 1)); err != nil || count != 1 {
				t.Fatalf("new day did not use the new channel: %d %v", count, err)
			}
			drain()
			if newCalls.Load() == 0 {
				t.Fatal("new notifications were sent to the old channel")
			}
			owner := store.Settings.Memos.User
			store.Settings.Memos.User = "users/other"
			if _, err := app.Notifications.Detail(root.ID); errorCode(err) != "NOTIFICATION_NOT_FOUND" {
				t.Fatalf("notification owner boundary was lost: %v", err)
			}
			store.Settings.Memos.User = owner
		})
	}
}

func TestBoundFeishuReviewCompletesAfterDestinationChange(t *testing.T) {
	reviews, _ := reviewFixture(t, 4)
	store := reviews.Store
	config := feishuSettings(t)
	store.Settings.Web.StateEncryptionKey = config.Web.StateEncryptionKey
	store.Settings.Channel = config.Channel
	store.Settings.Channel.FeishuMode, store.Settings.Channel.UserOpenID = "websocket", nil
	store.Settings.Notifications.Enabled = true
	if err := store.initializeProductSettings(); err != nil {
		t.Fatal(err)
	}
	startup := store.Settings
	app := NewApp(startup, store, reviews.Source)
	defer app.Close()
	if err := transaction(store.state, func(tx *sql.Tx) error {
		_, err := store.bindBotPeer(tx, "feishu-app", configured(config.Channel.AppID), "ou_bound", time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	platform := &reviewDeliveryPlatform{}
	app.Feishu.Client = platform
	built, closed := 0, 0
	app.Notifications.channelTransport = func(channel ChannelConfig) (NotificationTransport, func()) {
		built++
		transport, release := app.notificationTransport(channel)
		return transport, func() { closed++; release() }
	}
	at := time.Now().Add(48 * time.Hour)
	location, _ := time.LoadLocation(store.Settings.Timezone)
	day := at.In(location)
	at = time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 0, 0, location)
	if n, err := app.Notifications.Schedule(t.Context(), at); err != nil || n != 1 {
		t.Fatalf("schedule bound Feishu review: %d %v", n, err)
	}
	for range 2 {
		deliverFeishuReply(t, app.Feishu)
	}
	if built != 1 || closed != 0 || platform.recipient != "ou_bound" || len(platform.texts) != 2 {
		t.Fatalf("bound Feishu transport built=%d closed=%d recipient=%s sent=%d", built, closed, platform.recipient, len(platform.texts))
	}
	switchTo := settingsChange(store.productVersion, `{"channel":{"kind":"feishu-webhook"}}`)
	switchTo.Secrets = map[string]SecretUpdate{"webhook_url": {Action: "replace", Value: new("https://example.invalid/unexpected-destination")}}
	if _, err := app.UpdateSettings(t.Context(), switchTo); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("released %d Feishu transports on settings change", closed)
	}
	app.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = testStore(t, startup)
	app = NewApp(startup, store, reviews.Source)
	defer app.Close()
	app.Feishu.Client = platform
	for range 10 {
		worked, err := app.Notifications.Deliver(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	pending, err := app.Notifications.List("pending")
	if err != nil || len(pending) != 0 || len(platform.texts) < 3 || platform.recipient != "ou_bound" {
		t.Fatalf("queued Feishu review did not finish at the bound destination: pending=%d sent=%d err=%v", len(pending), len(platform.texts), err)
	}
}

package moerise

import (
	"bytes"
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
	for _, mode := range []string{"sent", "pending", "unknown_sent", "unknown_not_sent", "unknown_without_slot"} {
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
			if mode == "unknown_without_slot" {
				if _, err := store.state.Exec("DELETE FROM review_schedule_slots"); err != nil {
					t.Fatal(err)
				}
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
				if detail.Notification.Status != "unknown" || detail.Notification.Attempts != 1 {
					t.Fatalf("channel change altered unknown outcome: %+v", detail.Notification)
				}
				drain()
				if oldCalls.Load() != beforeCalls || newCalls.Load() != 0 {
					t.Fatal("unknown notification was automatically resent after changing channel")
				}
				decision := "not_sent"
				if mode == "unknown_sent" {
					decision = "sent"
				}
				fail.Store(false)
				resolved, err := app.Notifications.Resolve(t.Context(), root.ID, decision, "", "users/owner")
				if err != nil || len(resolved.Resolutions) != 1 {
					t.Fatalf("old notification cannot be resolved: %v", err)
				}
				if decision == "not_sent" && resolved.Notification.PlatformUUID == root.PlatformUUID {
					t.Fatal("confirmed retry reused the original send UUID")
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

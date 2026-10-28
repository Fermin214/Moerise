//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	moerise "moerise/internal"
)

// This opt-in check sends synthetic notes to the configured real Feishu targets.
func TestFeishuLiveReviewAcceptance(t *testing.T) {
	path := os.Getenv("MOERISE_FEISHU_LIVE_CONFIG")
	if path == "" {
		t.Skip("MOERISE_FEISHU_LIVE_CONFIG is not set")
	}
	live, err := moerise.LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	ctx := context.Background()
	settings := h.settings
	settings.StateDir, settings.IndexDir = filepath.Join(h.root, "live-state"), filepath.Join(h.root, "live-index")
	settings.Channel = live.Channel
	settings.Review.Count, settings.Review.MinAgeHours = 1, 0
	settings.Notifications = moerise.NotificationConfig{Enabled: true, Times: []string{"00:00"}}
	source := moerise.NewMemosClient(settings.Memos)
	defer source.Close()
	store, err := moerise.NewStore(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	body := "Moerise 纯文字全文验证\n" + strings.Repeat("全文🙂 <>&\"\\\n", 450) + "全文结束。"
	note, _, err := moerise.NewRecordService(store, source).Create(ctx, "feishu-live-review", moerise.CreateNote{
		Content: body})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := moerise.NewSyncService(store, source).Sync(ctx, moerise.Job{Mode: "full"}); err != nil {
		t.Fatal(err)
	}
	reviews := moerise.NewReviewService(store, source)
	batch, err := reviews.Daily(ctx, time.Now())
	if err != nil || len(batch.Items) != 1 || batch.Items[0].Name != note.Name {
		t.Fatalf("synthetic daily review: %v", err)
	}
	channelKinds := []string{"feishu-app"}
	var webhook struct {
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}
	if path := os.Getenv("MOERISE_FEISHU_LIVE_WEBHOOK"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &webhook) != nil || webhook.URL == "" {
			t.Fatal("cannot read the Webhook configuration")
		}
		channelKinds = append(channelKinds, "feishu-webhook")
	}
	for _, kind := range channelKinds {
		t.Run(kind, func(t *testing.T) {
			settings.Channel = live.Channel
			settings.Channel.Kind = kind
			if kind == "feishu-webhook" {
				settings.Channel.WebhookURL, settings.Channel.WebhookSecret = &webhook.URL, &webhook.Secret
				settings.Channel.AppID, settings.Channel.AppSecret = nil, nil
				settings.Channel.VerificationToken, settings.Channel.EncryptKey = nil, nil
				settings.Channel.UserOpenID, settings.Channel.Destination = nil, nil
			}
			if err := settings.Validate(); err != nil {
				t.Fatal(err)
			}
			store.Settings = settings
			makeQueue := func() *moerise.NotificationService {
				reviews = moerise.NewReviewService(store, source)
				events := moerise.NewFeishuService(store, source, moerise.NewRecordService(store, source), reviews, nil)
				return moerise.NewNotificationService(store, reviews, moerise.NewFeishuDelivery(store, reviews, events))
			}
			queue := makeQueue()
			if count, err := queue.Schedule(ctx, time.Now()); err != nil || count != 1 {
				t.Fatalf("%s schedule: count=%d err=%v", kind, count, err)
			}
			if progress, err := queue.Deliver(ctx); err != nil || !progress {
				t.Fatalf("%s first fragment: %v", kind, err)
			}
			first, err := queue.List("sent")
			if err != nil || len(first) != 1 {
				t.Fatalf("%s first send was not confirmed: %v", kind, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = moerise.NewStore(settings)
			if err != nil {
				t.Fatal(err)
			}
			queue = makeQueue()
			if count, err := queue.Schedule(ctx, time.Now()); err != nil || count != 0 {
				t.Fatalf("%s restart duplicated its slot: count=%d err=%v", kind, count, err)
			}
			deadline := time.Now().Add(90 * time.Second)
			for time.Now().Before(deadline) {
				progress, err := queue.Deliver(ctx)
				if err != nil {
					t.Fatal(err)
				}
				unknown, _ := queue.List("unknown")
				if len(unknown) != 0 {
					t.Fatalf("%s platform outcome requires confirmation; notification=%s", kind, unknown[0].ID)
				}
				pending, _ := queue.List("pending")
				if len(pending) == 0 {
					break
				}
				if !progress {
					time.Sleep(500 * time.Millisecond)
				}
			}
			pending, _ := queue.List("pending")
			sent, err := queue.List("sent")
			if err != nil || len(pending) != 0 || len(sent) != 2 {
				t.Fatalf("%s incomplete review: pending=%d sent=%d err=%v", kind, len(pending), len(sent), err)
			}
			if kind == "feishu-app" {
				client := moerise.NewFeishuClient(settings.Channel)
				byPosition := make(map[int]string)
				for _, item := range sent {
					if item.Receipt == nil {
						t.Fatal("application send has no platform receipt")
					}
					message, err := client.GetMessage(ctx, *item.Receipt)
					if err != nil {
						t.Fatal(err)
					}
					_, text, ok := strings.Cut(message.Text, "\n\n")
					if !ok {
						t.Fatal("platform text has no full review body")
					}
					lines := strings.SplitN(message.Text, "\n", 4)
					var position int
					if len(lines) < 3 {
						t.Fatal("platform text has no fragment position")
					}
					if _, err := fmt.Sscanf(lines[2], "第 %d 片", &position); err != nil {
						t.Fatal(err)
					}
					byPosition[position] = text
				}
				var joined strings.Builder
				for position := 1; joined.Len() < len(body); position++ {
					text := byPosition[position]
					if text == "" {
						t.Fatal("platform text is missing or out of order")
					}
					joined.WriteString(text)
				}
				if joined.String() != body {
					t.Fatal("platform full content differs from the source note")
				}
			}
			t.Logf("PASS %s: %d confirmed fragments, restart resumes without duplicate slot", kind, len(sent))
		})
	}
}

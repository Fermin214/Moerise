//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
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
	body := "今天整理了一点想法：先把事情记下来，再留些时间回看。\n文字保持原样，图片直接呈现。🙂\n#回顾"
	picture := image.NewRGBA(image.Rect(0, 0, 320, 180))
	draw.Draw(picture, picture.Bounds(), image.NewUniform(color.RGBA{R: 40, G: 130, B: 170, A: 255}), image.Point{}, draw.Src)
	var pictureData bytes.Buffer
	if err := png.Encode(&pictureData, picture); err != nil {
		t.Fatal(err)
	}
	note, _, err := moerise.NewRecordService(store, source).Create(ctx, "feishu-live-review", moerise.CreateNote{
		Content: body, Attachments: []moerise.CreateAttachment{
			{Filename: "first.png", Type: "image/png", Content: pictureData.Bytes()},
			{Filename: "second.png", Type: "image/png", Content: pictureData.Bytes()},
			{Filename: "file.txt", Type: "text/plain", Content: []byte("ordinary attachment")},
		}})
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
			if err != nil || len(pending) != 0 || len(sent) != 5 {
				t.Fatalf("%s incomplete review: pending=%d sent=%d err=%v", kind, len(pending), len(sent), err)
			}
			if kind == "feishu-app" {
				client := moerise.NewFeishuClient(settings.Channel)
				// List returns newest first; the first fragment precedes the resumed fragments.
				ordered := first
				for i := len(sent) - 1; i >= 0; i-- {
					if sent[i].ID != first[0].ID {
						ordered = append(ordered, sent[i])
					}
				}
				var joined strings.Builder
				images, titles, footers := 0, 0, 0
				created, _ := time.Parse(time.RFC3339Nano, note.CreateTime)
				location, _ := time.LoadLocation(settings.Timezone)
				footer := "#回顾  创建于" + created.In(location).Format(time.DateOnly)
				for i, item := range ordered {
					if item.Receipt == nil {
						t.Fatal("application send has no platform receipt")
					}
					message, err := client.GetMessage(ctx, *item.Receipt)
					if err != nil {
						t.Fatal(err)
					}
					if message.ImageKey != "" {
						file, err := client.Download(ctx, message.ID, message.ImageKey, "image", "")
						if err != nil {
							t.Fatal(err)
						}
						decoded, _, err := image.Decode(bytes.NewReader(file.Content))
						if err != nil || decoded.Bounds() != picture.Bounds() {
							t.Fatalf("platform image cannot be read at original dimensions: %v", err)
						}
						images++
					} else if message.Text == "「今日回顾」" {
						titles++
						if i != 0 {
							t.Fatal("title was not the first standalone message")
						}
					} else if message.Text == footer {
						footers++
						if i != len(ordered)-1 {
							t.Fatal("note metadata was not the last message")
						}
					} else {
						joined.WriteString(message.Text)
					}
				}
				if joined.String() != body {
					t.Fatal("platform full content differs from the source note")
				}
				if images != 2 || titles != 1 || footers != 1 {
					t.Fatalf("platform layout: images=%d titles=%d footers=%d", images, titles, footers)
				}
			}
			t.Logf("PASS %s: one standalone title, original body, two images and a tags/date footer; restart resumes without duplicate slot", kind)
		})
	}
}

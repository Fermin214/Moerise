//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
	"time"

	moerise "moerise/internal"
)

type fullReviewPlatform struct {
	feishuPlatform
	texts      []string
	sendCounts map[string]int
	loseAt     int
}

func (p *fullReviewPlatform) SendText(ctx context.Context, recipient, uuid, text string) (string, error) {
	p.texts = append(p.texts, text)
	p.sendCounts[uuid]++
	if len(p.texts) == p.loseAt {
		p.dropNextSend = true
	}
	return p.feishuPlatform.SendText(ctx, recipient, uuid, text)
}

func TestFeishuFullReviewDeliveryWithRealMemosAcceptance(t *testing.T) {
	h := newHarness(t)
	settings := h.settings
	settings.StateDir, settings.IndexDir = filepath.Join(h.root, "full-review-state"), filepath.Join(h.root, "full-review-index")
	settings.Review.Count, settings.Review.MinAgeHours = 1, 0
	settings.Notifications.Enabled, settings.Notifications.Time = true, "00:00"
	pointer := func(value string) *string { return &value }
	settings.Channel.AppID, settings.Channel.AppSecret = pointer("cli_acceptance"), pointer(strings.Repeat("s", 32))
	settings.Channel.VerificationToken, settings.Channel.UserOpenID = pointer(strings.Repeat("v", 32)), pointer("ou_owner")
	source := moerise.NewMemosClient(settings.Memos)
	defer source.Close()
	store, err := moerise.NewStore(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	platform := &fullReviewPlatform{feishuPlatform: feishuPlatform{
		replies: map[string]string{}, uuids: map[string]string{}}, sendCounts: map[string]int{}, loseAt: 2}
	// Use the same queue and transport as production, with only Feishu replaced.
	makeQueue := func() (*moerise.NotificationService, *moerise.ReviewService) {
		reviews := moerise.NewReviewService(store, source)
		events := moerise.NewFeishuService(store, source, nil, reviews, nil)
		events.Client = platform
		return moerise.NewNotificationService(store, reviews, moerise.NewFeishuDelivery(store, reviews, events)), reviews
	}
	queue, reviews := makeQueue()
	ctx := context.Background()
	content := "正文🙂\n" + strings.Repeat("<>&\n", 1900) + "长文最终一行"
	picture := image.NewRGBA(image.Rect(0, 0, 320, 180))
	draw.Draw(picture, picture.Bounds(), image.NewUniform(color.RGBA{R: 40, G: 130, B: 170, A: 255}), image.Point{}, draw.Src)
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, picture); err != nil {
		t.Fatal(err)
	}
	_, _, err = moerise.NewRecordService(store, source).Create(ctx, "full-review-text", moerise.CreateNote{
		Content: content, Attachments: []moerise.CreateAttachment{
			{Filename: "first.png", Type: "image/png", Content: pngBytes.Bytes()},
			{Filename: "second.png", Type: "image/png", Content: pngBytes.Bytes()},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue("full", true); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim()
	if err != nil || job == nil {
		t.Fatalf("claim initial synchronization: %v", err)
	}
	synced, err := moerise.NewSyncService(store, source).Sync(ctx, *job)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(job.ID, synced); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	batch, err := reviews.Daily(ctx, at)
	if err != nil || len(batch.Items) != 1 {
		t.Fatalf("daily batch: %+v %v", batch, err)
	}
	if count, err := queue.Schedule(ctx, at); err != nil || count != 1 {
		t.Fatalf("schedule: %d %v", count, err)
	}
	for range 2 {
		if sent, err := queue.Deliver(ctx); err != nil || !sent {
			t.Fatalf("initial delivery: %v", err)
		}
	}
	unknown, err := queue.List("unknown")
	if err != nil || len(unknown) != 1 {
		t.Fatalf("missing isolated unknown fragment: %+v %v", unknown, err)
	}
	receipt := "push_" + unknown[0].PlatformUUID
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = moerise.NewStore(settings)
	if err != nil {
		t.Fatal(err)
	}
	queue, _ = makeQueue()
	if sent, err := queue.Deliver(ctx); err != nil || sent {
		t.Fatalf("restart sent a fragment behind an unknown predecessor: %v", err)
	}
	if _, err := queue.Resolve(ctx, unknown[0].ID, "sent", receipt, "acceptance"); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		sent, err := queue.Deliver(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !sent {
			break
		}
	}
	var joined strings.Builder
	for _, text := range platform.texts {
		_, body, found := strings.Cut(text, "\n\n")
		if !found || strings.Contains(text, "/file/") || strings.Contains(text, "first.png") {
			t.Fatal("missing full review header")
		}
		joined.WriteString(body)
	}
	if len(platform.texts) < 3 || joined.String() != content {
		t.Fatal("full Memos body was truncated, repeated or reordered")
	}
	for _, count := range platform.sendCounts {
		if count != 1 {
			t.Fatal("restart repeated a confirmed platform UUID")
		}
	}
	pending, _ := queue.List("pending")
	unknown, _ = queue.List("unknown")
	if len(pending) != 0 || len(unknown) != 0 {
		t.Fatalf("unfinished delivery: pending=%d unknown=%d", len(pending), len(unknown))
	}
	t.Logf("PASS full Memos note, %d text fragments, attachments omitted, response-loss confirmation and reopened-state continuation", len(platform.texts))
}

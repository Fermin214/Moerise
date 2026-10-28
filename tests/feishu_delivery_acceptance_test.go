//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"fmt"
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
	uploads    [][]byte
	images     map[string]string
	loseImage  bool
}

func (p *fullReviewPlatform) UploadImage(_ context.Context, image moerise.CreateAttachment) (string, error) {
	p.uploads = append(p.uploads, bytes.Clone(image.Content))
	return fmt.Sprintf("img_%d", len(p.uploads)), nil
}

func (p *fullReviewPlatform) SendImage(ctx context.Context, recipient, uuid, key string) (string, error) {
	if p.images == nil {
		p.images = map[string]string{}
	}
	p.images[uuid] = key
	p.sendCounts[uuid]++
	if p.loseImage {
		p.loseImage, p.dropNextSend = false, true
	}
	return p.feishuPlatform.SendText(ctx, recipient, uuid, "")
}

func (p *fullReviewPlatform) GetMessage(ctx context.Context, receipt string) (moerise.FeishuSentMessage, error) {
	message, err := p.feishuPlatform.GetMessage(ctx, receipt)
	message.ImageKey = p.images[strings.TrimPrefix(receipt, "push_")]
	return message, err
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
	settings.Review.Count, settings.Review.MinAgeHours = 2, 0
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
		replies: map[string]string{}, uuids: map[string]string{}}, sendCounts: map[string]int{}, loseImage: true}
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
	note, _, err := moerise.NewRecordService(store, source).Create(ctx, "full-review-text", moerise.CreateNote{
		Content: content, Attachments: []moerise.CreateAttachment{
			{Filename: "first.png", Type: "image/png", Content: pngBytes.Bytes()},
			{Filename: "second.png", Type: "image/png", Content: pngBytes.Bytes()},
			{Filename: "file.txt", Type: "text/plain", Content: []byte("not a review image")},
		}})
	if err != nil {
		t.Fatal(err)
	}
	expectedImage, err := source.DownloadAttachment(ctx, note.Attachments[0])
	if err != nil {
		t.Fatal(err)
	}
	imageOnly, _, err := moerise.NewRecordService(store, source).Create(ctx, "full-review-image", moerise.CreateNote{
		Content: "image placeholder", Attachments: []moerise.CreateAttachment{
			{Filename: "only.png", Type: "image/png", Content: pngBytes.Bytes()},
		}})
	if err != nil {
		t.Fatal(err)
	}
	h.call(h.memos, "/api/v1/"+imageOnly.Name+"?updateMask=content", "PATCH", h.upstream,
		map[string]string{"content": ""}, 200)
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
	if err != nil || len(batch.Items) != 2 {
		t.Fatalf("daily batch: %+v %v", batch, err)
	}
	if count, err := queue.Schedule(ctx, at); err != nil || count != 1 {
		t.Fatalf("schedule: %d %v", count, err)
	}
	for range 10 {
		if sent, err := queue.Deliver(ctx); err != nil || !sent {
			t.Fatalf("initial delivery: %v", err)
		}
		unknown, _ := queue.List("unknown")
		if len(unknown) != 0 {
			break
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
	titles, footers := 0, 0
	for _, text := range platform.texts {
		if strings.Contains(text, "/file/") || strings.Contains(text, "first.png") {
			t.Fatal("review emitted an attachment or attachment link")
		}
		switch {
		case text == "「今日回顾」":
			titles++
		case strings.HasPrefix(text, "创建于"):
			footers++
		default:
			joined.WriteString(text)
		}
	}
	if len(platform.texts) < 6 || joined.String() != content || titles != 1 || footers != 2 {
		t.Fatal("full Memos body was truncated, repeated or reordered")
	}
	if len(platform.images) != 3 || len(platform.uploads) != 3 {
		t.Fatalf("missing or repeated image upload: sent=%d uploads=%d", len(platform.images), len(platform.uploads))
	}
	for _, data := range platform.uploads {
		if !bytes.Equal(data, expectedImage.Content) {
			t.Fatal("uploaded bytes differ from the original Memos image")
		}
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
	t.Logf("PASS one title, full Memos body, two creation-date footers, three original images including an image-only note, image response-loss confirmation and reopened-state continuation")
}

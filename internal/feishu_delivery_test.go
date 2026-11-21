package moerise

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFeishuWebhookAcknowledgementAndSignature(t *testing.T) {
	for _, test := range []struct {
		name, response, code string
		status               int
	}{
		{"success", `{"code":0}`, "", 200},
		{"status-code-success", `{"StatusCode":0}`, "", 200},
		{"rejected", `{"code":19021}`, "CHANNEL_REJECTED", 200},
		{"bad-response", `{"message":"ok"}`, "SEND_OUTCOME_UNKNOWN", 200},
		{"interrupted-response", `{"code":`, "SEND_OUTCOME_UNKNOWN", 200},
		{"server-error", `{"code":0}`, "SEND_OUTCOME_UNKNOWN", 503},
		{"http-rate-limited", `{}`, "CHANNEL_RATE_LIMITED", 429},
		{"redirect", `{}`, "CHANNEL_REJECTED", 307},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct {
					Timestamp string `json:"timestamp"`
					Sign      string `json:"sign"`
					Type      string `json:"msg_type"`
					Content   struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid Webhook payload")
					return
				}
				mac := hmac.New(sha256.New, []byte(body.Timestamp+"\nwebhook-secret"))
				if body.Timestamp == "" || body.Sign != base64.StdEncoding.EncodeToString(mac.Sum(nil)) ||
					body.Type != "text" || body.Content.Text != "private review" {
					t.Error("Webhook content or signature did not match")
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()
			settings := testSettings(t)
			settings.Channel.Kind = "feishu-webhook"
			settings.Channel.WebhookURL, settings.Channel.WebhookSecret = stringPointer(server.URL), stringPointer("webhook-secret")
			store := testStore(t, settings)
			delivery := newFeishuDelivery(store.Settings.Channel, store, nil, nil).(*FeishuDelivery)
			receipt, err := delivery.webhook(context.Background(), "notification-id", "private review")
			if err != nil && errorCode(err) != test.code || err == nil && test.code != "" {
				t.Fatalf("Webhook outcome: receipt=%s err=%v", receipt, err)
			}
			if test.code == "" && receipt != "webhook:notification-id" || calls != 1 {
				t.Fatal("Webhook receipt or redirect handling failed")
			}

		})
	}
}

type deliveryPlatform struct {
	unknownFeishuReply
	text      string
	recipient string
	message   FeishuSentMessage
}

func (p *deliveryPlatform) SendText(_ context.Context, recipient, _, text string) (string, error) {
	p.text, p.recipient = text, recipient
	return "om_push", nil
}

func (p *deliveryPlatform) GetMessage(context.Context, string) (FeishuSentMessage, error) {
	return p.message, nil
}

func TestFeishuPushReadsLocalProjection(t *testing.T) {
	reviews, memos := reviewFixture(t, 6)
	store := reviews.Store
	store.Settings.Channel = feishuSettings(t).Channel
	store.Settings.Notifications.Enabled = true
	batch, err := reviews.DailyLocal(t.Context(), time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	edited, deleted := batch.Items[0].Name, batch.Items[1].Name
	memos[edited]["content"] = "fresh local content"
	delete(memos, deleted)
	refreshReviewFixture(t, store, memos)
	reviews.Source = fakeClient(store.Settings, func(*http.Request) (int, any, error) { t.Fatal("delivery contacted Memos"); return 500, nil, nil })
	platform := &reviewDeliveryPlatform{}
	events := NewFeishuService(store, reviews.Source, nil, nil)
	events.Client = platform
	queue := NewNotificationService(store, reviews, newFeishuDelivery(store.Settings.Channel, store, reviews, events))
	if _, err := queue.enqueue("review", "local", batch.Date, &batch.ID, nil); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		worked, err := queue.Deliver(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	if len(platform.texts) < 2 || !strings.Contains(strings.Join(platform.texts, ""), "fresh local content") || platform.recipient != "ou_owner" {
		t.Fatalf("local delivery: %+v", platform.texts)
	}
}

type reviewDeliveryPlatform struct {
	deliveryPlatform
	texts, uuids []string
	fail         string
	messages     map[string]FeishuSentMessage
}

func (p *reviewDeliveryPlatform) SendText(_ context.Context, recipient, uuid, text string) (string, error) {
	p.text, p.recipient = text, recipient
	p.texts, p.uuids = append(p.texts, text), append(p.uuids, uuid)
	if p.messages == nil {
		p.messages = map[string]FeishuSentMessage{}
	}
	p.messages["om_"+uuid] = FeishuSentMessage{ID: "om_" + uuid, Text: text,
		SenderType: "app", SenderIDType: "app_id", SenderID: "cli_test_app",
		ChatID: "oc_owner", CreateTime: time.Now()}
	if p.fail != "" {
		code := p.fail
		p.fail = ""
		return "", upstreamError(code, "simulated platform failure")
	}
	return "om_" + uuid, nil
}

func (p *reviewDeliveryPlatform) GetMessage(ctx context.Context, id string) (FeishuSentMessage, error) {
	if message, ok := p.messages[id]; ok {
		return message, nil
	}
	return p.deliveryPlatform.GetMessage(ctx, id)
}

func TestFeishuReviewFullTextQueueResumeAndUnknown(t *testing.T) {
	reviews, memos := reviewFixture(t, 1)
	store := reviews.Store
	store.Settings.Channel = feishuSettings(t).Channel
	store.Settings.Notifications.Enabled = true
	store.Settings.Timezone = "America/Los_Angeles"
	store.Settings.Review.RecentDays = 0
	content := strings.Repeat("长文🙂 <&> \"原文\"\\\n", 4000) + "最终正文\nhttps://example.com/user-link"
	for _, raw := range memos {
		raw["content"] = content
		raw["createTime"] = "2020-01-01T00:30:00Z"
		raw["updateTime"] = "2020-01-01T00:30:00Z"
	}
	refreshReviewFixture(t, store, memos)
	platform := &reviewDeliveryPlatform{}
	events := NewFeishuService(store, reviews.Source, nil, nil)
	events.Client = platform
	transport := newFeishuDelivery(store.Settings.Channel, store, reviews, events)
	queue := NewNotificationService(store, reviews, transport)
	ctx := context.Background()
	batch, err := reviews.Daily(ctx, time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	id, err := queue.enqueue("review", "full-text", batch.Date, &batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sent, err := queue.Deliver(ctx); err != nil || !sent || len(platform.texts) != 1 || platform.texts[0] != "「今日回顾」" {
		t.Fatalf("standalone header: %+v %v", platform.texts, err)
	}
	platform.fail = "SEND_OUTCOME_UNKNOWN"
	if sent, err := queue.Deliver(ctx); err != nil || !sent {
		t.Fatalf("unknown part: %v", err)
	}
	unknown, _ := queue.List("failed")
	if len(unknown) != 1 || len(platform.texts) != 2 {
		t.Fatalf("unknown part was not isolated: %+v", unknown)
	}
	// A new queue/transport models a worker restart; durable state owns progress.
	queue = NewNotificationService(store, reviews, newFeishuDelivery(store.Settings.Channel, store, reviews, events))
	if err := queue.recover(); err != nil {
		t.Fatal(err)
	}
	if sent, err := queue.Deliver(ctx); err != nil || sent || len(platform.texts) != 2 {
		t.Fatalf("unknown predecessor released later parts: %v", err)
	}
	held, err := queue.List("pending")
	if err != nil || len(held) == 0 {
		t.Fatalf("remaining fragments not marked interrupted: %+v %v", held, err)
	}
	later := &reviewDeliveryPlatform{}
	events.Client = later
	if count, err := queue.Schedule(ctx, time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC)); err != nil || count != 1 {
		t.Fatalf("new slot after interrupted delivery: %d %v", count, err)
	}
	for range 100 {
		worked, err := queue.Deliver(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	if len(later.texts) < 3 || later.texts[0] != "「今日回顾」" || strings.Join(later.texts[1:len(later.texts)-1], "") != content {
		t.Fatal("interrupted slot blocked the next complete review")
	}
	events.Client = platform
	if _, err := queue.Retry(ctx, unknown[0].ID); err != nil {
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
	for _, text := range platform.texts {
		if !utf8.ValidString(text) || !reviewTextFits(text) {
			t.Fatal("invalid UTF-8 or oversized request")
		}
	}
	if len(platform.texts) < 4 || platform.texts[0] != "「今日回顾」" ||
		platform.texts[len(platform.texts)-1] != "#topic-0/child  创建于2019-12-31" {
		t.Fatal("missing standalone header or source creation date in configured timezone")
	}
	if len(platform.texts) < 4 || platform.texts[1] != platform.texts[2] ||
		strings.Join(platform.texts[2:len(platform.texts)-1], "") != content {
		t.Fatal("long note was truncated, repeated or reordered")
	}
	if platform.uuids[1] != platform.uuids[2] {
		t.Fatal("retry changed the platform idempotency key")
	}
	var root string
	if err := store.state.QueryRow(`SELECT root_id FROM delivery_logs WHERE id = ?`, id).Scan(&root); err != nil || root != id {
		t.Fatalf("missing durable source references: %v", err)
	}
	pending, _ := queue.List("pending")
	if len(pending) != 0 {
		t.Fatalf("unfinished parts: %d", len(pending))
	}
}

func TestFeishuReviewEmptyAndSourceOutage(t *testing.T) {
	for _, scenario := range []string{"deleted", "outage", "attachment-only"} {
		t.Run(scenario, func(t *testing.T) {
			reviews, memos := reviewFixture(t, 1)
			store := reviews.Store
			store.Settings.Channel = feishuSettings(t).Channel
			platform := &reviewDeliveryPlatform{}
			events := NewFeishuService(store, reviews.Source, nil, nil)
			events.Client = platform
			transport := newFeishuDelivery(store.Settings.Channel, store, reviews, events)
			queue := NewNotificationService(store, reviews, transport)
			ctx := context.Background()
			batch, err := reviews.Daily(ctx, time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			id, err := queue.enqueue("review", "empty", batch.Date, &batch.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "outage" {
				reviews.Source = fakeClient(store.Settings, func(*http.Request) (int, any, error) { return 503, nil, nil })
			} else if scenario == "attachment-only" {
				for _, raw := range memos {
					raw["content"] = ""
					raw["attachments"] = []any{map[string]any{
						"name": "attachments/image1", "filename": "first.png", "type": "image/png"}}
				}
			} else {
				clear(memos)
			}
			refreshReviewFixture(t, store, memos)
			detail, _ := queue.Detail(id)
			receipt, err := transport.Send(ctx, detail.Notification)
			if err != nil || scenario == "outage" && receipt == "empty:"+id || scenario != "outage" && receipt != "empty:"+id || scenario != "outage" && len(platform.texts) != 0 {
				t.Fatalf("empty/outage: receipt=%s err=%v calls=%d", receipt, err, len(platform.texts))
			}
		})
	}
}

func TestFeishuWebhookFullTextIgnoresAttachments(t *testing.T) {
	reviews, memos := reviewFixture(t, 1)
	store := reviews.Store
	store.Settings.Channel.Kind = "feishu-webhook"
	store.Settings.Notifications.Enabled = true
	store.Settings.Timezone = "Asia/Hong_Kong"
	content := strings.Repeat("Webhook 全文🙂 \"<&>\"\\\n", 2000)
	for _, raw := range memos {
		raw["content"] = content
		raw["tags"] = []string{}
		raw["createTime"] = "2019-12-31T18:30:00Z"
		raw["attachments"] = []any{
			map[string]any{"name": "attachments/image1", "filename": "first.png", "type": "image/png"},
			map[string]any{"name": "attachments/file1", "filename": "report.pdf", "type": "application/pdf"},
		}
	}
	var texts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if len(data) > 20*1024 || !utf8.Valid(data) {
			t.Error("oversized or invalid Webhook request")
		}
		var body struct {
			Type    string            `json:"msg_type"`
			Content map[string]string `json:"content"`
		}
		if json.Unmarshal(data, &body) != nil || body.Type != "text" {
			t.Error("Webhook must send only text")
		}
		texts = append(texts, body.Content["text"])
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer server.Close()
	store.Settings.Channel.WebhookURL = stringPointer(server.URL)
	store.Settings.Channel.WebhookSecret = stringPointer("webhook-secret")
	ctx := context.Background()
	batch, err := reviews.Daily(ctx, time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	queue := NewNotificationService(store, reviews, newFeishuDelivery(store.Settings.Channel, store, reviews, nil))
	if _, err := queue.enqueue("review", "webhook-text", batch.Date, &batch.ID, nil); err != nil {
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
	for _, text := range texts {
		if strings.Contains(text, "/file/") || strings.Contains(text, "report.pdf") {
			t.Fatal("Webhook emitted an attachment or attachment link")
		}
	}
	if len(texts) < 4 || texts[0] != "「今日回顾」" || texts[len(texts)-1] != "创建于2020-01-01" {
		t.Fatal("Webhook header or untagged creation-date footer did not match")
	}
	if strings.Join(texts[1:len(texts)-1], "") != content {
		t.Fatal("Webhook truncated or repeated the full note")
	}
}
func TestFeishuReviewSourceChangeCancelsOldPartsAndContinues(t *testing.T) {
	reviews, memos := reviewFixture(t, 3)
	store := reviews.Store
	store.Settings.Channel = feishuSettings(t).Channel
	store.Settings.Notifications.Enabled = true
	for name, raw := range memos {
		raw["content"] = name + "\n" + strings.Repeat("完整正文🙂\n", 3000)
	}
	refreshReviewFixture(t, store, memos)
	platform := &reviewDeliveryPlatform{}
	events := NewFeishuService(store, reviews.Source, nil, nil)
	events.Client = platform
	queue := NewNotificationService(store, reviews, newFeishuDelivery(store.Settings.Channel, store, reviews, events))
	ctx := context.Background()
	batch, err := reviews.Daily(ctx, time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC))
	if err != nil || len(batch.Items) != 3 {
		t.Fatalf("batch: %v", err)
	}
	if _, err := queue.enqueue("review", "source-change", batch.Date, &batch.ID, nil); err != nil {
		t.Fatal(err)
	}
	if sent, err := queue.Deliver(ctx); err != nil || !sent {
		t.Fatalf("header: %v", err)
	}
	if sent, err := queue.Deliver(ctx); err != nil || !sent {
		t.Fatalf("first body fragment: %v", err)
	}
	if len(platform.texts) != 2 || platform.texts[0] != "「今日回顾」" ||
		platform.texts[1] == "" || !strings.HasPrefix(batch.Items[0].Note.Content, platform.texts[1]) ||
		platform.texts[1] == batch.Items[0].Note.Content {
		t.Fatal("source change must follow the header and a partial body")
	}
	changed := batch.Items[0].Name
	memos[changed]["content"] = "修改后的新正文"
	refreshReviewFixture(t, store, memos)
	for range 100 {
		sent, err := queue.Deliver(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !sent {
			break
		}
	}
	remaining := platform.texts[2:]
	for _, entry := range batch.Items[1:] {
		footer := "#" + entry.Note.Tags[0] + "  创建于2020-01-01"
		end := slices.Index(remaining, footer)
		if end < 0 || strings.Join(remaining[:end], "") != entry.Note.Content {
			t.Fatal("source change sent stale text or a notice, or blocked another note's body and footer")
		}
		remaining = remaining[end+1:]
	}
	if len(remaining) != 0 {
		t.Fatalf("unexpected messages after the remaining notes: %+v", remaining)
	}
	pending, _ := queue.List("pending")
	if len(pending) != 0 {
		t.Fatalf("source change left %d pending parts", len(pending))
	}
}

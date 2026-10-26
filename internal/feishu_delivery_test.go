package moerise

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFeishuWebhookAcknowledgementAndSignature(t *testing.T) {
	for _, test := range []struct {
		name, response, code string
		status               int
	}{
		{"success", `{"code":0}`, "", 200},
		{"legacy-success", `{"StatusCode":0}`, "", 200},
		{"rejected", `{"code":19021}`, "CHANNEL_REJECTED", 200},
		{"bad-response", `{"message":"ok"}`, "SEND_OUTCOME_UNKNOWN", 200},
		{"interrupted-response", `{"code":`, "SEND_OUTCOME_UNKNOWN", 200},
		{"server-error", `{"code":0}`, "SEND_OUTCOME_UNKNOWN", 503},
		{"http-rejected", `{}`, "CHANNEL_REJECTED", 429},
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
			delivery := NewFeishuDelivery(store, nil, nil).(*FeishuDelivery)
			receipt, err := delivery.webhook(context.Background(), "notification-id", "private review")
			if err != nil && errorCode(err) != test.code || err == nil && test.code != "" {
				t.Fatalf("Webhook outcome: receipt=%s err=%v", receipt, err)
			}
			if test.code == "" && receipt != "webhook:notification-id" || calls != 1 {
				t.Fatal("Webhook receipt or redirect handling failed")
			}
			if _, err := delivery.Check(context.Background(), Notification{}, receipt); errorCode(err) != "CHANNEL_RECEIPT_UNSUPPORTED" {
				t.Fatal("Webhook ACK was treated as a queryable message receipt")
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

func TestFeishuPushRechecksSourcesAndReceiptIdentity(t *testing.T) {
	reviews, memos := reviewFixture(t, 6)
	store := reviews.Store
	store.Settings.Channel = feishuSettings(t).Channel
	ctx := context.Background()
	batch, err := reviews.Daily(ctx, time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	platform := &deliveryPlatform{}
	events := NewFeishuService(store, reviews.Source, NewRecordService(store, reviews.Source), reviews, nil)
	events.Client = platform
	delivery := NewFeishuDelivery(store, reviews, events)
	item := Notification{Kind: "review", Date: batch.Date, BatchID: &batch.ID}
	edited, deleted := batch.Items[0].Name, batch.Items[1].Name
	memos[edited]["content"] = "fresh source content"
	delete(memos, deleted)
	if _, err := delivery.Send(ctx, item); err != nil || !strings.Contains(platform.text, "fresh source content") ||
		strings.Contains(platform.text, deleted) || platform.recipient != "ou_owner" {
		t.Fatalf("push leaked stale content or used a different identity: %s %v", platform.text, err)
	}
	platform.message = FeishuSentMessage{ID: "om_push", Text: platform.text, SenderType: "app",
		SenderIDType: "app_id", SenderID: "cli_test_app"}
	if found, err := delivery.Check(ctx, item, "om_push"); err != nil || !found {
		t.Fatalf("matching platform receipt was rejected: %v", err)
	}
	platform.message.SenderID = "cli_foreign"
	if found, _ := delivery.Check(ctx, item, "om_push"); found {
		t.Fatal("a different application's receipt was accepted")
	}
	platform.message.SenderID, platform.message.Deleted = "cli_test_app", true
	if found, _ := delivery.Check(ctx, item, "om_push"); found {
		t.Fatal("a deleted platform message was accepted")
	}
	platform.text = ""
	reviews.Source = fakeClient(store.Settings, func(*http.Request) (int, any, error) { return 503, nil, nil })
	if _, err := delivery.Send(ctx, item); err == nil || platform.text != "" {
		t.Fatal("source outage sent cached private content")
	}
	reviews.Source = fakeClient(store.Settings, func(r *http.Request) (int, any, error) {
		if strings.HasSuffix(r.URL.Path, "/profile") {
			return 200, map[string]string{"version": "0.31.0"}, nil
		}
		return 200, map[string]any{"user": map[string]string{"name": "users/foreign"}}, nil
	})
	if _, err := delivery.Send(ctx, item); err == nil || platform.text != "" {
		t.Fatal("source identity change sent private content")
	}
}

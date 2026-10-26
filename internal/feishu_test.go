package moerise

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func stringPointer(value string) *string { return &value }

func feishuSettings(t *testing.T) Settings {
	t.Helper()
	settings := testSettings(t)
	settings.Channel.AppID = stringPointer("cli_test_app")
	settings.Channel.AppSecret = stringPointer(strings.Repeat("s", 32))
	settings.Channel.VerificationToken = stringPointer(strings.Repeat("v", 32))
	settings.Channel.UserOpenID = stringPointer("ou_owner")
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	return settings
}

func testFeishuEvent(settings Settings, id, sender, text string) []byte {
	body := map[string]any{
		"header": map[string]string{"event_id": "event_" + id, "event_type": "im.message.receive_v1",
			"app_id": configured(settings.Channel.AppID), "token": configured(settings.Channel.VerificationToken)},
		"event": map[string]any{
			"sender":  map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": sender}},
			"message": map[string]string{"message_id": id, "chat_type": "p2p", "message_type": "text", "content": string(canonicalJSON(map[string]string{"text": text}))},
		},
	}
	result, _ := json.Marshal(body)
	return result
}

func encryptedFeishuEvent(settings Settings, plain []byte) []byte {
	key := sha256.Sum256([]byte(configured(settings.Channel.EncryptKey)))
	block, _ := aes.NewCipher(key[:])
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(bytes.Clone(plain), bytes.Repeat([]byte{byte(padding)}, padding)...)
	data := make([]byte, aes.BlockSize+len(padded))
	copy(data, "0123456789abcdef")
	cipher.NewCBCEncrypter(block, data[:aes.BlockSize]).CryptBlocks(data[aes.BlockSize:], padded)
	body, _ := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(data)})
	return body
}

func TestFeishuCallbackAuthenticationAndEncryptedQueue(t *testing.T) {
	settings := feishuSettings(t)
	settings.Channel.EncryptKey = stringPointer("test-encryption-key")
	store := testStore(t, settings)
	client := fakeClient(settings, func(*http.Request) (int, any, error) {
		t.Error("callback must not contact Memos before acknowledging its durable queue")
		return 500, nil, nil
	})
	reviews := NewReviewService(store, client)
	service := NewFeishuService(store, client, NewRecordService(store, client), reviews, NewNotificationService(store, reviews, nil))
	call := func(body []byte, sign bool) error {
		t.Helper()
		request := httptest.NewRequest("POST", "/hooks/feishu", bytes.NewReader(body))
		if sign {
			request.Header.Set("X-Lark-Request-Timestamp", "1792972800")
			request.Header.Set("X-Lark-Request-Nonce", "nonce")
			request.Header.Set("X-Lark-Signature", hashBytes(append([]byte("1792972800nonce"+configured(settings.Channel.EncryptKey)), body...)))
		}
		_, _, err := service.Receive(request)
		return err
	}
	plain := testFeishuEvent(settings, "om_private", "ou_owner", "记录 private incoming text")
	encrypted := encryptedFeishuEvent(settings, plain)
	if errorCode(call(plain, false)) != "UNAUTHENTICATED" || errorCode(call(encrypted, false)) != "UNAUTHENTICATED" {
		t.Fatal("unsigned or unencrypted events bypassed verification")
	}
	if err := call(encrypted, true); err != nil {
		t.Fatal(err)
	}
	if err := call(encrypted, true); err != nil {
		t.Fatal("duplicate event was not acknowledged")
	}
	foreign := encryptedFeishuEvent(settings, testFeishuEvent(settings, "om_foreign", "ou_other", "查看 memos/private"))
	if err := call(foreign, true); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.state.QueryRow("SELECT COUNT(*) FROM channel_events").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate or foreign event entered the queue")
	}
	var sealed []byte
	if err := store.state.QueryRow("SELECT payload FROM channel_events").Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("private incoming text")) {
		t.Fatal("incoming private content was stored unencrypted")
	}
	data, err := service.unseal("om_private", sealed)
	if err != nil || !bytes.Contains(data, []byte("private incoming text")) {
		t.Fatal("queued event could not be recovered")
	}
	if _, err := service.unseal("om_other", sealed); err == nil {
		t.Fatal("encrypted queue payload was not bound to its message")
	}
	challenge, _ := json.Marshal(map[string]string{"type": "url_verification", "token": configured(settings.Channel.VerificationToken), "challenge": "setup"})
	if err := call(encryptedFeishuEvent(settings, challenge), false); err != nil {
		t.Fatalf("encrypted URL verification requires its token, not a message signature: %v", err)
	}
}

func TestFeishuClientTokenResourcesAndReplyUncertainty(t *testing.T) {
	settings := feishuSettings(t)
	tokenCalls, replies := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v3/tenant_access_token/internal":
			tokenCalls++
			writeJSON(w, 200, map[string]any{"code": 0, "tenant_access_token": "tenant-token", "expire": 7200})
		case "/im/v1/messages/om_file/resources/file_key":
			if r.Header.Get("Authorization") != "Bearer tenant-token" || r.URL.Query().Get("type") != "file" {
				t.Error("invalid authenticated Feishu resource request")
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "file bytes")
		case "/im/v1/messages/om_file/reply":
			replies++
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["uuid"] != "stable-uuid" || body["msg_type"] != "text" || body["content"] != `{"text":"saved"}` {
				t.Error("reply lost its stable deduplication UUID")
			}
			if replies == 1 {
				writeJSON(w, 200, map[string]any{"code": 0, "data": map[string]string{"message_id": "om_receipt"}})
			} else {
				_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
			}
		case "/im/v1/messages":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer tenant-token" ||
				r.URL.Query().Get("receive_id_type") != "open_id" || body["receive_id"] != "ou_owner" ||
				body["uuid"] != "push-uuid" || body["msg_type"] != "text" || body["content"] != `{"text":"daily review"}` {
				t.Error("proactive message lost its destination, credentials or UUID")
			}
			writeJSON(w, 200, map[string]any{"code": 0, "data": map[string]string{"message_id": "om_push"}})
		case "/im/v1/messages/om_push":
			if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer tenant-token" {
				t.Error("receipt lookup was not authenticated")
			}
			writeJSON(w, 200, map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{
				"message_id": "om_push", "sender": map[string]string{"id": "cli_test_app", "id_type": "app_id", "sender_type": "app"},
				"body": map[string]string{"content": `{"text":"daily review"}`},
			}}}})
		default:
			t.Errorf("unexpected Feishu request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewFeishuClient(settings.Channel)
	client.baseURL = server.URL
	file, err := client.Download(context.Background(), "om_file", "file_key", "file", "notes.txt")
	if err != nil || file.Type != "text/plain" || string(file.Content) != "file bytes" {
		t.Fatalf("file download: %+v %v", file, err)
	}
	receipt, err := client.Reply(context.Background(), "om_file", "stable-uuid", "saved")
	if err != nil || receipt != "om_receipt" {
		t.Fatal("reply did not save its platform receipt")
	}
	if _, err := client.Reply(context.Background(), "om_file", "stable-uuid", "saved"); errorCode(err) != "SEND_OUTCOME_UNKNOWN" {
		t.Fatal("missing receipt was not treated as unknown")
	}
	if receipt, err := client.SendText(context.Background(), "ou_owner", "push-uuid", "daily review"); err != nil || receipt != "om_push" {
		t.Fatalf("proactive send: receipt=%s err=%v", receipt, err)
	}
	if message, err := client.GetMessage(context.Background(), "om_push"); err != nil ||
		message.SenderID != "cli_test_app" || message.SenderIDType != "app_id" ||
		message.SenderType != "app" || message.Text != "daily review" || message.Deleted {
		t.Fatalf("receipt lookup: %+v %v", message, err)
	}
	if tokenCalls != 1 {
		t.Fatal("tenant token was not reused")
	}
}

type unknownFeishuReply struct{ calls int }

func (p *unknownFeishuReply) Reply(context.Context, string, string, string) (string, error) {
	p.calls++
	return "", upstreamError("SEND_OUTCOME_UNKNOWN", "response lost")
}

func (p *unknownFeishuReply) Download(context.Context, string, string, string, string) (CreateAttachment, error) {
	return CreateAttachment{}, failure("INVALID_ARGUMENT", "unexpected download", 422, false)
}

func (p *unknownFeishuReply) SendText(context.Context, string, string, string) (string, error) {
	return "", failure("INVALID_ARGUMENT", "unexpected proactive send", 422, false)
}

func (p *unknownFeishuReply) GetMessage(context.Context, string) (FeishuSentMessage, error) {
	return FeishuSentMessage{}, failure("INVALID_ARGUMENT", "unexpected receipt lookup", 422, false)
}

func TestUnknownFeishuReplyIsHeldWithoutRepeatingBusiness(t *testing.T) {
	settings := feishuSettings(t)
	store := testStore(t, settings)
	client := fakeClient(settings, func(*http.Request) (int, any, error) {
		t.Error("help message contacted Memos")
		return 500, nil, nil
	})
	reviews := NewReviewService(store, client)
	notifications := NewNotificationService(store, reviews, nil)
	service := NewFeishuService(store, client, NewRecordService(store, client), reviews, notifications)
	notifications.Transport = NewFeishuDelivery(store, reviews, service)
	platform := &unknownFeishuReply{}
	service.Client = platform
	body := testFeishuEvent(settings, "om_help", "ou_owner", "帮助")
	request := httptest.NewRequest("POST", "/hooks/feishu", bytes.NewReader(body))
	if _, _, err := service.Receive(request); err != nil {
		t.Fatal(err)
	}
	if progress, err := service.processOne(context.Background()); err != nil || !progress {
		t.Fatalf("event processing: progress=%v err=%v", progress, err)
	}
	if progress, err := notifications.Deliver(context.Background()); err != nil || !progress {
		t.Fatalf("queued reply delivery: progress=%v err=%v", progress, err)
	}
	if _, err := store.state.Exec("UPDATE channel_events SET available_at = 0"); err != nil {
		t.Fatal(err)
	}
	if progress, err := service.processOne(context.Background()); err != nil || progress || platform.calls != 1 {
		t.Fatal("unknown reply was sent again")
	}
	var status, replyStatus string
	var payload []byte
	if err := store.state.QueryRow("SELECT status, reply_status, payload FROM channel_events").
		Scan(&status, &replyStatus, &payload); err != nil || status != "processed" || replyStatus != "unknown" || len(payload) != 0 {
		t.Fatal("unknown reply or removal of processed private content was not durable")
	}
}

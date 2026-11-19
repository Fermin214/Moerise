package moerise

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelegramBotPollCommitsPrivateStartAndPreservesWebhook(t *testing.T) {
	const token = "9301:abcdefghijklmnopqrstuvwxyz"
	settings := testSettings(t)
	settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	settings.Channel.BotToken = new(token)
	store := testStore(t, settings)
	receiver := botReceiverStore{store}
	bot := newTelegramBot(token, "chosen", receiver, store)
	var polls atomic.Int32
	var webhook atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var result any
		switch strings.TrimPrefix(request.URL.Path, "/bot"+token+"/") {
		case "getMe":
			result = map[string]any{"id": 9301, "is_bot": true, "username": "MoeriseTestBot"}
		case "getWebhookInfo":
			endpoint := ""
			if webhook.Load() {
				endpoint = "https://example.test/receiver"
			}
			result = map[string]any{"url": endpoint}
		case "getUpdates":
			var query struct {
				Offset, Timeout int
				Allowed         []string `json:"allowed_updates"`
			}
			if err := json.NewDecoder(request.Body).Decode(&query); err != nil || query.Timeout < 1 ||
				len(query.Allowed) != 1 || query.Allowed[0] != "message" {
				t.Error("expected Telegram long polling for messages")
			}
			poll := polls.Add(1)
			offsets := []int{0, 8, 10}
			if poll > 3 || query.Offset != offsets[poll-1] {
				t.Errorf("unexpected poll %d offset %d", poll, query.Offset)
				writer.WriteHeader(500)
				return
			}
			update := func(id, message, peer int, chatType string, content map[string]any) map[string]any {
				fields := map[string]any{"message_id": message, "date": time.Now().Unix(),
					"chat": map[string]any{"id": peer, "type": chatType}, "from": map[string]any{"id": peer, "is_bot": false}}
				for key, value := range content {
					fields[key] = value
				}
				return map[string]any{"update_id": id, "message": fields}
			}
			switch poll {
			case 1:
				result = []any{
					update(1, 10, 31, "private", map[string]any{"text": "before binding"}),
					update(2, 11, 31, "private", map[string]any{"text": "/start wrong"}),
					update(3, 12, 31, "private", map[string]any{"text": "/start chosen"}),
					update(4, 13, 32, "private", map[string]any{"text": "/start chosen"}),
					update(5, 14, 31, "group", map[string]any{"text": "/review"}),
					update(6, 15, 31, "private", map[string]any{"text": " a\n#验收 "}),
					update(7, 15, 31, "private", map[string]any{"text": " a\n#验收 "}),
				}
			case 2:
				result = []any{
					update(8, 16, 31, "private", map[string]any{"photo": []any{"media"}, "caption": "/review"}),
					update(9, 17, 31, "private", map[string]any{"text": "/review"}),
				}
			case 3:
				result = []any{}
			}
		case "sendMessage":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["chat_id"] != "31" || body["text"] != "已保存" {
				t.Error("reply sent outside originating private chat")
			}
			result = map[string]any{"message_id": 35, "chat": map[string]any{"id": 31, "type": "private"},
				"from": map[string]any{"id": 9301, "is_bot": true}}
		default:
			t.Error("unexpected Telegram operation")
			writer.WriteHeader(404)
			return
		}
		json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": result})
	}))
	defer server.Close()
	bot.baseURL = server.URL
	link, err := bot.identify(t.Context())
	if err != nil || link != "https://t.me/MoeriseTestBot?start=chosen" {
		t.Fatalf("Telegram link = %s err=%v", link, err)
	}
	if err := bot.checkWebhook(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		cursor, err := receiver.Cursor(t.Context(), "telegram", bot.id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bot.poll(t.Context(), cursor); err != nil {
			t.Fatal(err)
		}
	}
	peer, _, err := store.botPeer("telegram", bot.id)
	if err != nil || peer != "31" {
		t.Fatalf("Telegram binding = %s err=%v", peer, err)
	}
	var total int
	if err := store.state.QueryRow(`SELECT count(*) FROM bot_inbox`).Scan(&total); err != nil || total != 3 {
		t.Fatalf("Telegram inbox=%d err=%v", total, err)
	}
	for _, task := range []struct{ message, op string }{{"15", "text"}, {"16", "attachment"}, {"17", "review"}} {
		var operation string
		if err := store.state.QueryRow(`SELECT operation FROM bot_inbox WHERE message_id=?`, task.message).
			Scan(&operation); err != nil || operation != task.op {
			t.Fatalf("message %s operation=%s err=%v", task.message, operation, err)
		}
	}
	if receipt, err := bot.Send(t.Context(), peer, "15", "已保存"); err != nil || receipt != "telegram:31:35" {
		t.Fatalf("Telegram receipt=%s err=%v", receipt, err)
	}
	webhook.Store(true)
	if err := bot.checkWebhook(t.Context()); errorCode(err) != "TELEGRAM_WEBHOOK" {
		t.Fatalf("external webhook err=%v", err)
	}
}

func TestTelegramBotPollAcrossUpdateIDReset(t *testing.T) {
	settings := testSettings(t)
	settings.Channel = telegramTestChannel()
	store := testStore(t, settings)
	receiver := botReceiverStore{store}
	bot := newTelegramBot(telegramTestToken, "chosen", receiver, store)
	older := time.Now().Add(-20 * time.Hour).Truncate(time.Second).UTC()
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var params struct{ Offset int }
		if err := json.NewDecoder(request.Body).Decode(&params); err != nil {
			t.Error(err)
		}
		step := polls.Add(1)
		var update any
		switch step {
		case 1, 2:
			if params.Offset != 0 {
				t.Errorf("poll %d would confirm unseen updates with offset %d", step, params.Offset)
			}
			updateID, text := 3501, "/start chosen"
			if step == 2 {
				updateID, text = 41, "/review"
			}
			update = map[string]any{"update_id": updateID, "message": map[string]any{
				"message_id": step, "date": older.Unix(), "chat": map[string]any{"id": 31, "type": "private"},
				"from": map[string]any{"id": 31, "is_bot": false}, "text": text}}
		case 3, 4:
			if params.Offset != int(step)+39 {
				t.Errorf("poll %d did not acknowledge the committed update: offset %d", step, params.Offset)
			}
			update = map[string]any{"update_id": params.Offset, "poll": map[string]string{"id": "undated"}}
		}
		json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": []any{update}})
	}))
	defer server.Close()
	bot.baseURL = server.URL
	cursor := botCursor{Session: time.Now().Format(time.RFC3339Nano), Sequence: 9900}
	poll := func() {
		t.Helper()
		var err error
		cursor, err = bot.poll(t.Context(), cursor)
		if err != nil {
			t.Fatal(err)
		}
		persisted, err := receiver.Cursor(t.Context(), "telegram", bot.id)
		if err != nil || persisted != cursor {
			t.Fatalf("Telegram persisted cursor=%+v err=%v", persisted, err)
		}
	}
	poll()
	if cursor.Session != "update:"+older.Format(time.RFC3339Nano) || cursor.Sequence != 3502 {
		t.Fatalf("delayed Telegram update must use source time: %+v", cursor)
	}
	cursor.Session = "update:" + time.Now().Add(-7*24*time.Hour+10*time.Second).UTC().Format(time.RFC3339Nano)
	poll()
	if cursor.Sequence != 42 {
		t.Fatalf("reset Telegram ID was skipped: %+v", cursor)
	}
	var count int
	if err := store.state.QueryRow(`SELECT count(*) FROM bot_inbox WHERE message_id='2' AND operation='review'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("new Telegram sequence did not reach inbox: %d %v", count, err)
	}
	poll()
	stamp, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(cursor.Session, "update:"))
	if err != nil || time.Since(stamp) < 24*time.Hour || time.Since(stamp) > 24*time.Hour+time.Minute {
		t.Fatalf("undated update advanced the Telegram timing bound: %+v %v", cursor, err)
	}
	poll()
}

func TestQQPrivateContentAndMessageIndex(t *testing.T) {
	parse := func(extra string) *botMessage {
		return qqPrivateMessage(json.RawMessage(fmt.Sprintf(`{"id":"same","author":{"user_openid":"owner","bot":false},
			"timestamp":"2026-01-03T04:05:06Z","content":" /review\n  ",%s}`, extra)))
	}
	if got := parse(`"message_scene":{"ext":["auth_token=anything","msg_idx=001=="]}`); got.Kind != "text" || got.Text != " /review\n  " || got.Index != "001==" {
		t.Fatalf("QQ plain message = %+v", got)
	}
	for _, extra := range []string{`"message_type":3`, `"ark_data":{"prompt":"preview"}`, `"msg_elements":[{"content":"quoted"}]`,
		`"message_scene":{"ext":["ref_msg_idx=reference"]}`, `"message_type":999`} {
		if got := parse(extra); got.Kind != "unsupported" {
			t.Fatalf("QQ structured message accepted as %s", got.Kind)
		}
	}
	first := parse(`"attachments":[{"filename":"img.jpg","url":"https://example.test/one"}]`)
	replay := parse(`"attachments":[{"filename":"img.jpg","url":"https://example.test/two"}]`)
	if first.Kind != "attachment" || first.Digest != replay.Digest {
		t.Fatal("QQ attachment digest retained an expiring URL")
	}
}

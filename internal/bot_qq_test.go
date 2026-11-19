package moerise

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestQQGatewayResumesCommittedInboxAndResetsInvalidSession(t *testing.T) {
	settings := testSettings(t)
	settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("q", 32)))
	settings.Channel.QQAppID, settings.Channel.QQAppSecret = new("41009"), new("test-secret")
	store := testStore(t, settings)
	receiver := botReceiverStore{store}
	bot := newQQBot("41009", "test-secret", store)
	var connections atomic.Int32
	upgrader := websocket.Upgrader{}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app/getAppAccessToken":
			var fields map[string]string
			if err := json.NewDecoder(request.Body).Decode(&fields); err != nil || fields["appId"] != bot.appID || fields["clientSecret"] != bot.secret {
				t.Error("QQ token credentials missing")
			}
			fmt.Fprint(writer, `{"access_token":"access","expires_in":"7200"}`)
		case "/gateway":
			if request.Header.Get("Authorization") != "QQBot access" {
				t.Error("QQ gateway discovery did not use a bot token")
			}
			fmt.Fprintf(writer, `{"url":"%s/ws"}`, strings.Replace(server.URL, "https:", "wss:", 1))
		case "/ws":
			conn, err := upgrader.Upgrade(writer, request, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			step := connections.Add(1)
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			write := func(packet any) {
				if err := conn.WriteJSON(packet); err != nil {
					t.Error(err)
				}
			}
			event := func(seq int, name string, data any) {
				write(map[string]any{"op": 0, "s": seq, "t": name, "d": data})
			}
			write(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 100}})
			var login struct {
				Op   int `json:"op"`
				Data struct {
					Token, Session string
					SessionID      string `json:"session_id"`
					Seq, Intents   int
				} `json:"d"`
			}
			if err := conn.ReadJSON(&login); err != nil {
				t.Error(err)
				return
			}
			if login.Data.Token != "QQBot access" {
				t.Error("QQ login token missing")
			}
			direct := map[string]any{"id": "message", "author": map[string]any{"user_openid": "owner", "bot": false},
				"timestamp": time.Now().UTC(), "content": "  一\n#二  ", "message_type": 0,
				"message_scene": map[string]any{"ext": []string{"msg_idx=idx=="}}}
			switch step {
			case 1:
				if login.Op != 2 || login.Data.Intents != 1<<25 {
					t.Error("QQ identify did not subscribe to C2C")
				}
				event(1, "READY", map[string]any{"session_id": "original"})
				event(2, "GROUP_AT_MESSAGE_CREATE", map[string]any{"id": "group"})
				event(3, "C2C_MESSAGE_CREATE", direct)
				var heartbeat struct {
					Op int
					D  int
				}
				if err := conn.ReadJSON(&heartbeat); err != nil || heartbeat.Op != 1 || heartbeat.D != 3 {
					t.Errorf("heartbeat = %+v, %v", heartbeat, err)
				}
				write(map[string]any{"op": 11})
				event(4, "C2C_MESSAGE_CREATE", direct)
				direct["id"] = "uncommitted"
				event(5, "C2C_MESSAGE_CREATE", direct)
			case 2:
				if login.Op != 6 || login.Data.SessionID != "original" || login.Data.Seq != 4 {
					t.Errorf("resume = %+v", login)
				}
				direct["id"] = "uncommitted"
				event(5, "C2C_MESSAGE_CREATE", direct)
				event(6, "RESUMED", "")
				write(map[string]any{"op": 9, "d": false})
			case 3:
				if login.Op != 2 {
					t.Error("expired session reused")
				}
				event(1, "READY", map[string]any{"session_id": "replacement"})
				direct["id"], direct["content"] = "new", "/review"
				event(2, "C2C_MESSAGE_CREATE", direct)
				direct["message_scene"] = map[string]any{"ext": []string{"msg_idx=conflicting"}}
				event(3, "C2C_MESSAGE_CREATE", direct)
				write(map[string]any{"op": 7})
			}
		default:
			t.Errorf("unexpected QQ request %s", request.URL.Path)
			writer.WriteHeader(404)
		}
	}))
	defer server.Close()
	bot.http, bot.baseURL = server.Client(), server.URL
	if _, err := store.state.Exec(`CREATE TRIGGER inject_failure BEFORE INSERT ON bot_inbox WHEN NEW.message_id='uncommitted'
		BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	lost := false
	if ready, err := bot.connect(ctx, &lost); !ready || errorCode(err) != "BOT_STORAGE" {
		t.Fatalf("first connection ready=%v err=%v", ready, err)
	}
	if _, err := store.state.Exec(`DROP TRIGGER inject_failure`); err != nil {
		t.Fatal(err)
	}
	if ready, err := bot.connect(ctx, &lost); !ready || !lost || errorCode(err) != "QQ_SESSION_LOST" {
		t.Fatalf("resume ready=%v lost=%v err=%v", ready, lost, err)
	}
	if ready, err := bot.connect(ctx, &lost); !ready || errorCode(err) != "BOT_CONNECTION" {
		t.Fatalf("re-identify ready=%v err=%v", ready, err)
	}
	cursor, err := receiver.Cursor(ctx, "qq", bot.appID)
	if err != nil || cursor != (botCursor{"replacement", 3}) {
		t.Fatalf("final QQ cursor = %+v, %v", cursor, err)
	}
	var count, uniqueKeys int
	if err := store.state.QueryRow(`SELECT count(*), count(DISTINCT write_key) FROM bot_inbox WHERE peer='owner'`).
		Scan(&count, &uniqueKeys); err != nil || count != 3 || uniqueKeys != 3 {
		t.Fatalf("QQ inbox count=%d write keys=%d err=%v", count, uniqueKeys, err)
	}
	if err := store.state.QueryRow(`SELECT count(*) FROM bot_inbox WHERE msg_index='idx==' AND (
		(message_id IN ('message','uncommitted') AND phase='pending') OR
		(message_id='new' AND phase='conflict' AND error_code='MESSAGE_CONFLICT' AND body IS NULL))`).
		Scan(&count); err != nil || count != 3 {
		t.Fatalf("QQ inbox index deduplication matched %d rows: %v", count, err)
	}
	if status := store.botStatuses()["qq"]; status.State != "connected" || status.Detail != "QQ_SESSION_LOST" {
		t.Fatalf("QQ replacement status = %+v", status)
	}
}

func TestQQSendChecksTokenReceiptRefusalAndCooldown(t *testing.T) {
	var calls atomic.Int32
	var tokens atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/app/getAppAccessToken" {
			if tokens.Add(1) == 1 {
				fmt.Fprint(writer, `{"code":100016,"access_token":"invalid","expires_in":7200}`)
			} else {
				fmt.Fprint(writer, `{"access_token":"access","expires_in":7200}`)
			}
			return
		}
		if request.URL.Path != "/v2/users/owner/messages" || request.Header.Get("Authorization") != "QQBot access" {
			t.Error("unexpected QQ recipient or credentials")
		}
		var payload map[string]any
		err := json.NewDecoder(request.Body).Decode(&payload)
		if err != nil || payload["content"] != "摘录" || payload["msg_type"] != float64(0) {
			t.Error("incorrect QQ text payload")
		}
		index := calls.Add(1)
		switch index {
		case 1:
			if len(payload) != 4 || payload["msg_id"] != "event" || payload["msg_seq"] != float64(1) {
				t.Error("incorrect QQ reply reference")
			}
			fmt.Fprint(writer, `{"code":304103}`)
		case 2:
			if len(payload) != 2 {
				t.Errorf("proactive message keys = %v", payload)
			}
			fmt.Fprint(writer, `{"code":40054013}`)
		case 3:
			writer.Header().Set("Retry-After", "32")
			writer.WriteHeader(429)
		case 4:
			fmt.Fprint(writer, `{}`)
		case 5:
			conn, _, err := writer.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				conn.Close()
			}
		default:
			fmt.Fprint(writer, `{"id":"receipt"}`)
		}
	}))
	defer server.Close()
	bot := newQQBot("41009", "secret", nil)
	bot.baseURL = server.URL
	for idx, want := range []string{"BOT_CREDENTIALS", "QQ_REPLY_EXPIRED", "QQ_REJECTED_40054013", "BOT_RATE_LIMITED", "DELIVERY_UNKNOWN", "DELIVERY_UNKNOWN", ""} {
		ref := ""
		if idx == 1 {
			ref = "event"
		}
		receipt, err := bot.send(t.Context(), "owner", ref, "摘录")
		if want == "" {
			if err != nil || receipt != "receipt" {
				t.Fatalf("receipt = %q, %v", receipt, err)
			}
		} else if errorCode(err) != want {
			t.Fatalf("send %d error = %v, want %s", idx, err, want)
		}
		if idx == 3 {
			var rate *notificationRetryError
			if !errors.As(err, &rate) || rate.after != 32*time.Second {
				t.Errorf("QQ cooldown = %v", err)
			}
		}
	}
	if calls.Load() != 6 || tokens.Load() != 2 {
		t.Fatalf("QQ requests=%d tokens=%d", calls.Load(), tokens.Load())
	}
}

func TestQQSharedSendCooldownSurvivesRestart(t *testing.T) {
	settings := testSettings(t)
	settings.Channel.QQAppID, settings.Channel.QQAppSecret = new("41009"), new("secret")
	store := testStore(t, settings)
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/app/getAppAccessToken" {
			fmt.Fprint(writer, `{"access_token":"access","expires_in":7200}`)
			return
		}
		if sends.Add(1) <= 2 {
			writer.Header().Set("Retry-After", "90")
			writer.WriteHeader(429)
			return
		}
		fmt.Fprint(writer, `{"id":"sent"}`)
	}))
	defer server.Close()
	clients := func() (*qqBot, *qqBot) {
		reply := newQQBot("41009", "secret", store)
		scheduled := newQQDelivery(settings.Channel, store, nil).(*qqDelivery).bot.(*qqBot)
		reply.baseURL, scheduled.baseURL = server.URL, server.URL
		return reply, scheduled
	}
	limited := func(bot *qqBot, ref string, wantSends int32) {
		t.Helper()
		_, err := bot.Send(t.Context(), "owner", ref, "摘录")
		var rate *notificationRetryError
		if !errors.As(err, &rate) || errorCode(err) != "BOT_RATE_LIMITED" || rate.after <= time.Minute || sends.Load() != wantSends {
			t.Fatalf("QQ rate-limited send ref=%q err=%v requests=%d", ref, err, sends.Load())
		}
	}
	expire := func() {
		t.Helper()
		if _, err := store.state.Exec(`UPDATE notification_rate_limits SET retry_at=0`); err != nil {
			t.Fatal(err)
		}
	}
	reply, scheduled := clients()
	limited(reply, "message", 1)
	limited(scheduled, "", 1)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = testStore(t, settings)
	reply, scheduled = clients()
	limited(scheduled, "", 1)
	expire()
	limited(scheduled, "", 2)
	limited(reply, "next", 2)
	expire()
	if receipt, err := reply.Send(t.Context(), "owner", "next", "摘录"); err != nil || receipt != "sent" || sends.Load() != 3 {
		t.Fatalf("QQ expired cooldown receipt=%q err=%v requests=%d", receipt, err, sends.Load())
	}
}

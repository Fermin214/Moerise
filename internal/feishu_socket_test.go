package moerise

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func TestFeishuSocketPrivateBindingAndDailyReview(t *testing.T) {
	reviews, memos := reviewFixture(t, 2)
	store := reviews.Store
	settings := feishuSettings(t)
	settings.Channel.Kind, settings.Channel.FeishuMode = "none", "websocket"
	settings.Channel.UserOpenID, settings.Channel.VerificationToken = nil, nil
	store.Settings.Channel, store.Settings.Web = settings.Channel, settings.Web
	app := NewApp(store.Settings, store, reviews.Source)
	defer app.Close()
	platform := &capturePlatform{messages: map[string]FeishuSentMessage{}}
	app.Feishu.Client = platform
	connections := make(chan *websocket.Conn, 1)
	var bootstrapCalls, clients atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == larkws.GenEndpointUri {
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["AppID"] != "cli_test_app" ||
				body["AppSecret"] != configured(settings.Channel.AppSecret) {
				t.Error("socket bootstrap credentials differ")
			}
			if bootstrapCalls.Add(1) <= 3 {
				writeJSON(w, 200, map[string]any{"code": 10003, "msg": "invalid credentials"})
				return
			}
			writeJSON(w, 200, map[string]any{"code": 0, "data": map[string]string{
				"URL": "ws://" + r.Host + "/gateway?service_id=1&device_id=test"}})
			return
		}
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		connections <- conn
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.connectFeishu(ctx, store.socketConfig(), 10*time.Millisecond, larkws.WithDomain(server.URL),
			func(*larkws.Client) { clients.Add(1) })
	}()
	defer func() { cancel(); <-done }()
	var conn *websocket.Conn
	select {
	case conn = <-connections:
		defer conn.Close()
	case <-ctx.Done():
		t.Fatal("Feishu SDK did not open the socket")
	}
	if clients.Load() != 1 || bootstrapCalls.Load() != 4 {
		t.Fatalf("credential rejection used %d clients for %d requests", clients.Load(), bootstrapCalls.Load())
	}
	at := time.Now().UTC().Truncate(time.Second).Add(500 * time.Millisecond)
	send := func(id, peer, chat string, created time.Time) {
		t.Helper()
		var event feishuEvent
		if err := json.Unmarshal(testFeishuEvent(settings, id, peer, "/review"), &event); err != nil {
			t.Fatal(err)
		}
		event.Event.Message.Chat, event.Event.Message.CreateTime = chat, strconv.FormatInt(created.UnixMilli(), 10)
		wire, err := (&larkws.Frame{Method: int32(larkws.FrameTypeData), Headers: []larkws.Header{
			{Key: larkws.HeaderType, Value: string(larkws.MessageTypeEvent)}, {Key: larkws.HeaderMessageID, Value: id},
		}, Payload: canonicalJSON(event)}).Marshal()
		if err != nil || conn.WriteMessage(websocket.BinaryMessage, wire) != nil {
			t.Fatalf("could not send an event: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			_, response, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			var frame larkws.Frame
			if err := frame.Unmarshal(response); err != nil {
				t.Fatal(err)
			}
			if frame.Method == int32(larkws.FrameTypeData) {
				var ack struct{ Code int }
				if json.Unmarshal(frame.Payload, &ack) != nil || ack.Code != 200 {
					t.Fatalf("unexpected ACK %s", frame.Payload)
				}
				return
			}
		}
	}
	if _, err := store.state.Exec("INSERT INTO bot_restore VALUES (?,?)", settings.Memos.InstanceID, at.Add(-time.Millisecond).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	send("om_old", "ou_old", "p2p", at.Add(-2*time.Hour))
	send("om_group", "ou_group", "group", at)
	send("om_command", "ou_owner", "p2p", at)
	send("om_command", "ou_owner", "p2p", at)
	send("om_other", "ou_other", "p2p", at)
	var count int
	peer, _, err := store.botPeer("feishu-app", "cli_test_app")
	if err != nil || peer != "ou_owner" || store.state.QueryRow("SELECT COUNT(*) FROM bot_inbox").Scan(&count) != nil || count != 1 {
		t.Fatalf("bound peer=%s count=%d err=%v", peer, count, err)
	}
	if ok, err := processFeishuMessage(ctx, app.Feishu); !ok || err != nil {
		t.Fatalf("review command: %v", err)
	}
	for name := range memos {
		delete(memos, name)
	}
	refreshReviewFixture(t, store, memos)
	deliverFeishuReply(t, app.Feishu)
	if len(platform.texts) != 1 || platform.texts[0] != "今天没有可回顾的笔记" {
		t.Fatalf("private reply returned stale review text: %v", platform.texts)
	}
}

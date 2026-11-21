//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	moerise "moerise/internal"
)

type acceptancePrivateBots struct {
	t         *testing.T
	server    *httptest.Server
	mu        sync.Mutex
	qqConn    *websocket.Conn
	sequence  int
	updates   []map[string]any
	qqReplies map[string][]string
	tgReplies []string
	proactive []string
	next      string
}

const acceptanceTelegramID = "129301"

func privatePlatforms(t *testing.T) *acceptancePrivateBots {
	fixture := &acceptancePrivateBots{t: t, qqReplies: map[string][]string{}}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(fixture.serve))
	originalHTTP, originalWS := http.DefaultTransport, websocket.DefaultDialer
	transport := originalHTTP.(*http.Transport).Clone()
	trusted := fixture.server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	trusted.ServerName = "example.com"
	transport.Proxy, transport.TLSClientConfig = nil, trusted
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		switch host {
		case "api.bot.qq.com", "api.telegram.org":
			address = fixture.server.Listener.Addr().String()
		case "127.0.0.1", "::1":
		default:
			return nil, fmt.Errorf("unexpected external dial %s", host)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = transport
	dialer := *originalWS
	dialer.Proxy, dialer.TLSClientConfig = nil, trusted
	dialer.NetDialContext = transport.DialContext
	websocket.DefaultDialer = &dialer
	t.Cleanup(func() {
		http.DefaultTransport, websocket.DefaultDialer = originalHTTP, originalWS
		transport.CloseIdleConnections()
		fixture.server.Close()
	})
	return fixture
}

func (fixture *acceptancePrivateBots) serve(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/gateway/socket" {
		fixture.gateway(writer, request)
		return
	}
	if strings.HasSuffix(request.URL.Path, "/getUpdates") {
		var params struct{ Offset int }
		if err := json.NewDecoder(request.Body).Decode(&params); err != nil {
			fixture.t.Error(err)
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-request.Context().Done():
			return
		}
		fixture.mu.Lock()
		remaining := []map[string]any{}
		for _, update := range fixture.updates {
			if update["update_id"].(int) >= params.Offset {
				remaining = append(remaining, update)
			}
		}
		fixture.mu.Unlock()
		json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": remaining})
		return
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	switch {
	case request.URL.Path == "/app/getAppAccessToken":
		fmt.Fprint(writer, `{"access_token":"access","expires_in":"7200"}`)
	case request.URL.Path == "/gateway":
		fmt.Fprint(writer, `{"url":"wss://api.bot.qq.com/gateway/socket"}`)
	case request.URL.Path == "/v2/users/owner/messages":
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			fixture.t.Error(err)
			return
		}
		text, _ := body["content"].(string)
		if ref, ok := body["msg_id"].(string); ok {
			fixture.qqReplies[ref] = append(fixture.qqReplies[ref], text)
		} else {
			if len(body) != 2 || body["msg_type"] != float64(0) {
				fixture.t.Error("unexpected QQ proactive message fields")
			}
			fixture.proactive = append(fixture.proactive, text)
			outcome := fixture.next
			fixture.next = ""
			switch outcome {
			case "refuse":
				fmt.Fprint(writer, `{"code":40054013}`)
				return
			case "limit":
				writer.Header().Set("Retry-After", "1")
				writer.WriteHeader(429)
				return
			case "drop":
				conn, _, err := writer.(http.Hijacker).Hijack()
				if err != nil {
					fixture.t.Error(err)
				} else {
					conn.Close()
				}
				return
			}
		}
		fmt.Fprint(writer, `{"id":"receipt"}`)
	case strings.HasSuffix(request.URL.Path, "/getMe"):
		fmt.Fprintf(writer, `{"ok":true,"result":{"id":%s,"is_bot":true,"username":"MoeriseAcceptanceBot"}}`, acceptanceTelegramID)
	case strings.HasSuffix(request.URL.Path, "/getWebhookInfo"):
		fmt.Fprint(writer, `{"ok":true,"result":{"url":""}}`)
	case strings.HasSuffix(request.URL.Path, "/sendMessage"):
		var body struct {
			Chat string `json:"chat_id"`
			Text string `json:"text"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Chat != "31" {
			fixture.t.Error("Telegram replied outside the bound private chat")
		}
		fixture.tgReplies = append(fixture.tgReplies, body.Text)
		fmt.Fprintf(writer, `{"ok":true,"result":{"message_id":%d,"chat":{"id":31,"type":"private"},"from":{"id":%s,"is_bot":true}}}`, len(fixture.tgReplies), acceptanceTelegramID)
	default:
		fixture.t.Errorf("unexpected bot request to %s", request.URL.Path)
		writer.WriteHeader(404)
	}
}

func (fixture *acceptancePrivateBots) gateway(writer http.ResponseWriter, request *http.Request) {
	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 12000}})
	var login struct{ Op int }
	if conn.ReadJSON(&login) != nil {
		return
	}
	fixture.mu.Lock()
	fixture.qqConn = conn
	fixture.sequence++
	event := map[string]any{"op": 0, "s": fixture.sequence, "t": "READY", "d": map[string]string{"session_id": "gateway"}}
	if login.Op == 6 {
		event["t"], event["d"] = "RESUMED", ""
	}
	conn.WriteJSON(event)
	fixture.mu.Unlock()
	defer func() {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		if fixture.qqConn == conn {
			fixture.qqConn = nil
		}
	}()
	for {
		var heartbeat struct{ Op int }
		if conn.ReadJSON(&heartbeat) != nil {
			return
		}
		fixture.mu.Lock()
		conn.WriteJSON(map[string]int{"op": 11})
		fixture.mu.Unlock()
	}
}

func (fixture *acceptancePrivateBots) qq(event, peer, id, text string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.sequence++
	body := map[string]any{"id": id, "content": text, "timestamp": time.Now(), "message_type": 0,
		"author": map[string]any{"user_openid": peer, "bot": false}}
	err := fixture.qqConn.WriteJSON(map[string]any{"op": 0, "s": fixture.sequence, "t": event, "d": body})
	if err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *acceptancePrivateBots) telegram(peer, id int, chat, text string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	msg := map[string]any{"chat": map[string]any{"id": peer, "type": chat}, "date": time.Now().Unix(),
		"from": map[string]any{"id": peer, "is_bot": false}, "message_id": id, "text": text}
	fixture.updates = append(fixture.updates, map[string]any{"update_id": len(fixture.updates) + 1, "message": msg})
}

func TestKnownPrivateBotAddressesAcceptance(t *testing.T) {
	h := newHarness(t)
	h.stop()
	fixture := privatePlatforms(t)
	settings := h.settings
	settings.Channel = moerise.ChannelConfig{Kind: "qq", QQAppID: new("41009"), QQAppSecret: new(randomToken()),
		BotToken: new(acceptanceTelegramID + ":" + randomToken())}
	settings.Web.StateEncryptionKey = randomStateKey()
	settings.Notifications.Enabled = true
	settings.Review = moerise.ReviewConfig{Count: 1}
	settings.Timezone = "UTC"
	app, err := moerise.OpenApp(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	db := h.database("state")
	defer db.Close()
	seedLiveBotPeers(t, db, settings, "owner", "31")
	ctx := t.Context()
	if _, _, err := app.Records.Create(ctx, "acceptance-invite", moerise.CreateNote{Content: "开始私聊验收 #验收"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Worker.Sync(ctx, moerise.Job{Mode: "full"}); err != nil {
		t.Fatal(err)
	}
	tomorrow := time.Now().UTC().AddDate(0, 0, 1)
	slot := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 9, 0, 0, 0, time.UTC)
	if count, err := app.Notifications.Schedule(ctx, slot); err != nil || count != 1 {
		t.Fatalf("expected one QQ invitation: count=%d error=%v", count, err)
	}
	if sent, err := app.Notifications.Deliver(ctx); err != nil || !sent {
		t.Fatalf("QQ invitation: sent=%v error=%v", sent, err)
	}
	fixture.mu.Lock()
	invitations := slices.Clone(fixture.proactive)
	fixture.mu.Unlock()
	if len(invitations) != 1 || !strings.Contains(invitations[0], "开始私聊验收") {
		t.Fatal("QQ did not receive the scheduled note before any private message")
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = app.RunChannels(runCtx)
	}()
	defer func() { stop(); <-done }()
	fixture.telegram(31, 1, "private", "Telegram #验收")
	wait(t, "existing Telegram private chat saved without start", func() bool {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		return slices.Equal(fixture.tgReplies, []string{"已保存"})
	})
}

func TestPrivateBotsWithMemosAcceptance(t *testing.T) {
	fault := &recordFaultProxy{mode: "drop-response"}
	h := newHarnessWithUpstream(t, func(address string) string { return fault.decorate(t, address) })
	h.stop()
	fixture := privatePlatforms(t)
	settings := h.settings
	settings.StateDir, settings.IndexDir = filepath.Join(h.root, "bots-state"), filepath.Join(h.root, "bots-index")
	settings.Timezone = "UTC"
	settings.Review = moerise.ReviewConfig{Count: 1}
	settings.Notifications.Times = []string{"01:00", "02:00", "03:00", "04:00", "05:00"}
	settings.Web = moerise.WebConfig{Enabled: true, StateEncryptionKey: randomStateKey(), PublicOrigin: h.base}
	settings.Channel.QQAppID, settings.Channel.QQAppSecret = new("41009"), new(randomToken())
	settings.Channel.BotToken = new(acceptanceTelegramID + ":" + randomToken())
	app, client := (*moerise.App)(nil), (*clientHarness)(nil)
	var shutdown func()
	open := func() {
		t.Helper()
		var err error
		app, err = moerise.OpenApp(settings)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(moerise.NewClientAPI(app))
		view := *h
		view.base, view.settings = server.URL, settings
		client = &clientHarness{h: &view}
		client.call("POST", "/api/app/v1/session", map[string]string{"username": "acceptance-owner", "password": h.password}, 200)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = app.RunChannels(ctx)
		}()
		shutdown = func() {
			cancel()
			<-done
			server.Close()
			app.Close()
			shutdown = nil
		}
	}
	defer func() {
		if shutdown != nil {
			shutdown()
		}
	}()
	botLink := func() string {
		t.Helper()
		link := ""
		wait(t, "bot gateway ready and private Telegram link available", func() bool {
			view := client.call("GET", "/api/app/v1/settings", nil, 200)["channel_status"].(map[string]any)
			qq, tg := view["qq"].(map[string]any), view["telegram"].(map[string]any)
			link, _ = tg["link"].(string)
			return tg["state"] == "connected" && qq["state"] == "connected" && link != ""
		})
		return link
	}
	open()
	link, err := url.Parse(botLink())
	if err != nil || link.Query().Get("start") == "" {
		t.Fatalf("missing private Telegram start link: %v", err)
	}
	const qqText = "  QQ 文字\n#验收  "
	const tgText = "  Telegram 文字\n#验收  "
	fault.mu.Lock()
	fault.armed = true
	fault.mu.Unlock()
	fixture.qq("GROUP_AT_MESSAGE_CREATE", "group", "group", "ignored group")
	fixture.qq("C2C_MESSAGE_CREATE", "owner", "first", qqText)
	fixture.qq("C2C_MESSAGE_CREATE", "owner", "first", qqText)
	wait(t, "successful Memos POST with dropped HTTP response", func() bool {
		fault.mu.Lock()
		defer fault.mu.Unlock()
		return fault.droppedStatus == 200 && fault.dropError == nil
	})
	shutdown()
	posts := recordPosts(fault.snapshot())
	if len(posts) != 1 {
		t.Fatalf("QQ first message generated %d Memos writes", len(posts))
	}
	h.requireSourceRecords("memos/" + posts[0].memoID)
	open()
	botLink()
	fixture.qq("C2C_MESSAGE_CREATE", "owner", "first", qqText)
	fixture.qq("C2C_MESSAGE_CREATE", "stranger", "other", "/review")
	fixture.telegram(31, 10, "private", "/start "+link.Query().Get("start"))
	fixture.telegram(32, 11, "private", "/start "+link.Query().Get("start"))
	fixture.telegram(32, 12, "private", "foreign note")
	fixture.telegram(-302, 13, "group", "group note")
	fixture.telegram(31, 14, "private", tgText)
	fixture.telegram(31, 14, "private", tgText)
	wait(t, "exactly one save receipt in each bound private chat", func() bool {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		return slices.Equal(fixture.qqReplies["first"], []string{"已保存"}) && slices.Equal(fixture.tgReplies, []string{"已保存"})
	})
	notes := h.call(h.memos, "/api/v1/memos?pageSize=30", "GET", h.upstream, nil, 200)["memos"].([]any)
	if len(notes) != 2 || len(recordPosts(fault.snapshot())) != 2 {
		t.Fatalf("duplicate private note: %d Memos notes", len(notes))
	}
	for _, value := range notes {
		note := value.(map[string]any)
		if note["content"] != tgText && note["content"] != qqText || note["visibility"] != "PRIVATE" && note["visibility"] != nil ||
			note["creator"] != settings.Memos.User || fmt.Sprint(note["tags"]) != "[验收]" {
			t.Fatalf("private Memos text, tags or author changed: %v", note)
		}
	}
	client.key = "bot-acceptance-seed"
	client.call("POST", "/api/app/v1/notes", map[string]string{"content": "Web 笔记 #验收"}, 201)
	if _, err := app.Store.Enqueue("full", true); err != nil {
		t.Fatal(err)
	}
	job, err := app.Store.Claim()
	if err != nil || job == nil {
		t.Fatalf("failed to claim initial full sync: %v", err)
	}
	result, err := app.Worker.Sync(t.Context(), *job)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Store.Complete(job.ID, result); err != nil {
		t.Fatal(err)
	}
	for step, command := range []string{"/review", "/回顾"} {
		if step == 1 {
			client.call("PATCH", "/api/app/v1/settings", map[string]any{"version": 1,
				"settings": map[string]any{"channel": map[string]string{"kind": "telegram"}, "notifications": map[string]bool{"enabled": true}}}, 200)
		}
		fixture.qq("C2C_MESSAGE_CREATE", "owner", command, command)
		fixture.telegram(31, 20+step, "private", command)
		wait(t, "private daily excerpts for "+command, func() bool {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			qq := fixture.qqReplies[command]
			return len(qq) == 1 && len(fixture.tgReplies) == 2+step && strings.Contains(qq[0], "#验收") && qq[0] == fixture.tgReplies[step+1]
		})
	}
	batch, err := app.Reviews.ReadDaily(t.Context(), time.Now())
	if err != nil || len(batch.Items) != 3 {
		t.Fatalf("unexpected Web daily plan: %+v %v", batch, err)
	}
	fixture.mu.Lock()
	summary := fixture.qqReplies["/review"][0]
	fixture.mu.Unlock()
	if !strings.Contains(summary, batch.Date+" · 3 条笔记") {
		t.Fatalf("private review does not match daily count: %s", summary)
	}
	for _, item := range batch.Items {
		if item.Note == nil || !strings.Contains(summary, strings.Join(strings.Fields(item.Note.Content), " ")) {
			t.Fatalf("missing Web daily item in %q", summary)
		}
	}
	if notifications, err := app.Notifications.List(""); err != nil || len(notifications) != 0 || len(recordPosts(fault.snapshot())) != 3 {
		t.Fatalf("commands created notes or scheduled deliveries: %v %v", notifications, err)
	}
	for idx, text := range []string{"//review", "//回顾"} {
		fixture.qq("C2C_MESSAGE_CREATE", "owner", text, text)
		fixture.telegram(31, 24+idx, "private", text)
	}
	wait(t, "both literal slash commands saved by QQ and Telegram", func() bool {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		return len(fixture.qqReplies["//review"]) == 1 && len(fixture.qqReplies["//回顾"]) == 1 && len(fixture.tgReplies) == 5
	})
	counts := map[string]int{}
	for _, value := range h.call(h.memos, "/api/v1/memos?pageSize=30", "GET", h.upstream, nil, 200)["memos"].([]any) {
		counts[value.(map[string]any)["content"].(string)]++
	}
	if counts["/review"] != 2 || counts["/回顾"] != 2 || len(counts) != 5 {
		t.Fatalf("literal note count = %v", counts)
	}
	client.call("PATCH", "/api/app/v1/settings", map[string]any{"version": 2,
		"settings": map[string]any{"channel": map[string]string{"kind": "qq"}}}, 200)
	testQQScheduledFailures(t, app, fixture)
}

func testQQScheduledFailures(t *testing.T, app *moerise.App, fixture *acceptancePrivateBots) {
	t.Helper()
	tomorrow := time.Now().UTC().AddDate(0, 0, 1)
	day := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, time.UTC)
	var attempts int
	for index, outcome := range []string{"refuse", "", "limit", "drop", ""} {
		fixture.mu.Lock()
		fixture.next = outcome
		fixture.mu.Unlock()
		count, err := app.Notifications.Schedule(t.Context(), day.Add(time.Duration(index+1)*time.Hour))
		if err != nil || count != 1 {
			t.Fatalf("scheduled QQ slot %d count=%d err=%v", index, count, err)
		}
		if sent, err := app.Notifications.Deliver(t.Context()); err != nil || !sent {
			t.Fatalf("scheduled QQ slot %d delivery=%v err=%v", index, sent, err)
		}
		attempts++
		status := "sent"
		switch outcome {
		case "refuse":
			status = "failed"
		case "drop":
			status = "unknown"
		case "limit":
			pending, err := app.Notifications.List("pending")
			if err != nil || len(pending) != 1 || pending[0].AvailableAt <= float64(time.Now().Unix()) {
				t.Fatalf("QQ retry cooldown absent: %+v %v", pending, err)
			}
			wait(t, "QQ retry after rate limit", func() bool {
				sent, err := app.Notifications.Deliver(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				return sent
			})
			attempts++
		}
		all, err := app.Notifications.List("")
		slot := slices.IndexFunc(all, func(job moerise.Notification) bool {
			return job.Date == day.Format(time.DateOnly) && job.Clock == fmt.Sprintf("%02d:00", index+1)
		})
		if err != nil || len(all) != index+1 || slot < 0 || all[slot].Status != status {
			t.Fatalf("QQ scheduled slot %d jobs=%+v err=%v", index, all, err)
		}
		if outcome == "refuse" && (all[slot].LastError == nil || *all[slot].LastError != "QQ_REJECTED_40054013") {
			t.Fatalf("QQ refusal code not retained for slot %d", index)
		}
		if sent, err := app.Notifications.Deliver(t.Context()); sent || err != nil {
			t.Fatalf("QQ repeated a terminal outcome from slot %d: %v", index, err)
		}
		fixture.mu.Lock()
		texts := slices.Clone(fixture.proactive)
		fixture.mu.Unlock()
		if len(texts) != attempts || !strings.HasPrefix(texts[len(texts)-1], day.Format(time.DateOnly)+" · 1 条笔记") {
			t.Fatalf("QQ scheduled excerpts %s: %v", strconv.Itoa(index+1), texts)
		}
	}
}

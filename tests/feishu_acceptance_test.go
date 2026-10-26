//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	moerise "moerise/internal"
)

type feishuPlatform struct {
	mu           sync.Mutex
	replies      map[string]string
	uuids        map[string]string
	downloads    int
	calls        int
	pushes       map[string]string
	destinations map[string]string
	dropNextSend bool
	pushCalls    int
}

func (p *feishuPlatform) Reply(_ context.Context, messageID, uuid, text string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prior := p.uuids[messageID]; prior != "" && prior != uuid {
		return "", fmt.Errorf("reply UUID changed")
	}
	p.uuids[messageID], p.replies[messageID] = uuid, text
	p.calls++
	return "receipt_" + messageID, nil
}

func (p *feishuPlatform) Download(_ context.Context, _, _, kind, filename string) (moerise.CreateAttachment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.downloads++
	if kind == "image" {
		filename = "image.txt"
	}
	return moerise.CreateAttachment{Filename: filename, Type: "text/plain", Content: []byte("Feishu attachment")}, nil
}

func (p *feishuPlatform) reply(id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.replies[id]
}

func (p *feishuPlatform) replyCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *feishuPlatform) SendText(_ context.Context, recipient, uuid, text string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pushes == nil {
		p.pushes, p.destinations = map[string]string{}, map[string]string{}
	}
	p.pushes[uuid], p.destinations[uuid] = text, recipient
	p.pushCalls++
	if p.dropNextSend {
		p.dropNextSend = false
		return "", &moerise.ServiceError{Code: "SEND_OUTCOME_UNKNOWN", Message: "response lost", Status: 503, Retryable: true}
	}
	return "push_" + uuid, nil
}

func (p *feishuPlatform) GetMessage(_ context.Context, receipt string) (moerise.FeishuSentMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	message := moerise.FeishuSentMessage{ID: receipt, SenderType: "app", SenderIDType: "app_id", SenderID: "cli_acceptance"}
	if strings.HasPrefix(receipt, "push_") {
		message.Text = p.pushes[strings.TrimPrefix(receipt, "push_")]
	} else if strings.HasPrefix(receipt, "receipt_") {
		message.ParentID = strings.TrimPrefix(receipt, "receipt_")
		message.Text = p.replies[message.ParentID]
	}
	return message, nil
}

func (p *feishuPlatform) lastPush() (string, string, string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for uuid, text := range p.pushes {
		return "push_" + uuid, text, p.destinations[uuid], p.pushCalls
	}
	return "", "", "", p.pushCalls
}

func TestFeishuProactiveReviewWithRealMemosAcceptance(t *testing.T) {
	h := newHarness(t)
	settings := h.settings
	settings.StateDir, settings.IndexDir = filepath.Join(h.root, "push-state"), filepath.Join(h.root, "push-index")
	settings.Review.MinAgeHours = 0
	settings.Notifications.Enabled, settings.Notifications.Time = true, "00:00"
	settings.AccessTokens[0].Scopes = append(settings.AccessTokens[0].Scopes, "reviews:read")
	settings.AccessTokens[1].Scopes = append(settings.AccessTokens[1].Scopes, "reviews:read", "reviews:write")
	pointer := func(value string) *string { return &value }
	settings.Channel.AppID, settings.Channel.AppSecret = pointer("cli_acceptance"), pointer(strings.Repeat("s", 32))
	settings.Channel.VerificationToken, settings.Channel.UserOpenID = pointer(strings.Repeat("v", 32)), pointer("ou_owner")
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	source := moerise.NewMemosClient(settings.Memos)
	defer source.Close()
	store, err := moerise.NewStore(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api, err := moerise.NewAPI(settings, store, source, moerise.NewSyncService(store, source))
	if err != nil {
		t.Fatal(err)
	}
	platform := &feishuPlatform{replies: map[string]string{}, uuids: map[string]string{}, dropNextSend: true}
	api.Feishu.Client = platform
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- api.Feishu.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Feishu worker stopped: %v", err)
		}
	}()
	server := httptest.NewServer(api)
	defer server.Close()
	for i := range 7 {
		if _, _, err := api.Records.Create(ctx, fmt.Sprintf("push-record-%d", i),
			moerise.CreateNote{Content: fmt.Sprintf("proactive review note %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	shared, err := api.Reviews.Daily(ctx, now)
	if err != nil || len(shared.Items) != 3 {
		t.Fatalf("shared daily batch: %+v %v", shared, err)
	}
	if count, err := api.Notifications.Schedule(ctx, now); err != nil || count != 1 {
		t.Fatalf("proactive schedule: count=%d err=%v", count, err)
	}
	pending, err := api.Notifications.List("pending")
	if err != nil || len(pending) != 1 {
		t.Fatalf("missing scheduled review: %+v %v", pending, err)
	}
	notification := pending[0]
	batch, err := api.Reviews.Get(ctx, *notification.BatchID)
	if err != nil || len(batch.Items) != 3 || batch.ID != shared.ID {
		t.Fatalf("scheduled batch: %+v %v", batch, err)
	}
	edited, deleted := batch.Items[0].Name, batch.Items[1].Name
	h.call(h.memos, "/api/v1/"+edited+"?updateMask=content", "PATCH", h.upstream, map[string]string{"content": "edited before push"}, 200)
	h.call(h.memos, "/api/v1/"+deleted, "DELETE", h.upstream, nil, 200)
	if progress, err := api.Notifications.Deliver(ctx); err != nil || !progress {
		t.Fatalf("proactive send: progress=%v err=%v", progress, err)
	}
	receipt, text, recipient, calls := platform.lastPush()
	if calls != 1 || recipient != "ou_owner" || !strings.Contains(text, batch.ID) ||
		!strings.Contains(text, "edited before push") || strings.Contains(text, deleted) {
		t.Fatalf("push destination or live content: recipient=%s text=%s calls=%d", recipient, text, calls)
	}
	detail, err := api.Notifications.Detail(notification.ID)
	if err != nil || detail.Notification.Status != "unknown" || len(detail.Attempts) != 1 {
		t.Fatalf("lost response was not held: %+v %v", detail, err)
	}
	if progress, err := api.Notifications.Deliver(ctx); err != nil || progress {
		t.Fatal("unknown proactive message was sent again")
	}
	_, _, _, calls = platform.lastPush()
	if calls != 1 {
		t.Fatal("unknown push generated another platform request")
	}
	resolved := h.call(server.URL, "/api/ext/v1/notifications/"+notification.ID+"/resolve", "POST", h.operator,
		map[string]string{"decision": "sent", "receipt": receipt}, 200)
	if resolved["notification"].(map[string]any)["receipt"] != receipt {
		t.Fatal("verified platform receipt was not saved")
	}
	batch, err = api.Reviews.Get(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range batch.Items {
		if item.OpenedAt != nil || item.SkippedAt != nil {
			t.Fatal("sending a review changed reading history")
		}
	}
	command := func(id, text string) {
		t.Helper()
		content, _ := json.Marshal(map[string]string{"text": text})
		event := map[string]any{
			"header": map[string]string{"event_id": "event_" + id, "event_type": "im.message.receive_v1",
				"app_id": *settings.Channel.AppID, "token": *settings.Channel.VerificationToken},
			"event": map[string]any{
				"sender":  map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": "ou_owner"}},
				"message": map[string]string{"message_id": id, "message_type": "text", "chat_type": "p2p", "content": string(content)},
			},
		}
		h.call(server.URL, "/hooks/feishu", "POST", "", event, 200)
		h.call(server.URL, "/hooks/feishu", "POST", "", event, 200)
		wait(t, "queued Feishu action reply", func() bool {
			items, err := api.Notifications.List("pending")
			for _, item := range items {
				if item.SourceEvent != nil && *item.SourceEvent == id {
					return err == nil
				}
			}
			return false
		})
		if progress, err := api.Notifications.Deliver(ctx); err != nil || !progress || platform.reply(id) == "" {
			t.Fatalf("action reply: progress=%v err=%v", progress, err)
		}
	}
	command("om_push_open", "打开 "+batch.ID+" "+edited)
	command("om_push_skip", "略过 "+batch.ID+" "+edited)
	read := h.call(server.URL, "/api/ext/v1/reviews/"+batch.ID, "GET", h.reader, nil, 200)
	first := read["items"].([]any)[0].(map[string]any)
	if first["opened_at"] == nil || first["skipped_at"] == nil {
		t.Fatal("push interaction did not share REST reading history")
	}
	command("om_push_replace", "换一组")
	replacement := h.call(server.URL, "/api/ext/v1/reviews/daily", "GET", h.reader, nil, 200)
	if replacement["id"] == batch.ID || !strings.Contains(platform.reply("om_push_replace"), replacement["id"].(string)) {
		t.Fatal("push replacement did not share the next fixed batch")
	}
	if count, err := api.Notifications.Schedule(ctx, now); err != nil || count != 0 {
		t.Fatal("interactive replacement duplicated the scheduled notification")
	}
}

func TestFeishuEventsWithRealMemosAcceptance(t *testing.T) {
	h := newHarness(t)
	settings := h.settings
	settings.StateDir, settings.IndexDir = filepath.Join(h.root, "feishu-state"), filepath.Join(h.root, "feishu-index")
	settings.Review.MinAgeHours = 0
	settings.AccessTokens[0].Scopes = append(settings.AccessTokens[0].Scopes, "reviews:read")
	settings.AccessTokens[1].Scopes = append(settings.AccessTokens[1].Scopes, "reviews:read", "reviews:write")
	pointer := func(value string) *string { return &value }
	settings.Channel.AppID, settings.Channel.AppSecret = pointer("cli_acceptance"), pointer(strings.Repeat("s", 32))
	settings.Channel.VerificationToken, settings.Channel.UserOpenID = pointer(strings.Repeat("v", 32)), pointer("ou_owner")
	source := moerise.NewMemosClient(settings.Memos)
	defer source.Close()
	platform := &feishuPlatform{replies: map[string]string{}, uuids: map[string]string{}}
	var store *moerise.Store
	var api *moerise.API
	var server *httptest.Server
	var cancel context.CancelFunc
	var workerDone chan error
	var notificationDone chan error
	start := func(run bool) {
		t.Helper()
		var err error
		store, err = moerise.NewStore(settings)
		if err != nil {
			t.Fatal(err)
		}
		api, err = moerise.NewAPI(settings, store, source, moerise.NewSyncService(store, source))
		if err != nil {
			t.Fatal(err)
		}
		api.Feishu.Client = platform
		server = httptest.NewServer(api)
		if run {
			var ctx context.Context
			ctx, cancel = context.WithCancel(context.Background())
			workerDone = make(chan error, 1)
			notificationDone = make(chan error, 1)
			go func() { workerDone <- api.Feishu.Run(ctx) }()
			go func() { notificationDone <- api.Notifications.Run(ctx) }()
		}
	}
	stop := func() {
		if cancel != nil {
			cancel()
			err := <-workerDone
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Feishu worker stopped: %v", err)
			}
			err = <-notificationDone
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("notification worker stopped: %v", err)
			}
			cancel = nil
		}
		if server != nil {
			server.Close()
		}
		if store != nil {
			store.Close()
		}
	}
	t.Cleanup(stop)
	start(false)
	event := func(id, sender, kind string, content any) map[string]any {
		body, _ := json.Marshal(content)
		return map[string]any{
			"header": map[string]string{"event_id": "event_" + id, "event_type": "im.message.receive_v1",
				"app_id": *settings.Channel.AppID, "token": *settings.Channel.VerificationToken},
			"event": map[string]any{
				"sender":  map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": sender}},
				"message": map[string]string{"message_id": id, "message_type": kind, "content": string(body), "chat_type": "p2p"},
			},
		}
	}
	send := func(body map[string]any) {
		t.Helper()
		h.call(server.URL, "/hooks/feishu", "POST", "", body, 200)
	}
	record := event("om_record", "ou_owner", "text", map[string]string{"text": "记录 #飞书 测试正文"})
	send(record)
	send(record)
	send(event("om_foreign", "ou_other", "text", map[string]string{"text": "记录 foreign"}))
	snapshot := filepath.Join(h.root, "queued-feishu")
	if _, err := moerise.Backup(settings, snapshot); err != nil {
		t.Fatal(err)
	}
	stop()
	start(true)
	wait(t, "durable incoming Feishu record", func() bool { return platform.reply("om_record") != "" })
	reply := platform.reply("om_record")
	lines := strings.Split(reply, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "memos/") || lines[2] != settings.Memos.PublicURL+"/"+lines[1] {
		t.Fatalf("missing saved resource and original URL: %s", reply)
	}
	name := lines[1]
	note := h.call(h.memos, "/api/v1/"+name, "GET", h.upstream, nil, 200)
	if note["content"] != "#飞书 测试正文" || note["creator"] != settings.Memos.User {
		t.Fatalf("Feishu text or tags were not saved in Memos: %v", note)
	}
	h.requireSourceRecords(name)
	if platform.reply("om_foreign") != "" {
		t.Fatal("foreign Feishu identity received a private reply")
	}
	send(record)
	send(event("om_file", "ou_owner", "file", map[string]string{"file_key": "file_123", "file_name": "notes.txt"}))
	wait(t, "Feishu attachment capture", func() bool { return platform.reply("om_file") != "" })
	fileName := strings.Split(platform.reply("om_file"), "\n")[1]
	fileNote := h.call(h.memos, "/api/v1/"+fileName, "GET", h.upstream, nil, 200)
	if len(fileNote["attachments"].([]any)) != 1 {
		t.Fatal("Feishu file was not linked in Memos")
	}
	send(event("om_query", "ou_owner", "text", map[string]string{"text": "查看 " + name}))
	wait(t, "Feishu source query", func() bool { return strings.Contains(platform.reply("om_query"), "#飞书 测试正文") })
	send(event("om_search", "ou_owner", "text", map[string]string{"text": "搜索 测试正文"}))
	wait(t, "Feishu Chinese search", func() bool { return strings.Contains(platform.reply("om_search"), name) })
	if !strings.Contains(platform.reply("om_search"), "本次使用关键词检索") {
		t.Fatal("Feishu did not explain embedding-disabled fallback")
	}
	send(event("om_walk", "ou_owner", "text", map[string]string{"text": "随机跳转 " + name}))
	wait(t, "Feishu random discovery", func() bool { return strings.Contains(platform.reply("om_walk"), fileName) })
	send(event("om_foreign_search", "ou_other", "text", map[string]string{"text": "搜索 测试正文"}))
	if platform.reply("om_foreign_search") != "" {
		t.Fatal("foreign identity received search results")
	}
	send(event("om_review", "ou_owner", "text", map[string]string{"text": "回顾"}))
	wait(t, "Feishu shared review", func() bool { return platform.reply("om_review") != "" })
	batch := h.call(server.URL, "/api/ext/v1/reviews/daily", "GET", h.operator, nil, 200)
	id := batch["id"].(string)
	if !strings.Contains(platform.reply("om_review"), id) {
		t.Fatal("Feishu and REST did not share their review batch")
	}
	open := event("om_open", "ou_owner", "text", map[string]string{"text": "打开 " + id + " " + name})
	send(open)
	wait(t, "Feishu open action", func() bool { return platform.reply("om_open") != "" })
	read := h.call(server.URL, "/api/ext/v1/reviews/"+id, "GET", h.reader, nil, 200)
	found := false
	for _, value := range read["items"].([]any) {
		item := value.(map[string]any)
		if item["name"] == name && item["opened_at"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("Feishu open action was not shared with REST")
	}
	stop()
	if err := moerise.Restore(settings, snapshot, filepath.Join(h.root, "before-feishu-restore")); err != nil {
		t.Fatal(err)
	}
	before := platform.replyCount()
	start(true)
	wait(t, "restored Feishu reply outcome review", func() bool {
		unknown, err := api.Notifications.List("unknown")
		return err == nil && len(unknown) == 1
	})
	if platform.replyCount() != before {
		t.Fatal("restoring an old incoming event snapshot resent its completed reply")
	}
	h.requireSourceRecords(name, fileName)
}

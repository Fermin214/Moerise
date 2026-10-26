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

	moerise "moerise/internal"
)

type feishuPlatform struct {
	mu        sync.Mutex
	replies   map[string]string
	uuids     map[string]string
	downloads int
	calls     int
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
			go func() { workerDone <- api.Feishu.Run(ctx) }()
		}
	}
	stop := func() {
		if cancel != nil {
			cancel()
			err := <-workerDone
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Feishu worker stopped: %v", err)
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
	wait(t, "queued Feishu event replay after restore", func() bool { return platform.replyCount() > before })
	h.requireSourceRecords(name, fileName)
}

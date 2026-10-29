//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
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
	sentAt       map[string]time.Time
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
		var data bytes.Buffer
		if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
			return moerise.CreateAttachment{}, err
		}
		return moerise.CreateAttachment{Filename: "image.png", Type: "image/png", Content: data.Bytes()}, nil
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
		p.sentAt = map[string]time.Time{}
	}
	p.pushes[uuid], p.destinations[uuid] = text, recipient
	p.sentAt[uuid] = time.Now()
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
	message := moerise.FeishuSentMessage{ID: receipt, SenderType: "app", SenderIDType: "app_id",
		SenderID: "cli_acceptance", ChatID: "oc_owner"}
	if strings.HasPrefix(receipt, "push_") {
		message.Text = p.pushes[strings.TrimPrefix(receipt, "push_")]
		message.CreateTime = p.sentAt[strings.TrimPrefix(receipt, "push_")]
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
	api, err := moerise.NewAPI(moerise.NewApp(settings, store, source))
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
	if _, err := store.Enqueue("full", true); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim()
	if err != nil || job == nil {
		t.Fatalf("claim initial sync: %+v %v", job, err)
	}
	synced, err := api.Worker.Sync(ctx, *job)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(job.ID, synced); err != nil {
		t.Fatal(err)
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
	if calls != 1 || recipient != "ou_owner" || text != "「今日回顾」" {
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
	resolved, err := api.Notifications.Resolve(ctx, notification.ID, "sent", receipt, "users/owner")
	if err != nil || resolved.Notification.Receipt == nil || *resolved.Notification.Receipt != receipt {
		t.Fatalf("verified receipt: %+v %v", resolved, err)
	}
	for range 10 {
		progress, err := api.Notifications.Deliver(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !progress {
			break
		}
	}
	sent, err := api.Notifications.List("sent")
	waiting, waitingErr := api.Notifications.List("pending")
	unknown, unknownErr := api.Notifications.List("unknown")
	if err != nil || waitingErr != nil || unknownErr != nil || len(sent) != 5 || len(waiting) != 0 || len(unknown) != 0 {
		t.Fatalf("review fragments did not finish: sent=%d pending=%d unknown=%d", len(sent), len(waiting), len(unknown))
	}
	expected := batch.Items[2].Note.Content
	platform.mu.Lock()
	header, editedBody, otherBody, footers := 0, 0, 0, 0
	for _, body := range platform.pushes {
		switch {
		case body == "「今日回顾」":
			header++
		case body == "edited before push":
			editedBody++
		case body == expected:
			otherBody++
		case strings.HasPrefix(body, "创建于"):
			footers++
		default:
			t.Errorf("unexpected review message: %q", body)
		}
	}
	platform.mu.Unlock()
	if header != 1 || editedBody != 1 || otherBody != 1 || footers != 2 {
		t.Fatalf("review layout: titles=%d edited=%d other=%d footers=%d", header, editedBody, otherBody, footers)
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
	if count, err := api.Notifications.Schedule(ctx, now); err != nil || count != 0 {
		t.Fatal("repeated scheduling duplicated the notification")
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
		api, err = moerise.NewAPI(moerise.NewApp(settings, store, source))
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
	text := " \t#飞书 测试正文\n\n第二段\r\n末尾空白 \t\n"
	record := event("om_record", "ou_owner", "text", map[string]string{"text": text})
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
	if note["content"] != text || note["creator"] != settings.Memos.User {
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
	if len(fileNote["attachments"].([]any)) != 1 || (fileNote["content"] != nil && fileNote["content"] != "") {
		t.Fatal("Feishu file must be linked without placeholder content")
	}
	imageEvent := event("om_image", "ou_owner", "image", map[string]string{"image_key": "img_123"})
	send(imageEvent)
	send(imageEvent)
	wait(t, "Feishu image capture", func() bool { return platform.reply("om_image") != "" })
	imageName := strings.Split(platform.reply("om_image"), "\n")[1]
	imageNote := h.call(h.memos, "/api/v1/"+imageName, "GET", h.upstream, nil, 200)
	attachments := imageNote["attachments"].([]any)
	if len(attachments) != 1 || (imageNote["content"] != nil && imageNote["content"] != "") {
		t.Fatal("Feishu image must be linked once without placeholder content")
	}
	attachment := attachments[0].(map[string]any)
	if attachment["type"] != "image/png" || attachment["memo"] != imageName {
		t.Fatalf("image metadata or parent changed: %v", attachment)
	}
	request, _ := http.NewRequest("GET", h.memos+"/file/"+attachment["name"].(string)+"/image.png", nil)
	request.Header.Set("Authorization", "Bearer "+h.upstream)
	response, err := h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if _, decodeErr := png.Decode(bytes.NewReader(data)); err != nil || response.StatusCode != 200 || decodeErr != nil {
		t.Fatalf("saved image cannot be read: status=%d read=%v decode=%v", response.StatusCode, err, decodeErr)
	}
	names := []string{name, fileName, imageName}
	for i, command := range []string{"记录 #原样 内容", "查看 " + name, "搜索 测试", "洞察 " + name, "问答 测试", "回顾", "换一组"} {
		id := fmt.Sprintf("om_literal_%d", i)
		send(event(id, "ou_owner", "text", map[string]string{"text": command}))
		wait(t, "literal former command capture", func() bool { return platform.reply(id) != "" })
		saved := strings.Split(platform.reply(id), "\n")
		if len(saved) != 3 || saved[0] != "已保存" {
			t.Fatalf("former command intercepted text: %q", platform.reply(id))
		}
		note := h.call(h.memos, "/api/v1/"+saved[1], "GET", h.upstream, nil, 200)
		if note["content"] != command {
			t.Fatalf("former command changed text: got=%q want=%q", note["content"], command)
		}
		names = append(names, saved[1])
	}
	send(imageEvent)
	stop()
	platform.mu.Lock()
	downloads := platform.downloads
	platform.mu.Unlock()
	if downloads != 2 {
		t.Fatalf("duplicate event downloaded attachments again: %d", downloads)
	}
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
	h.requireSourceRecords(names...)
}

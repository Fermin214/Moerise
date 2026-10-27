//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	moerise "moerise/internal"
)

type aiAcceptanceLLM struct {
	name, content string
	calls         atomic.Int32
}

func (p *aiAcceptanceLLM) Close() {}

func (p *aiAcceptanceLLM) Generate(ctx context.Context, messages []moerise.LLMMessage, emit func(string) error) (moerise.LLMResult, error) {
	p.calls.Add(1)
	var input struct {
		Mode      string             `json:"mode"`
		Requested int                `json:"requested_sources"`
		Evidence  []moerise.Evidence `json:"evidence"`
	}
	if len(messages) != 2 || messages[1].Role != "user" ||
		json.Unmarshal([]byte(messages[1].Content), &input) != nil ||
		input.Mode != "single" || input.Requested != 1 || len(input.Evidence) != 1 ||
		input.Evidence[0].Name != p.name || input.Evidence[0].Content != p.content {
		return moerise.LLMResult{}, fmt.Errorf("model did not receive the selected live source")
	}
	if err := ctx.Err(); err != nil {
		return moerise.LLMResult{}, err
	}
	citations := []string{input.Evidence[0].ID}
	records := []any{
		map[string]any{"type": "fact", "text": strings.Split(p.content, "\n")[0], "citations": citations},
		map[string]any{"type": "interpretation", "text": "这条记录可能适合与后续观察对照。", "citations": citations},
		map[string]any{"type": "question", "text": "后续是否继续记录薄荷变化？", "citations": []string{}},
		map[string]any{"type": "result", "status": "answered", "missing": []string{}},
	}
	var output strings.Builder
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			return moerise.LLMResult{}, err
		}
		output.Write(data)
		output.WriteByte('\n')
	}
	if emit != nil {
		if err := emit(output.String()); err != nil {
			return moerise.LLMResult{}, err
		}
	}
	return moerise.LLMResult{Content: output.String(), Model: "acceptance-controlled", FinishReason: "stop"}, nil
}

// Notification delivery is manual so generation, restart and source edits have
// deterministic boundaries while the actual Feishu and sync workers run.
type aiAcceptanceRuntime struct {
	h        *harness
	settings moerise.Settings
	provider *aiAcceptanceLLM
	platform *feishuPlatform
	store    *moerise.Store
	api      *moerise.API
	server   *httptest.Server
	source   *moerise.MemosClient
	cancel   context.CancelFunc
	done     []chan error
}

func newAIAcceptanceRuntime(t *testing.T) (*aiAcceptanceRuntime, string) {
	t.Helper()
	h := newHarness(t)
	content := "AI_CACHE_PRIVATE_" + randomToken() + "\n窗台薄荷长出两片新叶。"
	note := h.call(h.memos, "/api/v1/memos", "POST", h.upstream,
		map[string]string{"content": content, "visibility": "PRIVATE"}, 200)
	name := note["name"].(string)
	if note["creator"] != h.settings.Memos.User || note["content"] != content {
		t.Fatalf("unexpected official Memos source: %v", note)
	}
	h.sync("full")
	h.stop()
	settings := h.settings
	settings.AccessTokens = append([]moerise.AccessToken(nil), settings.AccessTokens...)
	settings.AccessTokens[1].Scopes = append(append([]string(nil), settings.AccessTokens[1].Scopes...),
		"ai:generate", "reviews:read", "reviews:write")
	aiOnly := randomToken()
	settings.AccessTokens = append(settings.AccessTokens,
		moerise.AccessToken{Name: "ai-only", Token: aiOnly, Scopes: []string{"ai:generate"}})
	address, key, model := "https://llm.invalid/v1", strings.Repeat("k", 32), "acceptance-controlled"
	settings.LLM.Enabled = true
	settings.LLM.BaseURL, settings.LLM.APIKey, settings.LLM.Model = &address, &key, &model
	app, secret, verification, owner := "cli_acceptance", strings.Repeat("s", 32), strings.Repeat("v", 32), "ou_owner"
	settings.Channel.AppID, settings.Channel.AppSecret = &app, &secret
	settings.Channel.VerificationToken, settings.Channel.UserOpenID = &verification, &owner
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	run := &aiAcceptanceRuntime{h: h, settings: settings,
		provider: &aiAcceptanceLLM{name: name, content: content},
		platform: &feishuPlatform{replies: map[string]string{}, uuids: map[string]string{}}}
	t.Cleanup(run.stop)
	run.start()
	return run, aiOnly
}

func (r *aiAcceptanceRuntime) start() {
	r.h.t.Helper()
	var err error
	r.source = moerise.NewMemosClient(r.settings.Memos)
	r.store, err = moerise.NewStore(r.settings)
	if err != nil {
		r.h.t.Fatal(err)
	}
	r.api, err = moerise.NewAPI(r.settings, r.store, r.source, moerise.NewSyncService(r.store, r.source))
	if err != nil {
		r.h.t.Fatal(err)
	}
	if r.api.AI == nil || r.api.Feishu == nil || r.api.Feishu.AI != r.api.AI || r.api.AI.Search != r.api.Search {
		r.h.t.Fatal("REST, MCP and Feishu do not share AI and evidence services")
	}
	r.api.AI.Provider, r.api.Feishu.Client = r.provider, r.platform
	r.server = httptest.NewServer(r.api)
	r.h.base = r.server.URL
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = []chan error{make(chan error, 1), make(chan error, 1)}
	go func() { r.done[0] <- r.api.Worker.Run(ctx) }()
	go func() { r.done[1] <- r.api.Feishu.Run(ctx) }()
	r.h.ready()
}

func (r *aiAcceptanceRuntime) stop() {
	if r.cancel != nil {
		r.cancel()
		for _, done := range r.done {
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				r.h.t.Errorf("AI acceptance worker stopped: %v", err)
			}
		}
		r.cancel = nil
	}
	if r.server != nil {
		r.server.Close()
		r.server = nil
	}
	if r.store != nil {
		if err := r.store.Close(); err != nil {
			r.h.t.Error(err)
		}
		r.store = nil
	}
	if r.source != nil {
		r.source.Close()
		r.source = nil
	}
}

func (r *aiAcceptanceRuntime) receive(id string) {
	r.h.t.Helper()
	content, _ := json.Marshal(map[string]string{"text": "洞察 " + r.provider.name})
	r.h.call(r.server.URL, "/hooks/feishu", "POST", "", map[string]any{
		"header": map[string]string{"event_id": "event_" + id, "event_type": "im.message.receive_v1",
			"app_id": *r.settings.Channel.AppID, "token": *r.settings.Channel.VerificationToken},
		"event": map[string]any{
			"sender": map[string]any{"sender_type": "user",
				"sender_id": map[string]string{"open_id": *r.settings.Channel.UserOpenID}},
			"message": map[string]string{"message_id": id, "message_type": "text",
				"chat_type": "p2p", "content": string(content)},
		},
	}, 200)
}

func (r *aiAcceptanceRuntime) queued(id string) moerise.Notification {
	r.h.t.Helper()
	var result moerise.Notification
	wait(r.h.t, "completed AI generation queued for "+id, func() bool {
		items, err := r.api.Notifications.List("pending")
		if err != nil {
			r.h.t.Fatal(err)
		}
		for _, item := range items {
			if item.SourceEvent != nil && *item.SourceEvent == id {
				result = item
				return true
			}
		}
		return false
	})
	return result
}

func (r *aiAcceptanceRuntime) deliver(want bool) {
	r.h.t.Helper()
	progress, err := r.api.Notifications.Deliver(context.Background())
	if err != nil || progress != want {
		r.h.t.Fatalf("notification delivery: progress=%v want=%v err=%v", progress, want, err)
	}
}

func aiAcceptanceResult(t *testing.T, raw any) moerise.AIResult {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var result moerise.AIResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Completed || result.Status != "answered" || result.Kind != "single" ||
		len(result.Statements) != 3 || len(result.References) != 1 || len(result.UsedSources) != 1 {
		t.Fatalf("incomplete insight result: %+v", result)
	}
	return result
}

func aiAcceptanceRendered(t *testing.T, text string, result moerise.AIResult) {
	t.Helper()
	labels := map[string]string{"fact": "事实", "interpretation": "解释", "question": "追问"}
	for _, item := range result.Statements {
		line := "[" + labels[item.Kind] + "] " + item.Text
		if len(item.Citations) > 0 {
			line += " [" + strings.Join(item.Citations, ", ") + "]"
		}
		if !strings.Contains(text, line+"\n") {
			t.Fatalf("Feishu statement/citation differs from REST/MCP: missing %q in %s", line, text)
		}
	}
	for _, item := range result.References {
		line := fmt.Sprintf("引用 %s：%s 第 %d-%d 行\n%s\n",
			item.ID, item.Name, item.LineStart, item.LineEnd, item.URL)
		if !strings.Contains(text, line) {
			t.Fatalf("Feishu reference mapping differs from REST/MCP: missing %q in %s", line, text)
		}
	}
	if !strings.Contains(text, "实际引用：1 条笔记。") {
		t.Fatalf("Feishu lost used source count: %s", text)
	}
}

func TestAISelectedSourceAcrossRESTMCPAndFeishuAcceptance(t *testing.T) {
	r, aiOnly := newAIAcceptanceRuntime(t)
	h := r.h
	request := moerise.AIRequest{Names: []string{r.provider.name}}
	for _, token := range []string{h.reader, aiOnly} {
		h.call(r.server.URL, "/api/ext/v1/insights", "POST", token, request, 403)
		rpc, status, err := h.requestWithHeaders(r.server.URL, "/mcp", "POST", token,
			map[string]any{"jsonrpc": "2.0", "id": "denied-ai", "method": "tools/call",
				"params": map[string]any{"name": "analyze_memos", "arguments": request}},
			map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
		result, _ := rpc["result"].(map[string]any)
		if err != nil || status != 200 || rpc["error"] == nil && result["isError"] != true {
			t.Fatalf("MCP scope did not deny AI: status=%d err=%v response=%v", status, err, rpc)
		}
	}
	if r.provider.calls.Load() != 0 {
		t.Fatal("scope denial invoked the model")
	}
	rest := aiAcceptanceResult(t, h.call(r.server.URL, "/api/ext/v1/insights", "POST", h.operator, request, 200))
	tool := h.mcp(h.operator, "tools/call", map[string]any{"name": "analyze_memos", "arguments": request})
	if tool["isError"] == true {
		t.Fatalf("MCP insight failed: %v", tool)
	}
	mcp := aiAcceptanceResult(t, tool["structuredContent"])
	rest.Metrics, mcp.Metrics = moerise.AIMetrics{}, moerise.AIMetrics{}
	if !reflect.DeepEqual(rest, mcp) {
		t.Fatalf("REST/MCP result or reference mapping differs: REST=%+v MCP=%+v", rest, mcp)
	}
	reference := rest.References[0]
	if reference.ID != "E1" || reference.Name != r.provider.name || reference.SourceHash == "" ||
		reference.ByteStart != 0 || reference.ByteEnd != len(r.provider.content) ||
		reference.Content != r.provider.content || reference.LineStart != 1 || reference.LineEnd != 2 ||
		reference.URL != r.settings.Memos.PublicURL+"/"+r.provider.name ||
		rest.UsedSources[0] != reference.Name {
		t.Fatalf("reference does not map to the selected official Memos source: %+v", reference)
	}
	for _, statement := range rest.Statements {
		for _, citation := range statement.Citations {
			if citation != reference.ID {
				t.Fatalf("unmapped citation: %+v", statement)
			}
		}
	}
	r.receive("om_ai_parity")
	r.receive("om_ai_parity")
	item := r.queued("om_ai_parity")
	r.deliver(true)
	aiAcceptanceRendered(t, r.platform.reply("om_ai_parity"), rest)
	r.receive("om_ai_parity")
	r.deliver(false)
	detail, err := r.api.Notifications.Detail(item.ID)
	if err != nil || detail.Notification.Status != "sent" || len(detail.Attempts) != 1 ||
		r.provider.calls.Load() != 3 || r.platform.replyCount() != 1 {
		t.Fatalf("redelivery regenerated or resent insight: calls=%d sends=%d detail=%+v err=%v",
			r.provider.calls.Load(), r.platform.replyCount(), detail, err)
	}
}

type aiAcceptanceCachedGeneration struct {
	request, result []byte
}

func aiAcceptanceCache(t *testing.T, h *harness, id string) aiAcceptanceCachedGeneration {
	t.Helper()
	db := h.database("state")
	defer db.Close()
	var cached aiAcceptanceCachedGeneration
	var status string
	if err := db.QueryRow("SELECT status, request, result FROM feishu_ai_generations WHERE message_id = ?", id).
		Scan(&status, &cached.request, &cached.result); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || len(cached.request) == 0 || len(cached.result) == 0 {
		t.Fatalf("generation was not durably completed: status=%s request=%d result=%d",
			status, len(cached.request), len(cached.result))
	}
	return cached
}

func TestAICachedDeliveryRebuildAndRestoreWithRealMemosAcceptance(t *testing.T) {
	r, _ := newAIAcceptanceRuntime(t)
	h := r.h
	r.receive("om_ai_sent")
	sent := r.queued("om_ai_sent")
	r.deliver(true)
	sentText := r.platform.reply("om_ai_sent")
	r.receive("om_ai_cached")
	cached := r.queued("om_ai_cached")
	sentGeneration := aiAcceptanceCache(t, h, "om_ai_sent")
	cachedGeneration := aiAcceptanceCache(t, h, "om_ai_cached")
	if r.provider.calls.Load() != 2 || r.platform.replyCount() != 1 {
		t.Fatal("unexpected model/send count before online backup")
	}
	snapshot := filepath.Join(h.root, "ai-online-backup")
	if _, err := moerise.Backup(r.settings, snapshot); err != nil {
		t.Fatal(err)
	}

	r.stop()
	if err := moerise.RebuildIndex(r.settings); err != nil {
		t.Fatal(err)
	}
	r.start()
	for id, before := range map[string]aiAcceptanceCachedGeneration{
		"om_ai_sent": sentGeneration, "om_ai_cached": cachedGeneration,
	} {
		after := aiAcceptanceCache(t, h, id)
		if !bytes.Equal(before.request, after.request) || !bytes.Equal(before.result, after.result) {
			t.Fatalf("index rebuild/reopen replaced the persisted generation for %s", id)
		}
	}
	r.receive("om_ai_sent")
	r.receive("om_ai_cached")
	r.deliver(true)
	if r.platform.reply("om_ai_cached") != sentText || r.platform.replyCount() != 2 || r.provider.calls.Load() != 2 {
		t.Fatalf("Store/Feishu reopen did not reuse the cached result: calls=%d sends=%d text=%s",
			r.provider.calls.Load(), r.platform.replyCount(), r.platform.reply("om_ai_cached"))
	}
	r.deliver(false)
	confirmed, err := r.api.Notifications.Detail(sent.ID)
	if err != nil || confirmed.Notification.Status != "sent" || len(confirmed.Attempts) != 1 {
		t.Fatalf("index rebuild lost confirmed part: %+v %v", confirmed, err)
	}
	delivered, err := r.api.Notifications.Detail(cached.ID)
	if err != nil || delivered.Notification.Status != "sent" || delivered.Notification.Receipt == nil {
		t.Fatalf("cached part did not save a receipt: %+v %v", delivered, err)
	}
	receipt := *delivered.Notification.Receipt

	r.stop()
	if err := moerise.Restore(r.settings, snapshot, filepath.Join(h.root, "before-ai-restore")); err != nil {
		t.Fatal(err)
	}
	r.start()
	restored := aiAcceptanceCache(t, h, "om_ai_cached")
	if !bytes.Equal(cachedGeneration.request, restored.request) || !bytes.Equal(cachedGeneration.result, restored.result) {
		t.Fatal("backup restore lost the completed generation")
	}
	confirmed, err = r.api.Notifications.Detail(sent.ID)
	if err != nil || confirmed.Notification.Status != "sent" ||
		confirmed.Notification.PlatformUUID != sent.PlatformUUID || len(confirmed.Attempts) != 1 {
		t.Fatalf("restore lost already confirmed part: %+v %v", confirmed, err)
	}
	uncertain, err := r.api.Notifications.Detail(cached.ID)
	if err != nil || uncertain.Notification.Status != "unknown" || len(uncertain.Attempts) != 0 {
		t.Fatalf("older snapshot did not hold uncertain send outcome: %+v %v", uncertain, err)
	}
	r.receive("om_ai_sent")
	r.receive("om_ai_cached")
	r.deliver(false)
	if r.provider.calls.Load() != 2 || r.platform.replyCount() != 2 {
		t.Fatal("restore/redelivery automatically regenerated or resent a saved result")
	}
	// The platform confirms the send made after the snapshot. Resolving it must
	// record that receipt without another platform request or model generation.
	h.call(r.server.URL, "/api/ext/v1/notifications/"+cached.ID+"/resolve", "POST", h.operator,
		map[string]string{"decision": "sent", "receipt": receipt}, 200)
	resolved, err := r.api.Notifications.Detail(cached.ID)
	if err != nil || resolved.Notification.Status != "sent" || resolved.Notification.Receipt == nil ||
		*resolved.Notification.Receipt != receipt || len(resolved.Resolutions) != 1 {
		t.Fatalf("restored outcome was not resolved from platform receipt: %+v %v", resolved, err)
	}
	r.deliver(false)

	r.receive("om_ai_source_changed")
	changed := r.queued("om_ai_source_changed")
	if r.provider.calls.Load() != 3 {
		t.Fatal("new event did not generate exactly once")
	}
	h.call(h.memos, "/api/v1/"+r.provider.name+"?updateMask=content", "PATCH", h.upstream,
		map[string]string{"content": "原有观察已删除，改为全新内容。"}, 200)
	r.deliver(true)
	want := "AI 回复（第 1 片）\n笔记来源已变化、删除或无法核实，AI 回复已终止。请重新发送请求。"
	if text := r.platform.reply("om_ai_source_changed"); text != want ||
		strings.Contains(text, strings.Split(r.provider.content, "\n")[0]) {
		t.Fatalf("changed official source leaked cached private content: %q", text)
	}
	detail, err := r.api.Notifications.Detail(changed.ID)
	if err != nil || detail.Notification.Status != "sent" || len(detail.Attempts) != 1 ||
		r.provider.calls.Load() != 3 || r.platform.replyCount() != 3 {
		t.Fatalf("source protection regenerated or resent content: calls=%d sends=%d detail=%+v err=%v",
			r.provider.calls.Load(), r.platform.replyCount(), detail, err)
	}
	r.deliver(false)
}

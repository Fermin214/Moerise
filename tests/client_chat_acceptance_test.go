//go:build integration

package acceptance

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	moerise "moerise/internal"
)

type chatAcceptanceModel struct {
	key      string
	mode     atomic.Int32
	mu       sync.Mutex
	requests [][]moerise.LLMMessage
	release  chan struct{}
}

const chatAcceptanceReply = "  ## 笔记原意\n\n保留原 Markdown、换行与 #联想 标签。 😀\n\n这是一种可能的解释。  \n"

func (m *chatAcceptanceModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model    string               `json:"model"`
		Messages []moerise.LLMMessage `json:"messages"`
		Stream   bool                 `json:"stream"`
	}
	if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost ||
		r.Header.Get("Authorization") != "Bearer "+m.key ||
		json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "chat-acceptance" || !body.Stream {
		http.Error(w, "invalid fixture request", 400)
		return
	}
	m.mu.Lock()
	m.requests = append(m.requests, body.Messages)
	m.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := map[string]any{"model": body.Model, "choices": []any{map[string]any{
		"index": 0, "delta": map[string]any{"content": chatAcceptanceReply}, "finish_reason": nil,
	}}}
	data, _ := json.Marshal(chunk)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
	switch m.mode.Load() {
	case 1:
		<-r.Context().Done()
		return
	case 2:
		return
	case 3:
		select {
		case <-m.release:
		case <-r.Context().Done():
			return
		}
	}
	stop := map[string]any{"model": body.Model, "choices": []any{map[string]any{
		"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
	}}}
	data, _ = json.Marshal(stop)
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
}

func (m *chatAcceptanceModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *chatAcceptanceModel) messages(index int) []moerise.LLMMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.requests[index])
}

func acceptanceChat(t *testing.T, value map[string]any) moerise.ChatSession {
	t.Helper()
	var chat moerise.ChatSession
	data, err := json.Marshal(value)
	if err != nil || json.Unmarshal(data, &chat) != nil || chat.ID == "" {
		t.Fatal("invalid chat response")
	}
	return chat
}

func openAcceptanceChatStream(t *testing.T, c *clientHarness, id string, input moerise.ChatGenerate) (*http.Response, *bufio.Scanner) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, c.h.base+"/api/app/v1/chats/"+id+"/messages", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(c.cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", c.h.settings.Web.PublicOrigin)
	request.Header.Set("X-CSRF-Token", c.csrf)
	response, err := c.h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") ||
		response.Header.Get("Cache-Control") != "no-store" {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("chat did not open a private stream: %d %s", response.StatusCode, data)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 8*1024*1024)
	return response, scanner
}

func completeAcceptanceChat(t *testing.T, c *clientHarness, id string, input moerise.ChatGenerate) moerise.ChatTurn {
	t.Helper()
	response, scanner := openAcceptanceChatStream(t, c, id, input)
	defer response.Body.Close()
	events := []string{}
	var result moerise.ChatTurn
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
		if strings.HasPrefix(line, "data: ") {
			var event moerise.ChatEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "complete" && event.Turn != nil {
				result = *event.Turn
			}
		}
	}
	if scanner.Err() != nil || len(events) < 2 || events[0] != "message" || events[len(events)-1] != "complete" ||
		slices.Contains(events, "error") || result.Assistant.Status != "completed" || result.Assistant.Content != chatAcceptanceReply {
		t.Fatalf("chat did not explicitly complete with original text: events=%v status=%s error=%v",
			events, result.Assistant.Status, scanner.Err())
	}
	return result
}

func TestClientChatMemosAcceptance(t *testing.T) {
	model := &chatAcceptanceModel{key: randomToken(), release: make(chan struct{}, 1)}
	server := httptest.NewServer(model)
	t.Cleanup(server.Close)
	h := newHarness(t)
	h.settings.AccessTokens[1].Scopes = append(h.settings.AccessTokens[1].Scopes, "ai:generate", "reviews:write")
	c := managedClient(t, h)
	h.client.Timeout = 30 * time.Second
	prompt := "明确区分原文与联想，讨论这组完整材料。"
	settings := c.call("PATCH", "/api/app/v1/settings", map[string]any{
		"version": 1,
		"settings": map[string]any{
			"llm": map[string]any{"enabled": true, "base_url": server.URL + "/v1", "model": "chat-acceptance",
				"max_retries": 0, "context_bytes": 128000},
			"embedding": map[string]any{"enabled": false},
			"insights":  map[string]any{"default_prompt": prompt},
		},
		"secrets": map[string]any{"llm_api_key": map[string]any{"action": "replace", "value": model.key}},
	}, 200)
	updateSettings := func(changes map[string]any) {
		t.Helper()
		settings = c.call("PATCH", "/api/app/v1/settings", map[string]any{"version": settings["version"], "settings": changes}, 200)
	}
	models := c.call("GET", "/api/app/v1/models", nil, 200)
	if models["embedding"].(map[string]any)["enabled"] != false {
		t.Fatal("chat acceptance must run without Embedding")
	}
	original := "  研究：叶片与光照 #材料 #共同\n\n完整原文 😀  \n"
	content := map[string]string{
		"memos/chat-normal":     original,
		"memos/chat-archive":    "研究：归档的观察也参与讨论。 #材料 #共同\n",
		"memos/chat-duplicate":  original,
		"memos/chat-unselected": "研究 UNSELECTED_MATERIAL #材料",
	}
	names := []string{"memos/chat-normal", "memos/chat-archive", "memos/chat-duplicate"}
	for _, id := range []string{"chat-normal", "chat-archive", "chat-duplicate", "chat-unselected"} {
		h.call(h.memos, "/api/v1/memos?memoId="+id, "POST", h.upstream,
			map[string]string{"content": content["memos/"+id], "visibility": "PRIVATE"}, 200)
		patch := map[string]any{"createTime": "2026-10-20T08:00:00Z"}
		mask := "create_time"
		if id == "chat-archive" {
			patch["state"], mask = "ARCHIVED", "create_time,state"
		}
		h.call(h.memos, "/api/v1/memos/"+id+"?updateMask="+mask, "PATCH", h.upstream, patch, 200)
	}
	query := url.Values{"page_size": {"1"}, "query": {"研究"}, "tag": {"材料", "共同"},
		"from": {"2026-10-20"}, "until": {"2026-10-21"}}
	selected := map[string]bool{}
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("selection pagination did not terminate")
		}
		result := c.call("GET", "/api/app/v1/chat-materials?"+query.Encode(), nil, 200)
		for _, raw := range result["notes"].([]any) {
			note := raw.(map[string]any)
			name := note["name"].(string)
			if !slices.Contains(names, name) || selected[name] || note["content"] != content[name] {
				t.Fatal("selection duplicated, filtered, or rewrote a selected source")
			}
			selected[name] = true
		}
		token := result["next_page_token"].(string)
		if token == "" {
			break
		}
		query.Set("page_token", token)
	}
	if len(selected) != len(names) {
		t.Fatal("combined selection lost normal or archived sources")
	}
	resolved := c.call("POST", "/api/app/v1/chat-materials/resolve", map[string]any{"names": names}, 200)
	wantCharacters := 0
	for _, name := range names {
		wantCharacters += utf8.RuneCountInString(content[name])
	}
	if resolved["characters"] != float64(wantCharacters) {
		t.Fatal("material character count is not the full Unicode count")
	}
	t.Log("PASS real normal/archive pagination with combined date, keyword and exact AND tags")

	creation := map[string]any{"request_id": "chat-original", "title": "真实材料讨论", "names": names}
	chat := acceptanceChat(t, c.call("POST", "/api/app/v1/chats", creation, 201))
	if replay := acceptanceChat(t, c.call("POST", "/api/app/v1/chats", creation, 200)); replay.ID != chat.ID || model.calls() != 0 {
		t.Fatal("chat creation was duplicated or called the model")
	}
	base := "/api/app/v1/chats/" + chat.ID
	firstInput := moerise.ChatGenerate{RequestID: "first", Version: chat.Version, Kind: "insight"}
	first := completeAcceptanceChat(t, c, chat.ID, firstInput)
	messages := model.messages(0)
	var materialBody []struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if len(messages) != 3 || messages[2].Content != prompt ||
		json.Unmarshal([]byte(strings.TrimPrefix(messages[1].Content, "本轮明确选定的全部笔记材料：\n")), &materialBody) != nil ||
		len(materialBody) != len(names) {
		t.Fatal("model did not receive the complete explicit materials and saved Prompt")
	}
	for i, source := range materialBody {
		if source.Name != names[i] || source.Content != content[names[i]] {
			t.Fatal("model materials were deduplicated by content, shortened or reordered")
		}
	}
	completeAcceptanceChat(t, c, chat.ID, firstInput)
	if model.calls() != 1 {
		t.Fatal("same request reran the model")
	}
	question := "还有哪些可能的解释？请保持材料原意。"
	follow := completeAcceptanceChat(t, c, chat.ID, moerise.ChatGenerate{
		RequestID: "follow", Version: chat.Version, Kind: "question", Content: question,
	})
	messages = model.messages(1)
	if len(messages) != 5 || messages[2].Content != prompt ||
		messages[3].Content != chatAcceptanceReply || messages[4].Content != question {
		t.Fatal("free followup lost successful conversation or changed the user's question")
	}
	saved := c.call("POST", base+"/messages/first/save", map[string]any{"version": first.Assistant.Version}, 201)
	replayedSave := c.call("POST", base+"/messages/first/save", map[string]any{"version": first.Assistant.Version}, 200)
	sourceSaved := h.call(h.memos, "/api/v1/"+saved["name"].(string), "GET", h.upstream, nil, 200)
	if saved["name"] != replayedSave["name"] || sourceSaved["content"] != chatAcceptanceReply ||
		saved["content"] != chatAcceptanceReply {
		t.Fatal("saved reply was rewritten or created more than once")
	}
	c.call("POST", base+"/messages/first/save", map[string]any{"version": first.Assistant.Version + 1}, 409)
	before := acceptanceChat(t, c.call("GET", base, nil, 200))
	if len(before.Turns) != 2 || before.Turns[0].Assistant.SavedName != saved["name"] || before.Turns[1].ID != follow.ID {
		t.Fatal("save association or successful messages were not persisted")
	}
	t.Log("PASS natural streaming, complete materials, followup, request replay and original Markdown save")

	calls := model.calls()
	h.stop()
	h.start()
	if after := acceptanceChat(t, c.call("GET", base, nil, 200)); !reflect.DeepEqual(before, after) || model.calls() != calls {
		t.Fatal("ordinary restart lost chat or regenerated a reply")
	}
	snapshot := filepath.Join(h.root, "chat-snapshot")
	if _, err := moerise.Backup(h.settings, snapshot); err != nil {
		t.Fatal(err)
	}
	h.stop()
	h.command(true, "rebuild-index")
	h.start()
	if after := acceptanceChat(t, c.call("GET", base, nil, 200)); !reflect.DeepEqual(before, after) {
		t.Fatal("index rebuild changed product chat state")
	}
	h.stop()
	if err := moerise.Restore(h.settings, snapshot, filepath.Join(h.root, "before-chat-restore")); err != nil {
		t.Fatal(err)
	}
	h.start()
	c.call("GET", base, nil, 401)
	c.call("POST", "/api/app/v1/session", map[string]string{"username": "acceptance-owner", "password": h.password}, 200)
	if after := acceptanceChat(t, c.call("GET", base, nil, 200)); !reflect.DeepEqual(before, after) || model.calls() != calls {
		t.Fatal("backup restore changed chat, save association or model call count")
	}
	c.call("POST", base+"/messages/first/save", map[string]any{"version": first.Assistant.Version}, 200)
	t.Log("PASS restart, index rebuild and backup restore preserve history and saves without regeneration")

	largeNames := []string{}
	for i := range 3 {
		c.key = fmt.Sprintf("chat-oversize-material-%d", i)
		large := c.call("POST", "/api/app/v1/notes", map[string]any{"content": strings.Repeat("m", 7000)}, 201)
		largeNames = append(largeNames, large["name"].(string))
	}
	over := acceptanceChat(t, c.call("POST", "/api/app/v1/chats", map[string]any{
		"request_id": "chat-over", "title": "材料上限", "names": largeNames,
	}, 201))
	if value := c.call("POST", "/api/app/v1/chat-materials/resolve",
		map[string]any{"names": largeNames}, 200); value["characters"] != float64(21000) {
		t.Fatal("oversize resolution did not report the intact material")
	}
	rejected := c.call("POST", "/api/app/v1/chats/"+over.ID+"/messages",
		moerise.ChatGenerate{RequestID: "over", Version: over.Version, Kind: "insight"}, 422)
	if rejected["error"].(map[string]any)["code"] != "CHAT_MATERIAL_LIMIT" {
		t.Fatal("oversize materials did not fail explicitly")
	}
	if model.calls() != calls {
		t.Fatal("oversize materials reached the model")
	}
	if after := acceptanceChat(t, c.call("GET", base, nil, 200)); !reflect.DeepEqual(before, after) {
		t.Fatal("budget failures deleted or changed persistent messages")
	}
	updateSettings(map[string]any{"insights": map[string]any{"default_prompt": "新的讨论视角"}})
	t.Log("PASS material overflow fails before the model without truncation or history removal")

	content[names[0]] = "来源已明确更新。 #材料 #共同"
	h.call(h.memos, "/api/v1/"+names[0]+"?updateMask=content", "PATCH", h.upstream,
		map[string]string{"content": content[names[0]]}, 200)
	rejected = c.call("POST", base+"/messages", moerise.ChatGenerate{
		RequestID: "stale-source", Version: chat.Version, Kind: "insight",
	}, 409)
	if rejected["error"].(map[string]any)["code"] != "SOURCE_CHANGED" || model.calls() != calls {
		t.Fatal("changed sources reached the model")
	}
	chat = acceptanceChat(t, c.call("PATCH", base, map[string]any{"version": chat.Version, "title": chat.Title, "names": names}, 200))
	updated := completeAcceptanceChat(t, c, chat.ID, moerise.ChatGenerate{
		RequestID: "reselected", Version: chat.Version, Kind: "question", Content: "根据重新选择的材料继续。",
	})
	if updated.Prompt != "新的讨论视角" || updated.SettingsVersion <= first.SettingsVersion ||
		updated.Materials[0].SourceHash == first.Materials[0].SourceHash || model.calls() != calls+1 {
		t.Fatal("explicit reselection failed to freeze the new source or settings version")
	}
	if data, _ := json.Marshal(model.messages(calls)); !bytes.Contains(data, []byte(content[names[0]])) {
		t.Fatal("reselected model input lost the new full original")
	}
	calls = model.calls()
	h.call(h.memos, "/api/v1/"+names[1], "DELETE", h.upstream, nil, 200)
	rejected = c.call("POST", base+"/messages", moerise.ChatGenerate{
		RequestID: "deleted-source", Version: chat.Version, Kind: "insight",
	}, 404)
	if rejected["error"].(map[string]any)["code"] != "SOURCE_NOT_FOUND" || model.calls() != calls ||
		len(acceptanceChat(t, c.call("GET", base, nil, 200)).Turns) != 3 {
		t.Fatal("deleted source reached the model or removed historical messages")
	}
	t.Log("PASS changed and deleted sources block generation while preserving historical messages")

	otherPassword := randomToken()
	h.call(h.memos, "/api/v1/users", "POST", h.upstream,
		map[string]string{"username": "chat-other", "password": otherPassword, "role": "USER"}, 200)
	otherLogin := h.call(h.memos, "/api/v1/auth/signin", "POST", "",
		map[string]any{"passwordCredentials": map[string]string{"username": "chat-other", "password": otherPassword}}, 200)
	foreign := h.call(h.memos, "/api/v1/memos", "POST", otherLogin["accessToken"].(string),
		map[string]string{"content": "其他作者公开材料 #材料 #共同", "visibility": "PUBLIC"}, 200)
	c.call("POST", "/api/app/v1/chat-materials/resolve", map[string]any{"names": []string{foreign["name"].(string)}}, 404)
	c.call("POST", "/api/app/v1/chats", map[string]any{"request_id": "foreign",
		"title": "越权材料", "names": []string{foreign["name"].(string)}}, 404)
	for _, path := range []string{"/api/app/v1/chats", "/api/app/v1/chat-materials"} {
		h.call(h.base, path, "GET", h.operator, nil, 403)
	}
	for _, path := range []string{"/api/ext/v1/chats", "/api/ext/v1/chat-materials", "/api/ext/v1/chats/" + chat.ID + "/messages"} {
		h.call(h.base, path, "POST", h.operator, map[string]any{}, 403)
	}
	mcp := func(method string, params map[string]any) map[string]any {
		t.Helper()
		value, status, err := h.requestWithHeaders(h.base, "/mcp", "POST", h.operator,
			map[string]any{"jsonrpc": "2.0", "id": "chat-machine", "method": method, "params": params},
			map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
		if err != nil || status != 200 {
			t.Fatalf("MCP chat isolation: status=%d err=%v", status, err)
		}
		return value
	}
	for _, raw := range mcp("tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any) {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		if strings.Contains(name, "chat") || strings.Contains(name, "generate") || strings.Contains(name, "save") ||
			tool["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Fatal("machine MCP exposed a chat or write tool")
		}
	}
	for _, name := range []string{"create_chat", "generate_chat", "save_chat_message"} {
		if value := mcp("tools/call", map[string]any{"name": name, "arguments": map[string]any{}}); value["error"] == nil {
			t.Fatal("machine MCP accepted a chat operation")
		}
	}
	if model.calls() != calls {
		t.Fatal("unauthorized or machine requests reached the model")
	}
	t.Log("PASS author restriction and machine REST/MCP isolation with write scopes")

	model.mode.Store(3)
	changing := acceptanceChat(t, c.call("POST", "/api/app/v1/chats", map[string]any{
		"request_id": "source-during-stream", "title": "流中来源变更", "names": []string{names[0]},
	}, 201))
	response, scanner := openAcceptanceChatStream(t, c, changing.ID, moerise.ChatGenerate{
		RequestID: "changing", Version: changing.Version, Kind: "question", Content: "完成前复查来源。",
	})
	var streamText strings.Builder
	for scanner.Scan() {
		streamText.WriteString(scanner.Text())
		if scanner.Text() == "event: delta" {
			break
		}
	}
	if !strings.Contains(streamText.String(), "event: delta") {
		t.Fatalf("stream never delivered before the source edit: %v", scanner.Err())
	}
	h.call(h.memos, "/api/v1/"+names[0]+"?updateMask=content", "PATCH", h.upstream,
		map[string]string{"content": "流中再次修改的来源。"}, 200)
	model.release <- struct{}{}
	for scanner.Scan() {
		streamText.WriteString(scanner.Text())
	}
	response.Body.Close()
	changed := acceptanceChat(t, c.call("GET", "/api/app/v1/chats/"+changing.ID, nil, 200)).Turns[0]
	if scanner.Err() != nil || changed.Assistant.Status != "failed" || changed.Assistant.ErrorCode != "SOURCE_CHANGED" ||
		changed.Assistant.Content != chatAcceptanceReply || strings.Contains(streamText.String(), "event: complete") ||
		!strings.Contains(streamText.String(), "event: error") {
		t.Fatal("source change during streaming was published as successful completion or lost partial text")
	}
	t.Log("PASS live source changes before completion cannot publish a successful reply")

	for _, mode := range []string{"cancel", "disconnect", "restart", "protocol", "logout"} {
		model.mode.Store(1)
		if mode == "protocol" {
			model.mode.Store(2)
		}
		current := acceptanceChat(t, c.call("POST", "/api/app/v1/chats", map[string]any{
			"request_id": "terminal-" + mode, "title": mode, "names": []string{names[0]},
		}, 201))
		input := moerise.ChatGenerate{RequestID: "terminal", Version: current.Version, Kind: "question", Content: "检查流中断。"}
		response, scanner := openAcceptanceChatStream(t, c, current.ID, input)
		delivered := false
		for scanner.Scan() {
			if scanner.Text() == "event: delta" {
				delivered = true
				break
			}
		}
		if !delivered {
			t.Fatalf("%s did not expose a durable partial reply: %v", mode, scanner.Err())
		}
		path := "/api/app/v1/chats/" + current.ID
		expected := "interrupted"
		switch mode {
		case "cancel":
			c.call("POST", path+"/messages/terminal/cancel", map[string]any{}, 200)
			expected = "canceled"
		case "restart":
			h.stop()
			response.Body.Close()
			h.start()
		case "protocol":
			expected = "failed"
		case "logout":
			c.call("DELETE", "/api/app/v1/session", nil, 204)
			c.call("GET", path, nil, 401)
			c.call("POST", "/api/app/v1/session", map[string]string{"username": "acceptance-owner", "password": h.password}, 200)
		}
		response.Body.Close()
		var terminal moerise.ChatTurn
		wait(t, "durable chat terminal "+mode, func() bool {
			current = acceptanceChat(t, c.call("GET", path, nil, 200))
			if len(current.Turns) != 1 || current.Turns[0].Assistant.Status == "running" {
				return false
			}
			terminal = current.Turns[0]
			return true
		})
		if terminal.Assistant.Status != expected || terminal.Assistant.Content != chatAcceptanceReply {
			t.Fatalf("%s terminal lost partial text or status: %s", mode, terminal.Assistant.Status)
		}
		if mode == "protocol" && terminal.Assistant.ErrorCode != "LLM_PROTOCOL" {
			t.Fatal("supplier EOF was mistaken for completion")
		}
		c.call("POST", path+"/messages/terminal/save", map[string]any{"version": terminal.Assistant.Version}, 409)
		beforeReplay := model.calls()
		replayed, scan := openAcceptanceChatStream(t, c, current.ID, input)
		var replayText strings.Builder
		for scan.Scan() {
			replayText.WriteString(scan.Text())
		}
		replayed.Body.Close()
		if scan.Err() != nil || model.calls() != beforeReplay || strings.Contains(replayText.String(), "event: complete") ||
			!strings.Contains(replayText.String(), "event: error") {
			t.Fatal("terminal request replay regenerated or claimed successful completion")
		}
		t.Log("PASS durable partial reply and no automatic regeneration:", mode)
	}
	model.mode.Store(0)
	chat = acceptanceChat(t, c.call("GET", base, nil, 200))
	c.call("DELETE", base, map[string]any{"version": chat.Version}, 204)
	c.call("GET", base, nil, 404)
	if source := h.call(h.memos, "/api/v1/"+saved["name"].(string), "GET", h.upstream, nil, 200); source["content"] != chatAcceptanceReply {
		t.Fatal("deleting a chat deleted or changed the saved Memos note")
	}
	db := h.database("state")
	defer db.Close()
	for _, check := range []struct{ table, column, id string }{
		{"chat_turns", "chat_id", chat.ID},
		{"chat_materials", "chat_id", chat.ID},
		{"chat_saves", "turn_id", first.ID},
	} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+check.table+" WHERE "+check.column+" = ?", check.id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("delete did not clear chat association %s: count=%d error=%v", check.table, count, err)
		}
	}
	t.Log("PASS active deletion clears chat product state and keeps the original saved Memos note")
}

func TestClientChatMaterialCharacterLimitMemosAcceptance(t *testing.T) {
	model := &chatAcceptanceModel{key: randomToken(), release: make(chan struct{}, 1)}
	server := httptest.NewServer(model)
	t.Cleanup(server.Close)
	h := newHarness(t)
	address, modelName := server.URL+"/v1", "chat-acceptance"
	h.settings.LLM.Enabled, h.settings.LLM.BaseURL = true, &address
	h.settings.LLM.APIKey, h.settings.LLM.Model = &model.key, &modelName
	h.settings.LLM.ContextBytes, h.settings.LLM.MaxRetries = 1024, 0
	c := managedClient(t, h)
	exactNames, exactContents := []string{}, []string{}
	for i := range 10 {
		c.key = fmt.Sprintf("chat-exact-material-%d", i)
		text := strings.Repeat("字", 2000)
		if i == 9 {
			text = strings.Repeat("😀", 2000)
		}
		exactNote := c.call("POST", "/api/app/v1/notes", map[string]any{"content": text}, 201)
		exactNames = append(exactNames, exactNote["name"].(string))
		exactContents = append(exactContents, text)
	}
	exact := acceptanceChat(t, c.call("POST", "/api/app/v1/chats", map[string]any{
		"request_id": "chat-exact", "title": "字符边界", "names": exactNames,
	}, 201))
	firstExact := completeAcceptanceChat(t, c, exact.ID, moerise.ChatGenerate{
		RequestID: "exact-first", Version: exact.Version, Kind: "insight",
	})
	longQuestion := strings.Repeat("问", 20001)
	completeAcceptanceChat(t, c, exact.ID, moerise.ChatGenerate{
		RequestID: "exact-follow", Version: exact.Version, Kind: "question", Content: longQuestion,
	})
	sent := model.messages(1)
	if len(sent) != 5 || sent[2].Content != firstExact.User.Content ||
		sent[3].Content != firstExact.Assistant.Content || sent[4].Content != longQuestion || model.calls() != 2 {
		t.Fatal("exact material boundary or full follow-up was rejected or truncated")
	}
	var exactMaterials []struct{ Content string }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(sent[1].Content, "本轮明确选定的全部笔记材料：\n")), &exactMaterials); err != nil ||
		len(exactMaterials) != len(exactContents) {
		t.Fatal("complete Unicode materials did not reach the model")
	}
	for i, material := range exactMaterials {
		if material.Content != exactContents[i] {
			t.Fatal("Unicode materials were shortened or reordered")
		}
	}

	t.Log("PASS exactly 20000 Unicode material characters and complete follow-up pass independently of legacy byte budgets")
}

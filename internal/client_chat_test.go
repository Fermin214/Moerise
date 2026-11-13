package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

func clientChatFixture(t *testing.T) (*ClientAPI, *sessionAuthFixture, *http.Cookie, *SessionIdentity) {
	t.Helper()
	api, fixture := clientFixture(t)
	store := api.app.Store
	store.Settings.LLM.Enabled = true
	model := "chat-client-test"
	store.Settings.LLM.Model = &model
	source := fakeClient(store.Settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if r.URL.Path == "/api/v1/memos/one" {
			return 200, rawMemo("one", "  原文 #标签\n"), nil
		}
		return 404, nil, nil
	})
	api.app.Notes = NewNoteService(store.Settings, store, source)
	api.app.Chat.Notes, api.app.Chat.Records = api.app.Notes, NewRecordService(store, source)
	api.app.Chat.Provider = textChatProvider("## 自然回复\n\n可能存在另一种解释。\n")
	t.Cleanup(api.app.Chat.Close)
	rawID, identity, err := api.app.Sessions.Login(t.Context(), "owner", "correct")
	if err != nil {
		t.Fatal(err)
	}
	return api, fixture, &http.Cookie{Name: api.cookieName(), Value: rawID}, identity
}

func createClientChat(t *testing.T, api *ClientAPI, fixture *sessionAuthFixture, cookie *http.Cookie, identity *SessionIdentity) ChatSession {
	t.Helper()
	response := clientCall(api, "POST", "/api/app/v1/chats",
		`{"request_id":"create","title":"讨论","names":["memos/one"]}`, cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	var chat ChatSession
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &chat) != nil || chat.ID == "" {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	return chat
}

func TestClientChatLifecycleStrictRequestsAndSSE(t *testing.T) {
	api, fixture, cookie, identity := clientChatFixture(t)
	chat := createClientChat(t, api, fixture, cookie, identity)
	base := "/api/app/v1/chats/" + chat.ID
	for _, test := range []struct {
		method, path, body, code string
		status                   int
	}{
		{"POST", "/api/app/v1/chats", `{"request_id":"other","names":["memos/one"],"extra":true}`, "INVALID_ARGUMENT", 422},
		{"POST", base + "/messages", `{"request_id":"bad","version":1,"kind":"question","content":"a","content":"b"}`, "INVALID_ARGUMENT", 422},
		{"POST", base + "/messages", `{"request_id":"bad","version":1,"kind":null}`, "INVALID_ARGUMENT", 422},
		{"POST", base + "/messages?x=1", `{}`, "INVALID_ARGUMENT", 422},
		{"GET", base + "?version=1", ``, "INVALID_ARGUMENT", 422},
		{"GET", "/api/app/v1/chats?page_size=0", ``, "INVALID_ARGUMENT", 422},
		{"GET", "/api/app/v1/chats?page_token=bad", ``, "INVALID_PAGE_TOKEN", 422},
		{"GET", "/api/app/v1/chats?unknown=1", ``, "INVALID_ARGUMENT", 422},
		{"GET", "/api/app/v1/chats?state=ALL&state=NORMAL", ``, "INVALID_ARGUMENT", 422},
		{"PATCH", base, `{"version":9,"title":"stale","names":["memos/one"]}`, "CHAT_CONFLICT", 409},
		{"DELETE", base, `{"version":9}`, "CHAT_CONFLICT", 409},
		{"POST", base + "/messages", `{"request_id":"old","version":9,"kind":"question","content":"stale"}`, "CHAT_CONFLICT", 409},
	} {
		response := clientCall(api, test.method, test.path, test.body, cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
			t.Errorf("%s %s: %d %s", test.method, test.path, response.Code, response.Body.String())
		}
	}
	input := `{"request_id":"first","version":1,"kind":"insight","content":""}`
	response := clientCall(api, "POST", base+"/messages", input, cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if response.Code != 200 || response.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		response.Header().Get("X-Accel-Buffering") != "no" || response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(response.Body.String(), "event: message\n") || !strings.Contains(response.Body.String(), "event: delta\n") ||
		!strings.Contains(response.Body.String(), "event: complete\n") || strings.Contains(response.Body.String(), "event: error") {
		t.Fatalf("SSE contract: %d %v %s", response.Code, response.Header(), response.Body.String())
	}
	loaded := clientCall(api, "GET", base, "", cookie, "", "")
	var result ChatSession
	if loaded.Code != 200 || json.Unmarshal(loaded.Body.Bytes(), &result) != nil || len(result.Turns) != 1 ||
		result.Turns[0].Assistant.Status != "completed" || result.Turns[0].Assistant.Content != "## 自然回复\n\n可能存在另一种解释。\n" {
		t.Fatalf("persistent response: %d %s", loaded.Code, loaded.Body.String())
	}
	validateAPISchema(t, clientChatSchema(t, "ChatSession"), loaded.Body.Bytes())
	eventSchema := clientChatSchema(t, "ChatEvent")
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			validateAPISchema(t, eventSchema, []byte(strings.TrimPrefix(line, "data: ")))
		}
	}
	replay := clientCall(api, "POST", base+"/messages", input, cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if replay.Code != 200 || strings.Contains(replay.Body.String(), "event: delta") || !strings.Contains(replay.Body.String(), "event: complete") {
		t.Fatalf("generation replay: %s", replay.Body.String())
	}
	for _, path := range []string{"/api/app/v1/chats?page_size=1", base} {
		response := clientCall(api, "GET", path, "", cookie, "", "")
		if response.Code != 200 || !strings.Contains(response.Body.String(), chat.ID) ||
			strings.Contains(response.Body.String(), "sessionHash") || strings.Contains(response.Body.String(), fixture.settings.Memos.Token) {
			t.Fatalf("private chat scope: %d %s", response.Code, response.Body.String())
		}
	}
	deleted := clientCall(api, "DELETE", base, `{"version":1}`, cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if deleted.Code != 204 {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if response := clientCall(api, "GET", base, "", cookie, "", ""); response.Code != 404 {
		t.Fatalf("deleted chat remained readable: %d", response.Code)
	}
}

func TestClientChatRequiresSessionOriginCSRFAndMachineIsolation(t *testing.T) {
	api, fixture, cookie, identity := clientChatFixture(t)
	chat := createClientChat(t, api, fixture, cookie, identity)
	for _, route := range []struct{ method, path, body string }{
		{"GET", "/chats", ""},
		{"GET", "/chat-materials", ""},
		{"POST", "/chat-materials/resolve", `{"names":["memos/one"]}`},
		{"POST", "/chats", `{"request_id":"second","title":"","names":["memos/one"]}`},
		{"GET", "/chats/" + chat.ID, ""},
		{"PATCH", "/chats/" + chat.ID, `{"version":1,"title":"title","names":["memos/one"]}`},
		{"DELETE", "/chats/" + chat.ID, `{"version":1}`},
		{"POST", "/chats/" + chat.ID + "/messages", `{"request_id":"first","version":1,"kind":"question","content":"问题"}`},
		{"POST", "/chats/" + chat.ID + "/messages/first/cancel", `{}`},
		{"POST", "/chats/" + chat.ID + "/messages/first/save", `{"version":1}`},
	} {
		path := "/api/app/v1" + route.path
		if response := clientCall(api, route.method, path, route.body, nil, fixture.settings.Web.PublicOrigin, ""); response.Code != 401 {
			t.Errorf("anonymous %s: %d %s", route.path, response.Code, response.Body.String())
		}
		request := httptest.NewRequest(route.method, path, strings.NewReader(route.body))
		request.Header.Set("Authorization", "Bearer "+fixture.settings.AccessTokens[0].Token)
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		if recorder.Code != 403 {
			t.Errorf("machine %s: %d", route.path, recorder.Code)
		}
		if route.method == "GET" {
			continue
		}
		for _, check := range []struct{ origin, csrf string }{
			{"", identity.CSRFToken}, {"null", identity.CSRFToken}, {"https://other.invalid", identity.CSRFToken},
			{fixture.settings.Web.PublicOrigin, ""}, {fixture.settings.Web.PublicOrigin, "wrong"},
		} {
			response := clientCall(api, route.method, path, route.body, cookie, check.origin, check.csrf)
			if response.Code != 403 {
				t.Errorf("origin/CSRF %s: %d %s", route.path, response.Code, response.Body.String())
			}
		}
	}
	machine, err := NewAPI(api.app)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/ext/v1/chats", "/api/ext/v1/chat-materials"} {
		recorder := externalCall(machine, "POST", path, fixture.settings.AccessTokens[0].Token, `{}`)
		if recorder.Code != 403 {
			t.Errorf("machine chat endpoint: %d", recorder.Code)
		}
	}
}

func TestClientChatRemoveLastMaterialPreservesHistory(t *testing.T) {
	api, fixture, cookie, identity := clientChatFixture(t)
	chat := createClientChat(t, api, fixture, cookie, identity)
	base := "/api/app/v1/chats/" + chat.ID
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return clientCall(api, method, path, body, cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	}
	generated := call("POST", base+"/messages", `{"request_id":"first","version":1,"kind":"insight","content":""}`)
	if !strings.Contains(generated.Body.String(), "event: complete") {
		t.Fatalf("generate: %s", generated.Body.String())
	}
	cleared := call("PATCH", base, `{"version":1,"title":"讨论","names":[]}`)
	var empty ChatSession
	if cleared.Code != 200 || json.Unmarshal(cleared.Body.Bytes(), &empty) != nil || empty.MaterialCount != 0 ||
		len(empty.Materials) != 0 || len(empty.Turns) != 1 || empty.Turns[0].Assistant.Status != "completed" {
		t.Fatalf("empty selection lost history: %d %s", cleared.Code, cleared.Body.String())
	}
	validateAPISchema(t, clientChatSchema(t, "ChatSession"), cleared.Body.Bytes())
	blocked := call("POST", base+"/messages", `{"request_id":"empty","version":2,"kind":"insight","content":""}`)
	if blocked.Code != 422 || !strings.Contains(blocked.Body.String(), "INVALID_ARGUMENT") {
		t.Fatalf("empty selection generated: %d %s", blocked.Code, blocked.Body.String())
	}
	if response := call("PATCH", base, `{"version":2,"title":"讨论","names":null}`); response.Code != 422 {
		t.Fatalf("null names accepted: %d %s", response.Code, response.Body.String())
	}
	restored := call("PATCH", base, `{"version":2,"title":"讨论","names":["memos/one"]}`)
	if restored.Code != 200 {
		t.Fatalf("reselect: %d %s", restored.Code, restored.Body.String())
	}
}

func TestClientChatHistoryPreviewPreservesTitleAndUnicode(t *testing.T) {
	for _, kind := range []string{"question", "insight"} {
		t.Run(kind, func(t *testing.T) {
			api, fixture, cookie, identity := clientChatFixture(t)
			chat := createClientChat(t, api, fixture, cookie, identity)
			content, preview := strings.Repeat("😀", 90), strings.Repeat("😀", 80)
			if kind == "insight" {
				content, preview = "", "一键洞察"
			}
			input, err := json.Marshal(ChatGenerate{RequestID: "preview", Version: chat.Version, Kind: kind, Content: content})
			if err != nil {
				t.Fatal(err)
			}
			generated := clientCall(api, "POST", "/api/app/v1/chats/"+chat.ID+"/messages", string(input),
				cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
			if !strings.Contains(generated.Body.String(), "event: complete") {
				t.Fatalf("generate: %s", generated.Body.String())
			}
			listed := clientCall(api, "GET", "/api/app/v1/chats", "", cookie, "", "")
			var page struct {
				Chats []ChatSummary `json:"chats"`
			}
			if json.Unmarshal(listed.Body.Bytes(), &page) != nil || len(page.Chats) != 1 ||
				page.Chats[0].Preview != preview || page.Chats[0].Title != chat.Title {
				t.Fatalf("history preview: %s", listed.Body.String())
			}
			validateAPISchema(t, clientChatSchema(t, "ChatSummary"), canonicalJSON(page.Chats[0]))
			loaded := clientCall(api, "GET", "/api/app/v1/chats/"+chat.ID, "", cookie, "", "")
			var restored ChatSession
			if json.Unmarshal(loaded.Body.Bytes(), &restored) != nil || restored.Preview != preview ||
				restored.Turns[0].User.Content != map[string]string{"question": content, "insight": fixture.settings.Insights.DefaultPrompt}[kind] {
				t.Fatalf("preview changed full content: %s", loaded.Body.String())
			}
			validateAPISchema(t, clientChatSchema(t, "ChatSession"), loaded.Body.Bytes())
		})
	}
}

func TestClientChatStreamErrorsAndLogoutCancellation(t *testing.T) {
	for _, mode := range []string{"model-error", "panic", "logout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			api, fixture, cookie, identity := clientChatFixture(t)
			chat := createClientChat(t, api, fixture, cookie, identity)
			started := make(chan struct{})
			api.app.Chat.Provider = aiLLMFunc(func(ctx context.Context, _ []LLMMessage, emit func(string) error) (LLMResult, error) {
				if err := emit("部分回复"); err != nil {
					return LLMResult{}, err
				}
				switch mode {
				case "model-error":
					return LLMResult{}, failure("LLM_PROTOCOL", "PRIVATE_VENDOR_BODY", 502, false)
				case "panic":
					panic("PRIVATE_PANIC")
				default:
					close(started)
					<-ctx.Done()
					return LLMResult{}, ctx.Err()
				}
			})
			path := "/api/app/v1/chats/" + chat.ID + "/messages"
			call := func() *httptest.ResponseRecorder {
				return clientCall(api, "POST", path, `{"request_id":"first","version":1,"kind":"question","content":"问题"}`,
					cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
			}
			var response *httptest.ResponseRecorder
			if mode == "logout" || mode == "cancel" {
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- call() }()
				<-started
				method, target, body := "DELETE", "/api/app/v1/session", ""
				if mode == "cancel" {
					method, target, body = "POST", path+"/first/cancel", "{}"
				}
				stopped := clientCall(api, method, target, body, cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
				if stopped.Code != 204 && stopped.Code != 200 {
					t.Fatalf("stop: %d %s", stopped.Code, stopped.Body.String())
				}
				select {
				case response = <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("stop did not reach chat")
				}
			} else {
				response = call()
			}
			if response.Code != 200 || strings.Contains(response.Body.String(), "event: complete") ||
				strings.Contains(response.Body.String(), "PRIVATE_") {
				t.Fatalf("failed stream became success or exposed details: %d %s", response.Code, response.Body.String())
			}
			turn, err := api.app.Store.chatTurn(chat.ID, "first")
			if err != nil || turn.Assistant.Content != "部分回复" || turn.Assistant.Status == "completed" {
				t.Fatalf("failed stream not durable: %+v %v", turn, err)
			}
			if mode != "logout" && !strings.Contains(response.Body.String(), "event: error") {
				t.Fatalf("missing explicit error: %s", response.Body.String())
			}
		})
	}
}

func TestChatSelectionIncludesArchivedAndCombinesFilters(t *testing.T) {
	notes, source := newNotesFilterFixture(t)
	service := NewChatService(notes.Store, notes, nil, nil)
	defer service.Close()
	// 重新限定夹具，包含归档、相同正文、跨页条目和上界之外条目。
	source.rows = nil
	for _, row := range []struct{ name, state, content, created string }{
		{"one", "NORMAL", "共同关键词", "2026-10-01T00:00:00Z"},
		{"two", "ARCHIVED", "共同关键词", "2026-10-02T00:00:00Z"},
		{"three", "ARCHIVED", "共同关键词", "2026-10-03T00:00:00Z"},
		{"outside", "ARCHIVED", "共同关键词", "2026-11-01T00:00:00Z"},
		{"other", "NORMAL", "无关正文", "2026-10-04T00:00:00Z"},
	} {
		raw := rawMemo(row.name, row.content)
		raw["state"], raw["createTime"], raw["tags"] = row.state, row.created, []string{"C++", "R&D"}
		memo, err := normalizeMemo(raw)
		if err != nil {
			t.Fatal(err)
		}
		source.rows = append(source.rows, memo)
	}
	slices.SortFunc(source.rows, func(a, b Memo) int { return strings.Compare(b.CreateTime, a.CreateTime) })
	service.Store.Settings.Timezone = "UTC"
	input := ChatSelection{State: "ALL", Query: "关键词", Tags: []string{"R&D", "C++"}, FromDate: "2026-10-01", UntilDate: "2026-11-01", PageSize: 1}
	names := []string{}
	for range 10 {
		page, err := service.Select(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		for _, note := range page.Notes {
			names = append(names, note.Name)
		}
		if page.NextPageToken == "" {
			break
		}
		input.PageToken = page.NextPageToken
	}
	if !reflect.DeepEqual(names, []string{"memos/one", "memos/three", "memos/two"}) {
		t.Fatalf("selected archive omitted, duplicate, or filter ignored: %v", names)
	}
	input.PageToken = ""
	first, err := service.Select(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.PageToken = first.NextPageToken
	input.Query = "changed"
	if _, err := service.Select(t.Context(), input); errorCode(err) != "INVALID_PAGE_TOKEN" {
		t.Fatalf("cross-filter token: %v", err)
	}
	for _, query := range []string{"from=2026-11-01&until=2026-10-01", "state=DELETED", "from=2026-02-30",
		"query=a&query=b", "unknown=x", "tag=", "page_size=101"} {
		parsed, _ := url.ParseQuery(query)
		input, err := parseChatSelection(parsed)
		if err == nil {
			_, err = service.Select(t.Context(), input)
		}
		if err == nil {
			t.Fatalf("accepted bad selection %s", query)
		}
	}
	if _, err := service.Select(t.Context(), ChatSelection{State: "NORMAL", PageSize: 1, PageToken: first.NextPageToken}); err == nil {
		t.Fatal("material cursor crossed state")
	}
}

func clientChatSchema(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	var spec struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(openAPIJSON, &spec); err != nil {
		t.Fatal(err)
	}
	defs := map[string]json.RawMessage{}
	pending := []string{name}
	references := regexp.MustCompile(`"#/components/schemas/([^"]+)"`)
	for len(pending) > 0 {
		key := pending[0]
		pending = pending[1:]
		if defs[key] != nil {
			continue
		}
		value := spec.Components.Schemas[key]
		if value == nil {
			t.Fatalf("missing OpenAPI chat schema %s", key)
		}
		defs[key] = json.RawMessage(strings.ReplaceAll(string(value), "#/components/schemas/", "#/$defs/"))
		for _, match := range references.FindAllStringSubmatch(string(value), -1) {
			pending = append(pending, match[1])
		}
	}
	data, err := json.Marshal(map[string]any{"$ref": "#/$defs/" + name, "$defs": defs})
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestClientChatPreflightFailureReturnsJSON(t *testing.T) {
	api, fixture, cookie, identity := clientChatFixture(t)
	chat := createClientChat(t, api, fixture, cookie, identity)
	api.app.Chat.Provider = nil
	response := clientCall(api, "POST", "/api/app/v1/chats/"+chat.ID+"/messages",
		`{"request_id":"first","version":1,"kind":"question","content":"问题"}`, cookie,
		fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if response.Code != 503 || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") ||
		!strings.Contains(response.Body.String(), "LLM_DISABLED") {
		t.Fatalf("preflight: %d %s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest("POST", "/api/app/v1/chats/"+chat.ID+"/messages", nil)
	bounded, cancel := chatRequestContext(request)
	defer cancel()
	deadline, ok := bounded.Context().Deadline()
	if !ok || time.Until(deadline) > chatRequestTimeout || time.Until(deadline) < chatRequestTimeout-time.Second {
		t.Fatal("chat auth and generation did not share a bounded deadline")
	}
	cancel()
	if !errors.Is(bounded.Context().Err(), context.Canceled) {
		t.Fatal("chat request deadline cannot cancel")
	}
}

type chatRevokingBody struct {
	*strings.Reader
	revoke func()
}

func (b *chatRevokingBody) Read(data []byte) (int, error) {
	if b.revoke != nil {
		revoke := b.revoke
		b.revoke = nil
		revoke()
	}
	return b.Reader.Read(data)
}

func (*chatRevokingBody) Close() error { return nil }

func TestClientChatRevocationDuringBodyCannotCreateState(t *testing.T) {
	api, fixture, cookie, identity := clientChatFixture(t)
	request := httptest.NewRequest("POST", "/api/app/v1/chats", nil)
	request.Body = &chatRevokingBody{Reader: strings.NewReader(`{"request_id":"revoked","title":"","names":["memos/one"]}`),
		revoke: func() {
			if err := api.app.Sessions.Logout(t.Context(), cookie.Value); err != nil {
				t.Fatal(err)
			}
		}}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", fixture.settings.Web.PublicOrigin)
	request.Header.Set("X-CSRF-Token", identity.CSRFToken)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	var count int
	if err := api.app.Store.state.QueryRow("SELECT COUNT(*) FROM chat_sessions").Scan(&count); err != nil ||
		count != 0 || response.Code >= 200 && response.Code < 300 {
		t.Fatalf("revoked request created chat state: count=%d status=%d error=%v", count, response.Code, err)
	}
}

func TestClientChatReplyLanguagePreservesPromptsMaterialsAndIdempotency(t *testing.T) {
	api, fixture, cookie, identity := clientChatFixture(t)
	chat := createClientChat(t, api, fixture, cookie, identity)
	path := "/api/app/v1/chats/" + chat.ID + "/messages"
	var calls [][]LLMMessage
	api.app.Chat.Provider = aiLLMFunc(func(_ context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
		calls = append(calls, messages)
		return LLMResult{Content: "A new connection", FinishReason: "stop"}, emit("A new connection")
	})
	send := func(body string) *httptest.ResponseRecorder {
		return clientCall(api, "POST", path, body, cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	}
	english := `{"request_id":"english","version":1,"kind":"insight","content":"","language":"en"}`
	response := send(english)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "event: complete") || len(calls) != 1 {
		t.Fatalf("English generation failed: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(calls[0][0].Content, "Reply in English") ||
		calls[0][len(calls[0])-1].Content != DefaultInsightPromptEnglish ||
		!strings.Contains(calls[0][1].Content, "原文 #标签") {
		t.Fatalf("language changed selected material or used the wrong prompt: %+v", calls[0])
	}
	response = send(english)
	if response.Code != 200 || len(calls) != 1 {
		t.Fatal("same-language replay generated another reply")
	}
	response = send(strings.Replace(english, `"language":"en"`, `"language":"zh-CN"`, 1))
	if response.Code != 409 || !strings.Contains(response.Body.String(), "IDEMPOTENCY_CONFLICT") || len(calls) != 1 {
		t.Fatal("changing the language reused an existing request ID")
	}
	for _, language := range []string{`"fr"`, `null`, `"EN"`} {
		response = send(`{"request_id":"bad-language","version":1,"kind":"insight","content":"","language":` + language + `}`)
		if response.Code != 422 || len(calls) != 1 {
			t.Fatalf("invalid language accepted: %s: %d", language, response.Code)
		}
	}
	custom := "保留自定义 Prompt exactly."
	api.app.Store.Settings.Insights.DefaultPrompt = custom
	response = send(`{"request_id":"custom","version":1,"kind":"insight","content":"","language":"en"}`)
	if response.Code != 200 || len(calls) != 2 || calls[1][len(calls[1])-1].Content != custom {
		t.Fatal("custom prompt was translated or discarded")
	}
	response = send(`{"request_id":"missing-language","version":1,"kind":"question","content":"继续"}`)
	if response.Code != 200 || len(calls) != 3 || !strings.Contains(calls[2][0].Content, "使用简体中文回复") {
		t.Fatal("omitted language did not preserve the Chinese default")
	}
	loaded, err := api.app.Store.chat(chat.ID)
	if err != nil || loaded.PreviewKind != "insight" || loaded.Turns[0].Prompt != DefaultInsightPromptEnglish ||
		loaded.Turns[1].Prompt != custom || api.app.Store.Settings.Insights.DefaultPrompt != custom {
		t.Fatalf("stored prompts or preview kind changed: %+v %v", loaded, err)
	}
}

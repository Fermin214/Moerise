package moerise

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMCPAuthenticationOriginAndPrivateResponses(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	client := fakeClient(settings, func(*http.Request) (int, any, error) {
		t.Error("MCP initialization accessed private notes")
		return 500, nil, nil
	})
	api, err := NewAPI(settings, store, client, NewSyncService(store, client))
	if err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	for _, test := range []struct {
		token, origin string
		status        int
	}{
		{"", "", 401},
		{settings.AccessTokens[0].Token, "https://foreign.example", 403},
		{settings.AccessTokens[0].Token, "", 200},
	} {
		request := httptest.NewRequest("POST", "https://moerise.example/mcp", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		if test.origin != "" {
			request.Header.Set("Origin", test.origin)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("MCP private response: %d %v %s", response.Code, response.Header(), response.Body.String())
		}
	}
}

func mcpReadFixture(t *testing.T) (*API, map[string]map[string]any) {
	t.Helper()
	reviews, memos := reviewFixture(t, 4)
	if err := reviews.Store.Advance(nowText(), true); err != nil {
		t.Fatal(err)
	}
	settings := reviews.Store.Settings
	settings.AccessTokens = append(settings.AccessTokens,
		AccessToken{"all", strings.Repeat("f", 40), []string{
			"notes:read", "notes:write", "reviews:read", "reviews:write", "discovery:write", "ai:generate"}},
		AccessToken{"reviews", strings.Repeat("v", 40), []string{"reviews:read"}},
		AccessToken{"writes", strings.Repeat("w", 40), []string{"notes:write", "reviews:write", "discovery:write"}})
	api, err := NewAPI(settings, reviews.Store, reviews.Source, NewSyncService(reviews.Store, reviews.Source))
	if err != nil {
		t.Fatal(err)
	}
	return api, memos
}

func mcpReadResult(t *testing.T, api *API, token, name string, args any) map[string]any {
	t.Helper()
	reply := aiMCPCall(t, api, token, "tools/call", map[string]any{"name": name, "arguments": args})
	result, _ := reply["result"].(map[string]any)
	if reply["error"] != nil || result == nil || result["isError"] == true {
		t.Fatalf("%s failed: %v", name, reply)
	}
	value, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("%s missing structured result: %v", name, result)
	}
	return value
}

func mcpReadError(t *testing.T, api *API, token, name string, args any, code string) {
	t.Helper()
	reply := aiMCPCall(t, api, token, "tools/call", map[string]any{"name": name, "arguments": args})
	result, _ := reply["result"].(map[string]any)
	if code == "" && (reply["error"] != nil || result["isError"] == true) {
		return
	}
	raw, _ := json.Marshal(result)
	if result["isError"] != true || !strings.Contains(string(raw), code) {
		t.Fatalf("%s: expected error %q, got %v", name, code, reply)
	}
}

func mcpUnknownTool(t *testing.T, api *API, token, name string) {
	t.Helper()
	reply := aiMCPCall(t, api, token, "tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
	rpcError, _ := reply["error"].(map[string]any)
	if rpcError["code"] != float64(-32602) || !strings.Contains(rpcError["message"].(string), "unknown tool") {
		t.Fatalf("%s reached a tool handler: %v", name, reply)
	}
}

func TestMCPReadOnlyDiscoveryAndDirectCalls(t *testing.T) {
	api, _ := mcpReadFixture(t)
	noteTools := []string{"get_memo", "list_memos", "semantic_search", "get_related_memos", "semantic_walk"}
	reviewTools := []string{"get_review_batch", "read_daily_review"}
	aiTools := []string{"analyze_memos", "ask_memos", "get_topic_timeline"}
	for _, test := range []struct {
		token string
		want  []string
	}{
		{strings.Repeat("r", 40), noteTools},
		{strings.Repeat("v", 40), reviewTools},
		{strings.Repeat("w", 40), []string{}},
		{strings.Repeat("f", 40), append(append(slices.Clone(noteTools), reviewTools...), aiTools...)},
	} {
		tools := aiMCPCall(t, api, test.token, "tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any)
		got := []string{}
		for _, raw := range tools {
			tool := raw.(map[string]any)
			name := tool["name"].(string)
			got = append(got, name)
			annotations := tool["annotations"].(map[string]any)
			if annotations["readOnlyHint"] != true || annotations["destructiveHint"] != false ||
				annotations["idempotentHint"] != !slices.Contains(aiTools, name) {
				t.Fatalf("incorrect tool hints: %v", tool)
			}
		}
		slices.Sort(got)
		slices.Sort(test.want)
		if !slices.Equal(got, test.want) {
			t.Fatalf("unexpected tools: %v; want %v", got, test.want)
		}
	}
	for _, token := range []string{strings.Repeat("f", 40), strings.Repeat("w", 40), strings.Repeat("r", 40)} {
		for _, name := range []string{"get_daily_review", "replace_daily_review", "open_review_memo",
			"skip_review_memo", "open_discovery_memo", "mark_memo_read"} {
			mcpUnknownTool(t, api, token, name)
		}
	}
	for _, name := range []string{"get_memo", "list_memos"} {
		mcpUnknownTool(t, api, strings.Repeat("w", 40), name)
	}
	mcpUnknownTool(t, api, strings.Repeat("r", 40), "read_daily_review")
	for _, table := range []string{"review_batches", "review_items", "discovery_reads", "note_writes"} {
		var count int
		if err := api.Store.state.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("denied MCP call wrote %s: count=%d err=%v", table, count, err)
		}
	}
}

func TestMCPFetchListAndRESTPagination(t *testing.T) {
	api, memos := mcpReadFixture(t)
	reader := strings.Repeat("r", 40)
	memos["memos/review-00"]["content"] = "fresh full note\nwith a second line"
	note := mcpReadResult(t, api, reader, "get_memo", map[string]any{"name": "memos/review-00"})
	if note["content"] != memos["memos/review-00"]["content"] || note["creator"] != api.Settings.Memos.User {
		t.Fatalf("direct read returned stale or foreign content: %v", note)
	}
	rest := aiAPICall(api, "GET", "/api/ext/v1/notes/review-00", reader, "")
	var restNote map[string]any
	if rest.Code != 200 || json.Unmarshal(rest.Body.Bytes(), &restNote) != nil || !reflect.DeepEqual(note, restNote) {
		t.Fatalf("MCP and REST notes differ: %v %s", note, rest.Body.String())
	}

	seen := map[string]bool{}
	page := mcpReadResult(t, api, reader, "list_memos", map[string]any{"page_size": 1})
	for {
		notes := page["notes"].([]any)
		if len(notes) != 1 {
			t.Fatalf("unexpected page: %v", page)
		}
		name := notes[0].(map[string]any)["name"].(string)
		if seen[name] {
			t.Fatal("pagination repeated a note")
		}
		seen[name] = true
		token := page["next_page_token"].(string)
		if token == "" {
			break
		}
		if len(seen)%2 == 0 {
			page = mcpReadResult(t, api, reader, "list_memos", map[string]any{
				"page_size": 1, "page_token": token})
		} else {
			response := aiAPICall(api, "GET", "/api/ext/v1/notes?page_size=1&page_token="+token, reader, "")
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil {
				t.Fatalf("REST rejected MCP cursor: %d %s", response.Code, response.Body.String())
			}
		}
	}
	if len(seen) != len(memos) {
		t.Fatalf("pagination lost notes: %v", seen)
	}
	archived := rawMemo("archived", "archived note")
	archived["state"] = "ARCHIVED"
	memos["memos/archived"] = archived
	refreshIndexMemo(t, api.Store, archived)
	page = mcpReadResult(t, api, reader, "list_memos", map[string]any{"state": "ARCHIVED"})
	if notes := page["notes"].([]any); len(notes) != 1 || notes[0].(map[string]any)["name"] != "memos/archived" {
		t.Fatalf("archived filter failed: %v", page)
	}
	for _, args := range []map[string]any{
		{"page_size": 0}, {"page_size": 101}, {"state": "ALL"}, {"page_token": strings.Repeat("x", 4097)},
	} {
		mcpReadError(t, api, reader, "list_memos", args, "INVALID_ARGUMENT")
	}
	mcpReadError(t, api, reader, "list_memos", map[string]any{"page_token": "invalid"}, "INVALID_PAGE_TOKEN")
	cursor := api.encodeCursor("NORMAL", []string{"2020-01-01T00:00:00Z", "memos/review-00"})
	mcpReadError(t, api, reader, "list_memos", map[string]any{"state": "ARCHIVED", "page_token": cursor}, "INVALID_PAGE_TOKEN")
	mcpReadError(t, api, reader, "get_memo", map[string]any{"name": "../users/foreign"}, "INVALID_RESOURCE")
	mcpReadError(t, api, reader, "get_memo", map[string]any{"name": "memos/missing"}, "SOURCE_NOT_FOUND")
	mcpReadError(t, api, reader, "get_memo", map[string]any{"name": "memos/review-00", "write": true}, "")
}

func TestMCPSourceIsolationAndFailures(t *testing.T) {
	for _, change := range []string{"foreign", "deleted", "offline", "identity"} {
		t.Run(change, func(t *testing.T) {
			api, memos := mcpReadFixture(t)
			reader := strings.Repeat("r", 40)
			code := "SOURCE_NOT_FOUND"
			switch change {
			case "foreign":
				for _, memo := range memos {
					memo["creator"] = "users/foreign"
				}
			case "deleted":
				clear(memos)
			case "offline", "identity":
				code = "UPSTREAM_UNAVAILABLE"
				api.Source = fakeClient(api.Settings, func(request *http.Request) (int, any, error) {
					if change == "identity" && strings.HasSuffix(request.URL.Path, "/auth/me") {
						return 200, map[string]any{"user": map[string]string{"name": "users/foreign"}}, nil
					}
					if status, body, ok := identityResponse(request); ok {
						return status, body, nil
					}
					return 503, nil, nil
				})
				if change == "identity" {
					code = "IDENTITY_MISMATCH"
				}
			}
			mcpReadError(t, api, reader, "get_memo", map[string]any{"name": "memos/review-00"}, code)
			if change == "foreign" || change == "deleted" {
				page := mcpReadResult(t, api, reader, "list_memos", map[string]any{})
				if len(page["notes"].([]any)) != 0 {
					t.Fatalf("list returned unavailable notes: %v", page)
				}
			} else {
				mcpReadError(t, api, reader, "list_memos", map[string]any{}, code)
			}
		})
	}
}

func TestMCPReviewReadsDoNotCreateOrMark(t *testing.T) {
	api, memos := mcpReadFixture(t)
	token := strings.Repeat("f", 40)
	mcpReadError(t, api, token, "read_daily_review", map[string]any{}, "REVIEW_NOT_FOUND")
	mcpReadError(t, api, token, "get_review_batch", map[string]any{"batch_id": newID()}, "REVIEW_NOT_FOUND")
	var count int
	if err := api.Store.state.QueryRow("SELECT COUNT(*) FROM review_batches").Scan(&count); err != nil || count != 0 {
		t.Fatalf("read created a daily batch: %d %v", count, err)
	}
	batch, err := api.Reviews.Daily(context.Background(), time.Now())
	if err != nil || len(batch.Items) == 0 {
		t.Fatalf("create native review: %+v %v", batch, err)
	}
	batch, err = api.Reviews.Mark(context.Background(), batch.ID, batch.Items[0].Name, "open", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		for _, tool := range []string{"read_daily_review", "get_review_batch"} {
			args := map[string]any{}
			if tool == "get_review_batch" {
				args["batch_id"] = batch.ID
			}
			got := mcpReadResult(t, api, token, tool, args)
			if got["id"] != batch.ID {
				t.Fatal("MCP did not read the existing daily batch")
			}
		}
	}
	mcpReadResult(t, api, token, "get_memo", map[string]any{"name": batch.Items[0].Name})
	mcpReadResult(t, api, token, "list_memos", map[string]any{})
	mcpReadResult(t, api, token, "semantic_search", map[string]any{"query": "review"})
	mcpReadResult(t, api, token, "get_related_memos", map[string]any{"name": batch.Items[0].Name})
	mcpReadResult(t, api, token, "semantic_walk", map[string]any{"mode": "random"})
	after, err := api.Reviews.Get(context.Background(), batch.ID)
	if err != nil || !reflect.DeepEqual(after, batch) {
		t.Fatalf("MCP changed the existing batch or item history: %+v %v", after, err)
	}
	for table, want := range map[string]int{"review_batches": 1, "review_items": len(batch.Items), "discovery_reads": 0} {
		if err := api.Store.state.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatalf("MCP changed %s: count=%d want=%d err=%v", table, count, want, err)
		}
	}
	memos[batch.Items[0].Name]["creator"] = "users/foreign"
	got := mcpReadResult(t, api, token, "get_review_batch", map[string]any{"batch_id": batch.ID})
	item := got["items"].([]any)[0].(map[string]any)
	if item["unavailable"] != true || item["note"] != nil {
		t.Fatalf("review leaked a foreign note: %v", item)
	}
}

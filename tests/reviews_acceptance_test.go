//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	moerise "moerise/internal"
)

func (h *harness) mcp(token, method string, params any) map[string]any {
	h.t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": "acceptance", "method": method, "params": params}
	response, status, err := h.requestWithHeaders(h.base, "/mcp", "POST", token, body,
		map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
	if err != nil || status != 200 {
		h.t.Fatalf("MCP %s: status=%d response=%v err=%v", method, status, response, err)
	}
	if response["error"] != nil {
		h.t.Fatalf("MCP protocol error: %v", response)
	}
	return response["result"].(map[string]any)
}

func (h *harness) reviewTool(name string, arguments any) map[string]any {
	h.t.Helper()
	result := h.mcp(h.operator, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if result["isError"] == true {
		h.t.Fatalf("MCP tool %s failed: %v", name, result)
	}
	batch, ok := result["structuredContent"].(map[string]any)
	if !ok {
		h.t.Fatalf("MCP tool %s missing structured result: %v", name, result)
	}
	return batch
}

func TestReviewsAcrossRESTAndMCPAcceptance(t *testing.T) {
	h := newHarness(t)
	for i := range 6 {
		h.recordInProcess(fmt.Sprintf("review-%d", i), map[string]string{"content": fmt.Sprintf("review %d", i)}, 201)
	}
	h.stop()
	// Prepare source notes before starting the daily review's product state.
	h.settings.StateDir = filepath.Join(h.root, "review-state")
	h.settings.IndexDir = filepath.Join(h.root, "review-index")
	h.settings.Review.MinAgeHours = 0
	h.settings.AccessTokens[0].Scopes = append(h.settings.AccessTokens[0].Scopes, "reviews:read")
	h.settings.AccessTokens[1].Scopes = append(h.settings.AccessTokens[1].Scopes, "reviews:read", "reviews:write", "discovery:write")
	data, _ := json.Marshal(h.settings)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.ready()
	requireRecordError(t, h.call(h.base, "/api/ext/v1/reviews/daily", "POST", h.reader, map[string]any{}, 403), "FORBIDDEN")
	h.call(h.base, "/api/ext/v1/reviews/daily", "POST", h.operator, map[string]any{}, 403)
	var batch map[string]any
	h.withOfflineApp(func(app *moerise.App) {
		value, err := app.Reviews.Daily(context.Background(), time.Now())
		batch = businessResult(h, value, 200, err, 200)
	})
	id := batch["id"].(string)
	if len(batch["items"].([]any)) != 3 {
		t.Fatalf("incorrect batch: %v", batch)
	}
	initialize := h.mcp(h.operator, "initialize", map[string]any{
		"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "acceptance", "version": "1"},
	})
	if initialize["protocolVersion"] == nil {
		t.Fatal("MCP initialization failed")
	}
	wantTools := []string{"get_memo", "list_memos", "get_review_batch", "read_daily_review",
		"semantic_search", "get_related_memos", "semantic_walk"}
	slices.Sort(wantTools)
	for _, token := range []string{h.reader, h.operator} {
		tools := h.mcp(token, "tools/list", map[string]any{})["tools"].([]any)
		names := []string{}
		for _, value := range tools {
			tool := value.(map[string]any)
			names = append(names, tool["name"].(string))
			hints := tool["annotations"].(map[string]any)
			if hints["readOnlyHint"] != true || hints["idempotentHint"] != true || hints["destructiveHint"] != false {
				t.Fatalf("incorrect read-only hints: %v", tool)
			}
		}
		slices.Sort(names)
		if !slices.Equal(names, wantTools) {
			t.Fatalf("incorrect MCP tools: %v", names)
		}
		for _, tool := range []string{"get_daily_review", "replace_daily_review", "open_review_memo",
			"skip_review_memo", "open_discovery_memo", "mark_memo_read", "analyze_memos", "ask_memos", "get_topic_timeline"} {
			denied, status, err := h.requestWithHeaders(h.base, "/mcp", "POST", token,
				map[string]any{"jsonrpc": "2.0", "id": "denied", "method": "tools/call",
					"params": map[string]any{"name": tool, "arguments": map[string]any{}}},
				map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
			rpcError, _ := denied["error"].(map[string]any)
			message, _ := rpcError["message"].(string)
			if err != nil || status != 200 || rpcError["code"] != float64(-32602) || !strings.Contains(message, "unknown tool") {
				t.Fatalf("MCP accepted product write %s: %v status=%d err=%v", tool, denied, status, err)
			}
		}
	}
	if got := h.reviewTool("read_daily_review", map[string]any{}); got["id"] != id {
		t.Fatal("REST and MCP did not share their daily batch")
	}
	item := batch["items"].([]any)[0].(map[string]any)
	name := item["name"].(string)
	if got := h.reviewTool("get_memo", map[string]string{"name": name}); got["name"] != name {
		t.Fatalf("MCP did not fetch the selected note: %v", got)
	}
	if got := h.reviewTool("list_memos", map[string]any{"page_size": 1}); len(got["notes"].([]any)) != 1 || got["next_page_token"] == "" {
		t.Fatalf("MCP did not paginate notes: %v", got)
	}
	path := "/api/ext/v1/reviews/" + id
	h.call(h.base, path+"/items/"+strings.TrimPrefix(name, "memos/")+"/open", "POST", h.operator, map[string]any{}, 403)
	h.withOfflineApp(func(app *moerise.App) {
		_, err := app.Reviews.Mark(context.Background(), id, name, "open", time.Now())
		if err != nil {
			t.Fatal(err)
		}
	})
	fresh := h.call(h.base, path, "GET", h.reader, nil, 200)
	if fresh["items"].([]any)[0].(map[string]any)["opened_at"] == nil {
		t.Fatal("shared service open was not persisted")
	}
	h.withOfflineApp(func(app *moerise.App) {
		_, err := app.Reviews.Mark(context.Background(), id, name, "skip", time.Now())
		if err != nil {
			t.Fatal(err)
		}
	})
	fresh = h.reviewTool("get_review_batch", map[string]string{"batch_id": id})
	if fresh["items"].([]any)[0].(map[string]any)["skipped_at"] == nil {
		t.Fatal("shared service skip was not visible over MCP")
	}
	h.call(h.base, "/api/ext/v1/reviews/daily/replace", "POST", h.operator, map[string]any{}, 403)
	var replacement map[string]any
	for range 2 {
		h.withOfflineApp(func(app *moerise.App) {
			value, err := app.Reviews.Replace(context.Background(), "shared-replacement", time.Now())
			replay := businessResult(h, value, 200, err, 200)
			if replacement != nil && replay["id"] != replacement["id"] {
				t.Fatal("replacement changed identity")
			}
			replacement = replay
		})
	}
	if got := h.reviewTool("read_daily_review", map[string]any{}); got["id"] != replacement["id"] {
		t.Fatal("MCP did not read the shared service replacement")
	}
	seen := map[string]bool{}
	for _, value := range batch["items"].([]any) {
		seen[value.(map[string]any)["name"].(string)] = true
	}
	for _, value := range replacement["items"].([]any) {
		if seen[value.(map[string]any)["name"].(string)] {
			t.Fatal("replacement repeated a recent memo")
		}
	}
	snapshot := filepath.Join(h.root, "reviews")
	h.command(true, "backup", "--output", snapshot)
	for _, operation := range []string{"restart", "rebuild-index", "restore"} {
		h.stop()
		switch operation {
		case "rebuild-index":
			h.command(true, "rebuild-index")
		case "restore":
			h.command(true, "restore", "--source", snapshot, "--safety-backup", filepath.Join(h.root, "before-reviews"))
		}
		h.start()
		h.ready()
		if got := h.reviewTool("read_daily_review", map[string]any{}); got["id"] != replacement["id"] {
			t.Fatalf("%s changed batch identity", operation)
		}
		got := h.call(h.base, path, "GET", h.reader, nil, 200)["items"].([]any)[0].(map[string]any)
		if got["opened_at"] == nil || got["skipped_at"] == nil {
			t.Fatalf("%s lost review history", operation)
		}
	}
	h.call(h.memos, "/api/v1/"+name, "DELETE", h.upstream, nil, 200)
	got := h.reviewTool("get_review_batch", map[string]string{"batch_id": id})["items"].([]any)[0].(map[string]any)
	if got["unavailable"] != true || got["note"] != nil {
		t.Fatal("deleted memo remained available through review MCP")
	}
}

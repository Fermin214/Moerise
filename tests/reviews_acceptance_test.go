//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	h.stop()
	h.settings.Review.MinAgeHours = 0
	h.settings.AccessTokens[0].Scopes = append(h.settings.AccessTokens[0].Scopes, "reviews:read")
	h.settings.AccessTokens[1].Scopes = append(h.settings.AccessTokens[1].Scopes, "reviews:read", "reviews:write")
	data, _ := json.Marshal(h.settings)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.ready()
	for i := range 6 {
		h.record(fmt.Sprintf("review-%d", i), h.operator, map[string]string{"content": fmt.Sprintf("review %d", i)}, 201)
	}
	requireRecordError(t, h.call(h.base, "/api/ext/v1/reviews/daily", "GET", h.reader, nil, 404), "REVIEW_NOT_FOUND")
	requireRecordError(t, h.call(h.base, "/api/ext/v1/reviews/daily", "POST", h.reader, map[string]any{}, 403), "FORBIDDEN")
	batch := h.call(h.base, "/api/ext/v1/reviews/daily", "POST", h.operator, map[string]any{}, 200)
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
	tools := h.mcp(h.operator, "tools/list", map[string]any{})["tools"].([]any)
	if len(tools) != 7 {
		t.Fatalf("missing review tools: %v", tools)
	}
	for _, value := range tools {
		tool := value.(map[string]any)
		name := tool["name"].(string)
		readonly := tool["annotations"].(map[string]any)["readOnlyHint"]
		if readonly != (name == "get_review_batch" || name == "read_daily_review" || name == "semantic_search") {
			t.Fatalf("incorrect state-change hint: %v", tool)
		}
	}
	if got := h.reviewTool("get_daily_review", map[string]any{}); got["id"] != id {
		t.Fatal("REST and MCP did not share their daily batch")
	}
	readerTools := h.mcp(h.reader, "tools/list", map[string]any{})["tools"].([]any)
	if len(readerTools) != 3 {
		t.Fatal("read-only principal was offered state-changing tools")
	}
	denied, denialStatus, denialErr := h.requestWithHeaders(h.base, "/mcp", "POST", h.reader,
		map[string]any{"jsonrpc": "2.0", "id": "denied", "method": "tools/call",
			"params": map[string]any{"name": "get_daily_review", "arguments": map[string]any{}}},
		map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
	result, _ := denied["result"].(map[string]any)
	if denialErr != nil || denialStatus != 200 || denied["error"] == nil && result["isError"] != true {
		t.Fatal("read-only principal changed review state through MCP")
	}
	item := batch["items"].([]any)[0].(map[string]any)
	name := item["name"].(string)
	h.reviewTool("open_review_memo", map[string]string{"batch_id": id, "name": name})
	path := "/api/ext/v1/reviews/" + id
	fresh := h.call(h.base, path, "GET", h.reader, nil, 200)
	if fresh["items"].([]any)[0].(map[string]any)["opened_at"] == nil {
		t.Fatal("MCP open was not visible over REST")
	}
	h.call(h.base, path+"/items/"+strings.TrimPrefix(name, "memos/")+"/skip", "POST", h.operator, map[string]any{}, 200)
	fresh = h.reviewTool("get_review_batch", map[string]string{"batch_id": id})
	if fresh["items"].([]any)[0].(map[string]any)["skipped_at"] == nil {
		t.Fatal("REST skip was not visible over MCP")
	}
	replacement := h.reviewTool("replace_daily_review", map[string]string{"idempotency_key": "shared-replacement"})
	rest, status, err := h.requestWithHeaders(h.base, "/api/ext/v1/reviews/daily/replace", "POST", h.operator, map[string]any{},
		map[string]string{"Idempotency-Key": "shared-replacement"})
	if err != nil || status != 200 || rest["id"] != replacement["id"] {
		t.Fatalf("cross-entry replacement replay: %v status=%d err=%v", rest, status, err)
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
		if got := h.reviewTool("get_daily_review", map[string]any{}); got["id"] != replacement["id"] {
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

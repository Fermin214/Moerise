//go:build integration

package acceptance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClientDiscoveryMemosAcceptance(t *testing.T) {
	var modelCalls atomic.Int64
	modelKey := randomToken()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if r.URL.Path != "/v1/embeddings" || r.Method != "POST" ||
			r.Header.Get("Authorization") != "Bearer "+modelKey ||
			json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Input) == 0 || body.Model != "discovery-fixture" {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		modelCalls.Add(1)
		data := make([]any, len(body.Input))
		for i, text := range body.Input {
			angle := 0.1 + float64(len(text)%7)/20
			data[i] = map[string]any{"index": i, "embedding": []float64{math.Cos(angle), math.Sin(angle), 0}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]int{"total_tokens": len(data)}})
	}))
	defer model.Close()
	h := newHarness(t)
	h.settings.Review.Count, h.settings.Review.MinAgeHours = 3, 0
	h.settings.Notifications.Times = []string{"08:00", "20:00"}
	h.settings.Notifications.Time = "08:00"
	h.settings.AccessTokens[0].Scopes = append(h.settings.AccessTokens[0].Scopes, "reviews:read")
	h.settings.AccessTokens[1].Scopes = append(h.settings.AccessTokens[1].Scopes,
		"reviews:read", "reviews:write", "discovery:write")
	c := managedClient(t, h)
	(&clientHarness{h: h}).call("GET", "/api/app/v1/reviews/daily", nil, 401)
	for _, path := range []string{"/api/app/v1/reviews/daily", "/api/app/v1/walks/start"} {
		h.call(h.base, path, "GET", h.operator, nil, 403)
	}
	for _, path := range []string{"/api/app/v1/walks/start", "/api/app/v1/walks/next"} {
		(&clientHarness{h: h}).call("POST", path, map[string]any{}, 401)
		for _, headers := range []map[string]string{
			{"Origin": h.base, "Cookie": c.cookie.String()},
			{"Origin": "https://other.invalid", "Cookie": c.cookie.String(), "X-CSRF-Token": c.csrf},
		} {
			_, status, err := h.requestWithHeaders(h.base, path, "POST", "", map[string]any{}, headers)
			if err != nil || status != 403 {
				t.Fatalf("walk browser security boundary: status=%d err=%v", status, err)
			}
		}
	}
	textNames := []string{}
	for i := range 5 {
		c.key = fmt.Sprintf("discovery-body-%d", i)
		note := c.call("POST", "/api/app/v1/notes",
			map[string]string{"content": fmt.Sprintf("discovery note %d\n\nfull body #original", i)}, 201)
		textNames = append(textNames, note["name"].(string))
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aP9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	createAttachment := func(key, filename, kind string, data []byte) map[string]any {
		t.Helper()
		c.key = key
		return c.call("POST", "/api/app/v1/notes", map[string]any{
			"content": "", "attachments": []map[string]string{{
				"filename": filename, "type": kind, "content": base64.StdEncoding.EncodeToString(data),
			}},
		}, 201)
	}
	image := createAttachment("discovery-image", "private.png", "image/png", png)
	file := createAttachment("discovery-file", "private.txt", "text/plain", []byte("file without body"))
	h.sync("full")
	t.Run("machine-reads-keep-existing-plan", func(t *testing.T) {
		existing := c.call("GET", "/api/app/v1/reviews/daily", nil, 200)
		state := h.database("state")
		defer state.Close()
		var before, after int
		if err := state.QueryRow("SELECT COUNT(*) FROM review_plans").Scan(&before); err != nil {
			t.Fatal(err)
		}
		result, status, err := h.request(h.base, "/api/ext/v1/reviews/daily", "GET", h.operator, nil)
		if err != nil || status != 200 || result["id"] != existing["id"] {
			t.Fatalf("machine daily read: status=%d err=%v response=%v", status, err, result)
		}
		raw, code, err := h.requestWithHeaders(h.base, "/mcp", "POST", h.operator,
			map[string]any{"jsonrpc": "2.0", "id": "daily-read", "method": "tools/call",
				"params": map[string]any{"name": "read_daily_review", "arguments": map[string]any{}}},
			map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
		if err != nil || code != 200 || raw["error"] != nil {
			t.Fatalf("machine MCP daily read: status=%d err=%v response=%v", code, err, raw)
		}
		if err := state.QueryRow("SELECT COUNT(*) FROM review_plans").Scan(&after); err != nil || before != after {
			t.Fatalf("machine read changed daily plans: before=%d after=%d err=%v", before, after, err)
		}
	})
	// 来源预置后用独立产品库覆盖恢复启动路径，避免预置期的计划影响其余检查。
	h.stop()
	h.settings.StateDir, h.settings.IndexDir = filepath.Join(h.root, "discovery-state"), filepath.Join(h.root, "discovery-index")
	config, err := json.Marshal(h.settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.config, config, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.ready()
	c.cookie, c.csrf = nil, ""
	c.call("POST", "/api/app/v1/session", map[string]string{"username": "acceptance-owner", "password": h.password}, 200)
	db := h.database("state")
	defer db.Close()
	count := func(query string, want int) {
		t.Helper()
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("state invariant %s: got=%d want=%d err=%v", query, got, want, err)
		}
	}
	batch := c.call("GET", "/api/app/v1/reviews/daily", nil, 200)
	items := batch["items"].([]any)
	if batch["id"] == "" || len(items) != 6 {
		t.Fatalf("two slots of three must expose all six notes: %v", batch)
	}
	selected := map[string]bool{}
	for _, value := range items {
		item := value.(map[string]any)
		name := item["name"].(string)
		if selected[name] || name == file["name"] || item["unavailable"] != false || item["note"] == nil {
			t.Fatalf("invalid daily eligibility: %v", item)
		}
		selected[name] = true
	}
	for _, name := range append(slices.Clone(textNames), image["name"].(string)) {
		if !selected[name] {
			t.Fatalf("eligible note omitted from six-position plan: %s", name)
		}
	}
	count("SELECT COUNT(*) FROM review_plans", 1)
	count("SELECT COUNT(*) FROM review_plan_slots WHERE item_count = 3", 2)
	count("SELECT COUNT(*) FROM review_plan_slots WHERE (ordinal = 0 AND first_position = 0 AND clock = '08:00') OR (ordinal = 1 AND first_position = 3 AND clock = '20:00')", 2)
	count("SELECT COUNT(DISTINCT memo_name) FROM review_items", 6)
	detail := c.call("GET", clientNotePath(image), nil, 200)
	protected := detail["attachments"].([]any)[0].(map[string]any)["content_url"].(string)
	if !strings.HasPrefix(protected, clientNotePath(image)+"/attachments/") {
		t.Fatalf("attachment lacks a protected same-origin URL: %s", protected)
	}
	for _, value := range items {
		item := value.(map[string]any)
		if item["name"] == image["name"] &&
			!reflect.DeepEqual(item["note"].(map[string]any)["attachments"], detail["attachments"]) {
			t.Fatal("daily review bypassed the client attachment projection")
		}
	}
	(&clientHarness{h: h}).call("GET", protected, nil, 401)
	h.call(h.base, protected, "GET", h.operator, nil, 403)
	request, _ := http.NewRequest("GET", h.base+protected, nil)
	request.AddCookie(c.cookie)
	response, err := h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != 200 || !bytes.Equal(content, png) ||
		!strings.HasPrefix(response.Header.Get("Content-Type"), "image/png") ||
		response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("protected image failed: status=%d err=%v headers=%v", response.StatusCode, readErr, response.Header)
	}
	assertPlan := func() {
		t.Helper()
		if got := c.call("GET", "/api/app/v1/reviews/daily", nil, 200); !reflect.DeepEqual(got, batch) {
			t.Fatalf("fixed daily plan changed: before=%v after=%v", batch, got)
		}
	}
	c.key = "discovery-after-plan"
	c.call("POST", "/api/app/v1/notes", map[string]string{"content": "late note outside fixed plan"}, 201)
	h.sync("full")
	assertPlan()
	h.stop()
	h.start()
	c.call("GET", "/api/app/v1/session", nil, 200)
	assertPlan()
	for _, path := range []string{"/api/ext/v1/reviews/daily", "/api/ext/v1/reviews/daily/replace",
		"/api/ext/v1/reviews/" + batch["id"].(string) + "/items/" + strings.TrimPrefix(textNames[0], "memos/") + "/open",
		"/api/ext/v1/discovery/open"} {
		h.call(h.base, path, "POST", h.operator, map[string]any{"name": textNames[0]}, 403)
	}
	if got := h.call(h.base, "/api/ext/v1/reviews/daily", "GET", h.operator, nil, 200); got["id"] != batch["id"] {
		t.Fatal("machine reader did not share the fixed plan")
	}
	for _, tool := range []string{"get_daily_review", "replace_daily_review", "open_review_memo", "open_discovery_memo"} {
		result, status, err := h.requestWithHeaders(h.base, "/mcp", "POST", h.operator,
			map[string]any{"jsonrpc": "2.0", "id": "denied", "method": "tools/call",
				"params": map[string]any{"name": tool, "arguments": map[string]any{}}},
			map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
		failed, _ := result["result"].(map[string]any)
		if err != nil || status != 200 || result["error"] == nil && failed["isError"] != true {
			t.Fatalf("MCP accepted a product write: tool=%s result=%v status=%d err=%v", tool, result, status, err)
		}
	}
	c.call("POST", "/api/app/v1/walks/start", map[string]any{}, 503)
	settings := c.call("GET", "/api/app/v1/settings", nil, 200)
	c.call("PATCH", "/api/app/v1/settings", map[string]any{
		"version": settings["version"], "settings": map[string]any{"embedding": map[string]any{
			"enabled": true, "provider": "openai-compatible", "protocol": "openai-compatible", "base_url": model.URL + "/v1",
			"model": "discovery-fixture", "dimensions": 3, "max_retries": 0,
		}},
		"secrets": map[string]any{"embedding_api_key": map[string]string{"action": "replace", "value": modelKey}},
	}, 200)
	wait(t, "discovery embedding ready", func() bool {
		index, status, err := h.request(h.base, "/api/ext/v1/search/index", "GET", h.reader, nil)
		return err == nil && status == 200 && index["ready"] == true && modelCalls.Load() > 0
	})
	c.call("POST", "/api/app/v1/walks/next", map[string]any{"path": []string{textNames[0]}}, 422)
	c.call("POST", "/api/app/v1/walks/start", map[string]any{"name": "invalid"}, 422)
	start := c.call("POST", "/api/app/v1/walks/start", map[string]string{"name": textNames[0]}, 200)
	if start["item"].(map[string]any)["note"].(map[string]any)["name"] != textNames[0] ||
		!reflect.DeepEqual(start["path"], []any{textNames[0]}) {
		t.Fatalf("specified walk start changed its seed: %v", start)
	}
	random := c.call("POST", "/api/app/v1/walks/start", map[string]any{}, 200)
	randomName := random["item"].(map[string]any)["note"].(map[string]any)["name"]
	if random["exhausted"] != false || !reflect.DeepEqual(random["path"], []any{randomName}) {
		t.Fatalf("random walk start must contain exactly its note: %v", random)
	}
	walk := start
	exhausted := false
	for range 10 {
		path := walk["path"].([]any)
		name := walk["item"].(map[string]any)["note"].(map[string]any)["name"]
		next := c.call("POST", "/api/app/v1/walks/next", map[string]any{"name": name, "path": path}, 200)
		if len(next["warnings"].([]any)) != 0 {
			t.Fatalf("technical failure misclassified as exhaustion: %v", next)
		}
		if next["exhausted"] == true {
			if next["item"] != nil || !reflect.DeepEqual(next["path"], path) || len(path) != 6 {
				t.Fatalf("walk did not exhaust the six text notes exactly once: %v", next)
			}
			exhausted = true
			break
		}
		newName := next["item"].(map[string]any)["note"].(map[string]any)["name"]
		if slices.Contains(path, newName) || !reflect.DeepEqual(next["path"], append(slices.Clone(path), newName)) {
			t.Fatalf("walk repeated or lost its path: %v", next)
		}
		walk = next
	}
	if !exhausted {
		t.Fatal("walk never exhausted its isolated fixture")
	}
	assertPlan()
	settings = c.call("GET", "/api/app/v1/settings", nil, 200)
	c.call("PATCH", "/api/app/v1/settings", map[string]any{
		"version": settings["version"], "settings": map[string]any{
			"embedding": map[string]bool{"enabled": false}, "llm": map[string]bool{"enabled": false},
		},
	}, 200)
	assertPlan()
	c.call("POST", "/api/app/v1/walks/start", map[string]any{}, 503)
	count("SELECT COUNT(*) FROM review_plans", 1)
	count("SELECT COUNT(*) FROM review_items WHERE opened_at IS NOT NULL OR skipped_at IS NOT NULL", 0)
	count("SELECT COUNT(*) FROM discovery_reads", 0)
	oldCookie := c.cookie
	c.call("DELETE", "/api/app/v1/session", nil, 204)
	(&clientHarness{h: h, cookie: oldCookie}).call("GET", "/api/app/v1/reviews/daily", nil, 401)
	(&clientHarness{h: h, cookie: oldCookie}).call("GET", protected, nil, 401)
	t.Log("PASS real Memos/Core browser security, fixed six-item plan, protected image, restart, semantic walks and model-independent reviews")
}

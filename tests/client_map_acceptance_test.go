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
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	moerise "moerise/internal"
)

func acceptanceMap(t *testing.T, c *clientHarness) moerise.MapResponse {
	t.Helper()
	var result moerise.MapResponse
	wait(t, "published map snapshot", func() bool {
		value := c.call("GET", "/api/app/v1/map", nil, 200)
		data, err := json.Marshal(value)
		if err != nil || json.Unmarshal(data, &result) != nil {
			t.Fatalf("invalid map response: %v", err)
		}
		return result.Status == "ready"
	})
	if len(result.LayoutVersion) != 64 || len(result.LayoutFrame) != 64 || result.GeneratedAt == "" {
		t.Fatal("invalid snapshot metadata")
	}
	return result
}

func TestClientMapMemosAcceptance(t *testing.T) {
	var faultName atomic.Value
	faultName.Store("")
	var faultStatus atomic.Int32
	h := newHarnessWithUpstream(t, func(upstream string) string {
		target, _ := url.Parse(upstream)
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ModifyResponse = func(response *http.Response) error {
			name := faultName.Load().(string)
			if name != "" && response.Request.Method == "GET" && response.Request.URL.Path == "/api/v1/"+name && response.StatusCode == 200 {
				if status := int(faultStatus.Load()); status != 0 {
					response.Body.Close()
					response.StatusCode, response.ContentLength = status, -1
					response.Body = io.NopCloser(strings.NewReader(`{"message":"controlled source failure"}`))
					response.Header.Del("Content-Length")
					return nil
				}
			}
			if name == "" || response.Request.Method != "GET" || response.Request.URL.Path != "/api/v1/memos" || response.StatusCode != 200 {
				return nil
			}
			var page map[string]any
			if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
				return err
			}
			response.Body.Close()
			found := false
			for _, value := range page["memos"].([]any) {
				memo := value.(map[string]any)
				if memo["name"] == name {
					found = true
				}
			}
			if status := int(faultStatus.Load()); found && status != 0 {
				response.StatusCode, response.ContentLength = status, -1
				response.Body = io.NopCloser(strings.NewReader(`{"message":"controlled source failure"}`))
			} else {
				data, _ := json.Marshal(page)
				response.Body, response.ContentLength = io.NopCloser(bytes.NewReader(data)), int64(len(data))
			}
			response.Header.Del("Content-Length")
			return nil
		}
		server := httptest.NewServer(proxy)
		t.Cleanup(server.Close)
		return server.URL
	})
	c := managedClient(t, h)
	if disabled := c.call("GET", "/api/app/v1/map", nil, 200); disabled["status"] != "disabled" || len(disabled["points"].([]any)) != 0 {
		t.Fatal("disabled embedding exposed map points")
	}
	(&clientHarness{h: h}).call("GET", "/api/app/v1/map", nil, 401)
	for _, token := range []string{h.reader, h.operator} {
		for _, path := range []string{"/api/app/v1/map", "/api/ext/v1/map"} {
			h.call(h.base, path, "GET", token, nil, 403)
		}
	}
	c.call("GET", "/api/app/v1/map?limit=1", nil, 422)
	for _, tool := range []string{"get_map", "read_map", "create_map"} {
		result, status, err := h.requestWithHeaders(h.base, "/mcp", "POST", h.operator,
			map[string]any{"jsonrpc": "2.0", "id": "map-denied", "method": "tools/call",
				"params": map[string]any{"name": tool, "arguments": map[string]any{}}},
			map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
		failed, _ := result["result"].(map[string]any)
		if err != nil || status != 200 || result["error"] == nil && failed["isError"] != true {
			t.Fatalf("MCP exposed map: tool=%s status=%d err=%v", tool, status, err)
		}
	}

	var calls atomic.Int64
	var modelFailure atomic.Bool
	key := randomToken()
	embedding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer "+key ||
			json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Input) == 0 || !strings.HasPrefix(input.Model, "map-fixture") {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		calls.Add(1)
		if modelFailure.Load() {
			http.Error(w, "controlled model failure", 503)
			return
		}
		data := make([]any, len(input.Input))
		for i, text := range input.Input {
			angle := 0.0
			if strings.Contains(text, "bicycle") {
				angle = 2 * math.Pi / 3
			} else if strings.Contains(text, "reading") {
				angle = 4 * math.Pi / 3
			}
			angle += float64(len(text)%5) * 0.003
			data[i] = map[string]any{"index": i, "embedding": []float64{math.Cos(angle), math.Sin(angle), 0.01}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer embedding.Close()
	contents := []string{
		"  balcony herbs #想法\n\n" + strings.Repeat("balcony herbs original paragraph\n", 160) + "\noriginal ending  ",
		"balcony herbs light #灵感", "bicycle brakes #摘录", "bicycle tyres #教训", "reading fiction", "reading history",
	}
	contents = append(contents, contents[0])
	notes := make([]map[string]any, 0, len(contents)+2)
	for i, content := range contents {
		c.key = fmt.Sprintf("map-note-%d", i)
		notes = append(notes, c.call("POST", "/api/app/v1/notes", map[string]string{"content": content}, 201))
	}
	h.call(h.memos, "/api/v1/"+notes[1]["name"].(string)+"?updateMask=state", "PATCH", h.upstream, map[string]string{"state": "ARCHIVED"}, 200)
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aP9sAAAAASUVORK5CYII=")
	for i, file := range []struct {
		name, kind string
		data       []byte
	}{{"original.png", "image/png", png}, {"original.txt", "text/plain", []byte("full original file\n")}} {
		c.key = fmt.Sprintf("map-attachment-%d", i)
		notes = append(notes, c.call("POST", "/api/app/v1/notes", map[string]any{"content": "", "attachments": []map[string]string{{
			"filename": file.name, "type": file.kind, "content": base64.StdEncoding.EncodeToString(file.data),
		}}}, 201))
	}
	h.sync("full")
	settings := c.call("GET", "/api/app/v1/settings", nil, 200)
	patch := func(changes map[string]any) {
		t.Helper()
		settings = c.call("PATCH", "/api/app/v1/settings", map[string]any{"version": settings["version"], "settings": changes,
			"secrets": map[string]any{"embedding_api_key": map[string]string{"action": "replace", "value": key}}}, 200)
	}
	patch(map[string]any{"embedding": map[string]any{"enabled": true, "provider": "openai-compatible", "protocol": "openai-compatible",
		"base_url": embedding.URL + "/v1", "model": "map-fixture", "dimensions": 3, "max_retries": 0},
		"review": map[string]any{"min_age_hours": 0, "recent_days": 0}})
	h.vectorsReady()
	first := acceptanceMap(t, c)
	if len(first.Points) != len(notes) {
		t.Fatalf("one note per point: %d != %d", len(first.Points), len(notes))
	}
	points := func(result moerise.MapResponse) map[string]moerise.MapPoint {
		values := map[string]moerise.MapPoint{}
		for _, point := range result.Points {
			if _, duplicate := values[point.Name]; duplicate {
				t.Fatal("duplicate map resource")
			}
			values[point.Name] = point
		}
		return values
	}
	initial := points(first)
	for i, note := range notes {
		point, exists := initial[note["name"].(string)]
		if !exists || point.Semantic != (i < len(contents)) {
			t.Fatalf("missing note or fabricated attachment semantics: %d", i)
		}
		detail := c.call("GET", clientNotePath(note), nil, 200)
		if detail["content"] != note["content"] || point.SourceHash != detail["source_hash"] || !reflect.DeepEqual(point.Tags, toStrings(detail["tags"].([]any))) {
			t.Fatal("map reader lost complete original, native tags, or latest source hash")
		}
		if i >= len(contents) {
			attachment := detail["attachments"].([]any)[0].(map[string]any)
			req, _ := http.NewRequest("GET", h.base+attachment["content_url"].(string), nil)
			req.AddCookie(c.cookie)
			res, err := h.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(res.Body)
			res.Body.Close()
			want := png
			if i == len(contents)+1 {
				want = []byte("full original file\n")
			}
			if err != nil || res.StatusCode != 200 || !bytes.Equal(data, want) {
				t.Fatal("protected map reading changed original attachment bytes")
			}
		}
	}
	distance := func(a, b int) float64 {
		x, y := initial[notes[a]["name"].(string)], initial[notes[b]["name"].(string)]
		return math.Hypot(x.X-y.X, x.Y-y.Y)
	}
	if distance(0, 1)*3 >= distance(0, 2) || distance(2, 3)*3 >= distance(2, 4) || distance(0, 6) == 0 {
		t.Fatal("real indexed map lost semantic neighbors or merged duplicate originals")
	}
	beforeCalls := calls.Load()
	if refreshed := acceptanceMap(t, c); !reflect.DeepEqual(first, refreshed) || beforeCalls != calls.Load() {
		t.Fatal("refresh changed layout or called embedding")
	}
	h.stop()
	h.start()
	c.call("GET", "/api/app/v1/session", nil, 200)
	if restarted := acceptanceMap(t, c); !reflect.DeepEqual(first, restarted) {
		t.Fatal("Core restart changed layout")
	}
	t.Log("PASS real paginated normal/archive/duplicate/pure-attachment projection, full reading, semantic neighbors and stable restart")

	// Trend snapshots remain readable when individual source reads fail.
	faultName.Store(first.Points[len(first.Points)-1].Name)
	for _, status := range []int32{503, 403} {
		faultStatus.Store(status)
		if snapshot := acceptanceMap(t, c); !reflect.DeepEqual(first, snapshot) {
			t.Fatal("source list fault changed published snapshot")
		}
		c.call("GET", "/api/app/v1/notes/"+strings.TrimPrefix(faultName.Load().(string), "memos/"), nil, 503)
	}
	faultStatus.Store(0)
	faultName.Store("")
	h.call(h.memos, "/api/v1/"+notes[2]["name"].(string)+"?updateMask=content", "PATCH", h.upstream, map[string]string{"content": "reading changed subject #原生"}, 200)
	detail := c.call("GET", clientNotePath(notes[2]), nil, 200)
	if detail["content"] != "reading changed subject #原生" || detail["source_hash"] == initial[notes[2]["name"].(string)].SourceHash {
		t.Fatal("snapshot reader did not fetch latest original")
	}
	h.call(h.memos, "/api/v1/"+notes[4]["name"].(string), "DELETE", h.upstream, nil, 200)
	c.call("GET", clientNotePath(notes[4]), nil, 404)
	c.key = "map-added"
	added := c.call("POST", "/api/app/v1/notes", map[string]string{"content": "balcony herbs new observation"}, 201)
	h.sync("full")
	h.vectorsReady()
	if stable := acceptanceMap(t, c); !reflect.DeepEqual(first, stable) {
		t.Fatal("ordinary edits bypassed daily snapshot interval")
	}
	// Age only the derived snapshot to exercise a day boundary without a production test switch.
	h.stop()
	db := h.database("index")
	_, err := db.Exec("UPDATE map_layouts SET generated_at = ?", time.Now().UTC().Add(-48*time.Hour).Format(time.RFC3339Nano))
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	h.start()
	var incremental moerise.MapResponse
	wait(t, "daily combined snapshot", func() bool {
		incremental = acceptanceMap(t, c)
		return incremental.LayoutVersion != first.LayoutVersion
	})
	updated := points(incremental)
	if incremental.LayoutFrame != first.LayoutFrame || len(updated) != len(initial) || updated[added["name"].(string)].Name == "" || updated[notes[4]["name"].(string)].Name != "" {
		t.Fatal("daily update lost combined changes or reset the frame")
	}
	for name, old := range initial {
		if name == notes[2]["name"] || name == notes[4]["name"] {
			continue
		}
		if point := updated[name]; point.X != old.X || point.Y != old.Y {
			t.Fatalf("daily update moved unchanged point: %s", name)
		}
	}
	docker(t, "stop", h.container)
	c.call("GET", "/api/app/v1/map", nil, 503)
	docker(t, "start", h.container)
	h.sync("full")
	h.ready()
	h.vectorsReady()
	if recovered := acceptanceMap(t, c); !reflect.DeepEqual(recovered, incremental) {
		t.Fatal("source recovery changed the daily snapshot")
	}
	t.Log("PASS daily snapshots combine edits/additions/deletions; originals stay live and source authentication stays required")

	chatModel := &chatAcceptanceModel{key: randomToken(), release: make(chan struct{}, 1)}
	chatServer := httptest.NewServer(chatModel)
	defer chatServer.Close()
	settings = c.call("PATCH", "/api/app/v1/settings", map[string]any{"version": settings["version"], "settings": map[string]any{
		"llm": map[string]any{"enabled": true, "protocol": "openai-compatible", "base_url": chatServer.URL + "/v1", "model": "chat-acceptance"}},
		"secrets": map[string]any{"llm_api_key": map[string]string{"action": "replace", "value": chatModel.key}}}, 200)
	if unchanged := acceptanceMap(t, c); !reflect.DeepEqual(incremental, unchanged) {
		t.Fatal("LLM-only change rebuilt the map")
	}
	chat := acceptanceChat(t, c.call("POST", "/api/app/v1/chats", map[string]any{"request_id": "map-product-chat", "title": "地图与产品恢复", "names": []string{notes[0]["name"].(string)}}, 201))
	completeAcceptanceChat(t, c, chat.ID, moerise.ChatGenerate{RequestID: "map-product-message", Version: chat.Version, Kind: "question", Content: "讨论原文"})
	chatState := c.call("GET", "/api/app/v1/chats/"+chat.ID, nil, 200)
	plan := c.call("GET", "/api/app/v1/reviews/daily", nil, 200)
	modelFailure.Store(true)
	patch(map[string]any{"embedding": map[string]any{"model": "map-fixture-next"}})
	wait(t, "map replacement failure", func() bool {
		status := h.vectorStatus()
		return status != nil && status.Building != nil && status.Building.LastError != nil
	})
	if failed := c.call("GET", "/api/app/v1/map", nil, 200); failed["status"] != "unavailable" || len(failed["points"].([]any)) != 0 || failed["layout_frame"] != "" {
		t.Fatal("failed new model exposed old-space map")
	}
	modelFailure.Store(false)
	h.vectorsReady()
	switched := acceptanceMap(t, c)
	if switched.LayoutFrame == incremental.LayoutFrame || switched.VectorGeneration == incremental.VectorGeneration {
		t.Fatal("model-space replacement retained old coordinates/frame")
	}
	backup := filepath.Join(h.root, "map-product-backup")
	h.command(true, "backup", "--output", backup)
	for _, operation := range []string{"rebuild-index", "restore"} {
		h.stop()
		if operation == "restore" {
			h.command(true, operation, "--source", backup, "--safety-backup", filepath.Join(h.root, "map-before-restore"))
		} else {
			h.command(true, operation)
		}
		h.start()
		h.ready()
		if operation == "restore" {
			c.call("GET", "/api/app/v1/session", nil, 401)
			c.call("POST", "/api/app/v1/session", map[string]string{"username": "acceptance-owner", "password": h.password}, 200)
		}
		h.vectorsReady()
		rebuilt := acceptanceMap(t, c)
		if rebuilt.VectorGeneration == switched.VectorGeneration || !reflect.DeepEqual(rebuilt.Points, switched.Points) ||
			!reflect.DeepEqual(c.call("GET", "/api/app/v1/chats/"+chat.ID, nil, 200), chatState) ||
			!reflect.DeepEqual(c.call("GET", "/api/app/v1/reviews/daily", nil, 200), plan) ||
			!reflect.DeepEqual(c.call("GET", "/api/app/v1/settings", nil, 200), settings) {
			t.Fatalf("%s lost complete map or durable product state", operation)
		}
		c.key = "map-note-0"
		if replay := c.call("POST", "/api/app/v1/notes", map[string]string{"content": contents[0]}, 200); replay["name"] != notes[0]["name"] {
			t.Fatalf("%s lost original note idempotency", operation)
		}
	}
	patch(map[string]any{"embedding": map[string]any{"enabled": false}, "llm": map[string]any{"enabled": false}})
	if disabled := c.call("GET", "/api/app/v1/map", nil, 200); disabled["status"] != "disabled" || len(disabled["points"].([]any)) != 0 {
		t.Fatal("disabled model retained private map")
	}
	c.call("GET", clientNotePath(notes[0]), nil, 200)
	c.call("GET", "/api/app/v1/notes?query=balcony", nil, 200)
	c.call("DELETE", "/api/app/v1/session", nil, 204)
	c.call("GET", "/api/app/v1/map", nil, 401)
	t.Log("PASS model replacement/disabled status, LLM independence, map rebuild and restore preserve encrypted settings, complete chat messages, fixed review and idempotent notes")
}

func toStrings(values []any) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.(string)
	}
	return result
}

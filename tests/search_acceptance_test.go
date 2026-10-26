//go:build integration

package acceptance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSearchDiscoveryWithRealMemosAcceptance(t *testing.T) {
	var requests atomic.Int64
	embedding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer model-only-secret" {
			t.Error("incorrect embedding endpoint or credential")
		}
		var body struct {
			Input []string `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid embedding request")
		}
		requests.Add(1)
		data := []any{}
		for i, text := range body.Input {
			vector := []float32{0.01, 0.01}
			for _, term := range []string{"学习", "复习", "记忆", "遗忘"} {
				if strings.Contains(text, term) {
					vector[0]++
				}
			}
			for _, term := range []string{"运动", "跑步"} {
				if strings.Contains(text, term) {
					vector[1]++
				}
			}
			data = append(data, map[string]any{"index": i, "embedding": vector})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer embedding.Close()
	h := newHarness(t)
	h.stop()
	pointer := func(value string) *string { return &value }
	h.settings.Embedding.Model, h.settings.Embedding.BaseURL, h.settings.Embedding.APIKey = pointer("acceptance-model"), pointer(embedding.URL+"/v1"), pointer("model-only-secret")
	h.settings.Embedding.Dimensions, h.settings.Embedding.BatchSize = 2, 2
	h.settings.AccessTokens[1].Scopes = append(h.settings.AccessTokens[1].Scopes, "discovery:write")
	writeConfig := func() {
		t.Helper()
		data, _ := json.Marshal(h.settings)
		if err := os.WriteFile(h.config, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig()
	h.start()
	h.ready()
	a := h.record("search-a", h.operator, map[string]any{"content": "间隔复习帮助长期记忆", "tags": []string{"学习/复习"}}, 201)["name"].(string)
	b := h.record("search-b", h.operator, map[string]any{"content": "学习结合主动回想", "tags": []string{"学习"}}, 201)["name"].(string)
	h.record("search-c", h.operator, map[string]any{"content": "运动跑步改善身体", "tags": []string{"运动"}}, 201)
	waitIndex := func() {
		t.Helper()
		wait(t, "complete embedding index", func() bool {
			result, status, err := h.request(h.base, "/api/ext/v1/search/index", "GET", h.reader, nil)
			return err == nil && status == 200 && result["status"] == "active" && result["pending_memos"] == float64(0)
		})
	}
	waitIndex()
	query := map[string]any{"query": "遗忘", "mode": "hybrid"}
	search := h.call(h.base, "/api/ext/v1/search", "POST", h.reader, query, 200)
	if len(search["results"].([]any)) != 2 || search["mode"] != "hybrid" {
		t.Fatalf("semantic retrieval: %v", search)
	}
	tool := h.reviewTool("semantic_search", query)
	if !reflect.DeepEqual(search["results"], tool["results"]) {
		t.Fatal("REST and MCP search differ")
	}
	short := h.call(h.base, "/api/ext/v1/search", "POST", h.reader, map[string]any{"query": "习", "mode": "keyword", "tags": []string{"学习"}}, 200)
	if len(short["results"].([]any)) != 2 {
		t.Fatalf("Chinese short query or tag filter: %v", short)
	}
	related := h.call(h.base, "/api/ext/v1/notes/"+strings.TrimPrefix(a, "memos/")+"/related", "GET", h.reader, nil, 200)
	if len(related["results"].([]any)) != 1 || related["results"].([]any)[0].(map[string]any)["note"].(map[string]any)["name"] != b {
		t.Fatalf("related: %v", related)
	}
	walk := h.reviewTool("semantic_walk", map[string]any{"name": a, "mode": "continue"})
	if walk["item"].(map[string]any)["note"].(map[string]any)["name"] != b {
		t.Fatalf("walk: %v", walk)
	}
	h.call(h.base, "/api/ext/v1/discovery/open", "POST", h.reader, map[string]string{"name": b}, 403)
	h.reviewTool("open_discovery_memo", map[string]string{"name": b})
	t.Log("PASS real Memos hybrid, Chinese short queries, tags, related, REST/MCP and explicit reading")

	// Live checks must reject stale semantic matches before a subsequent synchronization.
	h.call(h.memos, "/api/v1/"+a, "DELETE", h.upstream, nil, 200)
	h.call(h.memos, "/api/v1/"+b+"?updateMask=state", "PATCH", h.upstream, map[string]string{"state": "ARCHIVED"}, 200)
	search = h.call(h.base, "/api/ext/v1/search", "POST", h.reader, query, 200)
	if len(search["results"].([]any)) != 0 {
		t.Fatalf("deleted/archived notes leaked: %v", search)
	}
	h.sync("full")
	waitIndex()
	snapshot := filepath.Join(h.root, "search-snapshot")
	h.command(true, "backup", "--output", snapshot)
	for _, operation := range []string{"restart", "rebuild-index", "restore", "model-switch"} {
		h.stop()
		switch operation {
		case "rebuild-index":
			h.command(true, "rebuild-index")
		case "restore":
			h.command(true, "restore", "--source", snapshot, "--safety-backup", filepath.Join(h.root, "search-safety"))
		case "model-switch":
			h.settings.Embedding.Model = pointer("replacement-model")
			writeConfig()
		}
		h.start()
		h.ready()
		waitIndex()
		search = h.call(h.base, "/api/ext/v1/search", "POST", h.reader, map[string]any{"query": "遗忘", "mode": "semantic", "include_archived": true}, 200)
		if len(search["results"].([]any)) != 1 || search["results"].([]any)[0].(map[string]any)["note"].(map[string]any)["name"] != b {
			t.Fatalf("%s failed: %v", operation, search)
		}
		db := h.database("state")
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM discovery_reads WHERE memo_name = ?", b).Scan(&count)
		db.Close()
		if err != nil || count != 1 {
			t.Fatalf("%s lost read history: %v", operation, err)
		}
	}
	if requests.Load() == 0 {
		t.Fatal("embedding provider was not used")
	}
	t.Log("PASS live deletion/archive filtering, index rebuild, backup restore, restart and model replacement")
}

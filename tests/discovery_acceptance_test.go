//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	moerise "moerise/internal"
)

type discoveryEmbeddings struct {
	queryCalls int
}

func (p *discoveryEmbeddings) Close() {}

func (p *discoveryEmbeddings) Embed(_ context.Context, texts []string, purpose moerise.EmbeddingPurpose) (moerise.EmbeddingResult, error) {
	if purpose == moerise.EmbeddingQuery {
		p.queryCalls++
	}
	result := moerise.EmbeddingResult{}
	for _, text := range texts {
		vector := make([]float32, 256)
		vector[0] = 1
		if strings.Contains(text, "anglebody") {
			vector[0], vector[1] = 0.9, 0.4
		} else if strings.Contains(text, "otherbody") {
			vector[0], vector[1] = 0, 1
		}
		result.Vectors = append(result.Vectors, vector)
	}
	return result, nil
}

func TestDiscoveryAcrossEntrypointsAcceptance(t *testing.T) {
	h := newHarness(t)
	record := func(key, body, tag string) string {
		return h.record(key, h.operator, map[string]any{"content": body, "tags": []string{tag}}, 201)["name"].(string)
	}
	seed := record("walk-seed", "seedbody", "study/base")
	near := record("walk-near", "nearbody", "study/recall")
	angle := record("walk-angle", "anglebody", "creativity")
	other := record("walk-other", "otherbody", "sport")
	h.sync("full")
	h.stop()
	settings := h.settings
	settings.AccessTokens[1].Scopes = append(settings.AccessTokens[1].Scopes, "discovery:write")
	settings.Embedding.Enabled, settings.Embedding.Dimensions = true, 256
	address, key := "http://embedding.test/api/v1", strings.Repeat("k", 32)
	settings.Embedding.BaseURL, settings.Embedding.APIKey = &address, &key
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
	version, err := source.Identity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SourceHealth(true, "", version); err != nil {
		t.Fatal(err)
	}
	provider := &discoveryEmbeddings{}
	index := moerise.NewVectorIndexService(store, source, provider)
	for range 20 {
		if _, err := index.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
		status, err := store.VectorStatus()
		if err != nil {
			t.Fatal(err)
		}
		if status.Ready {
			break
		}
	}
	api, err := moerise.NewAPI(settings, store, source, moerise.NewSyncService(store, source))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api)
	defer server.Close()
	walk := func(request moerise.WalkRequest) moerise.WalkResponse {
		t.Helper()
		status, err := store.VectorStatus()
		if err != nil || !status.Ready {
			t.Fatalf("walk index before mode=%q path=%v: %+v %v", request.Mode, request.Path, status, err)
		}
		raw := h.call(server.URL, "/api/ext/v1/discovery/walk", "POST", h.reader, request, 200)
		data, _ := json.Marshal(raw)
		var result moerise.WalkResponse
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Path == nil || result.Warnings == nil {
			t.Fatalf("discovery arrays must not be null: %s", data)
		}
		return result
	}
	continued := walk(moerise.WalkRequest{Name: seed})
	if continued.Item == nil || continued.Item.Note.Name != near || !slices.Equal(continued.Path, []string{seed, near}) {
		t.Fatalf("continue did not prefer the same topic: %+v", continued)
	}
	angled := walk(moerise.WalkRequest{Name: seed, Mode: "angle"})
	if angled.Item == nil || angled.Item.Note.Name != angle {
		t.Fatalf("angle did not change topic: %+v", angled)
	}
	random := walk(moerise.WalkRequest{Name: seed, Mode: "random", Path: []string{near, angle}})
	if random.Item == nil || random.Item.Note.Name != other {
		t.Fatalf("random did not exclude the path: %+v", random)
	}
	indexStatus := h.call(server.URL, "/api/ext/v1/search/index", "GET", h.reader, nil, 200)
	if indexStatus["ready"] != true {
		t.Fatal("read-only index status does not expose the active main generation")
	}
	for _, invalid := range []any{
		map[string]any{}, map[string]any{"mode": "other"}, map[string]any{"mode": "random", "unknown": true},
		map[string]any{"name": seed, "path": []string{"bad-resource"}},
	} {
		requireRecordError(t, h.call(server.URL, "/api/ext/v1/discovery/walk", "POST", h.reader, invalid, 422), "INVALID_ARGUMENT")
	}
	h.call(server.URL, "/api/ext/v1/discovery/walk", "GET", h.reader, nil, 405)
	h.call(server.URL, "/api/ext/v1/discovery/walk", "POST", "", map[string]string{"mode": "random"}, 401)
	requireRecordError(t, h.call(server.URL, "/api/ext/v1/discovery/open", "POST", h.reader,
		map[string]string{"name": near}, 403), "FORBIDDEN")
	h.call(server.URL, "/api/ext/v1/discovery/open", "POST", h.operator, map[string]string{"name": near}, 200)
	afterOpen := walk(moerise.WalkRequest{Name: seed})
	if afterOpen.Item == nil || afterOpen.Item.Note.Name != angle {
		t.Fatal("REST open did not update shared recent-reading ranking")
	}
	mcp := func(token, method string, params any) map[string]any {
		t.Helper()
		raw, code, err := h.requestWithHeaders(server.URL, "/mcp", "POST", token,
			map[string]any{"jsonrpc": "2.0", "id": "discovery", "method": method, "params": params},
			map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
		if err != nil || code != 200 || raw["error"] != nil {
			t.Fatalf("MCP discovery: code=%d err=%v response=%v", code, err, raw)
		}
		return raw["result"].(map[string]any)
	}
	for _, token := range []string{h.reader, h.operator} {
		visible := map[string]bool{}
		for _, raw := range mcp(token, "tools/list", map[string]any{})["tools"].([]any) {
			tool := raw.(map[string]any)
			visible[tool["name"].(string)] = true
			if tool["name"] == "semantic_walk" && tool["annotations"].(map[string]any)["readOnlyHint"] != true {
				t.Fatal("MCP walk did not preserve its read-only annotation")
			}
		}
		if !visible["semantic_walk"] || visible["open_discovery_memo"] {
			t.Fatalf("incorrect discovery tool scopes: %v", visible)
		}
		denied, code, err := h.requestWithHeaders(server.URL, "/mcp", "POST", token,
			map[string]any{"jsonrpc": "2.0", "id": "removed-open", "method": "tools/call",
				"params": map[string]any{"name": "open_discovery_memo", "arguments": map[string]string{"name": angle}}},
			map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
		deniedResult, _ := denied["result"].(map[string]any)
		if err != nil || code != 200 || denied["error"] == nil && deniedResult["isError"] != true {
			t.Fatalf("MCP exposed the removed reading tool: status=%d err=%v response=%v", code, err, denied)
		}
	}
	result := mcp(h.reader, "tools/call", map[string]any{"name": "semantic_walk", "arguments": map[string]string{"name": seed}})
	data, _ := json.Marshal(result["structuredContent"])
	var fromMCP moerise.WalkResponse
	if json.Unmarshal(data, &fromMCP) != nil || !reflect.DeepEqual(fromMCP, afterOpen) {
		t.Fatalf("REST/MCP discovery differs: %s", data)
	}
	h.call(server.URL, "/api/ext/v1/discovery/open", "POST", h.operator, map[string]string{"name": other}, 200)
	db := h.database("state")
	defer db.Close()
	var opened string
	if err := db.QueryRow("SELECT opened_at FROM discovery_reads WHERE memo_name = ?", other).Scan(&opened); err != nil || opened == "" {
		t.Fatalf("REST open did not persist explicit reading: %q %v", opened, err)
	}
	h.call(h.memos, "/api/v1/"+seed, "DELETE", h.upstream, nil, 200)
	requireRecordError(t, h.call(server.URL, "/api/ext/v1/discovery/walk", "POST", h.reader,
		moerise.WalkRequest{Name: seed}, 404), "SOURCE_NOT_FOUND")
	if provider.queryCalls != 0 {
		t.Fatal("discovery called the query embedding model")
	}
	t.Log("PASS semantic/random walk, read-only MCP parity, REST reading, and deleted source boundary")
}

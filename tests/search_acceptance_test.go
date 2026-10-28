//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	moerise "moerise/internal"
)

func TestSearchAcceptance(t *testing.T) {
	const semanticQuery = "怎样舒缓焦虑"
	const semanticBody = "暮色沿湖慢跑，呼吸逐渐平稳。"
	var documentsEnabled, queryFailure, sourceFailure atomic.Bool
	var documentCalls, queryCalls atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input struct {
				Texts []string `json:"texts"`
			} `json:"input"`
			Parameters struct {
				Dimension int    `json:"dimension"`
				Purpose   string `json:"text_type"`
			} `json:"parameters"`
		}
		if r.Method != http.MethodPost ||
			r.URL.Path != "/api/v1/services/embeddings/text-embedding/text-embedding" ||
			json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Input.Texts) == 0 ||
			request.Parameters.Dimension != 256 ||
			!slices.Contains([]string{"document", "query"}, request.Parameters.Purpose) {
			t.Error("invalid DashScope search request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Parameters.Purpose == "document" {
			if !documentsEnabled.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			documentCalls.Add(1)
		} else {
			queryCalls.Add(1)
			if queryFailure.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		items := make([]any, len(request.Input.Texts))
		for i, text := range request.Input.Texts {
			vector := make([]float32, request.Parameters.Dimension)
			axis := 1
			if request.Parameters.Purpose == "query" {
				axis = 2
			}
			if text == semanticQuery || strings.Contains(text, semanticBody) {
				axis = 0
			}
			vector[axis] = 1
			items[len(items)-1-i] = map[string]any{"text_index": i, "embedding": vector}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{"embeddings": items},
			"usage":  map[string]int{"total_tokens": len(items)},
		})
	}))
	defer model.Close()
	var sourceProxy *httptest.Server
	h := newHarnessWithUpstream(t, func(address string) string {
		target, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		sourceProxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sourceFailure.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			proxy.ServeHTTP(w, r)
		}))
		t.Cleanup(sourceProxy.Close)
		return sourceProxy.URL
	})
	decode := func(raw map[string]any) moerise.SearchResponse {
		t.Helper()
		data, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		var result moerise.SearchResponse
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Results == nil {
			t.Fatalf("search results must be an array: %s", data)
		}
		seen := map[string]bool{}
		for _, hit := range result.Results {
			note := hit.Note
			if seen[note.Name] || hit.Score <= 0 || len(hit.Matches) < 1 || len(hit.Matches) > 3 ||
				note.Creator != h.settings.Memos.User || note.InstanceID != h.settings.Memos.InstanceID ||
				note.URL != h.settings.Memos.PublicURL+"/"+note.Name {
				t.Fatalf("invalid or duplicate search hit: %+v", hit)
			}
			seen[note.Name] = true
			ordinals := map[int]bool{}
			for _, match := range hit.Matches {
				if match.Ordinal < 0 || ordinals[match.Ordinal] || match.ByteStart < 0 ||
					match.ByteEnd > len(note.Content) || match.ByteStart >= match.ByteEnd ||
					note.Content[match.ByteStart:match.ByteEnd] != match.Content || !utf8.ValidString(match.Content) {
					t.Fatalf("invalid source byte range: %+v", match)
				}
				ordinals[match.Ordinal] = true
				last := match.ByteEnd
				if note.Content[last-1] == '\n' {
					last--
				}
				if match.LineStart != 1+strings.Count(note.Content[:match.ByteStart], "\n") ||
					match.LineEnd != 1+strings.Count(note.Content[:last], "\n") || len(match.Signals) == 0 {
					t.Fatalf("incorrect source line range or missing signals: %+v", match)
				}
				for _, signal := range match.Signals {
					if !slices.Contains([]string{"keyword", "tag", "semantic"}, signal) {
						t.Fatalf("unknown search signal: %q", signal)
					}
				}
			}
		}
		return result
	}
	search := func(base string, request moerise.SearchRequest) moerise.SearchResponse {
		t.Helper()
		result := decode(h.call(base, "/api/ext/v1/search", "POST", h.reader, request, 200))
		if result.Query != strings.TrimSpace(request.Query) {
			t.Fatalf("incorrect response query: %q", result.Query)
		}
		return result
	}
	requireNames := func(result moerise.SearchResponse, names ...string) {
		t.Helper()
		got := make([]string, 0, len(result.Results))
		for _, hit := range result.Results {
			got = append(got, hit.Note.Name)
		}
		want := append([]string{}, names...)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("search resources=%v, expected=%v", got, want)
		}
	}
	requireSemantic := func(result moerise.SearchResponse, used bool, reason string) {
		t.Helper()
		if result.Semantic.Used != used || result.Semantic.Reason != reason {
			t.Fatalf("unexpected semantic status: %+v", result.Semantic)
		}
	}
	record := func(key, content string, tags ...string) string {
		t.Helper()
		if tags == nil {
			tags = []string{}
		}
		return h.record(key, h.operator, moerise.CreateNote{Content: content, Tags: tags}, 201)["name"].(string)
	}
	var moonNames, gardenNames, andNames []string
	for i := range 12 {
		tags := []string{"庭院记"}
		if i < 4 {
			tags = []string{"庭院"}
		}
		if i%2 == 0 {
			tags = append(tags, "随记")
		}
		name := record(fmt.Sprintf("search-moon-%d", i), fmt.Sprintf("月港值守记录 %d：窗边留有一盏小灯。", i), tags...)
		moonNames = append(moonNames, name)
		if i < 4 {
			gardenNames = append(gardenNames, name)
			if i%2 == 0 {
				andNames = append(andNames, name)
			}
		}
	}
	longBody := "纸鹤甲\n" + strings.Repeat("a", 1500) + "\n\n" +
		"纸鹤乙\n" + strings.Repeat("b", 1500) + "\n\n" +
		"纸鹤丙\n" + strings.Repeat("c", 1500) + "\n"
	longName := record("search-long", longBody)
	semanticName := record("search-semantic", semanticBody)
	edited := record("search-edit", "琥珀旧稿仍在木桌上。")
	deleted := record("search-delete", "琥珀撤稿放在抽屉里。")
	archived := record("search-archive", "琥珀封箱写着旧航线。")
	h.sync("full")

	h.call(h.base, "/api/ext/v1/search", "POST", "", moerise.SearchRequest{Query: "月"}, 401)
	defaults := search(h.base, moerise.SearchRequest{Query: "  月  "})
	if len(defaults.Results) != 10 {
		t.Fatalf("default search limit: %d", len(defaults.Results))
	}
	requireSemantic(defaults, false, "EMBEDDING_DISABLED")
	requireNames(search(h.base, moerise.SearchRequest{Query: "月", Limit: 20}), moonNames...)
	requireNames(search(h.base, moerise.SearchRequest{Query: "月港", Tags: []string{"庭院"}}), gardenNames...)
	requireNames(search(h.base, moerise.SearchRequest{Query: "月港", Tags: []string{"庭院", "随记"}}), andNames...)
	tagHit := search(h.base, moerise.SearchRequest{Query: "#庭院", Tags: []string{"庭院"}})
	requireNames(tagHit, gardenNames...)
	for _, hit := range tagHit.Results {
		if !slices.Contains(hit.Matches[0].Signals, "tag") {
			t.Fatal("tag retrieval did not identify its signal")
		}
	}
	long := search(h.base, moerise.SearchRequest{Query: "纸鹤"})
	requireNames(long, longName)
	if len(long.Results[0].Matches) != 3 || long.Results[0].Note.Content != longBody {
		t.Fatalf("long note chunks were not merged: %+v", long)
	}
	for _, match := range long.Results[0].Matches {
		if !slices.Contains(match.Signals, "keyword") || !strings.Contains(match.Content, "纸鹤") {
			t.Fatalf("long note did not return all matching paragraphs: %+v", match)
		}
	}
	requireNames(search(h.base, moerise.SearchRequest{Query: semanticQuery}))
	t.Log("PASS Chinese short queries, exact AND tags, default limit, and original chunk byte/line positions")

	requireNames(search(h.base, moerise.SearchRequest{Query: "琥珀"}), edited, deleted, archived)
	h.call(h.memos, "/api/v1/"+edited+"?updateMask=content", "PATCH", h.upstream,
		map[string]string{"content": "红帆新稿已搬到书架。"}, 200)
	h.call(h.memos, "/api/v1/"+deleted, "DELETE", h.upstream, nil, 200)
	h.call(h.memos, "/api/v1/"+archived+"?updateMask=state", "PATCH", h.upstream,
		map[string]string{"state": "ARCHIVED"}, 200)
	// No sync intervenes: search must reject stale candidates using the live source.
	requireNames(search(h.base, moerise.SearchRequest{Query: "琥珀"}))
	requireNames(search(h.base, moerise.SearchRequest{Query: "琥珀", State: "ARCHIVED"}), archived)
	requireNames(search(h.base, moerise.SearchRequest{Query: "琥珀", State: "ALL"}), archived)
	fresh := search(h.base, moerise.SearchRequest{Query: "红帆"})
	requireNames(fresh, edited)
	if fresh.Results[0].Note.Content != "红帆新稿已搬到书架。" {
		t.Fatal("source refresh did not repair the keyword index")
	}
	t.Log("PASS unsynchronized source edit, deletion, archive, and repaired keyword retrieval")

	h.stop()
	h.settings.Embedding.Enabled = true
	h.settings.Embedding.Dimensions, h.settings.Embedding.BatchSize, h.settings.Embedding.MaxRetries = 256, 2, 0
	address, key := model.URL+"/api/v1", "search-acceptance-model-key"
	h.settings.Embedding.BaseURL, h.settings.Embedding.APIKey = &address, &key
	h.settings.Sync.RetrySeconds, h.settings.Sync.PollSeconds, h.settings.Sync.FullSeconds = 1, 3600, 3600
	data, err := json.Marshal(h.settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.ready()
	wait(t, "search index awaiting document model", func() bool {
		status := h.vectorStatus()
		return status != nil && status.Building != nil && status.Building.LastError != nil
	})
	notReady := search(h.base, moerise.SearchRequest{Query: "月港", Tags: []string{"庭院"}})
	requireNames(notReady, gardenNames...)
	requireSemantic(notReady, false, "INDEX_NOT_READY")
	documentsEnabled.Store(true)
	active := h.vectorsReady()
	paraphrase := search(h.base, moerise.SearchRequest{Query: semanticQuery})
	requireNames(paraphrase, semanticName)
	requireSemantic(paraphrase, true, "")
	if paraphrase.Results[0].Note.Content != semanticBody ||
		!slices.Equal(paraphrase.Results[0].Matches[0].Signals, []string{"semantic"}) {
		t.Fatalf("paraphrase was not recalled solely through vectors: %+v", paraphrase)
	}
	requireNames(search(h.base, moerise.SearchRequest{Query: semanticQuery, Tags: []string{"庭院"}}))
	queryFailure.Store(true)
	fallback := search(h.base, moerise.SearchRequest{Query: "月港", Tags: []string{"庭院"}})
	requireNames(fallback, gardenNames...)
	requireSemantic(fallback, false, "EMBEDDING_UNAVAILABLE")
	requireNames(search(h.base, moerise.SearchRequest{Query: semanticQuery}))
	queryFailure.Store(false)
	sourceFailure.Store(true)
	unavailable := h.call(h.base, "/api/ext/v1/search", "POST", h.reader, moerise.SearchRequest{Query: "月"}, 503)
	requireRecordError(t, unavailable, "UPSTREAM_UNAVAILABLE")
	if unavailable["results"] != nil {
		t.Fatal("source outage exposed cached search results")
	}
	sourceFailure.Store(false)
	requireNames(search(h.base, moerise.SearchRequest{Query: semanticQuery}), semanticName)
	t.Log("PASS vector-only paraphrase, unready index, model degradation, and source outage")

	beforeDocuments := documentCalls.Load()
	h.stop()
	h.start()
	h.ready()
	restarted := h.vectorsReady()
	afterRestart := search(h.base, moerise.SearchRequest{Query: semanticQuery})
	if restarted.Active.ID != active.Active.ID || documentCalls.Load() != beforeDocuments ||
		!reflect.DeepEqual(paraphrase, afterRestart) || queryCalls.Load() == 0 {
		t.Fatalf("restart did not retain the search index: old=%s new=%s document calls=%d/%d",
			active.Active.ID, restarted.Active.ID, beforeDocuments, documentCalls.Load())
	}
	mcpSearch := func(request moerise.SearchRequest) moerise.SearchResponse {
		t.Helper()
		result := h.mcp(h.reader, "tools/call", map[string]any{"name": "semantic_search", "arguments": request})
		if result["isError"] == true {
			t.Fatalf("MCP search failed: %v", result)
		}
		content, ok := result["structuredContent"].(map[string]any)
		if !ok {
			t.Fatalf("MCP search missing structured response: %v", result)
		}
		return decode(content)
	}
	for _, request := range []moerise.SearchRequest{
		{Query: semanticQuery}, {Query: "月港", Tags: []string{"随记", "庭院"}, Limit: 5},
		{Query: "琥珀", State: "ARCHIVED"},
	} {
		if rest, mcp := search(h.base, request), mcpSearch(request); !reflect.DeepEqual(rest, mcp) {
			t.Fatalf("REST/MCP search differs: REST=%+v MCP=%+v", rest, mcp)
		}
	}
	t.Log("PASS persistent restart and REST/MCP search parity")
}

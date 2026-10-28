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
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	moerise "moerise/internal"
)

func TestRelatedAcceptance(t *testing.T) {
	var sourceFailure, documentsEnabled atomic.Bool
	var documentCalls, queryCalls atomic.Int64
	var failCandidate, changeSource atomic.Pointer[string]
	var sourceReads atomic.Int64
	const seedBody, companionBody = "violetseed", "ambercompanion"
	var axesMu sync.Mutex
	axes := map[string]int{seedBody: 0, companionBody: 0}
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
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid embedding JSON")
			w.WriteHeader(400)
			return
		}
		if request.Parameters.Purpose == "query" {
			queryCalls.Add(1)
			t.Error("related notes called the query model")
			w.WriteHeader(503)
			return
		}
		if r.Method != http.MethodPost ||
			r.URL.Path != "/api/v1/services/embeddings/text-embedding/text-embedding" ||
			request.Parameters.Purpose != "document" || request.Parameters.Dimension != 256 ||
			len(request.Input.Texts) == 0 || len(request.Input.Texts) > 2 {
			t.Error("invalid document embedding request")
			w.WriteHeader(400)
			return
		}
		if !documentsEnabled.Load() {
			w.WriteHeader(503)
			return
		}
		documentCalls.Add(1)
		items := make([]any, len(request.Input.Texts))
		for i, text := range request.Input.Texts {
			vector := make([]float32, request.Parameters.Dimension)
			axesMu.Lock()
			axis, exists := axes[text]
			if !exists {
				axis = len(axes)
				axes[text] = axis
			}
			axesMu.Unlock()
			vector[axis] = 1
			items[len(items)-1-i] = map[string]any{"text_index": i, "embedding": vector}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{"embeddings": items},
			"usage":  map[string]int{"total_tokens": len(items)},
		})
	}))
	defer model.Close()

	var h *harness
	// Keep the dense fixture below the upstream per-user request budget.
	pace := time.NewTicker(125 * time.Millisecond)
	defer pace.Stop()
	h = newHarnessWithUpstream(t, func(address string) string {
		target, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ModifyResponse = func(response *http.Response) error {
			if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
				t.Logf("Memos %s %s returned %d", response.Request.Method, response.Request.URL.Path, response.StatusCode)
			}
			return nil
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if sourceFailure.Load() {
				w.WriteHeader(503)
				return
			}
			if name := failCandidate.Load(); name != nil && r.URL.Path == "/api/v1/"+*name {
				w.WriteHeader(503)
				return
			}
			// Mutate through the real Memos endpoint between the two source reads.
			if name := changeSource.Load(); name != nil && r.Method == http.MethodGet &&
				r.URL.Path == "/api/v1/"+*name && sourceReads.Add(1) == 2 {
				raw, code, err := h.request(address, "/api/v1/"+*name+"?updateMask=content",
					"PATCH", h.upstream, map[string]string{"content": "changed during related retrieval"})
				if err != nil || code != 200 {
					t.Errorf("source mutation: code=%d err=%v response=%v", code, err, raw)
					w.WriteHeader(503)
					return
				}
			}
			select {
			case <-pace.C:
				proxy.ServeHTTP(w, r)
			case <-r.Context().Done():
			}
		}))
		t.Cleanup(server.Close)
		return server.URL
	})
	record := func(key, body string, tags ...string) string {
		t.Helper()
		input := map[string]any{"content": body}
		if len(tags) > 0 {
			input["tags"] = tags
		}
		return h.record("related-"+key, h.operator, input, 201)["name"].(string)
	}
	path := func(name string) string {
		return "/api/ext/v1/notes/" + strings.TrimPrefix(name, "memos/") + "/related"
	}
	decode := func(raw map[string]any) moerise.RelatedResponse {
		t.Helper()
		data, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		var result moerise.RelatedResponse
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		checkNote := func(note moerise.Note) {
			t.Helper()
			if !strings.HasPrefix(note.Name, "memos/") || note.Creator != h.settings.Memos.User ||
				note.InstanceID != h.settings.Memos.InstanceID || note.SourceHash == "" ||
				note.URL != h.settings.Memos.PublicURL+"/"+note.Name {
				t.Fatalf("invalid related note: %+v", note)
			}
		}
		checkNote(result.Source)
		if result.Results == nil {
			t.Fatalf("related results must be an array: %s", data)
		}
		seen := map[string]bool{result.Source.Name: true}
		for _, hit := range result.Results {
			checkNote(hit.Note)
			if seen[hit.Note.Name] || hit.Score <= 0 || len(hit.Matches) < 1 || len(hit.Matches) > 3 {
				t.Fatalf("self, duplicate, or invalid related hit: %+v", hit)
			}
			seen[hit.Note.Name] = true
			texts, ordinals := map[string]bool{}, map[int]bool{}
			for _, match := range hit.Matches {
				if match.Ordinal < 0 || ordinals[match.Ordinal] || texts[match.Content] ||
					match.ByteStart < 0 || match.ByteEnd > len(hit.Note.Content) ||
					match.ByteStart >= match.ByteEnd || !utf8.ValidString(match.Content) ||
					hit.Note.Content[match.ByteStart:match.ByteEnd] != match.Content {
					t.Fatalf("duplicate body or invalid related byte range: %+v", match)
				}
				texts[match.Content], ordinals[match.Ordinal] = true, true
				last := match.ByteEnd
				if hit.Note.Content[last-1] == '\n' {
					last--
				}
				if match.LineStart != 1+strings.Count(hit.Note.Content[:match.ByteStart], "\n") ||
					match.LineEnd != 1+strings.Count(hit.Note.Content[:last], "\n") || len(match.Signals) == 0 {
					t.Fatalf("incorrect related line range: %+v", match)
				}
				signals := map[string]bool{}
				for _, signal := range match.Signals {
					if signals[signal] || !slices.Contains([]string{"keyword", "tag", "semantic"}, signal) {
						t.Fatalf("invalid related signals: %+v", match)
					}
					signals[signal] = true
				}
			}
		}
		return result
	}
	related := func(base, name, query string) moerise.RelatedResponse {
		t.Helper()
		result := decode(h.call(base, path(name)+query, "GET", h.reader, nil, 200))
		if result.Source.Name != name {
			t.Fatalf("wrong related source: %+v", result.Source)
		}
		return result
	}
	names := func(result moerise.RelatedResponse) []string {
		got := make([]string, 0, len(result.Results))
		for _, hit := range result.Results {
			got = append(got, hit.Note.Name)
		}
		return got
	}
	requireNames := func(result moerise.RelatedResponse, want ...string) {
		t.Helper()
		got := names(result)
		want = append([]string{}, want...)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("related resources=%v expected=%v", got, want)
		}
	}
	requireSemantic := func(result moerise.RelatedResponse, used bool, reason string) {
		t.Helper()
		if result.Semantic.Used != used || result.Semantic.Reason != reason {
			t.Fatalf("unexpected related semantic status: %+v", result.Semantic)
		}
	}
	noPartial := func(raw map[string]any, code string) {
		t.Helper()
		requireRecordError(t, raw, code)
		if raw["source"] != nil || raw["results"] != nil {
			t.Fatalf("related failure exposed partial content: %v", raw)
		}
	}

	tagSeed := record("tag-source", "originroot", "anchor", "secondary")
	var tagNames, exactNames, andNames []string
	for i := range 12 {
		tags := []string{"anchor", "precisely-more"}
		if i < 4 {
			tags = []string{"anchor", "precisely"}
		}
		if i%2 == 0 {
			tags = append(tags, "secondary")
		}
		name := record(fmt.Sprintf("tag-%d", i), fmt.Sprintf("candidateword%d", i), tags...)
		tagNames = append(tagNames, name)
		if i < 4 {
			exactNames = append(exactNames, name)
			if i%2 == 0 {
				andNames = append(andNames, name)
			}
		}
	}
	tailBody := "headword\n" + strings.Repeat("x", 350) + "\n\ntailbeacon"
	tailSeed := record("tail-source", tailBody)
	tailName := record("tail-candidate", "tailbeacon")
	record("oversize-word", strings.Repeat("x", 350))
	longSeed := record("long-source", "纸鹤")
	longBody := "纸鹤甲\n" + strings.Repeat("a", 1500) + "\n\n" +
		"纸鹤乙\n" + strings.Repeat("b", 1500) + "\n\n" +
		"纸鹤丙\n" + strings.Repeat("c", 1500) + "\n"
	longName := record("long-candidate", longBody)
	repeatedBody := strings.Repeat("纸鹤丁\n"+strings.Repeat("d", 1500)+"\n\n", 3)
	repeatedName := record("duplicate-candidate", repeatedBody)
	liveSeed := record("live-source", "saffronbeacon")
	edited := record("edit", "saffronbeacon old")
	deleted := record("delete", "saffronbeacon discarded")
	archived := record("archive", "saffronbeacon boxed")
	semanticSeed := record("semantic-source", seedBody)
	semanticName := record("semantic-candidate", companionBody)
	h.sync("full")

	h.call(h.base, path(tagSeed), "GET", "", nil, 401)
	h.call(h.base, path(tagSeed), "POST", h.reader, nil, 405)
	for _, query := range []string{
		"?unknown=1", "?limit=1&limit=2", "?state=NORMAL&state=ALL",
		"?limit=0", "?limit=21", "?limit=-1", "?limit=1.5", "?limit=nope", "?limit=",
		"?state=normal", "?state=DELETED", "?tags=", "?tags=%23anchor",
		"?tags=a%20b", "?tags=a//b", "?tags=" + strings.Repeat("a", 101),
		"?" + strings.Repeat("tags=anchor&", 20) + "tags=anchor",
	} {
		requireRecordError(t, h.call(h.base, path(tagSeed)+query, "GET", h.reader, nil, 422), "INVALID_ARGUMENT")
	}
	defaults := related(h.base, tagSeed, "")
	if len(defaults.Results) != 10 {
		t.Fatalf("default related limit=%d", len(defaults.Results))
	}
	requireSemantic(defaults, false, "EMBEDDING_DISABLED")
	all := related(h.base, tagSeed, "?limit=20")
	requireNames(all, tagNames...)
	requireNames(related(h.base, tagSeed, "?tags=precisely"), exactNames...)
	requireNames(related(h.base, tagSeed, "?tags=precisely&tags=secondary"), andNames...)
	if len(related(h.base, tagSeed, "?limit=1").Results) != 1 {
		t.Fatal("related limit=1 was not applied")
	}
	// Shared tags rank by overlap, even when the source lacks the candidate filter.
	for i, hit := range all.Results {
		if slices.Contains(hit.Note.Tags, "secondary") != (i < 6) ||
			!slices.Contains(hit.Matches[0].Signals, "tag") {
			t.Fatalf("shared-tag overlap ranking or signal: %+v", all)
		}
	}
	tail := related(h.base, tailSeed, "")
	requireNames(tail, tailName)
	if tail.Source.Content != tailBody || !slices.Contains(tail.Results[0].Matches[0].Signals, "keyword") {
		t.Fatal("full source tail did not participate in lexical retrieval")
	}
	long := related(h.base, longSeed, "")
	requireNames(long, longName, repeatedName)
	for _, hit := range long.Results {
		want := 3
		if hit.Note.Name == repeatedName {
			want = 1
		}
		if len(hit.Matches) != want {
			t.Fatalf("related chunk merge: got %d want %d for %s", len(hit.Matches), want, hit.Note.Name)
		}
	}
	t.Log("PASS related GET validation, full-source keywords, exact AND tags, overlap ranking, and distinct chunks")

	requireNames(related(h.base, liveSeed, ""), edited, deleted, archived)
	h.call(h.memos, "/api/v1/"+edited+"?updateMask=content", "PATCH", h.upstream,
		map[string]string{"content": "replacementbeacon"}, 200)
	h.call(h.memos, "/api/v1/"+deleted, "DELETE", h.upstream, nil, 200)
	h.call(h.memos, "/api/v1/"+archived+"?updateMask=state", "PATCH", h.upstream,
		map[string]string{"state": "ARCHIVED"}, 200)
	requireNames(related(h.base, liveSeed, ""))
	requireNames(related(h.base, liveSeed, "?state=ARCHIVED"), archived)
	requireNames(related(h.base, liveSeed, "?state=ALL"), archived)
	h.call(h.memos, "/api/v1/"+liveSeed+"?updateMask=content,state", "PATCH", h.upstream,
		map[string]string{"content": "replacementbeacon", "state": "ARCHIVED"}, 200)
	repaired := related(h.base, liveSeed, "")
	requireNames(repaired, edited)
	if repaired.Source.Content != "replacementbeacon" || repaired.Source.State != "ARCHIVED" {
		t.Fatal("archived source was not refreshed before candidate retrieval")
	}
	noPartial(h.call(h.base, path(deleted), "GET", h.reader, nil, 404), "SOURCE_NOT_FOUND")
	noPartial(h.call(h.base, path("memos/nonexistent-related-source"), "GET", h.reader, nil, 404), "SOURCE_NOT_FOUND")
	password := randomToken()
	h.call(h.memos, "/api/v1/users", "POST", h.upstream,
		map[string]string{"username": "related-other", "password": password, "role": "USER"}, 200)
	session := h.call(h.memos, "/api/v1/auth/signin", "POST", "",
		map[string]any{"passwordCredentials": map[string]string{"username": "related-other", "password": password}}, 200)
	foreign := h.call(h.memos, "/api/v1/memos", "POST", session["accessToken"].(string),
		map[string]string{"content": "replacementbeacon", "visibility": "PRIVATE"}, 200)["name"].(string)
	noPartial(h.call(h.base, path(foreign), "GET", h.reader, nil, 404), "SOURCE_NOT_FOUND")
	failCandidate.Store(&tailName)
	noPartial(h.call(h.base, path(tailSeed), "GET", h.reader, nil, 503), "UPSTREAM_UNAVAILABLE")
	failCandidate.Store(nil)
	sourceReads.Store(0)
	changeSource.Store(&tailSeed)
	changed := h.call(h.base, path(tailSeed), "GET", h.reader, nil, 409)
	changeSource.Store(nil)
	noPartial(changed, "SOURCE_CHANGED")
	if changed["error"].(map[string]any)["retryable"] != true || sourceReads.Load() < 2 {
		t.Fatal("source race must be detected by a final reread and be retryable")
	}
	sourceFailure.Store(true)
	noPartial(h.call(h.base, path(tagSeed), "GET", h.reader, nil, 503), "UPSTREAM_UNAVAILABLE")
	sourceFailure.Store(false)
	requireNames(related(h.base, semanticSeed, ""))
	t.Log("PASS unsynchronized candidate changes, archived source repair, author isolation, source race, and atomic failure")

	h.stop()
	h.settings.Embedding.Enabled = true
	h.settings.Embedding.Dimensions, h.settings.Embedding.BatchSize, h.settings.Embedding.MaxRetries = 256, 2, 0
	address, key := model.URL+"/api/v1", "related-acceptance-model-key"
	h.settings.Embedding.BaseURL, h.settings.Embedding.APIKey = &address, &key
	h.settings.Sync.RetrySeconds, h.settings.Sync.PollSeconds, h.settings.Sync.FullSeconds = 1, 3600, 3600
	noReadToken := randomToken()
	h.settings.AccessTokens = append(h.settings.AccessTokens,
		moerise.AccessToken{Name: "related-no-read", Token: noReadToken, Scopes: []string{"sync:read"}})
	data, err := json.Marshal(h.settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.ready()
	wait(t, "related index awaiting document embeddings", func() bool {
		status := h.vectorStatus()
		return status != nil && status.Building != nil && status.Building.LastError != nil
	})
	unready := related(h.base, tagSeed, "?limit=20")
	requireNames(unready, tagNames...)
	requireSemantic(unready, false, "INDEX_NOT_READY")
	documentsEnabled.Store(true)
	active := h.vectorsReady()
	semantic := related(h.base, semanticSeed, "")
	requireNames(semantic, semanticName)
	requireSemantic(semantic, true, "")
	if !slices.Equal(semantic.Results[0].Matches[0].Signals, []string{"semantic"}) {
		t.Fatalf("expected purely semantic related hit: %+v", semantic)
	}
	requireNames(related(h.base, semanticSeed, "?tags=anchor"))
	if documentCalls.Load() == 0 || queryCalls.Load() != 0 {
		t.Fatalf("document calls=%d query calls=%d", documentCalls.Load(), queryCalls.Load())
	}
	model.Close()
	if offline := related(h.base, semanticSeed, ""); !reflect.DeepEqual(semantic, offline) {
		t.Fatalf("related retrieval changed after model shutdown: %+v", offline)
	}
	beforeDocuments := documentCalls.Load()
	h.stop()
	h.start()
	h.ready()
	restarted := h.vectorsReady()
	if after := related(h.base, semanticSeed, ""); !reflect.DeepEqual(semantic, after) ||
		restarted.Active.ID != active.Active.ID || documentCalls.Load() != beforeDocuments {
		t.Fatalf("related restart stability: old=%s new=%s result=%+v", active.Active.ID, restarted.Active.ID, after)
	}
	requireRecordError(t, h.call(h.base, path(tagSeed), "GET", noReadToken, nil, 403), "FORBIDDEN")
	for _, token := range []string{h.reader, noReadToken} {
		found := false
		for _, value := range h.mcp(token, "tools/list", map[string]any{})["tools"].([]any) {
			tool := value.(map[string]any)
			if tool["name"] != "get_related_memos" {
				continue
			}
			found = true
			annotations := tool["annotations"].(map[string]any)
			if annotations["readOnlyHint"] != true || annotations["idempotentHint"] != true ||
				annotations["destructiveHint"] != false {
				t.Fatalf("related MCP annotations: %v", annotations)
			}
		}
		if found != (token == h.reader) {
			t.Fatal("related MCP visibility does not follow notes:read")
		}
	}
	denied, denialStatus, denialErr := h.requestWithHeaders(h.base, "/mcp", "POST", noReadToken,
		map[string]any{"jsonrpc": "2.0", "id": "related-denied", "method": "tools/call",
			"params": map[string]any{"name": "get_related_memos", "arguments": map[string]any{"name": semanticSeed}}},
		map[string]string{"Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-11-25"})
	denialResult, _ := denied["result"].(map[string]any)
	if denialErr != nil || denialStatus != 200 || denied["error"] == nil && denialResult["isError"] != true {
		t.Fatalf("MCP related scope denial: code=%d err=%v response=%v", denialStatus, denialErr, denied)
	}
	for _, args := range []map[string]any{
		{}, {"name": semanticSeed, "other": true}, {"name": semanticSeed, "limit": 0},
		{"name": "bad-name"}, {"name": semanticSeed, "state": "DELETED"},
	} {
		result := h.mcp(h.reader, "tools/call", map[string]any{"name": "get_related_memos", "arguments": args})
		if result["isError"] != true {
			t.Fatalf("MCP accepted invalid related arguments: args=%v result=%v", args, result)
		}
	}
	for _, scenario := range []struct {
		name, query string
		args        map[string]any
	}{
		{semanticSeed, "", map[string]any{"name": semanticSeed}},
		{tagSeed, "?limit=5&tags=precisely&tags=secondary",
			map[string]any{"name": tagSeed, "limit": 5, "tags": []string{"precisely", "secondary"}}},
		{liveSeed, "?state=ALL", map[string]any{"name": liveSeed, "state": "ALL"}},
	} {
		result := h.mcp(h.reader, "tools/call", map[string]any{"name": "get_related_memos", "arguments": scenario.args})
		content, ok := result["structuredContent"].(map[string]any)
		if result["isError"] == true || !ok {
			t.Fatalf("MCP related failed: %v", result)
		}
		if rest, mcp := related(h.base, scenario.name, scenario.query), decode(content); !reflect.DeepEqual(rest, mcp) {
			t.Fatalf("REST/MCP related mismatch: REST=%+v MCP=%+v", rest, mcp)
		}
	}
	t.Log("PASS pure semantic related retrieval without query calls, offline restart stability, and REST/MCP scope and parity")
}

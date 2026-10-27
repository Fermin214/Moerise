package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestSearchChineseKeywordsAndUpgrade(t *testing.T) {
	settings := testSettings(t)
	store, err := NewStore(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	memos := map[string]map[string]any{
		"memos/long": rawMemo("long", strings.Repeat("阳台种花。", 300)+"\n\n"+strings.Repeat("番茄浇水。", 300)),
		"memos/word": rawMemo("word", "SQLite FTS5 中文检索"),
		"memos/tag":  rawMemo("tag", "这条正文只写虚构计划"),
		"memos/edge": rawMemo("edge", strings.Repeat("甲", 2047)+"篆刻"),
	}
	memos["memos/tag"]["tags"] = []string{"园艺", "园艺/阳台"}
	for _, raw := range memos {
		refreshIndexMemo(t, store, raw)
	}
	// Recreate the pre-3.3 schema while retaining its projection and migration checksums.
	if _, err := store.index.Exec(`DROP TRIGGER search_chunks_insert; DROP TRIGGER search_chunks_delete;
		DROP TABLE search_fts; DROP TABLE search_chunks; DROP TABLE search_notes;
		DELETE FROM schema_migrations WHERE version = '003'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewStore(settings)
	if err != nil {
		t.Fatal(err)
	}
	client := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if raw := memos[strings.TrimPrefix(r.URL.Path, "/api/v1/")]; raw != nil {
			return 200, raw, nil
		}
		return 404, nil, nil
	})
	service := NewSearchService(store, client, nil)
	for _, test := range []struct {
		query, name string
		tags        []string
	}{
		{"花", "memos/long", nil}, {"浇水", "memos/long", nil},
		{"sqlite", "memos/word", nil}, {"#园艺", "memos/tag", []string{"园艺/阳台"}},
		{"篆刻", "memos/edge", nil},
	} {
		result, err := service.Search(context.Background(), SearchRequest{Query: test.query, Tags: test.tags})
		if err != nil || len(result.Results) != 1 || result.Results[0].Note.Name != test.name ||
			result.Semantic.Used || result.Semantic.Reason != "EMBEDDING_DISABLED" {
			t.Fatalf("query %q: %+v %v", test.query, result, err)
		}
		for _, match := range result.Results[0].Matches {
			if match.Content != result.Results[0].Note.Content[match.ByteStart:match.ByteEnd] ||
				match.LineStart != 1+strings.Count(result.Results[0].Note.Content[:match.ByteStart], "\n") {
				t.Fatal("search lost original source positions")
			}
		}
	}
	for _, query := range []string{`" OR tokens:*`, `"`, "NEAR(foo bar)", "不存在"} {
		if _, err := service.Search(context.Background(), SearchRequest{Query: query}); err != nil {
			t.Fatalf("query became FTS syntax: %q: %v", query, err)
		}
	}
	memos["memos/word"]["content"] = "完全换掉原文字"
	result, err := service.Search(context.Background(), SearchRequest{Query: "SQLite"})
	if err != nil || len(result.Results) != 0 {
		t.Fatal("changed source returned old keyword hit")
	}
	result, err = service.Search(context.Background(), SearchRequest{Query: "换掉"})
	if err != nil || len(result.Results) != 1 {
		t.Fatal("source refresh did not repair keyword index")
	}
	delete(memos, "memos/long")
	result, err = service.Search(context.Background(), SearchRequest{Query: "花"})
	if err != nil || len(result.Results) != 0 {
		t.Fatal("deleted source remained searchable")
	}
	var count int
	if err := store.index.QueryRow("SELECT COUNT(*) FROM search_chunks WHERE name = 'memos/long'").Scan(&count); err != nil || count != 0 {
		t.Fatal("source deletion did not clear FTS chunks")
	}
}

type searchEmbedding struct {
	mu       sync.Mutex
	dims     int
	err      error
	after    func()
	purposes []EmbeddingPurpose
}

func (p *searchEmbedding) Close() {}
func (p *searchEmbedding) Embed(ctx context.Context, texts []string, purpose EmbeddingPurpose) (EmbeddingResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.purposes = append(p.purposes, purpose)
	if p.after != nil {
		p.after()
	}
	if p.err != nil {
		return EmbeddingResult{}, p.err
	}
	result := EmbeddingResult{}
	for _, text := range texts {
		vector := make([]float32, p.dims)
		vector[1] = 1
		if strings.Contains(text, "花") || purpose == EmbeddingQuery {
			vector[0], vector[1] = 1, 0
		}
		result.Vectors = append(result.Vectors, vector)
	}
	return result, ctx.Err()
}

func TestSearchSemanticCacheFiltersAndFallback(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	memos["memos/a"]["content"] = "阳台种花。"
	memos["memos/a"]["tags"] = []string{"植物"}
	refreshIndexMemo(t, store, memos["memos/a"])
	provider := &searchEmbedding{dims: store.Settings.Embedding.Dimensions}
	index.Provider = provider
	finishIndex(t, index)
	service := NewSearchService(store, index.Source, provider)
	request := SearchRequest{Query: "如何照顾绿色生命", Tags: []string{"植物"}}
	result, err := service.Search(context.Background(), request)
	if err != nil || !result.Semantic.Used || len(result.Results) != 1 ||
		result.Results[0].Note.Name != "memos/a" || !slices.Equal(result.Results[0].Matches[0].Signals, []string{"semantic"}) {
		t.Fatalf("semantic paraphrase: %+v %v", result, err)
	}
	firstMatrix := service.matrix
	if _, err := service.Search(context.Background(), request); err != nil || service.matrix != firstMatrix {
		t.Fatal("published matrix was not reused")
	}
	var calls sync.WaitGroup
	for range 8 {
		calls.Go(func() {
			if result, err := service.Search(context.Background(), request); err != nil || len(result.Results) != 1 {
				t.Errorf("concurrent search: %+v %v", result, err)
			}
		})
	}
	calls.Wait()
	if provider.purposes[len(provider.purposes)-1] != EmbeddingQuery {
		t.Fatal("search used document embeddings")
	}
	provider.after = func() {
		memos["memos/b"]["content"] = "updated during query"
		refreshIndexMemo(t, store, memos["memos/b"])
	}
	result, err = service.Search(context.Background(), SearchRequest{Query: "花"})
	if err != nil || result.Semantic.Reason != "INDEX_NOT_READY" || len(result.Results) != 1 {
		t.Fatal("query model returned after the projection changed but used a stale matrix")
	}
	provider.after = nil
	finishIndex(t, index)
	provider.err = upstreamError("EMBEDDING_RATE_LIMITED", "rate limited")
	result, err = service.Search(context.Background(), SearchRequest{Query: "花"})
	if err != nil || result.Semantic.Used || result.Semantic.Reason != "EMBEDDING_RATE_LIMITED" || len(result.Results) != 1 {
		t.Fatalf("query failure did not preserve lexical search: %+v %v", result, err)
	}
	provider.err = nil
	memos["memos/a"]["state"] = "ARCHIVED"
	// The live source check catches an archive that polling has not observed yet.
	result, err = service.Search(context.Background(), SearchRequest{Query: "花"})
	if err != nil || len(result.Results) != 0 {
		t.Fatal("default search returned an archived live source")
	}
	result, err = service.Search(context.Background(), SearchRequest{Query: "花", State: "ARCHIVED"})
	if err != nil || len(result.Results) != 1 || result.Semantic.Reason != "INDEX_NOT_READY" || service.matrix != nil {
		t.Fatal("projection change did not invalidate the semantic matrix")
	}
	finishIndex(t, index)
	result, err = service.Search(context.Background(), SearchRequest{Query: "如何照顾绿色生命", State: "ARCHIVED"})
	if err != nil || len(result.Results) != 1 || !result.Semantic.Used || service.matrix == firstMatrix {
		t.Fatal("new generation did not replace the matrix")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	provider.after = cancel
	if _, err := service.Search(cancelled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query silently degraded: %v", err)
	}
}

func TestSearchFusionKeepsBestChunkAndSourceFailureIsAtomic(t *testing.T) {
	a := searchCandidate{name: "memos/a", sourceHash: "a", chunk: TextChunk{Ordinal: 0}}
	b := searchCandidate{name: "memos/b", sourceHash: "b", chunk: TextChunk{Ordinal: 0}}
	second := a
	second.chunk.Ordinal = 1
	fused := fuseSearch([]searchCandidate{a, second, b}, []searchCandidate{b}, []searchCandidate{b, a})
	if fused[0].name != b.name || len(fused[0].signals) != 3 || len(fused) != 3 {
		t.Fatal("rank fusion did not combine individual chunk ranks")
	}
	settings := testSettings(t)
	store := testStore(t, settings)
	raw := rawMemo("a", "花")
	refreshIndexMemo(t, store, raw)
	identityCalls := 0
	client := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if strings.HasSuffix(r.URL.Path, "/auth/me") {
			identityCalls++
			if identityCalls >= 2 {
				return 200, map[string]any{"user": map[string]string{"name": "users/other"}}, nil
			}
		}
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		return 200, raw, nil
	})
	result, err := NewSearchService(store, client, nil).Search(context.Background(), SearchRequest{Query: "花"})
	if errorCode(err) != "IDENTITY_MISMATCH" || len(result.Results) != 0 {
		t.Fatalf("identity change leaked collected results: %+v %v", result, err)
	}
}

func TestSearchRESTScopeValidationAndEncryptedFeishuRequest(t *testing.T) {
	settings := feishuSettings(t)
	settings.AccessTokens[1].Scopes = []string{"sync:read"}
	store := testStore(t, settings)
	raw := rawMemo("a", "花")
	refreshIndexMemo(t, store, raw)
	client := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		return 200, raw, nil
	})
	api, err := NewAPI(settings, store, client, NewSyncService(store, client))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		body, token string
		status      int
	}{
		{`{"query":"花"}`, "", 401},
		{`{"query":"花"}`, settings.AccessTokens[1].Token, 403},
		{`{"query":" "}`, settings.AccessTokens[0].Token, 422},
		{`{"query":"花","unexpected":true}`, settings.AccessTokens[0].Token, 422},
		{`{"query":"花","limit":21}`, settings.AccessTokens[0].Token, 422},
		{`{"query":"花","limit":0}`, settings.AccessTokens[0].Token, 422},
		{`{"query":"花","state":"DELETED"}`, settings.AccessTokens[0].Token, 422},
		{`{"query":"花","tags":["bad tag"]}`, settings.AccessTokens[0].Token, 422},
		{`{"query":"花"}`, settings.AccessTokens[0].Token, 200},
	} {
		request := httptest.NewRequest("POST", "/api/ext/v1/search", strings.NewReader(test.body))
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("search %s: status=%d body=%s", test.body, response.Code, response.Body.String())
		}
	}
	message := feishuMessage{ID: "om_search", Type: "text",
		Content: string(canonicalJSON(map[string]string{"text": "搜索 #植物 阳台花"}))}
	kind, reference, err := api.Feishu.execute(context.Background(), message)
	if err != nil || kind != "search" || strings.Contains(reference, "阳台") || strings.Contains(reference, "植物") {
		t.Fatal("search query persisted in plaintext")
	}
	text, err := api.Feishu.render(context.Background(), kind, reference)
	if err != nil || !strings.Contains(text, "搜索结果") {
		t.Fatalf("encrypted search could not be replayed: %s %v", text, err)
	}
	foreign := *api.Feishu
	otherSettings := settings
	otherSettings.Memos.InstanceID = "other"
	foreign.Store = testStore(t, otherSettings)
	if _, err := foreign.render(context.Background(), kind, reference); errorCode(err) != "CHANNEL_QUEUE_KEY" {
		t.Fatal("encrypted search crossed its source binding")
	}
	for _, query := range []string{"", strings.Repeat("花", 257)} {
		body, _ := json.Marshal(SearchRequest{Query: query})
		request := httptest.NewRequest("POST", "/api/ext/v1/search", strings.NewReader(string(body)))
		request.Header.Set("Authorization", "Bearer "+settings.AccessTokens[0].Token)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 422 {
			t.Fatal("search accepted an invalid text length")
		}
	}
	request := httptest.NewRequest("GET", "/api/ext/v1/search", nil)
	request.Header.Set("Authorization", "Bearer "+settings.AccessTokens[0].Token)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != 405 {
		t.Fatal("search accepted a method outside its contract")
	}
}

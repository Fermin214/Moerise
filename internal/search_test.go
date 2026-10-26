package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

type embedFunc func(context.Context, []string) ([][]float32, error)

func (f embedFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

func fixtureVectors(_ context.Context, texts []string) ([][]float32, error) {
	result := [][]float32{}
	for _, text := range texts {
		vector := []float32{0.01, 0.01, 0.01, 0.01}
		for i, terms := range [][]string{{"学习", "复习", "间隔", "遗忘", "记忆"}, {"散步", "创意", "灵感", "写作"}, {"睡眠", "休息", "睡觉"}, {"运动", "跑步", "身体"}} {
			for _, term := range terms {
				if strings.Contains(text, term) {
					vector[i]++
				}
			}
		}
		result = append(result, vector)
	}
	return result, nil
}

type searchFixture struct {
	store    *Store
	service  *SearchService
	source   *MemosClient
	mu       sync.Mutex
	memos    map[string]map[string]any
	offline  bool
	identity string
}

func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	s := testSettings(t)
	s.Embedding.Model, s.Embedding.BaseURL = stringPointer("fixture-model"), stringPointer("https://embedding.example/v1")
	f := &searchFixture{store: testStore(t, s), memos: map[string]map[string]any{}, identity: s.Memos.User}
	f.source = fakeClient(s, func(r *http.Request) (int, any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.offline {
			return 503, nil, nil
		}
		if strings.HasSuffix(r.URL.Path, "/auth/me") {
			return 200, map[string]any{"user": map[string]string{"name": f.identity}}, nil
		}
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		memo := f.memos[strings.TrimPrefix(r.URL.Path, "/api/v1/")]
		if memo == nil {
			return 404, nil, nil
		}
		data, _ := json.Marshal(memo)
		var copy map[string]any
		_ = json.Unmarshal(data, &copy)
		return 200, copy, nil
	})
	f.service = NewSearchService(f.store, f.source)
	f.service.Embedder = embedFunc(fixtureVectors)
	if err := f.store.Advance("2026-01-01T00:00:00Z", true); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *searchFixture) put(t *testing.T, name, content, state string, tags ...string) {
	t.Helper()
	raw := rawMemo(name, content)
	raw["state"], raw["tags"] = state, tags
	memo, err := normalizeMemo(raw)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.memos[memo.Name] = raw
	f.mu.Unlock()
	if err := f.store.Refresh(memo.Name, &memo); err != nil {
		t.Fatal(err)
	}
}

func hitNames(result SearchResponse) []string {
	names := []string{}
	for _, hit := range result.Results {
		names = append(names, hit.Note.Name)
	}
	return names
}

func TestChineseKeywordShortQueriesAndTagFilters(t *testing.T) {
	f := newSearchFixture(t)
	f.put(t, "chinese", "中文关键词检索与知识管理，AI", "NORMAL", "知识/检索")
	f.put(t, "other", "知识管理方法", "NORMAL", "其他")
	f.put(t, "archived", "中文关键词检索", "ARCHIVED", "知识/检索")
	f.put(t, "tag-only", "没有写在正文中的标签", "NORMAL", "知识/词典")
	for _, query := range []string{"中", "中文", "关键词", "中文关键词", "AI", `关键词 " OR * :`, "知识/检索"} {
		result, err := f.service.Search(context.Background(), SearchRequest{Query: query, Mode: "keyword"})
		if err != nil || !slices.Contains(hitNames(result), "memos/chinese") || slices.Contains(hitNames(result), "memos/archived") {
			t.Fatalf("%q: %+v %v", query, result, err)
		}
	}
	result, err := f.service.Search(context.Background(), SearchRequest{Query: "知识", Mode: "keyword", Tags: []string{"知识"}})
	if err != nil || slices.Contains(hitNames(result), "memos/other") || !slices.Contains(hitNames(result), "memos/tag-only") {
		t.Fatalf("tag filter: %+v %v", result, err)
	}
	result, err = f.service.Search(context.Background(), SearchRequest{Mode: "keyword", Tags: []string{"知识/检索"}, IncludeArchived: true})
	if err != nil || len(result.Results) != 2 {
		t.Fatalf("tag-only search: %+v %v", result, err)
	}
	f.put(t, "chinese", "正文已改为完全无关内容", "NORMAL")
	result, err = f.service.Search(context.Background(), SearchRequest{Query: "关键词", Mode: "keyword"})
	if err != nil || len(result.Results) != 0 {
		t.Fatalf("FTS update trigger: %+v %v", result, err)
	}
}

func TestHybridSearchRelatedAndSourceRechecks(t *testing.T) {
	f := newSearchFixture(t)
	f.put(t, "memory", "间隔复习能够帮助长期记忆", "NORMAL", "学习")
	f.put(t, "study", "学习要结合主动回想和练习", "NORMAL", "学习")
	f.put(t, "sleep", "睡眠休息改善生活", "NORMAL", "生活")
	if err := f.service.IndexStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.Search(context.Background(), SearchRequest{Query: "遗忘", Mode: "hybrid"})
	if err != nil || result.Mode != "hybrid" || len(result.Results) < 2 || result.Results[0].Similarity == nil {
		t.Fatalf("semantic recall: %+v %v", result, err)
	}
	for _, hit := range result.Results {
		runes := []rune(hit.Note.Content)
		if hit.Snippet.Text != string(runes[hit.Snippet.StartRune:hit.Snippet.EndRune]) || hit.Snippet.SourceHash != hit.Note.SourceHash {
			t.Fatal("snippet not traceable to source")
		}
	}
	related, err := f.service.Related(context.Background(), RelatedRequest{Name: "memos/memory"})
	if err != nil || !slices.Contains(hitNames(related), "memos/study") || slices.Contains(hitNames(related), "memos/memory") {
		t.Fatalf("related: %+v %v", related, err)
	}
	f.mu.Lock()
	delete(f.memos, "memos/memory")
	f.memos["memos/study"]["content"] = "休息与睡眠"
	f.mu.Unlock()
	result, err = f.service.Search(context.Background(), SearchRequest{Query: "遗忘", Mode: "semantic"})
	if err != nil || slices.Contains(hitNames(result), "memos/memory") || slices.Contains(hitNames(result), "memos/study") {
		t.Fatalf("stale semantic results: %+v %v", result, err)
	}
	f.mu.Lock()
	f.offline = true
	f.mu.Unlock()
	_, err = f.service.Search(context.Background(), SearchRequest{Query: "休息", Mode: "keyword"})
	if errorCode(err) != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("source outage: %v", err)
	}
}

func TestSearchFallbackAndReadOnlyScope(t *testing.T) {
	f := newSearchFixture(t)
	f.put(t, "memory", "学习复习", "NORMAL")
	result, err := f.service.Search(context.Background(), SearchRequest{Query: "学习"})
	if err != nil || result.Mode != "keyword" || !slices.Contains(result.Warnings, "SEARCH_NOT_READY") {
		t.Fatalf("building fallback: %+v %v", result, err)
	}
	if err := f.service.IndexStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.service.Embedder = embedFunc(func(context.Context, []string) ([][]float32, error) {
		return nil, upstreamError("EMBEDDING_UNAVAILABLE", "unavailable")
	})
	result, err = f.service.Search(context.Background(), SearchRequest{Query: "学习"})
	if err != nil || result.Mode != "keyword" || len(result.Results) != 1 || !slices.Contains(result.Warnings, "EMBEDDING_UNAVAILABLE") {
		t.Fatalf("provider fallback: %+v %v", result, err)
	}
	_, err = f.service.Search(context.Background(), SearchRequest{Query: "学习", Mode: "semantic"})
	if errorCode(err) != "EMBEDDING_UNAVAILABLE" {
		t.Fatalf("explicit semantic failure: %v", err)
	}
	api, err := NewAPI(f.store.Settings, f.store, f.source, NewSyncService(f.store, f.source))
	if err != nil {
		t.Fatal(err)
	}
	api.Search.Embedder = embedFunc(fixtureVectors)
	for _, test := range []struct {
		path, method, body, token string
		status                    int
	}{
		{"/api/ext/v1/search", "POST", `{"query":"学习","mode":"keyword"}`, f.store.Settings.AccessTokens[0].Token, 200},
		{"/api/ext/v1/search", "POST", `{"query":"学习"}`, "", 401},
		{"/api/ext/v1/search", "POST", `{"query":"学习","unknown":true}`, f.store.Settings.AccessTokens[0].Token, 422},
		{"/api/ext/v1/search", "POST", `{"query":null}`, f.store.Settings.AccessTokens[0].Token, 422},
		{"/api/ext/v1/discovery/open", "POST", `{"name":"memos/memory"}`, f.store.Settings.AccessTokens[0].Token, 403},
		{"/api/ext/v1/search/index", "GET", "", f.store.Settings.AccessTokens[0].Token, 200},
		{"/api/ext/v1/notes/memory/related", "GET", "", f.store.Settings.AccessTokens[0].Token, 200},
	} {
		r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		r.Header.Set("Authorization", "Bearer "+test.token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.path, w.Code, w.Body.String())
		}
	}
}

func TestEmbeddingIncrementalRestartAndModelSwitch(t *testing.T) {
	f := newSearchFixture(t)
	f.put(t, "a", "学习复习", "NORMAL")
	f.put(t, "b", "运动跑步", "NORMAL")
	calls := 0
	f.service.Embedder = embedFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		calls++
		return fixtureVectors(ctx, texts)
	})
	if err := f.service.IndexStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("first indexing calls: %d", calls)
	}
	f.put(t, "a", "学习复习", "ARCHIVED", "新标签")
	if err := f.service.IndexStep(context.Background()); err != nil || calls != 2 {
		t.Fatalf("metadata reembedded: %d %v", calls, err)
	}
	f.put(t, "a", "学习复习主动回想", "NORMAL")
	if err := f.service.IndexStep(context.Background()); err != nil || calls != 3 {
		t.Fatalf("incremental: %d %v", calls, err)
	}
	restarted := NewSearchService(f.store, f.source)
	restarted.Embedder = f.service.Embedder
	if err := restarted.IndexStep(context.Background()); err != nil || calls != 3 {
		t.Fatalf("restart reembedded: %d %v", calls, err)
	}
	old := restarted.spaceID()
	f.store.Settings.Embedding.Model = stringPointer("second-model")
	f.store.Settings.Embedding.BatchSize = 1
	next := NewSearchService(f.store, f.source)
	next.Embedder = f.service.Embedder
	if err := next.IndexStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	var active string
	if err := f.store.index.QueryRow("SELECT id FROM embedding_spaces WHERE status='active'").Scan(&active); err != nil || active != old {
		t.Fatalf("partial model published: %s %v", active, err)
	}
	if _, err := next.loadMatrix(); errorCode(err) != "SEARCH_NOT_READY" {
		t.Fatalf("mixed vector space: %v", err)
	}
	if err := next.IndexStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	matrix, err := next.loadMatrix()
	if err != nil || matrix.spaceID() == old || matrix.size() != 2 {
		t.Fatalf("model switch: %+v %v", matrix, err)
	}
	var spaces int
	if err := f.store.index.QueryRow("SELECT COUNT(*) FROM embedding_spaces").Scan(&spaces); err != nil || spaces != 1 {
		t.Fatal("retired vector space not cleaned")
	}
}

func TestEmbeddingInterruptedBatchAndSourceEdit(t *testing.T) {
	f := newSearchFixture(t)
	f.put(t, "a", "学习", "NORMAL")
	f.put(t, "b", "复习", "NORMAL")
	calls := 0
	f.service.Embedder = embedFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		calls++
		if calls == 2 {
			return nil, upstreamError("EMBEDDING_UNAVAILABLE", "failed")
		}
		return fixtureVectors(ctx, texts)
	})
	if err := f.service.IndexStep(context.Background()); errorCode(err) != "EMBEDDING_UNAVAILABLE" {
		t.Fatal(err)
	}
	status, err := f.service.IndexStatus()
	if err != nil || status.Chunks != 1 || status.PendingMemos != 1 || status.Status != "building" {
		t.Fatalf("partial progress: %+v %v", status, err)
	}
	if err := f.service.IndexStep(context.Background()); err != nil || calls != 3 {
		t.Fatalf("resume: %d %v", calls, err)
	}
	f.put(t, "a", "学习新内容", "NORMAL")
	f.service.Embedder = embedFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		f.mu.Lock()
		f.memos["memos/a"]["content"] = "不同的睡眠内容"
		f.mu.Unlock()
		return fixtureVectors(ctx, texts)
	})
	if err := f.service.IndexStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = f.service.IndexStatus()
	if err != nil || status.PendingMemos != 1 {
		t.Fatalf("inflight edit committed: %+v %v", status, err)
	}
}

func TestSearchRejectsOtherAuthorsAndLateIdentityChange(t *testing.T) {
	f := newSearchFixture(t)
	f.put(t, "a", "学习复习", "NORMAL")
	if err := f.service.IndexStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.memos["memos/a"]["creator"] = "users/other"
	f.mu.Unlock()
	result, err := f.service.Search(context.Background(), SearchRequest{Query: "学习", Mode: "keyword"})
	if err != nil || len(result.Results) != 0 {
		t.Fatalf("cross-author result: %+v %v", result, err)
	}
	f.put(t, "a", "学习复习", "NORMAL")
	f.service.Embedder = embedFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		f.mu.Lock()
		f.identity = "users/other"
		f.mu.Unlock()
		return fixtureVectors(ctx, texts)
	})
	result, err = f.service.Search(context.Background(), SearchRequest{Query: "学习", Mode: "semantic"})
	if err == nil || len(result.Results) != 0 {
		t.Fatalf("late identity change exposed results: %+v %v", result, err)
	}
}

func TestEmbeddingCapacityAndSearchConcurrency(t *testing.T) {
	f := newSearchFixture(t)
	f.put(t, "a", strings.Repeat("学习", 1100), "NORMAL")
	f.store.Settings.Embedding.MaxChunks = 1
	if err := f.service.IndexStep(context.Background()); errorCode(err) != "EMBEDDING_CAPACITY" {
		t.Fatalf("capacity: %v", err)
	}
	f.service.queries <- struct{}{}
	f.service.queries <- struct{}{}
	if _, err := f.service.Search(context.Background(), SearchRequest{Query: "学习"}); errorCode(err) != "SEARCH_BUSY" {
		t.Fatalf("concurrency: %v", err)
	}
}

func TestLongMemoSearchKeepsMatchingSourcePosition(t *testing.T) {
	f := newSearchFixture(t)
	f.put(t, "long", strings.Repeat("运动跑步身体。", 180)+"\n\n"+strings.Repeat("学习复习帮助记忆。", 180), "NORMAL")
	f.put(t, "memory", "间隔复习记忆", "NORMAL")
	if err := f.service.IndexStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.Search(context.Background(), SearchRequest{Query: "遗忘", Mode: "semantic"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range result.Results {
		if hit.Note.Name != "memos/long" {
			continue
		}
		found = true
		if hit.Snippet.StartRune < 1000 || !strings.Contains(hit.Snippet.Text, "记忆") || hit.Snippet.Text != string([]rune(hit.Note.Content)[hit.Snippet.StartRune:hit.Snippet.EndRune]) {
			t.Fatalf("wrong long-note chunk: %+v", hit.Snippet)
		}
	}
	if !found {
		t.Fatal("long memo wasn't recalled")
	}
	related, err := f.service.Related(context.Background(), RelatedRequest{Name: "memos/long"})
	if err != nil || !slices.Contains(hitNames(related), "memos/memory") {
		t.Fatalf("related ignored later paragraphs: %+v %v", related, err)
	}
}

func TestRandomWalkBoundsSourceRequests(t *testing.T) {
	f := newSearchFixture(t)
	for i := range 50 {
		f.put(t, fmt.Sprintf("n%02d", i), "随记", "NORMAL")
	}
	transport := f.source.client.Transport
	reads := 0
	f.source.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/memos/") {
			reads++
		}
		return transport.RoundTrip(r)
	})
	result, err := f.service.Walk(context.Background(), WalkRequest{Mode: "random"})
	if err != nil || result.Item == nil || reads != 1 {
		t.Fatalf("random selection fetched %d sources: %+v %v", reads, result, err)
	}
}

func TestDiscoveryWalkPathTopicsRecentReadsAndRestore(t *testing.T) {
	f := newSearchFixture(t)
	f.put(t, "seed", "学习写作", "NORMAL", "学习")
	f.put(t, "near", "学习写作", "NORMAL", "学习")
	f.put(t, "angle", "学习创意写作", "NORMAL", "创意")
	f.put(t, "other", "运动跑步", "NORMAL", "运动")
	if err := f.service.IndexStep(context.Background()); err != nil {
		t.Fatal(err)
	}
	cont, err := f.service.Walk(context.Background(), WalkRequest{Name: "memos/seed"})
	if err != nil || cont.Item == nil || cont.Item.Note.Name != "memos/near" {
		t.Fatalf("continue: %+v %v", cont, err)
	}
	angle, err := f.service.Walk(context.Background(), WalkRequest{Name: "memos/seed", Mode: "angle"})
	if err != nil || angle.Item == nil || angle.Item.Note.Name != "memos/angle" {
		t.Fatalf("angle: %+v %v", angle, err)
	}
	if _, err := f.service.Open(context.Background(), "memos/near"); err != nil {
		t.Fatal(err)
	}
	cont, err = f.service.Walk(context.Background(), WalkRequest{Name: "memos/seed"})
	if err != nil || cont.Item == nil || cont.Item.Note.Name == "memos/near" {
		t.Fatalf("recent read: %+v %v", cont, err)
	}
	jump, err := f.service.Walk(context.Background(), WalkRequest{Name: "memos/seed", Mode: "random", Path: []string{"memos/near", "memos/angle"}})
	if err != nil || jump.Item == nil || jump.Item.Note.Name != "memos/other" {
		t.Fatalf("path dedup: %+v %v", jump, err)
	}
	exhausted, err := f.service.Walk(context.Background(), WalkRequest{Name: "memos/seed", Mode: "random", Path: jump.Path})
	if err != nil || !exhausted.Exhausted || exhausted.Item != nil {
		t.Fatalf("exhausted: %+v %v", exhausted, err)
	}
	backup := filepath.Join(t.TempDir(), "snapshot")
	if _, err := Backup(f.store.Settings, backup); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildIndex(f.store.Settings); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(f.store.Settings)
	if err != nil {
		t.Fatal(err)
	}
	service := NewSearchService(store, f.source)
	reads, err := service.recentReads()
	if err != nil || !reads["memos/near"] {
		t.Fatalf("rebuild lost reads: %v %v", reads, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(f.store.Settings, backup, filepath.Join(t.TempDir(), "safety")); err != nil {
		t.Fatal(err)
	}
	store, err = NewStore(f.store.Settings)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reads, err = NewSearchService(store, f.source).recentReads()
	if err != nil || !reads["memos/near"] {
		t.Fatalf("restore lost reads: %v %v", reads, err)
	}
}

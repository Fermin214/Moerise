package moerise

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestRelatedFullSourceAndDistinctPassages(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	block := "篆刻\n" + strings.Repeat("x", 1500) + "\n\n"
	seed := rawMemo("seed", strings.Repeat("a", 300)+"\n\n篆刻")
	seed["state"], seed["tags"] = "ARCHIVED", []string{"艺术", "艺术/印章"}
	memos := map[string]map[string]any{
		"memos/seed":   seed,
		"memos/repeat": rawMemo("repeat", strings.Repeat(block, 4)),
		"memos/tag":    rawMemo("tag", "木桌上的收藏册"),
		"memos/other":  rawMemo("other", "虚构日程"),
	}
	memos["memos/tag"]["tags"] = []string{"艺术/印章"}
	memos["memos/other"]["tags"] = []string{"艺术/印章稿"}
	for _, raw := range memos {
		refreshIndexMemo(t, store, raw)
	}
	client := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		return 200, memos[strings.TrimPrefix(r.URL.Path, "/api/v1/")], nil
	})
	service := NewSearchService(store, client, nil)
	result, err := service.Related(context.Background(), RelatedRequest{Name: "memos/seed"})
	if err != nil || len(result.Results) != 2 || result.Semantic.Reason != "EMBEDDING_DISABLED" ||
		result.Source.Name != "memos/seed" || result.Source.State != "ARCHIVED" {
		t.Fatalf("full source retrieval: %+v %v", result, err)
	}
	for _, hit := range result.Results {
		if hit.Note.Name == "memos/repeat" {
			if len(hit.Matches) != 1 || hit.Matches[0].Content != block || hit.Matches[0].Ordinal != 0 {
				t.Fatalf("repeated passages were not merged at their best location: %+v", hit)
			}
		} else if hit.Note.Name != "memos/tag" || !slices.Contains(hit.Matches[0].Signals, "tag") {
			t.Fatalf("unrelated prefix tag or source was returned: %+v", hit)
		}
	}
	filtered, err := service.Related(context.Background(), RelatedRequest{Name: "memos/seed", Tags: []string{"艺术/印章"}})
	if err != nil || len(filtered.Results) != 1 || filtered.Results[0].Note.Name != "memos/tag" {
		t.Fatal("candidate tag filtering did not use exact tags")
	}
}

func TestRelatedSourceChangesAreAtomic(t *testing.T) {
	for _, mode := range []string{"edit", "delete", "identity", "candidate-outage", "foreign", "missing"} {
		t.Run(mode, func(t *testing.T) {
			settings := testSettings(t)
			store := testStore(t, settings)
			seed, candidate := rawMemo("seed", "阳台种花"), rawMemo("candidate", "阳台浇水")
			refreshIndexMemo(t, store, seed)
			refreshIndexMemo(t, store, candidate)
			identities, seedReads := 0, 0
			client := fakeClient(settings, func(r *http.Request) (int, any, error) {
				if strings.HasSuffix(r.URL.Path, "/auth/me") {
					identities++
					if mode == "identity" && identities > 1 {
						return 200, map[string]any{"user": map[string]string{"name": "users/other"}}, nil
					}
				}
				if status, body, ok := identityResponse(r); ok {
					return status, body, nil
				}
				if strings.HasSuffix(r.URL.Path, "/seed") {
					seedReads++
					if mode == "missing" || mode == "delete" && seedReads > 1 {
						return 404, nil, nil
					}
					if mode == "edit" && seedReads > 1 {
						seed["content"] = "来源正文改变了"
					}
					if mode == "foreign" {
						seed["creator"] = "users/other"
					}
					return 200, seed, nil
				}
				if mode == "candidate-outage" {
					return 503, nil, nil
				}
				return 200, candidate, nil
			})
			result, err := NewSearchService(store, client, nil).Related(context.Background(), RelatedRequest{Name: "memos/seed"})
			expected := map[string]string{"edit": "SOURCE_CHANGED", "delete": "SOURCE_NOT_FOUND", "identity": "IDENTITY_MISMATCH",
				"candidate-outage": "UPSTREAM_UNAVAILABLE", "foreign": "SOURCE_NOT_FOUND", "missing": "SOURCE_NOT_FOUND"}[mode]
			if errorCode(err) != expected || result.Source.Name != "" || len(result.Results) != 0 {
				t.Fatalf("source failure leaked a partial response: %+v %v", result, err)
			}
			if mode == "edit" {
				service, ok := err.(*ServiceError)
				if !ok || service.Status != 409 || !service.Retryable {
					t.Fatal("source mutation did not request a retry")
				}
				rows, queryErr := store.lexicalCandidates(context.Background(), SearchRequest{Query: "改变", State: "ALL"}, false)
				if queryErr != nil || len(rows) != 1 || rows[0].name != "memos/seed" {
					t.Fatal("final source recheck did not repair the projection")
				}
			}
		})
	}
}

func TestRelatedUsesPublishedDocumentVectors(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	memos["memos/a"]["content"] = "阳台种花"
	memos["memos/b"]["content"] = "窗边赏花"
	refreshIndexMemo(t, store, memos["memos/a"])
	refreshIndexMemo(t, store, memos["memos/b"])
	provider := &searchEmbedding{dims: store.Settings.Embedding.Dimensions}
	index.Provider = provider
	finishIndex(t, index)
	calls := len(provider.purposes)
	provider.err = upstreamError("EMBEDDING_UNAVAILABLE", "model is offline")
	service := NewSearchService(store, index.Source, provider)
	result, err := service.Related(context.Background(), RelatedRequest{Name: "memos/a"})
	if err != nil || !result.Semantic.Used || len(result.Results) != 1 ||
		result.Results[0].Note.Name != "memos/b" || len(provider.purposes) != calls {
		t.Fatalf("related retrieval called the model or lost document vectors: %+v %v", result, err)
	}
	memos["memos/a"]["content"] = "窗边花开"
	refreshIndexMemo(t, store, memos["memos/a"])
	result, err = service.Related(context.Background(), RelatedRequest{Name: "memos/a"})
	if err != nil || result.Semantic.Reason != "INDEX_NOT_READY" || len(result.Results) != 1 ||
		result.Source.Content != "窗边花开" || len(provider.purposes) != calls {
		t.Fatal("source mutation reused an old matrix or lost keyword fallback")
	}
	memos["memos/a"]["content"] = " \n\t"
	refreshIndexMemo(t, store, memos["memos/a"])
	provider.err = nil
	finishIndex(t, index)
	result, err = service.Related(context.Background(), RelatedRequest{Name: "memos/a"})
	if err != nil || result.Semantic.Reason != "NO_SOURCE_VECTORS" || result.Results == nil || len(result.Results) != 0 {
		t.Fatalf("empty source was not handled explicitly: %+v %v", result, err)
	}
}

type relatedEmbedding struct {
	dimensions int
	vectors    map[string][]float32
}

func (p *relatedEmbedding) Close() {}
func (p *relatedEmbedding) Embed(ctx context.Context, texts []string, purpose EmbeddingPurpose) (EmbeddingResult, error) {
	result := EmbeddingResult{}
	for _, text := range texts {
		vector := make([]float32, p.dimensions)
		copy(vector, p.vectors[text])
		result.Vectors = append(result.Vectors, vector)
	}
	return result, ctx.Err()
}

// A real model cannot reliably isolate the effects of repeated passages and vector magnitude.
func TestRelatedSemanticWeightsDistinctPassagesEqually(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	first, second := strings.Repeat("p", 1500)+"\n\n", strings.Repeat("q", 1500)+"\n\n"
	memos["memos/a"]["content"] = strings.Repeat(first, 3) + second
	provider := &relatedEmbedding{dimensions: store.Settings.Embedding.Dimensions,
		vectors: map[string][]float32{first: {100, 0}, second: {0, 1}}}
	for _, item := range []struct {
		name, body string
		vector     []float32
	}{
		{"b", "balanced", []float32{1, 1}},
		{"c", "first-axis", []float32{1, 0}},
		{"d", "second-axis", []float32{0, 1}},
		{"e", "opposite", []float32{-1, -1}},
	} {
		memos["memos/"+item.name]["content"] = item.body
		provider.vectors[item.body] = item.vector
	}
	for _, raw := range memos {
		refreshIndexMemo(t, store, raw)
	}
	index.Provider = provider
	finishIndex(t, index)
	// No provider is attached to the reader: all ranking uses the published document vectors.
	service := NewSearchService(store, index.Source, nil)
	result, err := service.Related(context.Background(), RelatedRequest{Name: "memos/a", State: "ALL"})
	if err != nil || !result.Semantic.Used || len(result.Results) != 3 {
		t.Fatalf("distinct source weighting: %+v %v", result, err)
	}
	for i, name := range []string{"memos/b", "memos/c", "memos/d"} {
		hit := result.Results[i]
		if hit.Note.Name != name || !slices.Equal(hit.Matches[0].Signals, []string{"semantic"}) {
			t.Fatalf("source repetition or vector magnitude changed the topic balance: %+v", result)
		}
	}
}

func TestRelatedRESTInputAndPermissions(t *testing.T) {
	settings := testSettings(t)
	settings.AccessTokens[1].Scopes = []string{"sync:read"}
	store := testStore(t, settings)
	raw := rawMemo("seed", "虚构笔记")
	refreshIndexMemo(t, store, raw)
	client := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if strings.HasSuffix(r.URL.Path, "/memos/related") {
			return 200, rawMemo("related", "资源名与相关路由同名"), nil
		}
		return 200, raw, nil
	})
	api, err := NewAPI(NewApp(settings, store, client))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, query, token string
		status               int
	}{
		{"GET", "", "", 401}, {"GET", "", settings.AccessTokens[1].Token, 403},
		{"POST", "", settings.AccessTokens[0].Token, 403},
		{"GET", "?limit=0", settings.AccessTokens[0].Token, 422},
		{"GET", "?limit=21", settings.AccessTokens[0].Token, 422},
		{"GET", "?limit=1&limit=2", settings.AccessTokens[0].Token, 422},
		{"GET", "?state=NORMAL&state=ALL", settings.AccessTokens[0].Token, 422},
		{"GET", "?state=", settings.AccessTokens[0].Token, 422},
		{"GET", "?state=DELETED", settings.AccessTokens[0].Token, 422},
		{"GET", "?tags=bad+tag", settings.AccessTokens[0].Token, 422},
		{"GET", "?tags=x%2Fy&tags=x&state=ALL", settings.AccessTokens[0].Token, 200},
		{"GET", "?extra=1", settings.AccessTokens[0].Token, 422},
		{"GET", "", settings.AccessTokens[0].Token, 200},
	} {
		request := httptest.NewRequest(test.method, "/api/ext/v1/notes/seed/related"+test.query, nil)
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s %s: status=%d body=%s", test.method, test.query, response.Code, response.Body.String())
		}
	}
	for _, body := range []string{`{"name":"memos/seed","limit":0}`, `{"name":"memos/seed","other":1}`} {
		var request RelatedRequest
		if err := strictJSON(strings.NewReader(body), &request); err == nil {
			t.Fatal("related tool parameters ignored invalid fields")
		}
	}
	for _, name := range []string{"", "seed", "memos/", "memos/a/b"} {
		if _, err := api.Search.Related(context.Background(), RelatedRequest{Name: name}); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("invalid related source name %q: %v", name, err)
		}
	}
	request := httptest.NewRequest("GET", "/api/ext/v1/notes/related", nil)
	request.Header.Set("Authorization", "Bearer "+settings.AccessTokens[0].Token)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	var note Note
	if err := strictJSON(response.Body, &note); err != nil || response.Code != 200 || note.Name != "memos/related" {
		t.Fatalf("related route intercepted a valid single-note resource: %+v %v", note, err)
	}
}

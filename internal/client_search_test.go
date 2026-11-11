package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

type clientSearchEmbedding struct {
	dimensions, calls int
}

func (*clientSearchEmbedding) Close() {}
func (p *clientSearchEmbedding) Embed(_ context.Context, _ []string, purpose EmbeddingPurpose) (EmbeddingResult, error) {
	if purpose != EmbeddingQuery {
		return EmbeddingResult{}, fmt.Errorf("unexpected document embedding")
	}
	p.calls++
	vector := make([]float32, p.dimensions)
	vector[0] = 1
	return EmbeddingResult{Vectors: [][]float32{vector}}, nil
}

func clientSearchFixture(t *testing.T) (*SearchService, *NoteService, map[string]map[string]any, *clientSearchEmbedding) {
	t.Helper()
	store, index, memos, _ := indexFixture(t)
	for i := range 27 {
		id := fmt.Sprintf("rank-%02d", i)
		raw := rawMemo(id, "needle garden")
		raw["tags"] = []string{"C++", "👩‍💻"}
		raw["createTime"] = time.Date(2020, 1, 1, 12, i, 0, 0, time.UTC).Format(time.RFC3339)
		memos["memos/"+id] = raw
		refreshIndexMemo(t, store, raw)
	}
	finishIndex(t, index)
	provider := &clientSearchEmbedding{dimensions: store.Settings.Embedding.Dimensions}
	return NewSearchService(store, index.Source, provider), NewNoteService(store.Settings, store, index.Source), memos, provider
}

func TestClientSearchPagesKeepRankFiltersAndDoNotRepeatModel(t *testing.T) {
	service, notes, _, provider := clientSearchFixture(t)
	for _, mode := range []string{"semantic", "hybrid"} {
		input := ClientSearchRequest{Query: "needle", Mode: mode, Tags: []string{"C++", "👩‍💻"},
			Date: "2020-01-01", PageSize: 20}
		var names []string
		before := provider.calls
		for {
			page, err := service.ClientPage(t.Context(), notes, input)
			if err != nil || page.Mode != mode || !page.Semantic.Used {
				t.Fatalf("enhanced page: %+v %v", page, err)
			}
			for _, note := range page.Notes {
				if slices.Contains(names, note.Name) || !slices.Contains(note.Tags, "C++") {
					t.Fatalf("duplicate or unfiltered source: %+v", note)
				}
				names = append(names, note.Name)
			}
			if page.NextPageToken == "" {
				break
			}
			raw, err := json.Marshal(page)
			if err != nil || !strings.Contains(string(raw), `"notes"`) {
				t.Fatal(err)
			}
			var cursor clientSearchCursor
			if notes.readCursorLimit(page.NextPageToken, &cursor, clientSearchTokenBytes) != nil ||
				strings.Contains(string(canonicalJSON(cursor)), "needle garden") {
				t.Fatal("pagination retained authoritative source content")
			}
			input.PageToken = page.NextPageToken
		}
		if len(names) != 27 || !slices.IsSorted(names) || provider.calls-before != 1 {
			t.Fatalf("ranked pagination lost notes or re-embedded: %v calls=%d", names, provider.calls-before)
		}
	}
}

func TestClientSearchCursorRejectsChangedFiltersIndexAndTampering(t *testing.T) {
	service, notes, _, _ := clientSearchFixture(t)
	input := ClientSearchRequest{Query: "needle", Mode: "hybrid", PageSize: 2}
	first, err := service.ClientPage(t.Context(), notes, input)
	if err != nil || first.NextPageToken == "" {
		t.Fatal(err)
	}
	input.PageToken = first.NextPageToken
	for _, change := range []func(*ClientSearchRequest){
		func(r *ClientSearchRequest) { r.Query = "other" },
		func(r *ClientSearchRequest) { r.Mode = "semantic" },
		func(r *ClientSearchRequest) { r.State = "ARCHIVED" },
		func(r *ClientSearchRequest) { r.Tags = []string{"C++"} },
		func(r *ClientSearchRequest) { r.Date = "2020-01-02" },
		func(r *ClientSearchRequest) { r.PageToken += "x" },
	} {
		changed := input
		change(&changed)
		if _, err := service.ClientPage(t.Context(), notes, changed); errorCode(err) != "INVALID_PAGE_TOKEN" {
			t.Fatalf("changed cursor accepted: %+v %v", changed, err)
		}
	}
	var cursor clientSearchCursor
	if err := notes.readCursorLimit(input.PageToken, &cursor, clientSearchTokenBytes); err != nil {
		t.Fatal(err)
	}
	cursor.Expires = time.Now().Add(-time.Second).Unix()
	expired := input
	expired.PageToken = notes.signCursor(cursor)
	if _, err := service.ClientPage(t.Context(), notes, expired); errorCode(err) != "INVALID_PAGE_TOKEN" {
		t.Fatal("expired search cursor accepted", err)
	}
	service.Store.Settings.Embedding.Enabled = false
	if _, err := service.ClientPage(t.Context(), notes, input); errorCode(err) != "INVALID_PAGE_TOKEN" {
		t.Fatal("search cursor kept semantic results after disabling the model", err)
	}
	service.Store.Settings.Embedding.Enabled = true
	refreshIndexMemo(t, service.Store, rawMemo("new", "needle new"))
	if _, err := service.ClientPage(t.Context(), notes, input); errorCode(err) != "INVALID_PAGE_TOKEN" {
		t.Fatal("changed index accepted", err)
	}
}

func TestClientSearchSourceFailureNeverReturnsPartialOrStaleNotes(t *testing.T) {
	for _, kind := range []string{"changed", "deleted", "foreign", "outage"} {
		t.Run(kind, func(t *testing.T) {
			service, notes, memos, _ := clientSearchFixture(t)
			switch kind {
			case "changed":
				memos["memos/rank-01"]["content"] = "PRIVATE_CHANGED_CONTENT"
			case "deleted":
				delete(memos, "memos/rank-01")
			case "foreign":
				memos["memos/rank-01"]["creator"] = "users/other"
			case "outage":
				notes.Source = fakeClient(notes.Settings, func(r *http.Request) (int, any, error) {
					if status, body, ok := identityResponse(r); ok {
						return status, body, nil
					}
					if strings.HasSuffix(r.URL.Path, "/rank-01") {
						return 503, nil, nil
					}
					return 200, memos[strings.TrimPrefix(r.URL.Path, "/api/v1/")], nil
				})
			}
			page, err := service.ClientPage(t.Context(), notes, ClientSearchRequest{Query: "needle", Mode: "hybrid"})
			if err == nil || len(page.Notes) != 0 || page.NextPageToken != "" {
				t.Fatalf("source failure returned partial/stale search: %+v %v", page, err)
			}
		})
	}
}

func TestClientSearchDisabledModelAndCanceledMatrixWait(t *testing.T) {
	service, notes, _, provider := clientSearchFixture(t)
	service.Store.Settings.Embedding.Enabled = false
	page, err := service.ClientPage(t.Context(), notes, ClientSearchRequest{Query: "needle", Mode: "hybrid"})
	if err != nil || page.Mode != "keyword" || page.Semantic.Reason != "EMBEDDING_DISABLED" ||
		len(page.Notes) != 20 || provider.calls != 0 {
		t.Fatalf("explicit hybrid fallback: %+v %v", page, err)
	}
	if _, err := service.ClientPage(t.Context(), notes, ClientSearchRequest{Query: "needle", Mode: "semantic"}); errorCode(err) != "EMBEDDING_DISABLED" {
		t.Fatal("semantic mode silently fell back", err)
	}
	service.Store.Settings.Embedding.Enabled = true
	service.mu.Lock()
	defer service.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := service.ClientPage(ctx, notes, ClientSearchRequest{Query: "needle", Mode: "semantic"}); err == nil || time.Since(start) > time.Second {
		t.Fatal("matrix wait ignored cancellation", err)
	}
}

func TestClientSearchRequiresSessionOriginAndCSRF(t *testing.T) {
	api, fixture := clientFixture(t)
	path, body := "/api/app/v1/search", `{"query":"needle","mode":"semantic"}`
	cookie, identity := settingsSession(t, api)
	for _, test := range []struct {
		cookie       *http.Cookie
		origin, csrf string
		status       int
	}{
		{nil, fixture.settings.Web.PublicOrigin, "", 401},
		{cookie, "", identity.CSRFToken, 403},
		{cookie, fixture.settings.Web.PublicOrigin, "", 403},
		{cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken, 503},
	} {
		response := clientCall(api, "POST", path, body, test.cookie, test.origin, test.csrf)
		if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("search boundary: %d %s", response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest("POST", path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+fixture.settings.AccessTokens[0].Token)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("machine credential entered native search", response.Code)
	}
}

func TestClientSearchStrictJSONAndResponseContract(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	memo := rawMemo("one", "needle original")
	api.app.Notes.Source = fakeClient(api.app.Store.Settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if r.URL.Path == "/api/v1/memos" {
			return 200, map[string]any{"memos": []any{memo}}, nil
		}
		return 200, memo, nil
	})
	call := func(body string) *httptest.ResponseRecorder {
		return clientCall(api, "POST", "/api/app/v1/search", body, cookie,
			fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	}
	for _, body := range []string{
		`{"query":null}`, `{"query":"x","query":"y"}`, `{"limit":10}`,
		`{"mode":"unknown"}`, `{"mode":"semantic"}`, `{"state":"ALL"}`,
		`{"page_size":0}`, `{"page_size":101}`, `{"page_size":1.5}`,
		`{"tags":[""]}`, `{"date":"2026-02-30"}`, `{"mode":""}`, `{"state":""}`,
	} {
		if response := call(body); response.Code != 422 {
			t.Fatalf("invalid client search accepted: %s -> %d %s", body, response.Code, response.Body.String())
		}
	}
	response := call(`{"query":"needle","mode":"keyword"}`)
	if response.Code != 200 {
		t.Fatalf("model-independent search: %d %s", response.Code, response.Body.String())
	}
	aiAPIValidateJSON(t, clientChatSchema(t, "ClientSearchPage"), response.Body.Bytes())
	for _, path := range []string{"/api/app/v1/search?query=needle", "/api/app/v1/search?extra=x"} {
		response := clientCall(api, "POST", path, `{}`, cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
		if response.Code != 422 {
			t.Fatal("search accepted query parameters", response.Code)
		}
	}
}

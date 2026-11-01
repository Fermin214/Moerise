package moerise

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientSourceReadBudgetIncludesAuthenticationAndRuntimeWait(t *testing.T) {
	for _, path := range []string{"/notes", "/tags", "/calendar?month=2026-11", "/chat-materials"} {
		t.Run(path, func(t *testing.T) {
			api, _ := clientFixture(t)
			cookie, _ := settingsSession(t, api)
			var deadlines []time.Time
			for _, client := range []*http.Client{api.app.Sessions.auth.client, api.app.Notes.Source.(*MemosClient).client} {
				client.Timeout = 0
				transport := client.Transport
				client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					deadline, ok := r.Context().Deadline()
					if !ok || time.Until(deadline) > liveListRequestTimeout {
						t.Fatalf("source read has no bounded context: %v", deadline)
					}
					deadlines = append(deadlines, deadline)
					return transport.RoundTrip(r)
				})
			}
			r := httptest.NewRequest(http.MethodGet, "/api/app/v1"+path, nil)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if len(deadlines) < 2 {
				t.Fatalf("source read did not authenticate and read source: %d %s", w.Code, w.Body.String())
			}
			for _, deadline := range deadlines {
				if !deadline.Equal(deadlines[0]) {
					t.Fatalf("source reads did not share the authentication budget: %v", deadlines)
				}
			}
		})
	}
}

func TestClientSourceReadCanceledWhileWaitingForSettings(t *testing.T) {
	api, _ := clientFixture(t)
	api.app.runtimeMu.Lock()
	defer api.app.runtimeMu.Unlock()
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		if deadline {
			ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		}
		cancel()
		r := httptest.NewRequest(http.MethodGet, "/api/app/v1/tags", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		want := "LIVE_LIST_CANCELED"
		if deadline {
			want = "LIVE_LIST_LIMIT"
		}
		if w.Code != 503 || !strings.Contains(w.Body.String(), want) || strings.Contains(w.Body.String(), `"tags"`) {
			t.Fatalf("resource wait escaped budget: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestClientSourceReadRateLimitDoesNotReturnPartialContent(t *testing.T) {
	for _, path := range []string{"/notes", "/tags", "/calendar?month=2020-01", "/chat-materials"} {
		t.Run(path, func(t *testing.T) {
			api, _ := clientFixture(t)
			cookie, _ := settingsSession(t, api)
			settings := api.app.Store.Settings
			settings.Memos.PageSize = 1
			pages := 0
			source := fakeClient(settings, func(r *http.Request) (int, any, error) {
				if status, body, ok := identityResponse(r); ok {
					return status, body, nil
				}
				if r.URL.Path == "/api/v1/memos" {
					pages++
					if pages == 1 {
						return 200, map[string]any{"memos": []any{rawMemo("one", "PRIVATE_ORIGINAL")}, "nextPageToken": "next"}, nil
					}
					return 429, map[string]string{"message": "PRIVATE_UPSTREAM_BODY"}, nil
				}
				t.Fatalf("unexpected per-note read after list failure: %s", r.URL.Path)
				return 500, nil, nil
			})
			api.app.Notes.Source = source
			api.app.Chat.Notes = api.app.Notes
			w := clientCall(api, "GET", "/api/app/v1"+path, "", cookie, "", "")
			if w.Code != 503 || !strings.Contains(w.Body.String(), "UPSTREAM_RATE_LIMITED") || pages != 2 ||
				strings.Contains(w.Body.String(), "PRIVATE") || strings.Contains(w.Body.String(), `"notes"`) ||
				strings.Contains(w.Body.String(), `"tags"`) || strings.Contains(w.Body.String(), `"days"`) {
				t.Fatalf("rate limit returned partial content or raw source error: %d %s (%d pages)", w.Code, w.Body.String(), pages)
			}
		})
	}
}

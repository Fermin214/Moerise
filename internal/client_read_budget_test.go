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
	for _, path := range []string{"/notes", "/tags", "/calendar?month=2026-11"} {
		t.Run(path, func(t *testing.T) {
			api, _ := clientFixture(t)
			cookie, _ := settingsSession(t, api)
			memo := rawMemo("one", "#tag original")
			memo["createTime"], memo["updateTime"] = "2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z"
			refreshIndexMemo(t, api.app.Store, memo)
			source := fakeClient(api.app.Store.Settings, func(r *http.Request) (int, any, error) {
				if status, body, ok := identityResponse(r); ok {
					return status, body, nil
				}
				t.Fatalf("unexpected upstream Memos call %s %s on local read", r.Method, r.URL.Path)
				return 500, nil, nil
			})
			api.app.Notes.Source = source
			var deadlines []time.Time
			for _, client := range []*http.Client{api.app.Sessions.auth.client, source.client} {
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
			if w.Code != 200 || len(deadlines) != 0 {
				t.Fatalf("warm local read failed or hit upstream: %d %s deadlines=%v", w.Code, w.Body.String(), deadlines)
			}
			api.app.Sessions.ClearCache()
			started := time.Now()
			r2 := httptest.NewRequest(http.MethodGet, "/api/app/v1"+path, nil)
			r2.AddCookie(cookie)
			w2 := httptest.NewRecorder()
			api.ServeHTTP(w2, r2)
			if w2.Code != 200 || len(deadlines) == 0 {
				t.Fatalf("uncached read did not authenticate within deadline: %d %s", w2.Code, w2.Body.String())
			}
			for _, deadline := range deadlines {
				if !deadline.Equal(deadlines[0]) || deadline.Before(started) {
					t.Fatalf("uncached auth did not share request budget: %v", deadlines)
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
	for _, path := range []string{"/notes", "/tags", "/calendar?month=2020-01"} {
		t.Run(path, func(t *testing.T) {
			api, _ := clientFixture(t)
			cookie, _ := settingsSession(t, api)
			settings := api.app.Store.Settings
			settings.Memos.PageSize = 1
			memo := rawMemo("one", "#tag local")
			memo["createTime"], memo["updateTime"] = "2020-01-01T00:00:00Z", "2020-01-01T00:00:00Z"
			refreshIndexMemo(t, api.app.Store, memo)
			pages := 0
			source := fakeClient(settings, func(r *http.Request) (int, any, error) {
				if status, body, ok := identityResponse(r); ok {
					return status, body, nil
				}
				if r.URL.Path == "/api/v1/memos" || r.URL.Path == "/api/v1/users:stats" {
					pages++
					if pages == 1 {
						return 200, map[string]any{"memos": []any{rawMemo("one", "PRIVATE_ORIGINAL")}, "nextPageToken": "next"}, nil
					}
					return 429, map[string]string{"message": "PRIVATE_UPSTREAM_BODY"}, nil
				}
				return 500, nil, nil
			})
			api.app.Notes.Source = source
			w := clientCall(api, "GET", "/api/app/v1"+path, "", cookie, "", "")
			if w.Code != 200 || pages != 0 {
				t.Fatalf("local client read failed or called upstream: %d %s (%d pages)", w.Code, w.Body.String(), pages)
			}
			_, err := api.app.Notes.ListLive(t.Context(), NoteList{State: "NORMAL", PageSize: 10})
			if errorCode(err) != "UPSTREAM_RATE_LIMITED" || pages != 2 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("external source list did not fail cleanly on rate limit: err=%v pages=%d", err, pages)
			}
		})
	}
}

func TestClientSourceReadCancelsIndexConnectionWait(t *testing.T) {
	for _, path := range []string{"/notes", "/tags"} {
		for _, deadline := range []bool{false, true} {
			t.Run(path+map[bool]string{false: "/cancel", true: "/deadline"}[deadline], func(t *testing.T) {
				api, _ := clientFixture(t)
				cookie, _ := settingsSession(t, api)
				refreshIndexMemo(t, api.app.Store, rawMemo("one", "original"))
				readerDB := api.app.Store.indexReader()
				readerDB.SetMaxOpenConns(1)
				defer readerDB.SetMaxOpenConns(4)
				connection, err := readerDB.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Close()
				ctx, cancel := context.WithCancel(t.Context())
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
				}
				defer cancel()
				r := httptest.NewRequest("GET", "/api/app/v1"+path, nil).WithContext(ctx)
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { api.ServeHTTP(w, r); close(done) }()
				if !deadline {
					time.Sleep(25 * time.Millisecond)
					cancel()
				}
				<-ctx.Done()
				select {
				case <-done:
				case <-time.After(500 * time.Millisecond):
					connection.Close()
					<-done
					t.Fatal("canceled request kept waiting for the index connection")
				}
				want := "LIVE_LIST_CANCELED"
				if deadline {
					want = "LIVE_LIST_LIMIT"
				}
				if w.Code != 503 || !strings.Contains(w.Body.String(), want) {
					t.Fatalf("index wait escaped request cancellation: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}

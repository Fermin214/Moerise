//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	moerise "moerise/internal"
)

func TestMemosOversizedPaginationAcceptance(t *testing.T) {
	for _, phase := range []string{"first", "next", "catchup-next"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			expected := map[string]bool{}
			for range 6 {
				memo := h.call(h.memos, "/api/v1/memos", "POST", h.upstream,
					map[string]string{"content": "pagination boundary", "visibility": "PRIVATE"}, 200)
				expected[memo["name"].(string)] = true
			}
			h.withOfflineApp(func(app *moerise.App) {
				target, err := url.Parse(h.memos)
				if err != nil {
					t.Fatal(err)
				}
				var oversized atomic.Int32
				proxy := httputil.NewSingleHostReverseProxy(target)
				proxy.ModifyResponse = func(response *http.Response) error {
					r := response.Request
					query := r.URL.Query()
					if r.URL.Path != "/api/v1/memos" || query.Get("state") != "NORMAL" ||
						(phase == "catchup-next") != strings.Contains(query.Get("filter"), "updated_ts") ||
						(phase != "first") != (query.Get("pageToken") != "") {
						return nil
					}
					body, err := io.ReadAll(response.Body)
					response.Body.Close()
					if err != nil {
						return err
					}
					var page struct {
						Memos []json.RawMessage `json:"memos"`
					}
					if err := json.Unmarshal(body, &page); err != nil {
						return err
					}
					response.Body = io.NopCloser(bytes.NewReader(body))
					// 保留真实 Memos 游标。合法 JSON 前缀模拟大响应，避免导入数百篇长笔记。
					if len(page.Memos) > 1 {
						oversized.Add(1)
						response.Body = io.NopCloser(io.MultiReader(strings.NewReader(strings.Repeat(" ", 32*1024*1024+1)), response.Body))
						response.ContentLength = -1
						response.Header.Del("Content-Length")
					}
					return nil
				}
				server := httptest.NewServer(proxy)
				defer server.Close()
				config := h.settings.Memos
				config.BaseURL, config.PageSize = server.URL, 2
				source := moerise.NewMemosClient(config)
				defer source.Close()
				app.Worker.Source = source
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				if _, err := app.Worker.Sync(ctx, moerise.Job{Mode: "full"}); err != nil {
					t.Fatal(err)
				}
				names, err := app.Store.Names()
				if err != nil || len(names) != len(expected) || oversized.Load() != 1 {
					t.Fatalf("incomplete pagination: notes=%v expected=%v oversized=%d error=%v", names, expected, oversized.Load(), err)
				}
				for name := range expected {
					if !names[name] {
						t.Fatalf("pagination lost %s", name)
					}
				}
				if phase == "next" {
					oversized.Store(0)
					live := moerise.NewMemosClient(config)
					defer live.Close()
					seen, token := map[string]bool{}, ""
					for {
						notes, next, err := live.ListLivePage(ctx, "NORMAL", 3, token)
						if err != nil {
							t.Fatal(err)
						}
						for _, note := range notes {
							if seen[note.Name] || !expected[note.Name] {
								t.Fatalf("invalid live pagination: %s", note.Name)
							}
							seen[note.Name] = true
						}
						if next == "" {
							break
						}
						token = next
					}
					if len(seen) != len(expected) || oversized.Load() != 1 {
						t.Fatalf("live rescan lost notes or repeated oversized requests: notes=%v oversized=%d", seen, oversized.Load())
					}
				}
			})
		})
	}
}

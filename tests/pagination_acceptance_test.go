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
	for _, phase := range []string{"first", "next", "catchup-next", "shared-minimum"} {
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
				var shrinkingSource *moerise.MemosClient
				proxy := httputil.NewSingleHostReverseProxy(target)
				proxy.ModifyResponse = func(response *http.Response) error {
					r := response.Request
					query := r.URL.Query()
					if r.URL.Path != "/api/v1/memos" {
						return nil
					}
					if query.Get("state") == "NORMAL" {
						if (phase == "catchup-next") != strings.Contains(query.Get("filter"), "updated_ts") ||
							(phase != "first") != (query.Get("pageToken") != "") {
							return nil
						}
					} else if phase != "shared-minimum" || query.Get("state") != "ARCHIVED" {
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
						if phase == "shared-minimum" && query.Get("state") == "NORMAL" && shrinkingSource != nil {
							// 旧续页返回前，另一读取先把共享批量缩到 1。
							if _, _, err := shrinkingSource.ListPage(r.Context(), "ARCHIVED", nil, ""); err != nil {
								return err
							}
						}
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
				if phase == "shared-minimum" {
					shrinkingSource = source
					for range 3 {
						memo := h.call(h.memos, "/api/v1/memos", "POST", h.upstream,
							map[string]string{"content": "concurrent shrink", "visibility": "PRIVATE"}, 200)
						name := memo["name"].(string)
						h.call(h.memos, "/api/v1/"+name+"?updateMask=state", "PATCH", h.upstream,
							map[string]string{"state": "ARCHIVED"}, 200)
						expected[name] = true
					}
				}
				app.Worker.Source = source
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				if _, err := app.Worker.Sync(ctx, moerise.Job{Mode: "full"}); err != nil {
					t.Fatal(err)
				}
				names, err := app.Store.Names()
				wantOversized := int32(1)
				if phase == "shared-minimum" {
					wantOversized = 2
				}
				if err != nil || len(names) != len(expected) || oversized.Load() != wantOversized {
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

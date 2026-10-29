//go:build integration

package acceptance

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAttachmentsAcceptance(t *testing.T) {
	for _, mode := range []string{"partial-failure", "lost-response"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			posts, failed := 0, false
			h := newHarnessWithUpstream(t, func(address string) string {
				target, _ := url.Parse(address)
				proxy := httputil.NewSingleHostReverseProxy(target)
				proxy.Transport = &http.Transport{Proxy: nil}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					inject := false
					if r.Method == "POST" && r.URL.Path == "/api/v1/attachments" {
						posts++
						inject = !failed && posts == 2
						if inject {
							failed = true
						}
					}
					mu.Unlock()
					if inject {
						if mode == "partial-failure" {
							http.Error(w, "injected attachment failure", 503)
						} else {
							result := httptest.NewRecorder()
							proxy.ServeHTTP(result, r)
							if result.Code != 200 {
								t.Errorf("real upload failed: %d %s", result.Code, result.Body.String())
							}
							conn, _, err := w.(http.Hijacker).Hijack()
							if err == nil {
								_ = conn.Close()
							}
						}
						return
					}
					proxy.ServeHTTP(w, r)
				}))
				t.Cleanup(server.Close)
				return server.URL
			})
			files := []map[string]string{
				{"filename": "first.txt", "type": "text/plain", "content": base64.StdEncoding.EncodeToString([]byte("first file"))},
				{"filename": "second.txt", "type": "text/plain", "content": base64.StdEncoding.EncodeToString([]byte("second file"))},
			}
			body := map[string]any{"content": "", "attachments": files}
			requireRecordError(t, h.call(h.base, "/api/ext/v1/notes", "POST", h.reader, body, 403), "FORBIDDEN")
			requireRecordError(t, h.recordInProcess("files", body, 503), "WRITE_OUTCOME_UNKNOWN")
			snapshot := filepath.Join(h.root, "partial-attachments")
			h.command(true, "backup", "--output", snapshot)
			h.stop()
			h.start()
			h.ready()
			note := h.recordInProcess("files", body, 200)
			name := h.requireRecord(note, "", "", nil)
			attachments := note["attachments"].([]any)
			if len(attachments) != 2 {
				t.Fatalf("missing linked attachments: %v", note)
			}
			for _, item := range attachments {
				attachment := item.(map[string]any)
				filename := attachment["filename"].(string)
				source := h.call(h.memos, "/api/v1/"+attachment["name"].(string), "GET", h.upstream, nil, 200)
				if source["memo"] != name || source["filename"] != filename {
					t.Fatalf("incorrect attachment parent: %v", source)
				}
				// Use Memos' file endpoint, the same URL the web client opens.
				address := h.memos + "/file/" + attachment["name"].(string) + "/" + url.PathEscape(filename)
				req, _ := http.NewRequest("GET", address, nil)
				req.Header.Set("Authorization", "Bearer "+h.upstream)
				response, err := h.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 ||
					string(data) != strings.TrimSuffix(filename, ".txt")+" file" {
					t.Fatalf("attachment cannot be opened: status=%d data=%q error=%v", response.StatusCode, data, err)
				}
			}
			mu.Lock()
			expectedPosts := posts
			mu.Unlock()
			if expectedPosts != map[string]int{"partial-failure": 3, "lost-response": 2}[mode] {
				t.Fatalf("unexpected upload attempts: %d", expectedPosts)
			}
			for _, operation := range []string{"rebuild-index", "restore"} {
				h.stop()
				if operation == "restore" {
					h.command(true, "restore", "--source", snapshot, "--safety-backup", filepath.Join(h.root, "before-restore"))
				} else {
					h.command(true, "rebuild-index")
				}
				h.start()
				h.ready()
				replay := h.recordInProcess("files", body, 200)
				if replay["name"] != name || len(replay["attachments"].([]any)) != 2 {
					t.Fatal("attachment resources changed after recovery")
				}
			}
			mu.Lock()
			count := posts
			mu.Unlock()
			if count != expectedPosts {
				t.Fatalf("recovery uploaded duplicates: %d != %d", count, expectedPosts)
			}
			h.requireSourceRecords(name)
			changed := map[string]any{"content": "", "attachments": []map[string]string{files[1], files[0]}}
			requireRecordError(t, h.recordInProcess("files", changed, 409), "IDEMPOTENCY_CONFLICT")
		})
	}
}

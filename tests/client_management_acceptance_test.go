//go:build integration

package acceptance

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	moerise "moerise/internal"
)

func managedClient(t *testing.T, h *harness) *clientHarness {
	t.Helper()
	h.stop()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	h.settings.Web = moerise.WebConfig{Enabled: true, PublicOrigin: h.base,
		StateEncryptionKey: base64.StdEncoding.EncodeToString(key)}
	data, _ := json.Marshal(h.settings)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	c := &clientHarness{h: h}
	c.call("POST", "/api/app/v1/session",
		map[string]string{"username": "acceptance-owner", "password": h.password}, 200)
	return c
}

func clientNotePath(note map[string]any) string {
	return "/api/app/v1/notes/" + strings.TrimPrefix(note["name"].(string), "memos/")
}

func TestClientManagementMemosAcceptance(t *testing.T) {
	var dropUpload atomic.Bool
	h := newHarnessWithUpstream(t, func(upstream string) string {
		target, _ := url.Parse(upstream)
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ModifyResponse = func(response *http.Response) error {
			if response.Request.URL.Path == "/api/v1/attachments" &&
				response.Request.Method == http.MethodPost && dropUpload.Swap(false) {
				response.Body.Close()
				response.StatusCode = 503
				response.Body = io.NopCloser(strings.NewReader(`{"message":"response lost"}`))
				response.ContentLength = -1
				response.Header.Del("Content-Length")
			}
			return nil
		}
		server := httptest.NewServer(proxy)
		t.Cleanup(server.Close)
		return server.URL
	})
	c := managedClient(t, h)
	c.key = "original-tags-and-whitespace"
	original := "  #甲\n#乙\n中文正文与空白 \n "
	note := c.call("POST", "/api/app/v1/notes", map[string]any{"content": original}, 201)
	path := clientNotePath(note)
	if note["content"] != original {
		t.Fatal("native creation rewrote the original")
	}
	c.key = "empty"
	c.call("POST", "/api/app/v1/notes", map[string]any{"content": " \n "}, 422)
	c.call("POST", "/api/app/v1/notes", map[string]any{"content": "body", "tags": []string{"implicit"}}, 422)
	c.key = "native-edit"
	edited := "  改过的 #甲 #丙\n\n末尾 \n"
	note = c.call("PATCH", path, map[string]any{"content": edited, "source_hash": note["source_hash"]}, 200)
	if note["content"] != edited {
		t.Fatal("inline editing rewrote the original")
	}
	c.key = "stale-edit"
	c.call("PATCH", path, map[string]any{"content": "overwrite", "source_hash": strings.Repeat("0", 64)}, 409)
	c.key = "archive"
	note = c.call("PATCH", path, map[string]any{"state": "ARCHIVED", "source_hash": note["source_hash"]}, 200)
	page := c.call("GET", "/api/app/v1/notes?state=ARCHIVED", nil, 200)
	if len(page["notes"].([]any)) != 1 || note["state"] != "ARCHIVED" {
		t.Fatal("archived note is outside the archive entry")
	}
	c.key = "restore"
	note = c.call("PATCH", path, map[string]any{"state": "NORMAL", "source_hash": note["source_hash"]}, 200)

	fileBytes := []byte("private attachment bytes\n")
	file := map[string]any{"filename": "资料.txt", "type": "text/plain", "content": base64.StdEncoding.EncodeToString(fileBytes)}
	c.key = "pure-file-response-loss"
	dropUpload.Store(true)
	c.call("POST", "/api/app/v1/notes", map[string]any{"content": "", "attachments": []any{file}}, 503)
	pure := c.call("POST", "/api/app/v1/notes", map[string]any{"content": "", "attachments": []any{file}}, 200)
	if pure["content"] != "" || len(pure["attachments"].([]any)) != 1 {
		t.Fatal("pure file recording inserted a placeholder or duplicated its attachment")
	}
	attachment := pure["attachments"].([]any)[0].(map[string]any)
	if len(attachment) != 5 || strings.Contains(fmt.Sprint(attachment), h.memos) {
		t.Fatal("browser attachment DTO leaked upstream fields")
	}
	download := attachment["content_url"].(string)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, ranged := range []bool{false, true} {
			request, _ := http.NewRequest(method, h.base+download, nil)
			request.AddCookie(c.cookie)
			wantStatus := 200
			if ranged {
				request.Header.Set("Range", "bytes=0-6")
				wantStatus = 206
			}
			response, err := h.client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != wantStatus ||
				response.Header.Get("X-Content-Type-Options") != "nosniff" ||
				!strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment;") {
				t.Fatalf("protected %s range=%v: %d %s %v", method, ranged, response.StatusCode, data, err)
			}
			if method == http.MethodHead && len(data) != 0 {
				t.Fatal("HEAD returned private file bytes")
			}
			if method == http.MethodGet {
				want := fileBytes
				if ranged {
					want = fileBytes[:7]
				}
				if !bytes.Equal(data, want) {
					t.Fatalf("file bytes changed: %q", data)
				}
			}
		}
	}
	unrelated := path + "/attachments/" + strings.TrimPrefix(attachment["name"].(string), "attachments/") + "/content"
	c.call("GET", unrelated, nil, 404)
	(&clientHarness{h: h}).call("GET", download, nil, 401)

	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	c.key = "pure-image"
	pureImage := c.call("POST", "/api/app/v1/notes", map[string]any{
		"content": "", "attachments": []any{map[string]any{
			"filename": "picture.png", "type": "image/png",
			"content": base64.StdEncoding.EncodeToString(picture.Bytes()),
		}},
	}, 201)
	if pureImage["content"] != "" || len(pureImage["attachments"].([]any)) != 1 {
		t.Fatal("pure image recording inserted a placeholder or lost its attachment")
	}
	pictureAttachment := pureImage["attachments"].([]any)[0].(map[string]any)
	pictureURL := pictureAttachment["content_url"].(string)
	if len(pictureAttachment) != 5 || pictureAttachment["type"] != "image/png" ||
		pictureAttachment["filename"] != "picture.png" || !strings.HasPrefix(pictureURL, clientNotePath(pureImage)+"/attachments/") {
		t.Fatalf("unexpected protected image DTO: %v", pictureAttachment)
	}

	// 更新在上游成功后丢失响应，重启后沿用来源版本与幂等键恢复。
	c.key = "update-attachment-response-loss"
	update := map[string]any{"source_hash": note["source_hash"], "attachments": []any{file}}
	dropUpload.Store(true)
	c.call("PATCH", path, update, 503)
	h.stop()
	h.start()
	note = c.call("PATCH", path, update, 200)
	if len(note["attachments"].([]any)) != 1 {
		t.Fatal("attachment retry duplicated the resource")
	}
	persistedImage := c.call("GET", clientNotePath(pureImage), nil, 200)
	if persistedImage["content"] != "" || len(persistedImage["attachments"].([]any)) != 1 ||
		persistedImage["attachments"].([]any)[0].(map[string]any)["name"] != pictureAttachment["name"] {
		t.Fatal("pure image did not survive Core restart")
	}
	sourceImage := h.call(h.memos, "/api/v1/"+pureImage["name"].(string), "GET", h.upstream, nil, 200)
	if (sourceImage["content"] != nil && sourceImage["content"] != "") || len(sourceImage["attachments"].([]any)) != 1 {
		t.Fatal("Memos did not persist the image with an empty body")
	}
	sourcePicture := sourceImage["attachments"].([]any)[0].(map[string]any)
	if sourcePicture["name"] != pictureAttachment["name"] || sourcePicture["memo"] != pureImage["name"] ||
		sourcePicture["type"] != "image/png" || sourcePicture["filename"] != "picture.png" {
		t.Fatalf("Memos did not retain the linked image: %v", sourcePicture)
	}
	t.Log("PASS empty-body image persisted in real Memos and remained readable after Core restart")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, ranged := range []bool{false, true} {
			request, _ := http.NewRequest(method, h.base+pictureURL, nil)
			request.AddCookie(c.cookie)
			wantStatus := 200
			want := picture.Bytes()
			if ranged {
				request.Header.Set("Range", "bytes=0-6")
				wantStatus, want = 206, want[:7]
			}
			response, err := h.client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != wantStatus ||
				response.Header.Get("Content-Type") != "image/png" ||
				!strings.HasPrefix(response.Header.Get("Content-Disposition"), "inline;") ||
				response.Header.Get("Cache-Control") != "no-store" ||
				response.Header.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("protected image %s range=%v: %d headers=%v body=%q error=%v",
					method, ranged, response.StatusCode, response.Header, data, err)
			}
			if method == http.MethodHead {
				if len(data) != 0 {
					t.Fatal("HEAD returned private image bytes")
				}
			} else if !bytes.Equal(data, want) {
				t.Fatalf("protected image bytes changed: %x want %x", data, want)
			}
		}
	}
	(&clientHarness{h: h}).call("GET", pictureURL, nil, 401)
	c.call("GET", path+"/attachments/"+strings.TrimPrefix(pictureAttachment["name"].(string), "attachments/")+"/content", nil, 404)
	t.Log("PASS protected inline PNG GET/HEAD and Range preserve bytes; anonymous and unrelated-note access denied")

	c.key = "remove-file-retain-text"
	attachedName := note["attachments"].([]any)[0].(map[string]any)["name"]
	note = c.call("PATCH", path, map[string]any{
		"source_hash": note["source_hash"], "remove_attachments": []any{attachedName},
	}, 200)
	if len(note["attachments"].([]any)) != 0 || note["content"] != edited {
		t.Fatal("removing the attachment changed the text")
	}
	c.key = "reject-empty-after-removal"
	c.call("PATCH", clientNotePath(pure), map[string]any{
		"source_hash": pure["source_hash"], "remove_attachments": []any{attachment["name"]},
	}, 422)

	// 服务端筛选跨过多页来源数据；时区为香港，15:59Z 与 16:00Z 分属两天。
	for i := range 7 {
		name := fmt.Sprintf("filter-%d", i)
		h.call(h.memos, "/api/v1/memos?memoId="+name, "POST", h.upstream,
			map[string]string{"content": fmt.Sprintf("针叶样例%d #过滤 #组合", i), "visibility": "PRIVATE"}, 200)
		created := "2026-10-28T16:00:00Z"
		if i == 0 {
			created = "2026-10-28T15:59:59Z"
		}
		h.call(h.memos, "/api/v1/memos/"+name+"?updateMask=create_time", "PATCH", h.upstream,
			map[string]string{"createTime": created}, 200)
	}
	query := "query=" + url.QueryEscape("针叶") + "&tag=" + url.QueryEscape("过滤") +
		"&tag=" + url.QueryEscape("组合") + "&date=2026-10-29&page_size=2"
	token := ""
	seen := map[string]bool{}
	for range 5 {
		page = c.call("GET", "/api/app/v1/notes?"+query+"&page_token="+url.QueryEscape(token), nil, 200)
		for _, raw := range page["notes"].([]any) {
			item := raw.(map[string]any)
			name := item["name"].(string)
			if seen[name] || name == "memos/filter-0" {
				t.Fatal("combined filtering leaked a duplicate or another creation day")
			}
			seen[name] = true
		}
		token = page["next_page_token"].(string)
		if token == "" {
			break
		}
		c.call("GET", "/api/app/v1/notes?query=other&page_token="+url.QueryEscape(token), nil, 422)
	}
	if len(seen) != 6 || token != "" {
		t.Fatalf("combined filtering did not cover all pages: %v token=%s", seen, token)
	}
	tags := c.call("GET", "/api/app/v1/tags", nil, 200)
	count := 0
	for _, raw := range tags["tags"].([]any) {
		tag := raw.(map[string]any)
		if tag["tag"] == "过滤" {
			count = int(tag["count"].(float64))
		}
	}
	if count != 7 {
		t.Fatalf("tag aggregation only counted a page: %v", tags)
	}
	calendar := c.call("GET", "/api/app/v1/calendar?month=2026-10", nil, 200)
	count = 0
	for _, raw := range calendar["days"].([]any) {
		day := raw.(map[string]any)
		if day["date"] == "2026-10-29" {
			count = int(day["count"].(float64))
		}
	}
	if count < 6 {
		t.Fatalf("calendar did not apply the product timezone: %v", calendar)
	}
	c.call("DELETE", path, map[string]any{"source_hash": note["source_hash"]}, 204)
	c.call("DELETE", path, map[string]any{"source_hash": note["source_hash"]}, 204)
	c.call("GET", path, nil, 404)
	c.call("DELETE", "/api/app/v1/session", nil, 204)
	c.call("GET", download, nil, 401)
	c.call("GET", pictureURL, nil, 401)
}

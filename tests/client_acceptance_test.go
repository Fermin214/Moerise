//go:build integration

package acceptance

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	moerise "moerise/internal"
)

type clientHarness struct {
	h      *harness
	cookie *http.Cookie
	csrf   string
}

func (c *clientHarness) call(method, path string, body any, status int) map[string]any {
	c.h.t.Helper()
	var input io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		input = bytes.NewReader(data)
	}
	request, _ := http.NewRequest(method, c.h.base+path, input)
	request.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		request.Header.Set("Origin", c.h.settings.Web.PublicOrigin)
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	if c.cookie != nil {
		request.AddCookie(c.cookie)
	}
	if path == "/api/app/v1/notes" && method == http.MethodPost {
		request.Header.Set("Idempotency-Key", "client-acceptance-original")
	}
	response, err := c.h.client.Do(request)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != status {
		c.h.t.Fatalf("%s %s: %d want %d, error=%v body=%s", method, path, response.StatusCode, status, err, data)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("WWW-Authenticate") != "" {
		c.h.t.Fatalf("client response headers: %v", response.Header)
	}
	for _, cookie := range response.Cookies() {
		if strings.Contains(cookie.Name, "moerise") && cookie.MaxAge >= 0 {
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
				c.h.t.Fatalf("invalid browser cookie: %v", cookie)
			}
			c.cookie = cookie
		}
	}
	if status == http.StatusNoContent {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		c.h.t.Fatal(err)
	}
	if csrf, ok := result["csrf_token"].(string); ok {
		c.csrf = csrf
	}
	if strings.Contains(string(data), c.h.upstream) || strings.Contains(string(data), "memos_refresh") ||
		strings.Contains(string(data), "accessToken") {
		c.h.t.Fatal("upstream credentials leaked to the client")
	}
	return result
}

func (c *clientHarness) all() []map[string]any {
	c.h.t.Helper()
	token := ""
	result := []map[string]any{}
	seen := map[string]bool{}
	for range 30 {
		page := c.call("GET", "/api/app/v1/notes?page_size=2&order=created_desc&page_token="+url.QueryEscape(token), nil, 200)
		for _, item := range page["notes"].([]any) {
			note := item.(map[string]any)
			name := note["name"].(string)
			if seen[name] || note["creator"] != c.h.settings.Memos.User || note["state"] != "NORMAL" {
				c.h.t.Fatalf("invalid live page: %v", note)
			}
			seen[name] = true
			result = append(result, note)
		}
		token = page["next_page_token"].(string)
		if token == "" {
			return result
		}
	}
	c.h.t.Fatal("live pagination did not terminate")
	return nil
}

func TestClientMemosAcceptance(t *testing.T) {
	var unavailable atomic.Bool
	h := newHarnessWithUpstream(t, func(upstream string) string {
		target, _ := url.Parse(upstream)
		proxy := httputil.NewSingleHostReverseProxy(target)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if unavailable.Load() {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			proxy.ServeHTTP(w, r)
		}))
		t.Cleanup(server.Close)
		return server.URL
	})
	h.stop()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	h.settings.Web = moerise.WebConfig{Enabled: true, PublicOrigin: h.base,
		StateEncryptionKey: base64.StdEncoding.EncodeToString(key)}
	h.settings.AccessTokens[1].Scopes = append(h.settings.AccessTokens[1].Scopes,
		"reviews:read", "reviews:write", "discovery:write", "ai:generate")
	data, _ := json.Marshal(h.settings)
	if err := os.WriteFile(h.config, data, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	c := &clientHarness{h: h}
	c.call("GET", "/api/app/v1/notes", nil, 401)
	c.call("POST", "/api/app/v1/session", map[string]string{"username": "acceptance-owner", "password": "wrong"}, 401)
	identity := c.call("POST", "/api/app/v1/session", map[string]string{"username": "acceptance-owner", "password": h.password}, 200)
	if identity["user"].(map[string]any)["name"] != h.settings.Memos.User || c.cookie == nil || c.csrf == "" {
		t.Fatal("login did not establish the bound identity")
	}
	otherPassword := randomToken()
	h.call(h.memos, "/api/v1/users", "POST", h.upstream,
		map[string]string{"username": "client-other", "password": otherPassword, "role": "USER"}, 200)
	other := &clientHarness{h: h}
	other.call("POST", "/api/app/v1/session", map[string]string{"username": "client-other", "password": otherPassword}, 403)
	if other.cookie != nil {
		t.Fatal("unbound account received a browser session")
	}
	otherSession := h.call(h.memos, "/api/v1/auth/signin", "POST", "",
		map[string]any{"passwordCredentials": map[string]string{"username": "client-other", "password": otherPassword}}, 200)
	h.call(h.memos, "/api/v1/memos", "POST", otherSession["accessToken"].(string),
		map[string]string{"content": "other author public", "visibility": "PUBLIC"}, 200)
	for _, id := range []string{"same-a", "same-z", "same-b", "same-m", "older", "archived"} {
		h.call(h.memos, "/api/v1/memos?memoId="+id, "POST", h.upstream,
			map[string]string{"content": id, "visibility": "PRIVATE"}, 200)
		patch := map[string]any{"createTime": "2026-01-02T00:00:00Z"}
		mask := "create_time"
		if id == "older" {
			patch["createTime"], patch["pinned"], mask = "2026-01-01T00:00:00Z", true, "create_time,pinned"
		}
		if id == "archived" {
			patch["state"], mask = "ARCHIVED", "create_time,state"
		}
		h.call(h.memos, "/api/v1/memos/"+id+"?updateMask="+mask, "PATCH", h.upstream, patch, 200)
	}
	notes := c.all()
	names := []string{}
	for _, note := range notes {
		names = append(names, note["name"].(string))
	}
	want := []string{"memos/same-z", "memos/same-m", "memos/same-b", "memos/same-a", "memos/older"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("live creation ordering: %v want %v", names, want)
	}
	h.call(h.memos, "/api/v1/memos/older?updateMask=content", "PATCH", h.upstream,
		map[string]string{"content": "old note edited now"}, 200)
	notes = c.all()
	if notes[len(notes)-1]["name"] != "memos/older" || notes[len(notes)-1]["content"] != "old note edited now" {
		t.Fatal("editing a pinned old memo changed creation order or returned stale content")
	}
	body := map[string]string{"content": "  原文 #标签一\n第二行 #标签二  \n"}
	created := c.call("POST", "/api/app/v1/notes", body, 201)
	replay := c.call("POST", "/api/app/v1/notes", body, 200)
	if replay["name"] != created["name"] || created["content"] != body["content"] || created["visibility"] != "PRIVATE" {
		t.Fatal("native create did not preserve the original body and resource")
	}
	c.call("POST", "/api/app/v1/notes", map[string]string{"content": "different"}, 409)
	notes = c.all()
	if len(notes) != 6 || notes[0]["name"] != created["name"] {
		t.Fatal("creation was duplicated or not immediately visible")
	}
	source := h.call(h.memos, "/api/v1/"+created["name"].(string), "GET", h.upstream, nil, 200)
	if source["content"] != body["content"] {
		t.Fatal("Memos did not retain the exact original body")
	}
	unavailable.Store(true)
	c.call("GET", "/api/app/v1/notes", nil, 503)
	unavailable.Store(false)
	c.call("GET", "/api/app/v1/session", nil, 200)
	h.stop()
	h.start()
	c.call("GET", "/api/app/v1/session", nil, 200)
	c.call("POST", "/api/app/v1/notes", body, 200)
	if len(c.all()) != 6 {
		t.Fatal("idempotency did not survive restart")
	}
	h.stop()
	h.command(true, "rebuild-index")
	h.start()
	c.call("GET", "/api/app/v1/session", nil, 200)
	if len(c.all()) != 6 {
		t.Fatal("live list depended on a rebuilt index")
	}
	for _, path := range []string{"/api/ext/v1/notes", "/api/ext/v1/sync", "/api/ext/v1/reviews/daily",
		"/api/ext/v1/discovery/open", "/api/ext/v1/insights", "/api/ext/v1/answers",
		"/api/ext/v1/notifications/id/resolve"} {
		h.call(h.base, path, "POST", h.operator, map[string]any{}, 403)
	}
	c.call("DELETE", "/api/app/v1/session", nil, 204)
	c.call("GET", "/api/app/v1/session", nil, 401)
	h.call(h.base, "/api/ext/v1/notes", "GET", h.reader, nil, 200)
}

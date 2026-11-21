package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaticRoutesAndEncodings(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html": "<html>app</html>", "app.0123456789abcdefabcd.js": "plain",
		"app.0123456789abcdefabcd.js.br": "brotli", "app.0123456789abcdefabcd.js.gz": "gzip",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler := Handler(dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	for _, path := range []string{"/", "/archived", "/review", "/walk", "/insights", "/map", "/settings", "/notes/a-b_1"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Body.String() != "<html>app</html>" ||
			w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
			t.Fatalf("%s: %d %v %s", path, w.Code, w.Header(), w.Body.String())
		}
	}
	for _, path := range []string{"/api/ext/v1/notes", "/api/app/v1/session", "/mcp", "/hooks/feishu", "/health/ready"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusTeapot {
			t.Fatalf("API boundary %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/unknown", "/notes/", "/notes/a/b", "/../index.html", "/%2e%2e/index.html", "/foo.js", "/.keep", "/src/app.js"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("Unknown path %s: %d", path, w.Code)
		}
	}
	for _, test := range []struct{ accept, encoding, body string }{
		{"br, gzip", "br", "brotli"}, {"br;q=0.5, gzip, identity;q=0", "gzip", "gzip"},
		{"BR, gzip;q=0", "br", "brotli"}, {"br;q=0, gzip;q=0", "", "plain"}, {"", "", "plain"},
	} {
		for _, method := range []string{"GET", "HEAD"} {
			r := httptest.NewRequest(method, "/app.0123456789abcdefabcd.js", nil)
			r.Header.Set("Accept-Encoding", test.accept)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 200 || w.Header().Get("Content-Encoding") != test.encoding ||
				w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
				(method == "GET" && w.Body.String() != test.body) || (method == "HEAD" && w.Body.Len() != 0) {
				t.Fatalf("%s %+v: %d %v %s", method, test, w.Code, w.Header(), w.Body.String())
			}
		}
	}
	r := httptest.NewRequest("GET", "/app.0123456789abcdefabcd.js", nil)
	r.Header.Set("Accept-Encoding", "*;q=0")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 406 {
		t.Fatalf("Unacceptable encoding: %d", w.Code)
	}
}

func TestStaticHandlerPreservesStreamingAndRanges(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(Handler("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		http.ServeContent(w, r, "attachment", time.Time{}, strings.NewReader("0123456789"))
	})))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	client := &http.Client{Timeout: 2 * time.Second}
	r, _ := http.NewRequest("GET", server.URL+"/api/attachment", nil)
	r.Header.Set("Range", "bytes=2-4")
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 206 || string(body) != "234" {
		t.Fatalf("Range: %d %s", response.StatusCode, body)
	}
	response, err = client.Get(server.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body = make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(response.Body, body); err != nil || string(body) != "data: first\n\n" {
		t.Fatalf("SSE: %q %v", body, err)
	}
}

package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed all:dist
var assets embed.FS

var notePage = regexp.MustCompile(`^/notes/[A-Za-z0-9_-]+$`)
var immutableAsset = regexp.MustCompile(`\.[a-f0-9]{20}\.(js|css|svg)$`)

func Handler(directory string, next http.Handler) http.Handler {
	files, _ := fs.Sub(assets, "dist")
	if directory != "" {
		files = os.DirFS(directory)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/") || path == "/mcp" || strings.HasPrefix(path, "/hooks/") ||
			strings.HasPrefix(path, "/health/") || path == "/docs" || path == "/openapi.json" || strings.HasPrefix(path, "/telemetry/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		name := strings.TrimPrefix(path, "/")
		switch path {
		case "/", "/archived", "/review", "/walk", "/map", "/settings":
			name = "index.html"
		default:
			if notePage.MatchString(path) {
				name = "index.html"
			}
		}
		if strings.ContainsAny(r.URL.EscapedPath(), "%\\") || strings.Contains(name, "/") ||
			!(name == "index.html" || strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".css") || strings.HasSuffix(name, ".svg")) {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := fs.Stat(files, name); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Vary", "Accept-Encoding")
		var body []byte
		var err error
		for _, encoding := range encodings(r.Header.Get("Accept-Encoding")) {
			suffix := map[string]string{"br": ".br", "gzip": ".gz", "identity": ""}[encoding]
			body, err = fs.ReadFile(files, name+suffix)
			if err == nil {
				if encoding != "identity" {
					w.Header().Set("Content-Encoding", encoding)
				}
				break
			}
		}
		if body == nil {
			http.Error(w, "No acceptable encoding", http.StatusNotAcceptable)
			return
		}
		if immutableAsset.MatchString(name) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		extension := name[strings.LastIndex(name, "."):]
		w.Header().Set("Content-Type", mime.TypeByExtension(extension))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	})
}

func encodings(header string) []string {
	qualities := map[string]float64{}
	for _, part := range strings.Split(strings.ToLower(header), ",") {
		fields := strings.Split(part, ";")
		quality := 1.0
		for _, parameter := range fields[1:] {
			parameter = strings.TrimSpace(parameter)
			if strings.HasPrefix(parameter, "q=") {
				value, err := strconv.ParseFloat(strings.TrimPrefix(parameter, "q="), 64)
				if err != nil || value < 0 || value > 1 {
					quality = 0
				} else {
					quality = value
				}
			}
		}
		qualities[strings.TrimSpace(fields[0])] = quality
	}
	quality := func(name string) float64 {
		if value, ok := qualities[name]; ok {
			return value
		}
		if name == "identity" && qualities["*"] != 0 {
			return 1
		}
		if name == "identity" {
			if _, exists := qualities["*"]; !exists {
				return 1
			}
		}
		return qualities["*"]
	}
	result := []string{"br", "gzip", "identity"}
	sort.SliceStable(result, func(i, j int) bool { return quality(result[i]) > quality(result[j]) })
	return result[:sort.Search(len(result), func(i int) bool { return quality(result[i]) <= 0 })]
}

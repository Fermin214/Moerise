package moerise

import (
	"net/http"
	"strings"
)

// Match operations rather than HTTP verbs: search and walking use read-only POSTs.
func externalReadOperation(method, path string) bool {
	switch path {
	case "/api/ext/v1/search", "/api/ext/v1/discovery/walk":
		return method == http.MethodPost
	case "/api/ext/v1/me", "/api/ext/v1/notes", "/api/ext/v1/sync",
		"/api/ext/v1/search/index", "/api/ext/v1/reviews/daily":
		return method == http.MethodGet
	}
	if method != http.MethodGet {
		return false
	}
	for _, prefix := range []string{"/api/ext/v1/jobs/", "/api/ext/v1/reviews/"} {
		tail := strings.TrimPrefix(path, prefix)
		if tail != path && tail != "" && !strings.Contains(tail, "/") {
			return true
		}
	}
	tail := strings.TrimPrefix(path, "/api/ext/v1/notes/")
	if tail == path {
		return false
	}
	if strings.HasSuffix(tail, "/related") {
		tail = strings.TrimSuffix(tail, "/related")
	}
	return tail != "" && !strings.Contains(tail, "/")
}

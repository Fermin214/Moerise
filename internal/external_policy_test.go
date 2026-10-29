package moerise

import "testing"

func TestExternalReadOperations(t *testing.T) {
	for _, item := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/api/ext/v1/notes", true},
		{"GET", "/api/ext/v1/notes/abc/related", true},
		{"POST", "/api/ext/v1/search", true},
		{"POST", "/api/ext/v1/discovery/walk", true},
		{"GET", "/api/ext/v1/reviews/daily", true},
		{"POST", "/api/ext/v1/notes", false},
		{"POST", "/api/ext/v1/sync", false},
		{"POST", "/api/ext/v1/reviews/daily", false},
		{"POST", "/api/ext/v1/reviews/daily/replace", false},
		{"POST", "/api/ext/v1/discovery/open", false},
		{"POST", "/api/ext/v1/insights", false},
		{"GET", "/api/ext/v1/notifications", false},
		{"POST", "/api/ext/v1/notifications/id/resolve", false},
		{"GET", "/api/ext/v1/notes/abc/unknown", false},
	} {
		if got := externalReadOperation(item.method, item.path); got != item.allowed {
			t.Errorf("%s %s: allowed=%v", item.method, item.path, got)
		}
	}
}

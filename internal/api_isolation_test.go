package moerise

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestExternalAPIRejectsWritesBeforeParsingOrSourceAccess(t *testing.T) {
	settings := testSettings(t)
	scopes := []string{"notes:read", "sync:read", "reviews:read",
		"notes:write", "sync:write", "reviews:write", "discovery:write", "ai:generate"}
	settings.AccessTokens = append(settings.AccessTokens,
		AccessToken{"all", strings.Repeat("f", 40), slices.Clone(scopes)})
	store := testStore(t, settings)
	source := fakeClient(settings, func(*http.Request) (int, any, error) {
		t.Error("forbidden external operation reached Memos")
		return 500, nil, nil
	})
	api, err := NewAPI(NewApp(settings, store, source))
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct{ method, path string }{
		{"POST", "/notes"}, {"PUT", "/notes/example"}, {"PATCH", "/notes/example"},
		{"DELETE", "/notes/example"}, {"POST", "/sync"}, {"DELETE", "/sync"},
		{"POST", "/reviews/daily"}, {"POST", "/reviews/daily/replace"},
		{"POST", "/reviews/example/items/example/open"}, {"POST", "/reviews/example/items/example/skip"},
		{"POST", "/discovery/open"}, {"GET", "/notifications"}, {"GET", "/notifications/example"},
		{"POST", "/notifications/example/resolve"}, {"POST", "/insights"}, {"POST", "/answers"},
		{"GET", "/search"}, {"DELETE", "/search/index"}, {"POST", "/jobs/example"},
		{"POST", "/future-write"}, {"GET", "/future-management"},
	} {
		t.Run(operation.method+operation.path, func(t *testing.T) {
			for _, token := range []string{"", "invalid", settings.AccessTokens[0].Token, strings.Repeat("f", 40)} {
				response := aiAPICall(api, operation.method, "/api/ext/v1"+operation.path, token, "{invalid")
				wantStatus, wantCode := 403, "FORBIDDEN"
				if token == "" || token == "invalid" {
					wantStatus, wantCode = 401, "UNAUTHENTICATED"
				}
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if response.Code != wantStatus || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Error.Code != wantCode {
					t.Fatalf("credential %q: status=%d body=%s", token, response.Code, response.Body.String())
				}
			}
		})
	}
	for _, table := range []string{"note_writes", "jobs", "review_batches", "discovery_reads", "notifications"} {
		var count int
		if err := store.state.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("external operation changed %s: count=%d err=%v", table, count, err)
		}
	}
}

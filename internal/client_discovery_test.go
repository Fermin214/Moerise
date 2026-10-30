package moerise

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientDiscoverySessionCSRFAndResourceBoundaries(t *testing.T) {
	api, fixture := clientFixture(t)
	for _, path := range []string{"/reviews/daily", "/walks/start", "/walks/next"} {
		method := "POST"
		if path == "/reviews/daily" {
			method = "GET"
		}
		response := clientCall(api, method, "/api/app/v1"+path, `{}`, nil, fixture.settings.Web.PublicOrigin, "")
		if response.Code != 401 {
			t.Fatalf("anonymous discovery: %d %s", response.Code, response.Body.String())
		}
		request := httptest.NewRequest(method, "/api/app/v1"+path, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+fixture.settings.AccessTokens[0].Token)
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		if recorder.Code != 403 {
			t.Fatalf("machine credential entered client discovery: %d", recorder.Code)
		}
	}
	rawID, identity, err := api.app.Sessions.Login(t.Context(), "owner", "correct")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: api.cookieName(), Value: rawID}
	for _, path := range []string{"/walks/start", "/walks/next"} {
		response := clientCall(api, "POST", "/api/app/v1"+path, `{}`, cookie, fixture.settings.Web.PublicOrigin, "")
		if response.Code != 403 {
			t.Fatalf("walk bypassed CSRF: %d", response.Code)
		}
	}
	for _, test := range []struct {
		path, body, code string
		status           int
	}{
		{"/walks/start", `{}`, "EMBEDDING_DISABLED", 503},
		{"/walks/start", `{"mode":"random"}`, "INVALID_ARGUMENT", 422},
		{"/walks/start", `{"name":null}`, "INVALID_ARGUMENT", 422},
		{"/walks/next", `{"path":[]}`, "INVALID_ARGUMENT", 422},
		{"/walks/next", `{"name":"memos/a","path":[],"path":[]}`, "INVALID_ARGUMENT", 422},
		{"/walks/next", `{"name":"memos/a","path":["` + strings.Repeat("a", 1024*1024) + `"]}`, "WALK_RESOURCE_LIMIT", 413},
	} {
		response := clientCall(api, "POST", "/api/app/v1"+test.path, test.body, cookie,
			fixture.settings.Web.PublicOrigin, identity.CSRFToken)
		if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
			t.Fatalf("%s: %d %s", test.path, response.Code, response.Body.String())
		}
	}
	var count int
	if err := api.app.Store.state.QueryRow(`SELECT COUNT(*) FROM discovery_reads`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("client walk wrote read history: %d %v", count, err)
	}
}

func TestClientDailyPlanSanitizesSourcesAndKeepsAllItems(t *testing.T) {
	api, _ := clientFixture(t)
	store := api.app.Store
	store.Settings.Notifications.Times = []string{"09:00", "18:00"}
	raw := rawMemo("daily-image", "")
	raw["attachments"] = []any{map[string]any{"name": "attachments/image", "type": "image/png",
		"filename": "example.png", "externalLink": "https://private.example/image"}}
	memo, err := normalizeMemo(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Refresh(memo.Name, &memo); err != nil {
		t.Fatal(err)
	}
	if _, err := store.state.Exec(`UPDATE sync_state SET last_full = ?`, nowText()); err != nil {
		t.Fatal(err)
	}
	source := fakeClient(store.Settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if r.URL.Path == "/api/v1/memos/daily-image" {
			return 200, raw, nil
		}
		return 404, nil, nil
	})
	api.app.Reviews = NewReviewService(store, source)
	rawID, _, err := api.app.Sessions.Login(t.Context(), "owner", "correct")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: api.cookieName(), Value: rawID}
	var first ReviewBatch
	for range 2 {
		response := clientCall(api, "GET", "/api/app/v1/reviews/daily", "", cookie, "", "")
		var batch ReviewBatch
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &batch) != nil || len(batch.Items) != 1 {
			t.Fatalf("client whole-day plan: %d %s", response.Code, response.Body.String())
		}
		note := batch.Items[0].Note
		if note.URL != "/notes/daily-image" ||
			note.Attachments[0]["content_url"] != "/api/app/v1/notes/daily-image/attachments/image/content" ||
			strings.Contains(response.Body.String(), "private.example") {
			t.Fatal("client review exposed an upstream attachment address")
		}
		if first.ID != "" && first.ID != batch.ID {
			t.Fatal("refresh selected another daily plan")
		}
		first = batch
	}
}

package moerise

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAPIOperationIDsAreUnique(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(openAPISpec(), &spec); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for path, methods := range spec.Paths {
		for method, operation := range methods {
			id := operation.OperationID
			if id == "" {
				continue
			}
			location := method + " " + path
			if previous, ok := seen[id]; ok {
				t.Errorf("operationId %q is shared by %s and %s", id, previous, location)
			}
			seen[id] = location
		}
	}
}

func TestClientNotificationRecoveryUsesBoundSession(t *testing.T) {
	api, fixture := clientFixture(t)
	rawID, identity, err := api.app.Sessions.Login(t.Context(), "owner", "correct")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: api.cookieName(), Value: rawID}
	id, err := api.app.Notifications.enqueue("review", "client-recovery", "2026-10-29", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.app.Store.state.Exec("UPDATE notifications SET status = 'unknown' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	path := "/api/app/v1/admin/notifications/" + id
	for _, route := range []string{"/api/app/v1/admin/notifications?status=unknown", path} {
		response := clientCall(api, "GET", route, "", cookie, "", "")
		if response.Code != 200 || !strings.Contains(response.Body.String(), id) {
			t.Fatalf("recovery read: %d %s", response.Code, response.Body.String())
		}
	}
	response := clientCall(api, "POST", path+"/resolve", `{"decision":"not_sent","receipt":""}`,
		cookie, fixture.settings.Web.PublicOrigin, "")
	if response.Code != 403 {
		t.Fatalf("recovery accepted missing CSRF: %d %s", response.Code, response.Body.String())
	}
	response = clientCall(api, "POST", path+"/resolve", `{"decision":"not_sent","receipt":""}`,
		cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if response.Code != 200 {
		t.Fatalf("recovery: %d %s", response.Code, response.Body.String())
	}
	var detail NotificationDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Notification.Status != "pending" || len(detail.Resolutions) != 1 ||
		detail.Resolutions[0].ResolvedBy != identity.User.Name {
		t.Fatalf("recovery did not record the bound user: %+v", detail)
	}
}

type observedBody struct {
	*strings.Reader
	started chan struct{}
}

func (r *observedBody) Read(data []byte) (int, error) {
	if r.started != nil {
		close(r.started)
		r.started = nil
	}
	return r.Reader.Read(data)
}

func clientFixture(t *testing.T) (*ClientAPI, *sessionAuthFixture) {
	t.Helper()
	fixture := newSessionAuthFixture(t)
	store := testStore(t, fixture.settings)
	source := NewMemosClient(fixture.settings.Memos)
	t.Cleanup(source.Close)
	sessions, err := NewSessionService(store, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessions.Close)
	app := NewApp(fixture.settings, store, source)
	app.Sessions = sessions
	return NewClientAPI(app), fixture
}

func clientCall(api *ClientAPI, method, path, body string, cookie *http.Cookie, origin, csrf string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "https://notes.example.com"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func TestClientOriginCredentialsAndOfflineLogout(t *testing.T) {
	api, fixture := clientFixture(t)
	login := `{"username":"owner","password":"correct"}`
	for _, origin := range []string{"", "null", "https://foreign.example"} {
		response := clientCall(api, "POST", "/api/app/v1/session", login, nil, origin, "")
		if response.Code != 403 || len(response.Result().Cookies()) != 0 {
			t.Fatalf("origin %q: %d %s", origin, response.Code, response.Body.String())
		}
	}
	response := clientCall(api, "POST", "/api/app/v1/session", login, nil, fixture.settings.Web.PublicOrigin, "")
	if response.Code != 200 || len(response.Result().Cookies()) != 1 {
		t.Fatalf("login: %d %s", response.Code, response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	if cookie.Name != "__Host-moerise_session" || !cookie.Secure || !cookie.HttpOnly ||
		cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" || cookie.Path != "/" {
		t.Fatalf("cookie attributes: %v", cookie)
	}
	identity, err := api.app.Sessions.LocalIdentity(t.Context(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		origin, csrf string
	}{
		{"", identity.CSRFToken},
		{"https://foreign.example", identity.CSRFToken},
		{fixture.settings.Web.PublicOrigin, ""},
		{fixture.settings.Web.PublicOrigin, "wrong"},
	} {
		response = clientCall(api, "DELETE", "/api/app/v1/session", "", cookie, item.origin, item.csrf)
		if response.Code != 403 {
			t.Fatalf("logout CSRF: %d %s", response.Code, response.Body.String())
		}
	}
	fixture.mu.Lock()
	fixture.userStatus, fixture.patStatus, fixture.signoutStatus = 503, 503, 503
	fixture.mu.Unlock()
	response = clientCall(api, "DELETE", "/api/app/v1/session", "", cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if response.Code != 204 || response.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("offline logout: %d %s", response.Code, response.Body.String())
	}
	response = clientCall(api, "GET", "/api/app/v1/session", "", cookie, "", "")
	if response.Code != 401 || response.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("revoked cookie: %d %v", response.Code, response.Header())
	}
}

func TestClientStrictRequestsAndIdentityIsolation(t *testing.T) {
	api, fixture := clientFixture(t)
	for _, body := range []string{
		`{"username":"owner","Username":"owner","password":"correct"}`,
		`{"username":"owner","username":"owner","password":"correct"}`,
		`{"username":null,"password":"correct"}`,
		`{"username":"owner","password":"correct","token":"machine"}`,
		`{"username":"owner","password":"correct"} {}`,
	} {
		response := clientCall(api, "POST", "/api/app/v1/session", body, nil, fixture.settings.Web.PublicOrigin, "")
		if response.Code != 422 {
			t.Fatalf("login JSON: %d %s", response.Code, response.Body.String())
		}
	}
	rawID, identity, err := api.app.Sessions.Login(t.Context(), "owner", "correct")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: api.cookieName(), Value: rawID}
	for _, path := range []string{
		"/api/app/v1/notes?state=NORMAL&state=ARCHIVED",
		"/api/app/v1/notes?order=updated_desc",
		"/api/app/v1/notes?filter=creator",
		"/api/app/v1/notes?page_size=0",
		"/api/app/v1/notes?page_token=%zz",
	} {
		response := clientCall(api, "GET", path, "", cookie, "", "")
		if response.Code != 422 {
			t.Fatalf("list query %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	response := clientCall(api, "POST", "/api/app/v1/notes", `{"content":"text","tags":["hidden"]}`,
		cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if response.Code != 422 {
		t.Fatalf("native create accepted tags: %d %s", response.Code, response.Body.String())
	}
	handler, err := NewHandler(api.app)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		path  string
		token string
		code  int
	}{
		{"/api/app/v1/session", fixture.settings.AccessTokens[0].Token, 403},
		{"/api/ext/v1/notes", "", 401},
	} {
		request := httptest.NewRequest("GET", "https://notes.example.com"+item.path, nil)
		request.AddCookie(cookie)
		if item.token != "" {
			request.Header.Set("Authorization", "Bearer "+item.token)
		}
		reply := httptest.NewRecorder()
		handler.ServeHTTP(reply, request)
		if reply.Code != item.code || reply.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("identity isolation: %d %s", reply.Code, reply.Body.String())
		}
	}
}

func TestClientRenewalKeepsCookieAndLoginDeadline(t *testing.T) {
	api, fixture := clientFixture(t)
	rawID, _, err := api.app.Sessions.Login(t.Context(), "owner", "correct")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := sessionHash(rawID)
	deadline := time.Now().Add(time.Hour).Truncate(time.Second)
	if _, err := api.app.Store.state.Exec("UPDATE client_sessions SET refresh_expires_at = ? WHERE session_hash = ?", deadline.Unix(), hash); err != nil {
		t.Fatal(err)
	}
	api.app.Sessions.cache(hash, memosCredentials{})
	cookie := &http.Cookie{Name: api.cookieName(), Value: rawID}
	response := clientCall(api, "GET", "/api/app/v1/session", "", cookie, "", "")
	var identity SessionIdentity
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &identity) != nil || !identity.ExpiresAt.Equal(deadline) {
		t.Fatalf("renewal changed the login deadline: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Set-Cookie") != "" {
		t.Fatal("session renewal rewrote the browser cookie")
	}
	fixture.mu.Lock()
	refreshes := fixture.refreshes
	fixture.mu.Unlock()
	if refreshes != 2 {
		t.Fatalf("expected login and renewal refreshes, got %d", refreshes)
	}
}

func TestClientLogoutCancelsQueuedCreation(t *testing.T) {
	api, fixture := clientFixture(t)
	rawID, identity, err := api.app.Sessions.Login(t.Context(), "owner", "correct")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: api.cookieName(), Value: rawID}
	api.app.Records.gate <- struct{}{}
	defer func() { <-api.app.Records.gate }()
	started := make(chan struct{})
	request := httptest.NewRequest("POST", "https://notes.example.com/api/app/v1/notes",
		&observedBody{strings.NewReader(`{"content":"queued private note"}`), started})
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", fixture.settings.Web.PublicOrigin)
	request.Header.Set("X-CSRF-Token", identity.CSRFToken)
	request.Header.Set("Idempotency-Key", "queued-logout")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		done <- response
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("creation request did not authenticate")
	}
	response := clientCall(api, "DELETE", "/api/app/v1/session", "", cookie,
		fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if response.Code != 204 {
		t.Fatalf("logout: %d %s", response.Code, response.Body.String())
	}
	select {
	case result := <-done:
		if result.Header().Get("Set-Cookie") != "" {
			t.Fatal("canceled creation rewrote the revoked session cookie")
		}
		if result.Code == 200 || result.Code == 201 {
			t.Fatal("queued creation succeeded after logout")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("logout did not cancel creation while the write queue remained blocked")
	}
	var count int
	if err := api.app.Store.state.QueryRow("SELECT COUNT(*) FROM note_writes").Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked queued request reserved a write: %d %v", count, err)
	}
}

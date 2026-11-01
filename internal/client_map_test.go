package moerise

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientMapContractAndMachineIsolation(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	path, origin := "/api/app/v1/map", fixture.settings.Web.PublicOrigin
	assertSettingsError(t, clientCall(api, "GET", path, "", nil, "", ""), 401, "SESSION_UNAUTHORIZED")
	response := clientCall(api, "GET", path, "", cookie, "", "")
	var result MapResponse
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Status != "disabled" {
		t.Fatalf("disabled response: %d %s", response.Code, response.Body)
	}
	aiAPIValidateJSON(t, clientChatSchema(t, "MapResponse"), response.Body.Bytes())
	clientMapIndex(t, api)
	response = clientCall(api, "GET", path, "", cookie, "", "")
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Status != "ready" ||
		len(result.Points) != 3 || len(result.LayoutVersion) != 64 || result.VectorGeneration == "" || result.ProjectionRevision < 1 ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") == "" ||
		len(response.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("map contract: %d %s %v", response.Code, response.Body, response.Header())
	}
	aiAPIValidateJSON(t, clientChatSchema(t, "MapResponse"), response.Body.Bytes())
	for _, secret := range []string{"private body", fixture.settings.Memos.Token, fixture.settings.Web.StateEncryptionKey, "vector_hash", "base_url"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("map leaked private original or credential: %s", secret)
		}
	}
	assertSettingsError(t, clientCall(api, "GET", path+"?layout_version=old", "", cookie, "", ""), 422, "INVALID_ARGUMENT")
	assertSettingsError(t, clientCall(api, "HEAD", path, "", cookie, "", ""), 405, "METHOD_NOT_ALLOWED")
	assertSettingsError(t, clientCall(api, "POST", path, `{}`, cookie, origin, identity.CSRFToken), 405, "METHOD_NOT_ALLOWED")
	request := httptest.NewRequest("GET", path, nil)
	request.AddCookie(cookie)
	request.Header.Set("Authorization", "Bearer "+fixture.settings.AccessTokens[0].Token)
	response = httptest.NewRecorder()
	api.ServeHTTP(response, request)
	assertSettingsError(t, response, 403, "FORBIDDEN")
	machine, err := NewAPI(api.app)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"notes:read", "notes:write", "sync:write"} {
		machine.Settings.AccessTokens[0].Scopes = []string{scope}
		request := httptest.NewRequest("GET", "/api/ext/v1/map", nil)
		request.Header.Set("Authorization", "Bearer "+fixture.settings.AccessTokens[0].Token)
		response := httptest.NewRecorder()
		machine.ServeHTTP(response, request)
		if response.Code != 403 {
			t.Fatalf("machine map enabled by %s scope: %d", scope, response.Code)
		}
	}
}

func TestClientMapAuthenticationAndSourceShareDeadline(t *testing.T) {
	api, _ := clientFixture(t)
	cookie, _ := settingsSession(t, api)
	clientMapIndex(t, api)
	var deadlines []time.Time
	for _, client := range []*http.Client{api.app.Sessions.auth.client, api.app.Source.(*MemosClient).client,
		api.app.Notes.Source.(*MemosClient).client} {
		client.Timeout = 0
		transport := client.Transport
		client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			if !ok {
				t.Error("map authentication or source has no deadline")
			}
			deadlines = append(deadlines, deadline)
			if err := runtimePause(r.Context(), time.Millisecond); err != nil {
				return nil, err
			}
			return transport.RoundTrip(r)
		})
	}
	started := time.Now()
	response := clientCall(api, "GET", "/api/app/v1/map", "", cookie, "", "")
	if response.Code != 200 || len(deadlines) < 8 {
		t.Fatalf("map did not authenticate and recheck sources: %d %s %v", response.Code, response.Body, deadlines)
	}
	for _, deadline := range deadlines {
		if !deadline.Equal(deadlines[0]) || deadline.Before(started) || deadline.After(started.Add(mapRequestTimeout+100*time.Millisecond)) {
			t.Fatalf("authentication and map used different budgets: %v", deadlines)
		}
	}
}

func TestClientMapDeadlineCoversAuthenticationAndLocks(t *testing.T) {
	for _, stage := range []string{"authentication", "runtime_lock", "session_lock"} {
		t.Run(stage, func(t *testing.T) {
			api, _ := clientFixture(t)
			cookie, _ := settingsSession(t, api)
			unlock := func() {}
			switch stage {
			case "authentication":
				api.app.Sessions.auth.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					<-r.Context().Done()
					return nil, r.Context().Err()
				})
			case "runtime_lock":
				api.app.runtimeMu.Lock()
				unlock = api.app.runtimeMu.Unlock
			case "session_lock":
				hash, _ := sessionHash(cookie.Value)
				var err error
				unlock, err = api.app.Sessions.lock(t.Context(), hash)
				if err != nil {
					t.Fatal(err)
				}
			}
			var once sync.Once
			defer once.Do(unlock)
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
			defer cancel()
			request := httptest.NewRequest("GET", "/api/app/v1/map", nil).WithContext(ctx)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			start := time.Now()
			api.ServeHTTP(response, request)
			if time.Since(start) > 500*time.Millisecond {
				t.Fatal("map budget did not interrupt lock or authentication")
			}
			assertSettingsError(t, response, 503, "MAP_RESOURCE_LIMIT")
		})
	}
}

func TestClientMapLogoutCancelsLiveSourceRead(t *testing.T) {
	api, _ := clientFixture(t)
	cookie, _ := settingsSession(t, api)
	clientMapIndex(t, api)
	started, stopped, done := make(chan struct{}), make(chan struct{}), make(chan *httptest.ResponseRecorder, 1)
	api.app.Notes.Source = mapSource{Source: api.app.Notes.Source,
		get: func(ctx context.Context, _ string) (*Memo, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return nil, ctx.Err()
		}}
	go func() { done <- clientCall(api, "GET", "/api/app/v1/map", "", cookie, "", "") }()
	<-started
	if err := api.app.Sessions.Logout(t.Context(), cookie.Value); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("logout did not cancel source read")
	}
	response := <-done
	assertSettingsError(t, response, 503, "MAP_REQUEST_CANCELED")
	assertSettingsError(t, clientCall(api, "GET", "/api/app/v1/map", "", cookie, "", ""), 401, "SESSION_UNAUTHORIZED")
	var count int
	if err := api.app.Store.index.QueryRow("SELECT COUNT(*) FROM map_layouts").Scan(&count); err != nil || count != 0 {
		t.Fatalf("logout published private layout: %d %v", count, err)
	}
}

func TestMapNotDiscoverableOrCallableThroughMCP(t *testing.T) {
	api, _ := mcpReadFixture(t)
	for _, token := range []string{strings.Repeat("r", 40), strings.Repeat("f", 40), strings.Repeat("w", 40)} {
		reply := aiMCPCall(t, api, token, "tools/list", map[string]any{})
		data, _ := json.Marshal(reply)
		for _, name := range []string{"get_map", "read_map", "get_cognitive_map", "rebuild_map"} {
			if strings.Contains(string(data), `"`+name+`"`) {
				t.Fatalf("map discovered: %s", name)
			}
			mcpUnknownTool(t, api, token, name)
		}
	}
}

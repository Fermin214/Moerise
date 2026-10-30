package moerise

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const clientSettingsPath = "/api/app/v1/settings"

func settingsSession(t *testing.T, api *ClientAPI) (*http.Cookie, *SessionIdentity) {
	t.Helper()
	rawID, identity, err := api.app.Sessions.Login(t.Context(), "owner", "correct")
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: api.cookieName(), Value: rawID}, identity
}

func assertSettingsError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &body) != nil ||
		body.Error.Code != code || body.Error.RequestID == "" ||
		body.Error.RequestID != response.Header().Get("X-Request-ID") {
		t.Fatalf("expected %d %s, got %d %s", status, code, response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("settings error is cacheable")
	}
}

func TestClientSettingsSessionAndMachineIsolation(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	origin := fixture.settings.Web.PublicOrigin
	body := `{"version":1,"settings":{"timezone":"UTC"}}`
	for _, method := range []string{"GET", "PATCH"} {
		response := clientCall(api, method, clientSettingsPath, body, nil, origin, identity.CSRFToken)
		assertSettingsError(t, response, 401, "SESSION_UNAUTHORIZED")
		if response.Header().Get("WWW-Authenticate") != "" {
			t.Fatal("client settings returned a Bearer challenge")
		}
	}
	for _, item := range []struct {
		origin, csrf, code string
	}{
		{"", identity.CSRFToken, "ORIGIN_REJECTED"},
		{"null", identity.CSRFToken, "ORIGIN_REJECTED"},
		{"https://foreign.example", identity.CSRFToken, "ORIGIN_REJECTED"},
		{origin, "", "CSRF_REJECTED"},
		{origin, "wrong", "CSRF_REJECTED"},
	} {
		response := clientCall(api, "PATCH", clientSettingsPath, body, cookie, item.origin, item.csrf)
		assertSettingsError(t, response, 403, item.code)
	}
	handler, err := NewHandler(api.app)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		method, path, token, code string
		status                    int
	}{
		{"GET", clientSettingsPath, fixture.settings.AccessTokens[0].Token, "FORBIDDEN", 403},
		{"PATCH", clientSettingsPath, fixture.settings.AccessTokens[0].Token, "FORBIDDEN", 403},
		{"GET", "/api/ext/v1/settings", "", "UNAUTHENTICATED", 401},
		{"PATCH", "/api/ext/v1/settings", "", "UNAUTHENTICATED", 401},
		{"GET", "/api/ext/v1/settings", fixture.settings.AccessTokens[0].Token, "FORBIDDEN", 403},
		{"PATCH", "/api/ext/v1/settings", fixture.settings.AccessTokens[0].Token, "FORBIDDEN", 403},
	} {
		request := httptest.NewRequest(item.method, "https://notes.example.com"+item.path, strings.NewReader(body))
		request.AddCookie(cookie)
		request.Header.Set("Origin", origin)
		request.Header.Set("X-CSRF-Token", identity.CSRFToken)
		request.Header.Set("Content-Type", "application/json")
		if item.token != "" {
			request.Header.Set("Authorization", "Bearer "+item.token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertSettingsError(t, response, item.status, item.code)
	}
	fixture.mu.Lock()
	fixture.userName = "users/another"
	fixture.mu.Unlock()
	response := clientCall(api, "GET", clientSettingsPath, "", cookie, "", "")
	assertSettingsError(t, response, 403, "ACCOUNT_NOT_BOUND")
	if api.app.Store.productVersion != 1 {
		t.Fatal("rejected settings request changed the version")
	}
}

func TestClientSettingsViewHidesEverySecret(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, _ := settingsSession(t, api)
	product := productSettings(api.app.Store.Settings)
	for name, field := range secretFields(&product) {
		*field = new("private-settings-" + name)
	}
	api.app.Store.Settings = product.apply(api.app.Store.Settings)
	response := clientCall(api, "GET", clientSettingsPath, "", cookie, "", "")
	var view SettingsView
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil {
		t.Fatalf("settings read: %d %s", response.Code, response.Body.String())
	}
	if view.Version != 1 || view.DefaultInsightPrompt != DefaultInsightPrompt ||
		len(view.Secrets) != 7 || len(view.Settings) != 7 {
		t.Fatalf("incomplete settings view: %s", response.Body.String())
	}
	for name := range secretFields(&product) {
		if !view.Secrets[name].Configured || strings.Contains(response.Body.String(), "private-settings-"+name) {
			t.Fatalf("secret %s is exposed or missing its status", name)
		}
	}
	for _, name := range []string{`"api_key"`, `"token"`, `"state_encryption_key"`} {
		if strings.Contains(response.Body.String(), name) {
			t.Fatalf("unexpected infrastructure or credential field %s", name)
		}
	}
	for _, section := range []string{"llm", "embedding"} {
		if _, exists := view.Settings[section].(map[string]any)["api_key"]; exists {
			t.Fatal("model view includes api_key")
		}
	}
	for _, name := range []string{"app_secret", "verification_token", "encrypt_key", "webhook_url", "webhook_secret"} {
		if _, exists := view.Settings["channel"].(map[string]any)[name]; exists {
			t.Fatalf("channel view includes %s", name)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" ||
		strings.Contains(response.Body.String(), fixture.settings.Memos.Token) ||
		strings.Contains(response.Body.String(), fixture.settings.Web.StateEncryptionKey) {
		t.Fatal("settings response exposes infrastructure credentials or lacks private response headers")
	}
}

func TestClientSettingsSaveConflictAndSecretOperations(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	call := func(body string) *httptest.ResponseRecorder {
		return clientCall(api, "PATCH", clientSettingsPath, body, cookie,
			fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	}
	body := `{"version":1,"settings":{"timezone":"UTC","review":{"count":6},"insights":{"default_prompt":"custom prompt"}},"secrets":{"llm_api_key":{"action":"replace","value":"private-new-llm-key"}}}`
	response := call(body)
	var view SettingsView
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil ||
		view.Version != 2 || view.Settings["timezone"] != "UTC" ||
		!view.Secrets["llm_api_key"].Configured || strings.Contains(response.Body.String(), "private-new-llm-key") ||
		view.DefaultInsightPrompt != DefaultInsightPrompt {
		t.Fatalf("settings save: %d %s", response.Code, response.Body.String())
	}
	if api.app.Store.Settings.Review.Count != 6 || api.app.Store.Settings.Review.MinAgeHours != 72 ||
		api.app.Store.Settings.Insights.DefaultPrompt != "custom prompt" {
		t.Fatal("save did not merge product settings")
	}
	for _, method := range []string{"GET", "POST"} {
		login := `{"username":"owner","password":"correct"}`
		session := clientCall(api, method, "/api/app/v1/session", login, cookie,
			fixture.settings.Web.PublicOrigin, identity.CSRFToken)
		var result SessionIdentity
		if session.Code != 200 || json.Unmarshal(session.Body.Bytes(), &result) != nil || result.Timezone != "UTC" {
			t.Fatalf("%s session timezone: %d %s", method, session.Code, session.Body.String())
		}
	}
	assertSettingsError(t, call(body), 409, "SETTINGS_CONFLICT")
	response = call(`{"version":2,"settings":{"review":{"count":7}},"secrets":{"llm_api_key":{"action":"keep"}}}`)
	if response.Code != 200 || configured(api.app.Store.Settings.LLM.APIKey) != "private-new-llm-key" {
		t.Fatalf("keep secret: %d %s", response.Code, response.Body.String())
	}
	response = call(`{"version":3,"secrets":{"llm_api_key":{"action":"clear"}}}`)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil ||
		view.Version != 4 || view.Secrets["llm_api_key"].Configured || configured(api.app.Store.Settings.LLM.APIKey) != "" {
		t.Fatalf("clear secret: %d %s", response.Code, response.Body.String())
	}
	response = clientCall(api, "GET", clientSettingsPath, "", cookie, "", "")
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil ||
		view.Version != 4 || view.Settings["review"].(map[string]any)["count"] != float64(7) {
		t.Fatalf("saved settings read: %d %s", response.Code, response.Body.String())
	}
}

func TestClientSettingsRejectInvalidJSONAndFields(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	for _, body := range []string{
		`{}`, `[]`, `null`, `{"version":1`,
		`{"settings":{"timezone":"UTC"}}`,
		`{"version":0,"settings":{"timezone":"UTC"}}`,
		`{"version":-1,"settings":{"timezone":"UTC"}}`,
		`{"version":1.5,"settings":{"timezone":"UTC"}}`,
		`{"version":"1","settings":{"timezone":"UTC"}}`,
		`{"version":1}`, `{"version":1,"settings":{},"secrets":{}}`,
		`{"version":1,"settings":{"review":{}}}`,
		`{"version":1,"version":1,"settings":{"timezone":"UTC"}}`,
		`{"version":1,"Settings":{"timezone":"UTC"}}`,
		`{"version":1,"extra":true,"settings":{"timezone":"UTC"}}`,
		`{"version":1,"settings":{"timezone":"UTC"}} {}`,
		`{"version":1,"settings":null}`,
		`{"version":1,"settings":{"timezone":null}}`,
		`{"version":1,"settings":{"review":{"count":null}}}`,
		`{"version":1,"settings":{"notifications":{"times":[null]}}}`,
		`{"version":1,"settings":{"timezone":"UTC","timezone":"Asia/Hong_Kong"}}`,
		`{"version":1,"settings":{"review":{"count":5,"count":6}}}`,
		`{"version":1,"settings":{"review":{"count":5,"\u0063ount":6}}}`,
		`{"version":1,"settings":{"review":{"Count":5}}}`,
		`{"version":1,"settings":{"memos":{"token":"private-injected-secret"}}}`,
		`{"version":1,"settings":{"llm":{"api_key":"private-injected-secret"}}}`,
		`{"version":1,"settings":{"embedding":{"api_key":"private-injected-secret"}}}`,
		`{"version":1,"settings":{"channel":{"webhook_url":"private-injected-secret"}}}`,
		`{"version":1,"settings":{"review":{"unknown":1}}}`,
		`{"version":1,"settings":{"review":{"count":0}}}`,
		`{"version":1,"secrets":null}`,
		`{"version":1,"secrets":{"unknown":{"action":"clear"}}}`,
		`{"version":1,"secrets":{"llm_api_key":null}}`,
		`{"version":1,"secrets":{"llm_api_key":{}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"Action":"clear"}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"action":"clear","extra":true}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"action":"clear","action":"keep"}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"action":"clear"},"llm_api_key":{"action":"keep"}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"action":"replace"}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"action":"replace","value":null}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"action":"replace","value":""}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"action":"replace","value":"*****"}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"action":"clear","value":null}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"action":"keep","value":"private-injected-secret"}}}`,
		`{"version":1,"secrets":{"llm_api_key":{"action":"clear","value":"private-injected-secret"}}}`,
		"{\"version\":1,\"settings\":{\"timezone\":\"\xff\"}}",
	} {
		response := clientCall(api, "PATCH", clientSettingsPath, body, cookie,
			fixture.settings.Web.PublicOrigin, identity.CSRFToken)
		assertSettingsError(t, response, 422, "INVALID_ARGUMENT")
		if strings.Contains(response.Body.String(), "private-injected-secret") ||
			api.app.Store.productVersion != 1 || api.app.Store.Settings.Timezone != fixture.settings.Timezone {
			t.Fatal("invalid request leaked input or published partial settings")
		}
	}
	request := httptest.NewRequest("PATCH", "https://notes.example.com"+clientSettingsPath,
		strings.NewReader(`{"version":1,"settings":{"timezone":"UTC"}}`))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Origin", fixture.settings.Web.PublicOrigin)
	request.Header.Set("X-CSRF-Token", identity.CSRFToken)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	assertSettingsError(t, response, 415, "UNSUPPORTED_MEDIA_TYPE")
	response = clientCall(api, "GET", clientSettingsPath+"?version=1", "", cookie, "", "")
	assertSettingsError(t, response, 422, "INVALID_ARGUMENT")
	response = clientCall(api, "POST", clientSettingsPath, `{}`, cookie,
		fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	assertSettingsError(t, response, 405, "METHOD_NOT_ALLOWED")
}

func TestClientSettingsBodyLimit(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	for _, item := range []struct {
		body string
		size int
		code int
	}{
		{`{"version":1,"settings":{"timezone":"UTC"}}`, 128 * 1024, 200},
		{`{"version":2,"settings":{"timezone":"Asia/Tokyo"}}`, 128*1024 + 1, 422},
	} {
		body := item.body + strings.Repeat(" ", item.size-len(item.body))
		response := clientCall(api, "PATCH", clientSettingsPath, body, cookie,
			fixture.settings.Web.PublicOrigin, identity.CSRFToken)
		if item.code == 422 {
			assertSettingsError(t, response, item.code, "INVALID_ARGUMENT")
		} else if response.Code != item.code {
			t.Fatalf("settings body of %d bytes: %d %s", item.size, response.Code, response.Body.String())
		}
	}
	if api.app.Store.productVersion != 2 || api.app.Store.Settings.Timezone != "UTC" {
		t.Fatal("oversized request published settings")
	}
}

func TestClientSettingsRuntimeLockCoversAuthentication(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	hash, _ := sessionHash(cookie.Value)
	for _, method := range []string{"GET", "PATCH"} {
		api.app.Sessions.cache(hash, memosCredentials{})
		observed := make(chan bool, 1)
		fixture.mu.Lock()
		fixture.refreshHook = func() {
			unlocked := api.app.runtimeMu.TryLock()
			if unlocked {
				api.app.runtimeMu.Unlock()
			}
			observed <- unlocked
		}
		fixture.mu.Unlock()
		response := clientCall(api, method, clientSettingsPath, `{"version":1}`, cookie,
			fixture.settings.Web.PublicOrigin, identity.CSRFToken)
		expectedStatus := map[string]int{"GET": 200, "PATCH": 422}[method]
		if response.Code != expectedStatus {
			t.Fatalf("%s: %d %s", method, response.Code, response.Body.String())
		}
		select {
		case unlocked := <-observed:
			if unlocked != (method == "PATCH") {
				t.Fatalf("%s authentication runtime lock: unlocked=%v", method, unlocked)
			}
		default:
			t.Fatal("request did not refresh its session")
		}
		if !api.app.runtimeMu.TryLock() {
			t.Fatal("request did not release its runtime lock")
		}
		api.app.runtimeMu.Unlock()
	}
}

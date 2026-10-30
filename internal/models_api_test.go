package moerise

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientModelsVersionAndIsolation(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	origin := fixture.settings.Web.PublicOrigin
	for _, path := range []string{"/api/app/v1/models", "/api/app/v1/models/embedding/check", "/api/app/v1/models/llm/check"} {
		method := "GET"
		if strings.HasSuffix(path, "/check") {
			method = "POST"
		}
		response := clientCall(api, method, path, `{"version":1}`, nil, origin, identity.CSRFToken)
		assertSettingsError(t, response, 401, "SESSION_UNAUTHORIZED")
		request := httptest.NewRequest(method, path, strings.NewReader(`{"version":1}`))
		request.Header.Set("Authorization", "Bearer "+fixture.settings.AccessTokens[0].Token)
		request.Header.Set("Origin", origin)
		request.AddCookie(cookie)
		response = httptest.NewRecorder()
		api.ServeHTTP(response, request)
		assertSettingsError(t, response, 403, "FORBIDDEN")
	}
	response := clientCall(api, "GET", "/api/app/v1/models", "", cookie, "", "")
	var status ClientModels
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &status) != nil ||
		status.Version != 1 || status.Embedding.Enabled || status.LLM.Enabled || status.IndexStatus != "disabled" {
		t.Fatalf("disabled model status: %s", response.Body.String())
	}
	for _, secret := range []string{"api_key", fixture.settings.Memos.Token, fixture.settings.Web.StateEncryptionKey} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("model status leaked a credential")
		}
	}
	for _, item := range []struct {
		body, csrf, code string
		status           int
	}{
		{`{"version":1}`, "", "CSRF_REJECTED", 403},
		{`{"version":2}`, identity.CSRFToken, "SETTINGS_CONFLICT", 409},
		{`{"version":0}`, identity.CSRFToken, "INVALID_ARGUMENT", 422},
		{`{"version":1,"model":"unsaved"}`, identity.CSRFToken, "INVALID_ARGUMENT", 422},
		{`{"version":1}`, identity.CSRFToken, "EMBEDDING_DISABLED", 503},
	} {
		response := clientCall(api, "POST", "/api/app/v1/models/embedding/check", item.body, cookie, origin, item.csrf)
		assertSettingsError(t, response, item.status, item.code)
	}
	response = clientCall(api, "POST", "/api/app/v1/models/llm/check", `{"version":1}`, cookie, origin, identity.CSRFToken)
	assertSettingsError(t, response, 503, "LLM_DISABLED")
	if api.app.Store.productVersion != 1 {
		t.Fatal("connection check mutated product settings")
	}
}

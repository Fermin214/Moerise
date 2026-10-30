package moerise

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestClientModelCheckProviderTimeout(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	config := llmTestConfig(server.URL)
	config.TimeoutSeconds, config.MaxRetries = 1, 0
	api.app.Store.Settings.LLM = config
	api.app.AI.Provider = llmTestProvider(t, config)
	response := clientCall(api, "POST", "/api/app/v1/models/llm/check", `{"version":1}`,
		cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	assertSettingsError(t, response, 503, "MODEL_CHECK_TIMEOUT")
}

func TestClientModelCheckSharedDeadline(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	var deadlines []time.Time
	for _, client := range []*http.Client{api.app.Sessions.auth.client, api.app.Source.(*MemosClient).client} {
		client.Timeout = 0
		transport := client.Transport
		client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			if !ok {
				t.Error("authentication has no model check deadline")
			}
			deadlines = append(deadlines, deadline)
			if err := runtimePause(r.Context(), 10*time.Millisecond); err != nil {
				return nil, err
			}
			return transport.RoundTrip(r)
		})
	}
	api.app.AI.Provider = aiLLMFunc(func(ctx context.Context, _ []LLMMessage, _ func(string) error) (LLMResult, error) {
		deadline, _ := ctx.Deadline()
		deadlines = append(deadlines, deadline)
		return LLMResult{Content: "OK"}, nil
	})
	started := time.Now()
	response := clientCall(api, "POST", "/api/app/v1/models/llm/check", `{"version":1}`,
		cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if response.Code != 200 || len(deadlines) < 4 {
		t.Fatalf("check did not authenticate and call the model: %d %s %v", response.Code, response.Body, deadlines)
	}
	for _, deadline := range deadlines {
		if !deadline.Equal(deadlines[0]) || deadline.Before(started) ||
			deadline.After(started.Add(modelCheckTimeout+100*time.Millisecond)) {
			t.Fatalf("authentication and model used different budgets: %v", deadlines)
		}
	}
}

func TestClientModelCheckDeadlineDuringAuthenticationAndLocks(t *testing.T) {
	for _, stage := range []string{"authentication", "runtime_lock", "session_lock"} {
		t.Run(stage, func(t *testing.T) {
			api, fixture := clientFixture(t)
			cookie, identity := settingsSession(t, api)
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
			release := func() { once.Do(unlock) }
			defer release()
			timer := time.AfterFunc(time.Second, release)
			defer timer.Stop()
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			request := httptest.NewRequest("POST", "/api/app/v1/models/llm/check",
				strings.NewReader(`{"version":1}`)).WithContext(ctx)
			request.AddCookie(cookie)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", fixture.settings.Web.PublicOrigin)
			request.Header.Set("X-CSRF-Token", identity.CSRFToken)
			response := httptest.NewRecorder()
			started := time.Now()
			api.ServeHTTP(response, request)
			if time.Since(started) > 500*time.Millisecond {
				t.Fatal("deadline did not interrupt authentication or lock waiting")
			}
			assertSettingsError(t, response, 503, "MODEL_CHECK_TIMEOUT")
		})
	}
}

func TestClientModelsIgnorePreviousEmbeddingBuild(t *testing.T) {
	store, service, _, _ := indexFixture(t)
	if worked, err := service.Step(t.Context()); err != nil || !worked {
		t.Fatalf("initial build: %v %v", worked, err)
	}
	if _, err := store.index.Exec("UPDATE vector_generations SET last_error = 'EMBEDDING_AUTH' WHERE status = 'building'"); err != nil {
		t.Fatal(err)
	}
	api := NewClientAPI(&App{Store: store})
	status, err := api.modelsStatus()
	if err != nil || status.IndexStatus != "unavailable" || status.VectorIndex.Building.CompletedChunks == 0 {
		t.Fatalf("failed build: %+v %v", status, err)
	}
	model := "replacement-model"
	store.Settings.Embedding.Model = &model
	status, err = api.modelsStatus()
	if err != nil || status.IndexStatus != "waiting" || status.VectorIndex.Building.LastError == nil {
		t.Fatalf("new model reused previous build state: %+v %v", status, err)
	}
	if _, err := service.prepare(); err != nil {
		t.Fatal(err)
	}
	status, err = api.modelsStatus()
	if err != nil || status.IndexStatus != "building" || status.VectorIndex.Building.LastError != nil ||
		status.VectorIndex.Building.CompletedChunks != 0 {
		t.Fatalf("new model did not start a fresh build: %+v %v", status, err)
	}
}

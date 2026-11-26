package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientModelsVersionAndIsolation(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	origin := fixture.settings.Web.PublicOrigin
	for _, path := range []string{"/api/app/v1/models", "/api/app/v1/models/embedding/check"} {
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
		status.Version != 1 || status.Embedding.Enabled || status.IndexStatus != "disabled" {
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
	assertSettingsError(t, response, 404, "NOT_FOUND")
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
	t.Cleanup(server.Close)
	config := embeddingTestConfig(server.URL)
	config.TimeoutSeconds, config.MaxRetries = 1, 0
	api.app.Store.Settings.Embedding = config
	provider, err := NewEmbeddingProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	api.app.Search.Provider = provider
	response := clientCall(api, "POST", "/api/app/v1/models/embedding/check", `{"version":1}`,
		cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	assertSettingsError(t, response, 503, "MODEL_CHECK_TIMEOUT")
}

func TestEmbeddingCandidateRejectsCrossOriginKeepBeforeNetwork(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	api.app.Store.Settings.Embedding = embeddingTestConfig("https://previous.example")
	var hits atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer candidate-key" {
			t.Error("probe used a different key")
		}
		var input struct {
			Input struct{ Texts []string }
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		writeJSON(w, 200, embeddingFixture(len(input.Input.Texts), 1024))
	}))
	t.Cleanup(remote.Close)
	const body = `{"version":1,"settings":{"embedding":{"base_url":%q}}%s}`
	for _, secrets := range []string{``, `,"secrets":{"embedding_api_key":{"action":"keep"}}`} {
		for _, route := range []struct{ method, path string }{
			{"POST", "/api/app/v1/models/embedding/check"}, {"PATCH", "/api/app/v1/settings"},
		} {
			response := clientCall(api, route.method, route.path, fmt.Sprintf(body, remote.URL+"/api/v1", secrets),
				cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
			assertSettingsError(t, response, 422, "EMBEDDING_KEY_ORIGIN")
		}
	}
	if hits.Load() != 0 {
		t.Fatal("rejected candidate reached the server")
	}
	replacement := fmt.Sprintf(body, remote.URL+"/api/v1", `,"secrets":{"embedding_api_key":{"action":"replace","value":"candidate-key"}}`)
	response := clientCall(api, "POST", "/api/app/v1/models/embedding/check", replacement,
		cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if response.Code != 200 || hits.Load() != 2 || configured(api.app.Store.Settings.Embedding.APIKey) != "embedding-test-credential" {
		t.Fatalf("candidate probe: %d, %d requests, %s", response.Code, hits.Load(), response.Body.String())
	}
	if embeddingOriginChanged(new("https://Example.com/api/v1"), new("https://example.com:443/different/api/v1")) {
		t.Fatal("explicit default HTTPS port and path change changed the origin")
	}
}

func TestEmbeddingCandidateReleasesRuntimeDuringNetworkCall(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input struct{ Texts []string }
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
			writeJSON(w, 200, embeddingFixture(len(input.Input.Texts), 1024))
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(remote.Close)
	t.Cleanup(unblock)
	config := embeddingTestConfig(remote.URL)
	config.Enabled = false
	api.app.Store.Settings.Embedding = config
	const body = `{"version":1,"settings":{"embedding":{"enabled":true}}}`
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- clientCall(api, "POST", "/api/app/v1/models/embedding/check", body,
			cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("candidate did not reach the model provider")
	}
	response := clientCall(api, "PATCH", "/api/app/v1/settings", `{"version":1,"settings":{"review":{"count":6}}}`,
		cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if response.Code != 200 {
		t.Fatalf("PATCH during probe: %d %s", response.Code, response.Body.String())
	}
	unblock()
	select {
	case response = <-done:
		if response.Code != 200 {
			t.Fatalf("probe did not retain candidate: %s", response.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not complete after releasing upstream")
	}
	response = clientCall(api, "PATCH", "/api/app/v1/settings", body,
		cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	assertSettingsError(t, response, 409, "SETTINGS_CONFLICT")
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
	api.app.Search.Provider = modelCheckEmbedding(func(ctx context.Context, _ []string, _ EmbeddingPurpose) (EmbeddingResult, error) {
		deadline, _ := ctx.Deadline()
		deadlines = append(deadlines, deadline)
		return EmbeddingResult{}, nil
	})
	api.app.Store.Settings.Embedding.TimeoutSeconds = 60
	started := time.Now()
	warm := clientCall(api, "POST", "/api/app/v1/models/embedding/check", `{"version":1}`,
		cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if warm.Code != 200 || len(deadlines) != 2 {
		t.Fatalf("cached model check unexpectedly called auth/source: %d %s %v", warm.Code, warm.Body, deadlines)
	}
	deadlines = nil
	started = time.Now()
	api.app.Sessions.ClearCache()
	response := clientCall(api, "POST", "/api/app/v1/models/embedding/check", `{"version":1}`,
		cookie, fixture.settings.Web.PublicOrigin, identity.CSRFToken)
	if response.Code != 200 || len(deadlines) < 3 {
		t.Fatalf("uncached check did not share auth and model deadline: %d %s %v", response.Code, response.Body, deadlines)
	}
	for _, deadline := range deadlines {
		if !deadline.Equal(deadlines[0]) || deadline.Before(started) ||
			deadline.After(started.Add(modelCheckTimeout+100*time.Millisecond)) {
			t.Fatalf("authentication and model used different budgets: %v", deadlines)
		}
	}
}

type modelCheckEmbedding func(context.Context, []string, EmbeddingPurpose) (EmbeddingResult, error)

func (provider modelCheckEmbedding) Embed(ctx context.Context, texts []string, purpose EmbeddingPurpose) (EmbeddingResult, error) {
	return provider(ctx, texts, purpose)
}

func (modelCheckEmbedding) Close() {}

func TestClientModelCheckDeadlineDuringAuthenticationAndLocks(t *testing.T) {
	for _, stage := range []string{"authentication", "runtime_lock", "session_lock"} {
		t.Run(stage, func(t *testing.T) {
			api, fixture := clientFixture(t)
			cookie, identity := settingsSession(t, api)
			api.app.Search.Provider = &clientSearchEmbedding{}
			unlock := func() {}
			switch stage {
			case "authentication":
				api.app.Sessions.ClearCache()
				api.app.Sessions.auth.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					<-r.Context().Done()
					return nil, r.Context().Err()
				})
			case "runtime_lock":
				api.app.runtimeMu.Lock()
				unlock = api.app.runtimeMu.Unlock
			case "session_lock":
				api.app.Sessions.ClearCache()
				hash, _ := sessionHash(cookie.Value)
				var err error
				unlock, err = api.app.Sessions.lock(t.Context(), hash)
				if err != nil {
					t.Fatal(err)
				}
			}
			var once sync.Once
			release := func() { once.Do(unlock) }
			t.Cleanup(release)
			timer := time.AfterFunc(time.Second, release)
			defer timer.Stop()
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			request := httptest.NewRequest("POST", "/api/app/v1/models/embedding/check",
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

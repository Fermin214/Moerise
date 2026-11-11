package moerise

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func aiAPIFixture(t *testing.T, provider LLMProvider) *API {
	t.Helper()
	evidence := evidenceFixture(t, map[string]map[string]any{"memos/seed": rawMemo("seed", "fictional basil")})
	settings := evidence.Search.Store.Settings
	settings.AccessTokens = append(settings.AccessTokens,
		AccessToken{"generator", strings.Repeat("g", 40), []string{"notes:read", "ai:generate"}},
		AccessToken{"model-only", strings.Repeat("a", 40), []string{"ai:generate"}})
	api, err := NewAPI(NewApp(settings, evidence.Search.Store, evidence.Search.Source))
	if err != nil {
		t.Fatal(err)
	}
	api.LLM, api.Chat.Provider = provider, provider
	return api
}

func aiAPICall(api *API, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func TestAIAPIChatAndHistoricalDeliveryShareSources(t *testing.T) {
	evidence := evidenceFixture(t, map[string]map[string]any{"memos/seed": rawMemo("seed", "fictional basil")})
	store := evidence.Search.Store
	appID := "fictional-app"
	store.Settings.Channel.Kind, store.Settings.Channel.AppID = "feishu-app", &appID
	api, err := NewAPI(NewApp(store.Settings, store, evidence.Search.Source))
	if err != nil {
		t.Fatal(err)
	}
	if api.Feishu == nil || api.Feishu.Search != api.Search || api.Chat.Notes != api.Notes || api.LLM != nil {
		t.Fatal("chat and historical delivery do not share application sources")
	}
}

func aiAPIValidateJSON(t *testing.T, schema *jsonschema.Resolved, data []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("response violates OpenAPI: %v\n%s", err, data)
	}
}

func TestExternalAIEndpointsRejectEveryCredential(t *testing.T) {
	var calls atomic.Int32
	api := aiAPIFixture(t, aiLLMFunc(func(context.Context, []LLMMessage, func(string) error) (LLMResult, error) {
		calls.Add(1)
		return LLMResult{}, nil
	}))
	for _, token := range []string{"", "invalid", strings.Repeat("r", 40), strings.Repeat("o", 40), strings.Repeat("a", 40), strings.Repeat("g", 40)} {
		for _, path := range []string{"insights", "answers"} {
			for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
				response := aiAPICall(api, method, "/api/ext/v1/"+path, token, `{"question":"basil","stream":true}`)
				want := 403
				if token == "" || token == "invalid" {
					want = 401
				}
				if response.Code != want || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") == "" || strings.Contains(response.Body.String(), "event:") {
					t.Fatalf("external generation: %d %v %s", response.Code, response.Header(), response.Body.String())
				}
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("external generation called the provider")
	}
}

func TestOpenAPIDoesNotAdvertiseRetiredGeneration(t *testing.T) {
	var spec struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(openAPIJSON, &spec); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AIRequest", "AIResult", "AIStatement", "Evidence", "EvidenceSet", "AIMetrics", "AITimelinePeriod", "AIStreamEvent", "CreateNoteRequest", "SyncRequest"} {
		if spec.Components.Schemas[name] != nil || strings.Contains(string(openAPIJSON), "#/components/schemas/"+name+`"`) {
			t.Fatalf("retired generation schema remains public: %s", name)
		}
	}
}

func TestAIAPIMCPVisibilityAndDirectCalls(t *testing.T) {
	api := aiAPIFixture(t, aiLLMFunc(func(context.Context, []LLMMessage, func(string) error) (LLMResult, error) {
		t.Fatal("removed MCP tool called the model")
		return LLMResult{}, nil
	}))
	for _, token := range []string{strings.Repeat("r", 40), strings.Repeat("o", 40), strings.Repeat("a", 40), strings.Repeat("g", 40)} {
		tools := aiMCPCall(t, api, token, "tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any)
		want := 5
		if token == strings.Repeat("a", 40) {
			want = 0
		}
		if len(tools) != want {
			t.Fatalf("unexpected tools: %v", tools)
		}
		for _, raw := range tools {
			tool := raw.(map[string]any)
			hints := tool["annotations"].(map[string]any)
			if hints["readOnlyHint"] != true || hints["idempotentHint"] != true || hints["destructiveHint"] != false {
				t.Fatalf("incorrect tool hints: %v", tool)
			}
		}
		for _, name := range []string{"analyze_memos", "ask_memos", "get_topic_timeline"} {
			mcpUnknownTool(t, api, token, name)
		}
	}
}

func aiMCPCall(t *testing.T, api *API, token, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	request := httptest.NewRequest("POST", "/mcp", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	var rpc map[string]any
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &rpc) != nil {
		t.Fatalf("MCP response: %d %s", response.Code, response.Body.String())
	}
	return rpc
}

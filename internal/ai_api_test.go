package moerise

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func aiAPIFixture(t *testing.T, provider LLMProvider) *API {
	t.Helper()
	service := insightFixture(t, map[string]map[string]any{"memos/seed": rawMemo("seed", "fictional basil")}, provider)
	settings := service.Search.Store.Settings
	settings.AccessTokens = append(settings.AccessTokens,
		AccessToken{"generator", strings.Repeat("g", 40), []string{"notes:read", "ai:generate"}},
		AccessToken{"model-only", strings.Repeat("a", 40), []string{"ai:generate"}})
	api, err := NewAPI(NewApp(settings, service.Search.Store, service.Search.Source))
	if err != nil {
		t.Fatal(err)
	}
	if api.AI == nil || api.AI.Search != api.Search || api.AI.Provider != nil {
		t.Fatal("NewAPI did not initialize the shared AI service")
	}
	api.Search, api.AI = service.Search, service
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

func aiAPIProvider(_ context.Context, _ []LLMMessage, emit func(string) error) (LLMResult, error) {
	if err := emit(insightOutput); err != nil {
		return LLMResult{}, err
	}
	return LLMResult{Content: insightOutput, Model: "fictional-model", FinishReason: "stop"}, nil
}

func TestAIAPINewAPISharedService(t *testing.T) {
	service := insightFixture(t, map[string]map[string]any{"memos/seed": rawMemo("seed", "fictional basil")}, nil)
	store := service.Search.Store
	appID := "fictional-app"
	store.Settings.Channel.Kind, store.Settings.Channel.AppID = "feishu-app", &appID
	api, err := NewAPI(NewApp(store.Settings, store, service.Search.Source))
	if err != nil {
		t.Fatal(err)
	}
	if api.Feishu == nil || api.Feishu.AI != api.AI || api.Feishu.Search != api.Search || api.AI.Search != api.Search {
		t.Fatal("NewAPI must share the AI and search instances across entry points")
	}
	api.AI.Provider = aiLLMFunc(aiAPIProvider)
	if api.Feishu.AI.Provider == nil {
		t.Fatal("provider injection was not visible to Feishu")
	}
}

func aiAPISchema(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	var spec struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(openAPIJSON, &spec); err != nil {
		t.Fatal(err)
	}
	definitions := map[string]json.RawMessage{}
	for _, key := range []string{"AIResult", "AIStatement", "Evidence", "AITimelinePeriod",
		"SearchSemantic", "LLMUsage", "AIMetrics", "AIStreamEvent", "ErrorDetail"} {
		definitions[key] = json.RawMessage(strings.ReplaceAll(string(spec.Components.Schemas[key]),
			"#/components/schemas/", "#/$defs/"))
	}
	raw, err := json.Marshal(map[string]any{"$ref": "#/$defs/" + name, "$defs": definitions})
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
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

func TestLegacyAIRequestParser(t *testing.T) {
	for _, body := range []string{
		`{"Names":["memos/seed"]}`, `{"question":"first","question":"basil"}`,
		`{"question":null}`, `{"timeline":null}`, `{"stream":null}`, `{"tags":null}`, `{"names":null}`,
		`{"state":null}`, `{"from":null}`, `{"until":null}`, `{"names":[null]}`,
		`{"question":"basil","source":"private"}`, `{"question":"basil"} {}`,
		"{\"question\":\"\xff\"}",
	} {
		if _, err := readAIRequest(strings.NewReader(body)); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("legacy parser accepted %q: %v", body, err)
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

func TestAIAPIRequestSizeBoundary(t *testing.T) {
	base := `{"question":"basil"}`
	for _, size := range []int{aiRequestBytes, aiRequestBytes + 1} {
		body := base + strings.Repeat(" ", size-len(base))
		_, err := readAIRequest(strings.NewReader(body))
		if (err == nil) != (size == aiRequestBytes) {
			t.Fatalf("request byte boundary %d: %v", size, err)
		}
	}
	_, err := readAIRequest(io.LimitReader(strings.NewReader(`{"question":"basil"}`), 5))
	if errorCode(err) != "INVALID_ARGUMENT" {
		t.Fatalf("truncated JSON accepted: %v", err)
	}
}

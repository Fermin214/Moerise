package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

func aiAPIFixture(t *testing.T, provider LLMProvider) *API {
	t.Helper()
	service := insightFixture(t, map[string]map[string]any{"memos/seed": rawMemo("seed", "fictional basil")}, provider)
	settings := service.Search.Store.Settings
	settings.AccessTokens = append(settings.AccessTokens,
		AccessToken{"generator", strings.Repeat("g", 40), []string{"notes:read", "ai:generate"}},
		AccessToken{"model-only", strings.Repeat("a", 40), []string{"ai:generate"}})
	api, err := NewAPI(settings, service.Search.Store, service.Search.Source, NewSyncService(service.Search.Store, service.Search.Source))
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
	api, err := NewAPI(store.Settings, store, service.Search.Source, NewSyncService(store, service.Search.Source))
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

func TestAIAPIScopesAndPreflight(t *testing.T) {
	var calls atomic.Int32
	api := aiAPIFixture(t, aiLLMFunc(func(ctx context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
		calls.Add(1)
		return aiAPIProvider(ctx, messages, emit)
	}))
	for _, test := range []struct {
		name, method, path, token, body string
		status                          int
	}{
		{"anonymous", "POST", "/insights", "", `{"names":["memos/seed"]}`, 401},
		{"old-reader", "POST", "/insights", strings.Repeat("r", 40), `{"names":["memos/seed"]}`, 403},
		{"old-operator", "POST", "/answers", strings.Repeat("o", 40), `{"question":"basil"}`, 403},
		{"model-only", "POST", "/answers", strings.Repeat("a", 40), `{"question":"basil"}`, 403},
		{"method", "GET", "/insights", strings.Repeat("g", 40), `{}`, 405},
		{"unknown", "POST", "/insights", strings.Repeat("g", 40), `{"names":["memos/seed"],"source":"private"}`, 422},
		{"case-variant", "POST", "/insights", strings.Repeat("g", 40), `{"Names":["memos/seed"]}`, 422},
		{"duplicate-key", "POST", "/answers", strings.Repeat("g", 40), `{"question":"first","question":"basil"}`, 422},
		{"null", "POST", "/answers", strings.Repeat("g", 40), `{"question":null}`, 422},
		{"null-timeline", "POST", "/answers", strings.Repeat("g", 40), `{"question":"basil","timeline":null}`, 422},
		{"null-stream", "POST", "/answers", strings.Repeat("g", 40), `{"question":"basil","stream":null}`, 422},
		{"null-tags", "POST", "/answers", strings.Repeat("g", 40), `{"question":"basil","tags":null,"stream":true}`, 422},
		{"null-names", "POST", "/answers", strings.Repeat("g", 40), `{"question":"basil","names":null}`, 422},
		{"null-state", "POST", "/answers", strings.Repeat("g", 40), `{"question":"basil","state":null}`, 422},
		{"null-from", "POST", "/answers", strings.Repeat("g", 40), "{ \"question\":\"basil\",\"from\": \n null \t }", 422},
		{"null-until", "POST", "/answers", strings.Repeat("g", 40), `{"question":"basil","until":null}`, 422},
		{"null-item", "POST", "/insights", strings.Repeat("g", 40), `{"names":[null]}`, 422},
		{"extra-json", "POST", "/answers", strings.Repeat("g", 40), `{"question":"basil"} {}`, 422},
		{"invalid-utf8", "POST", "/answers", strings.Repeat("g", 40), "{\"question\":\"\xff\"}", 422},
		{"oversize", "POST", "/answers", strings.Repeat("g", 40), `{"question":"` + strings.Repeat(" ", aiRequestBytes) + `"}`, 422},
		{"query", "POST", "/answers?stream=true", strings.Repeat("g", 40), `{"question":"basil"}`, 422},
		{"empty-insight", "POST", "/insights", strings.Repeat("g", 40), `{"question":"basil","stream":true}`, 422},
		{"timeline-insight", "POST", "/insights", strings.Repeat("g", 40), `{"names":["memos/seed"],"timeline":true}`, 422},
		{"empty-question", "POST", "/answers", strings.Repeat("g", 40), `{"question":" ","stream":true}`, 422},
		{"long-question", "POST", "/answers", strings.Repeat("g", 40), `{"question":"` + strings.Repeat("a", 257) + `"}`, 422},
		{"bad-date", "POST", "/answers", strings.Repeat("g", 40), `{"question":"basil","from":"tomorrow","stream":true}`, 422},
		{"reversed-date", "POST", "/answers", strings.Repeat("g", 40), `{"question":"basil","from":"2021-01-01T00:00:00Z","until":"2020-01-01T00:00:00Z"}`, 422},
		{"missing-source", "POST", "/insights", strings.Repeat("g", 40), `{"names":["memos/missing"],"stream":true}`, 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := aiAPICall(api, test.method, "/api/ext/v1"+test.path, test.token, test.body)
			if response.Code != test.status || response.Header().Get("Content-Type") != "application/json" ||
				response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") == "" ||
				strings.Contains(response.Body.String(), "event:") {
				t.Fatalf("preflight response: %d %v %s", response.Code, response.Header(), response.Body.String())
			}
		})
	}
	api.AI.Provider = nil
	response := aiAPICall(api, "POST", "/api/ext/v1/insights", strings.Repeat("g", 40), `{"names":["memos/seed"],"stream":true}`)
	if response.Code != 503 || strings.Contains(response.Body.String(), "event:") || calls.Load() != 0 {
		t.Fatalf("disabled or invalid request called the model: calls=%d, %d %s", calls.Load(), response.Code, response.Body.String())
	}
	settings := testSettings(t)
	settings.AccessTokens[0].Scopes = []string{"notes:read", "ai:generate"}
	if err := settings.Validate(); err != nil {
		t.Fatalf("AI scope rejected: %v", err)
	}
}

func TestAIAPIJSONAndSSE(t *testing.T) {
	api := aiAPIFixture(t, aiLLMFunc(aiAPIProvider))
	resultSchema, eventSchema := aiAPISchema(t, "AIResult"), aiAPISchema(t, "AIStreamEvent")
	for _, path := range []string{"insights", "answers"} {
		t.Run(path, func(t *testing.T) {
			body := AIRequest{Question: "basil"}
			wantKind := "answer"
			if path == "insights" {
				body = AIRequest{Names: []string{"memos/seed"}}
				wantKind = "single"
			}
			raw, _ := json.Marshal(body)
			response := aiAPICall(api, "POST", "/api/ext/v1/"+path, strings.Repeat("g", 40), string(raw))
			var result AIResult
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil ||
				!result.Completed || result.Kind != wantKind || result.Status != "answered" ||
				len(result.Statements) != 3 || len(result.References) != 1 ||
				!slices.Equal(result.UsedSources, []string{"memos/seed"}) {
				t.Fatalf("JSON result: %d %s", response.Code, response.Body.String())
			}
			aiAPIValidateJSON(t, resultSchema, response.Body.Bytes())
			if result.Timeline == nil || len(result.Timeline) != 0 {
				t.Fatal("non-timeline result must have an empty timeline array")
			}
			body.Stream = true
			raw, _ = json.Marshal(body)
			stream := aiAPICall(api, "POST", "/api/ext/v1/"+path, strings.Repeat("g", 40), string(raw))
			if stream.Code != 200 || stream.Header().Get("Content-Type") != "text/event-stream" || !stream.Flushed {
				t.Fatalf("SSE headers: %d %v", stream.Code, stream.Header())
			}
			events := []AIEvent{}
			for _, frame := range strings.Split(strings.TrimSpace(stream.Body.String()), "\n\n") {
				lines := strings.SplitN(frame, "\n", 2)
				var event AIEvent
				if len(lines) != 2 || json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &event) != nil ||
					lines[0] != "event: "+event.Type {
					t.Fatalf("invalid SSE frame: %s", frame)
				}
				aiAPIValidateJSON(t, eventSchema, []byte(strings.TrimPrefix(lines[1], "data: ")))
				events = append(events, event)
			}
			types := []string{}
			for _, event := range events {
				types = append(types, event.Type)
			}
			last := events[len(events)-1]
			if strings.Join(types, ",") != "references,content,content,content,complete" ||
				last.Result == nil || !last.Result.Completed {
				t.Fatalf("SSE completion: %v", events)
			}
			result.Metrics, last.Result.Metrics = AIMetrics{}, AIMetrics{}
			if !reflect.DeepEqual(result, *last.Result) {
				t.Fatal("JSON and SSE returned different business results")
			}
		})
	}
}

func TestAIAPIEmptyReferencesCanComplete(t *testing.T) {
	api := aiAPIFixture(t, aiLLMFunc(func(context.Context, []LLMMessage, func(string) error) (LLMResult, error) {
		t.Fatal("no-evidence response must not call the model")
		return LLMResult{}, nil
	}))
	response := aiAPICall(api, "POST", "/api/ext/v1/answers", strings.Repeat("g", 40),
		`{"question":"basil","tags":["nonexistent"],"stream":true}`)
	if response.Code != 200 {
		t.Fatalf("empty evidence response: %d %s", response.Code, response.Body.String())
	}
	frames := strings.Split(strings.TrimSpace(response.Body.String()), "\n\n")
	if len(frames) != 2 || !strings.HasPrefix(frames[0], "event: references\n") ||
		!strings.HasPrefix(frames[1], "event: complete\n") {
		t.Fatalf("empty references contract: %s", response.Body.String())
	}
	eventSchema := aiAPISchema(t, "AIStreamEvent")
	for _, frame := range frames {
		_, data, _ := strings.Cut(frame, "\ndata: ")
		aiAPIValidateJSON(t, eventSchema, []byte(data))
		var event AIEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "references" && len(event.References) != 0 ||
			event.Type == "complete" && (event.Result == nil || !event.Result.Completed || event.Result.Status != "insufficient") {
			t.Fatalf("empty references result: %s", data)
		}
	}
}

type aiAPIFailingSource struct {
	Source
}

func (aiAPIFailingSource) Identity(context.Context) (string, error) {
	return "", failure("UPSTREAM_UNAVAILABLE", "private source body fictional-secret", 503, true)
}

func TestAIAPIFailuresCannotCompleteOrLeak(t *testing.T) {
	eventSchema := aiAPISchema(t, "AIStreamEvent")
	for _, mode := range []string{"model", "source-preflight", "invalid-citation", "source-changed", "panic"} {
		t.Run(mode, func(t *testing.T) {
			var api *API
			api = aiAPIFixture(t, aiLLMFunc(func(_ context.Context, _ []LLMMessage, emit func(string) error) (LLMResult, error) {
				output := strings.SplitAfter(insightOutput, "\n")[0]
				if mode == "invalid-citation" {
					output = strings.ReplaceAll(output, "E1", "E99")
				}
				if err := emit(output); err != nil {
					return LLMResult{}, err
				}
				switch mode {
				case "panic":
					panic("private model body fictional-secret")
				case "source-changed":
					return LLMResult{}, failure("SOURCE_CHANGED", "private source body fictional-secret", 409, true)
				}
				return LLMResult{}, failure("LLM_UNAVAILABLE", "private model body fictional-secret", 503, true)
			}))
			if mode == "source-preflight" {
				api.AI.Search.Source = aiAPIFailingSource{api.AI.Search.Source}
			}
			response := aiAPICall(api, "POST", "/api/ext/v1/insights", strings.Repeat("g", 40), `{"names":["memos/seed"],"stream":true}`)
			text := response.Body.String()
			if strings.Contains(text, "fictional-secret") || strings.Contains(text, "private source body") ||
				strings.Contains(text, "private model body") || strings.Contains(text, "event: complete") {
				t.Fatalf("failure leaked or completed: %s", text)
			}
			if mode == "source-preflight" {
				if response.Code != 503 || strings.Contains(text, "event:") {
					t.Fatalf("source preflight started SSE: %d %s", response.Code, text)
				}
			} else if response.Code != 200 || !strings.Contains(text, "event: error\n") ||
				!strings.Contains(text, `"completed":false`) {
				t.Fatalf("stream failure lacked error event: %d %s", response.Code, text)
			}
			if mode == "model" || mode == "source-changed" {
				if !strings.Contains(text, `"retryable":false`) {
					t.Fatal("delivered content was marked replayable")
				}
			}
			if mode != "source-preflight" {
				frames := strings.Split(strings.TrimSpace(text), "\n\n")
				for _, frame := range frames {
					_, data, _ := strings.Cut(frame, "\ndata: ")
					aiAPIValidateJSON(t, eventSchema, []byte(data))
				}
			}
		})
	}
}

type aiAPIFlushFailure struct {
	*httptest.ResponseRecorder
	flushes int
}

func (writer *aiAPIFlushFailure) FlushError() error {
	writer.flushes++
	if writer.flushes > 1 {
		return errors.New("client disconnected fictional-secret")
	}
	return nil
}

func TestAIAPIFlushFailureCancelsGeneration(t *testing.T) {
	var canceled bool
	var calls int
	api := aiAPIFixture(t, aiLLMFunc(func(ctx context.Context, _ []LLMMessage, emit func(string) error) (LLMResult, error) {
		calls++
		err := emit(strings.SplitAfter(insightOutput, "\n")[0])
		canceled = ctx.Err() != nil
		return LLMResult{}, err
	}))
	request := httptest.NewRequest("POST", "/api/ext/v1/insights", strings.NewReader(`{"names":["memos/seed"],"stream":true}`))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("g", 40))
	writer := &aiAPIFlushFailure{ResponseRecorder: httptest.NewRecorder()}
	api.ServeHTTP(writer, request)
	if !canceled || calls != 1 || writer.flushes != 2 ||
		strings.Contains(writer.Body.String(), "event: complete") || strings.Contains(writer.Body.String(), "fictional-secret") {
		t.Fatalf("flush failure was lost: canceled=%v, calls=%d, flushes=%d, body=%s", canceled, calls, writer.flushes, writer.Body.String())
	}
}

func TestAIAPIClientDisconnectCancelsGeneration(t *testing.T) {
	canceled := make(chan struct{})
	api := aiAPIFixture(t, aiLLMFunc(func(ctx context.Context, _ []LLMMessage, emit func(string) error) (LLMResult, error) {
		if err := emit(strings.SplitAfter(insightOutput, "\n")[0]); err != nil {
			return LLMResult{}, err
		}
		<-ctx.Done()
		close(canceled)
		return LLMResult{}, ctx.Err()
	}))
	server := httptest.NewServer(api)
	defer server.Close()
	request, _ := http.NewRequest("POST", server.URL+"/api/ext/v1/insights",
		strings.NewReader(`{"names":["memos/seed"],"stream":true}`))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("g", 40))
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	if _, err := response.Body.Read(buffer); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("client disconnect did not cancel model generation")
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

func TestAIAPIMCPVisibilityAndResults(t *testing.T) {
	var calls atomic.Int32
	api := aiAPIFixture(t, aiLLMFunc(func(ctx context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
		calls.Add(1)
		return aiAPIProvider(ctx, messages, emit)
	}))
	for _, test := range []struct {
		token string
		count int
	}{{strings.Repeat("r", 40), 3}, {strings.Repeat("o", 40), 3}, {strings.Repeat("a", 40), 0}, {strings.Repeat("g", 40), 6}} {
		result := aiMCPCall(t, api, test.token, "tools/list", map[string]any{})["result"].(map[string]any)
		tools := result["tools"].([]any)
		if len(tools) != test.count {
			t.Fatalf("scope expanded tool visibility: %v", tools)
		}
		for _, raw := range tools {
			tool := raw.(map[string]any)
			name := tool["name"].(string)
			annotations := tool["annotations"].(map[string]any)
			isAI := slices.Contains([]string{"analyze_memos", "ask_memos", "get_topic_timeline"}, name)
			if annotations["readOnlyHint"] != true || annotations["destructiveHint"] != false ||
				annotations["idempotentHint"] != !isAI {
				t.Fatalf("tool annotations changed: %v", tool)
			}
		}
	}
	denied := aiMCPCall(t, api, strings.Repeat("r", 40), "tools/call", map[string]any{
		"name": "analyze_memos", "arguments": map[string]any{"names": []string{"memos/seed"}}})
	result, _ := denied["result"].(map[string]any)
	if denied["error"] == nil && result["isError"] != true || calls.Load() != 0 {
		t.Fatalf("old token called model: %v", denied)
	}
	for _, test := range []struct {
		tool, path, body string
	}{
		{"analyze_memos", "insights", `{"names":["memos/seed"],"stream":true}`},
		{"ask_memos", "answers", `{"question":"basil"}`},
	} {
		reply := aiMCPCall(t, api, strings.Repeat("g", 40), "tools/call", map[string]any{
			"name": test.tool, "arguments": json.RawMessage(test.body)})["result"].(map[string]any)
		if reply["isError"] == true {
			t.Fatalf("AI tool failed: %v", reply)
		}
		raw, _ := json.Marshal(reply["structuredContent"])
		var mcpResult AIResult
		if json.Unmarshal(raw, &mcpResult) != nil || !mcpResult.Completed || len(mcpResult.UsedSources) != 1 {
			t.Fatalf("missing structured AI result: %s", raw)
		}
		var body AIRequest
		_ = json.Unmarshal([]byte(test.body), &body)
		body.Stream = false
		raw, _ = json.Marshal(body)
		response := aiAPICall(api, "POST", "/api/ext/v1/"+test.path, strings.Repeat("g", 40), string(raw))
		var rest AIResult
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &rest) != nil {
			t.Fatalf("matching REST result: %s", response.Body.String())
		}
		rest.Metrics, mcpResult.Metrics = AIMetrics{}, AIMetrics{}
		if !reflect.DeepEqual(rest, mcpResult) {
			t.Fatal("REST and MCP did not share the AI result contract")
		}
	}
	callsBeforeInvalid := calls.Load()
	for _, body := range []string{`{"question":"basil","unknown":true}`, `{"question":null}`,
		`{"question":"basil","timeline":null}`, `{"question":"basil","tags":null}`,
		`{"question":"basil","state": null }`,
		`{"question":"first","question":"basil"}`, `{"question":"basil","until":"yesterday"}`} {
		reply := aiMCPCall(t, api, strings.Repeat("g", 40), "tools/call", map[string]any{
			"name": "ask_memos", "arguments": json.RawMessage(body)})["result"].(map[string]any)
		if reply["isError"] != true {
			t.Fatalf("MCP accepted invalid AI arguments: %v", reply)
		}
	}
	if calls.Load() != callsBeforeInvalid {
		t.Fatal("invalid MCP arguments called the model")
	}
	api.AI.Provider = aiLLMFunc(func(context.Context, []LLMMessage, func(string) error) (LLMResult, error) {
		return LLMResult{}, failure("LLM_UNAVAILABLE", "private upstream fictional-secret", 503, true)
	})
	reply := aiMCPCall(t, api, strings.Repeat("g", 40), "tools/call", map[string]any{
		"name": "analyze_memos", "arguments": map[string]any{"names": []string{"memos/seed"}}})["result"].(map[string]any)
	raw, _ := json.Marshal(reply)
	if reply["isError"] != true || strings.Contains(string(raw), "fictional-secret") ||
		!strings.Contains(string(raw), "LLM_UNAVAILABLE") {
		t.Fatalf("MCP leaked or lost provider error: %s", raw)
	}
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

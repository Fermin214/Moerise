package moerise

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func externalCall(api *API, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	return response
}

func validateAPISchema(t *testing.T, schema *jsonschema.Resolved, data []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("response violates OpenAPI: %v\n%s", err, data)
	}
}

func callMCP(t *testing.T, api *API, token, method string, params any) map[string]any {
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

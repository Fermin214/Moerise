package moerise

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPAuthenticationOriginAndPrivateResponses(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	client := fakeClient(settings, func(*http.Request) (int, any, error) {
		t.Error("MCP initialization accessed private notes")
		return 500, nil, nil
	})
	api, err := NewAPI(settings, store, client, NewSyncService(store, client))
	if err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	for _, test := range []struct {
		token, origin string
		status        int
	}{
		{"", "", 401},
		{settings.AccessTokens[0].Token, "https://foreign.example", 403},
		{settings.AccessTokens[0].Token, "", 200},
	} {
		request := httptest.NewRequest("POST", "https://moerise.example/mcp", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		if test.origin != "" {
			request.Header.Set("Origin", test.origin)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("MCP private response: %d %v %s", response.Code, response.Header(), response.Body.String())
		}
	}
}

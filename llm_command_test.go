package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	moerise "moerise/internal"
)

func TestLLMCheckCommand(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected model endpoint: %s", r.URL.Path)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["stream"] != true {
			t.Error("connection check must exercise streaming")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"model":"fictional-model","choices":[{"index":0,"delta":{"content":"basil"},"finish_reason":null}]}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, `data: {"model":"fictional-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, `data: {"model":"fictional-model","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`)
		fmt.Fprintln(w)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	root := t.TempDir()
	settings := moerise.DefaultSettings()
	settings.Memos.InstanceID, settings.Memos.BaseURL, settings.Memos.PublicURL = "llm-command-test", "http://unused:5230", "http://unused:5230"
	settings.Memos.User, settings.Memos.Token = "users/owner", strings.Repeat("m", 40)
	settings.AccessTokens = []moerise.AccessToken{{Name: "reader", Token: strings.Repeat("r", 40), Scopes: []string{"notes:read"}}}
	settings.CursorSecret = strings.Repeat("c", 40)
	settings.StateDir, settings.IndexDir = filepath.Join(root, "state"), filepath.Join(root, "index")
	address, key, model := server.URL+"/v1", "fictional-model-key", "fictional-model"
	settings.LLM.Enabled, settings.LLM.BaseURL, settings.LLM.APIKey, settings.LLM.Model = true, &address, &key, &model
	path := filepath.Join(root, "config.json")
	save := func(value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save(settings)
	if err := run([]string{"check-llm", "--config", path}); err != nil || calls != 1 {
		t.Fatalf("streaming model check: calls=%d error=%v", calls, err)
	}
	for _, directory := range []string{settings.StateDir, settings.IndexDir} {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("model connection check opened product databases")
		}
	}
	// A pre-AI configuration remains disabled even when it has reserved provider fields.
	body, _ := json.Marshal(settings)
	var legacy map[string]any
	_ = json.Unmarshal(body, &legacy)
	legacy["llm"] = map[string]any{"protocol": "openai-compatible", "base_url": nil, "api_key": nil, "model": nil}
	save(legacy)
	loaded, err := moerise.LoadSettings(path)
	if err != nil || loaded.LLM.Enabled || loaded.LLM.MaxOutputTokens != 2048 {
		t.Fatalf("legacy LLM defaults: %+v %v", loaded.LLM, err)
	}
	if err := run([]string{"check-llm", "--config", path}); err == nil || !strings.Contains(err.Error(), "LLM_DISABLED") || calls != 1 {
		t.Fatalf("disabled model should not connect: %v", err)
	}
}

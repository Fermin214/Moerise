package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	moerise "moerise/internal"
)

func TestCheckEmbeddingCommand(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/services/embeddings/text-embedding/text-embedding" {
			t.Error("model check contacted Memos")
			w.WriteHeader(500)
			return
		}
		requests.Add(1)
		var request struct {
			Input struct {
				Texts []string `json:"texts"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		items := []any{}
		for i := range request.Input.Texts {
			vector := make([]float32, 1024)
			vector[0] = 1
			items = append(items, map[string]any{"text_index": i, "embedding": vector})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"output": map[string]any{"embeddings": items},
			"usage": map[string]any{"total_tokens": len(items) * 7}})
	}))
	defer server.Close()
	root := t.TempDir()
	settings := moerise.DefaultSettings()
	settings.Memos.InstanceID, settings.Memos.User = "check-instance", "users/owner"
	settings.Memos.BaseURL, settings.Memos.PublicURL, settings.Memos.Token = server.URL, server.URL, strings.Repeat("m", 32)
	settings.AccessTokens = []moerise.AccessToken{{Name: "reader", Token: strings.Repeat("r", 32), Scopes: []string{"notes:read"}}}
	settings.CursorSecret = strings.Repeat("c", 32)
	settings.StateDir, settings.IndexDir = filepath.Join(root, "state"), filepath.Join(root, "index")
	base, key := server.URL+"/api/v1", "model-test-credential"
	settings.Embedding.Enabled, settings.Embedding.BaseURL, settings.Embedding.APIKey = true, &base, &key
	config := filepath.Join(root, "config.json")
	writeConfig := func(value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil || os.WriteFile(config, body, 0600) != nil {
			t.Fatal("cannot write command configuration")
		}
	}
	invoke := func(command string) (string, error) {
		t.Helper()
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		previous := os.Stdout
		os.Stdout = write
		defer func() { os.Stdout = previous }()
		err = run([]string{command, "--config", config})
		_ = write.Close()
		output, readErr := io.ReadAll(read)
		_ = read.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(output), err
	}
	writeConfig(settings)
	output, err := invoke("check-embedding")
	var result moerise.EmbeddingCheck
	if err != nil || json.Unmarshal([]byte(output), &result) != nil || result.Status != "ok" ||
		result.Model != "qwen3.7-text-embedding" || result.Dimensions != 1024 || result.DocumentVectors != 2 ||
		result.QueryVectors != 1 || result.TotalTokens != 21 || requests.Load() != 2 {
		t.Fatalf("model check failed: %s, %v", output, err)
	}
	if strings.Contains(output, key) || strings.Contains(output, "embedding\":") || strings.Contains(output, "basil") {
		t.Fatal("model check printed credentials, texts or vectors")
	}
	for _, path := range []string{settings.StateDir, settings.IndexDir} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("model check opened product or index storage")
		}
	}
	var legacy map[string]any
	body, _ := json.Marshal(settings)
	_ = json.Unmarshal(body, &legacy)
	legacy["embedding"] = map[string]any{"protocol": "openai-compatible", "base_url": nil, "api_key": nil, "model": nil}
	writeConfig(legacy)
	if _, err := invoke("check-config"); err != nil {
		t.Fatalf("legacy disabled model config stopped basic commands: %v", err)
	}
	if _, err := invoke("check-embedding"); err == nil || !strings.Contains(err.Error(), "EMBEDDING_DISABLED") || requests.Load() != 2 {
		t.Fatalf("disabled embedding made a model request: %v", err)
	}
}

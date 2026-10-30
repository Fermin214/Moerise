//go:build integration

package acceptance

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	moerise "moerise/internal"
)

func TestClientSettingsMemosAcceptance(t *testing.T) {
	var documentCalls atomic.Int64
	embedding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input struct {
				Texts []string `json:"texts"`
			} `json:"input"`
			Parameters struct {
				Dimension int `json:"dimension"`
			} `json:"parameters"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Input.Texts) == 0 ||
			body.Parameters.Dimension < 1 || r.Header.Get("Authorization") != "Bearer embedding-settings-key" {
			http.Error(w, "invalid embedding request", 400)
			return
		}
		documentCalls.Add(1)
		vectors := make([]any, len(body.Input.Texts))
		for i := range vectors {
			vector := make([]float32, body.Parameters.Dimension)
			vector[0] = 1
			vectors[i] = map[string]any{"text_index": i, "embedding": vector}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{"embeddings": vectors},
			"usage":  map[string]any{"total_tokens": len(vectors)},
		})
	}))
	defer embedding.Close()
	h := newHarness(t)
	h.settings.Review.MinAgeHours, h.settings.Review.RecentDays = 36, 2
	c := managedClient(t, h)
	first := c.call("GET", "/api/app/v1/settings", nil, 200)
	initialReview := first["settings"].(map[string]any)["review"].(map[string]any)
	if first["version"] != float64(1) || initialReview["min_age_hours"] != float64(36) ||
		initialReview["recent_days"] != float64(2) {
		t.Fatal("upgrade initialization replaced existing custom settings")
	}
	c.call("POST", "/api/app/v1/notes", map[string]any{"content": "用于设置与向量切换的虚构笔记"}, 201)
	update := map[string]any{
		"version": 1,
		"settings": map[string]any{
			"review":        map[string]any{"count": 5, "min_age_hours": 12},
			"notifications": map[string]any{"times": []string{"08:30", "19:15"}},
			"insights":      map[string]any{"default_prompt": "讨论所选材料中的关系"},
			"channel":       map[string]any{"kind": "feishu-webhook"},
			"embedding": map[string]any{
				"enabled": true, "base_url": embedding.URL + "/api/v1", "dimensions": 256,
			},
		},
		"secrets": map[string]any{
			"embedding_api_key": map[string]any{"action": "replace", "value": "embedding-settings-key"},
			"llm_api_key":       map[string]any{"action": "replace", "value": "private-settings-llm-key"},
			"webhook_url":       map[string]any{"action": "replace", "value": "http://127.0.0.1:1/private-webhook-path"},
		},
	}
	saved := c.call("PATCH", "/api/app/v1/settings", update, 200)
	if saved["version"] != float64(2) ||
		saved["channel_capabilities"].(map[string]any)["text"] != true ||
		saved["channel_capabilities"].(map[string]any)["images"] != false {
		t.Fatal("text-only Webhook capability or settings version is incorrect")
	}
	data, _ := json.Marshal(saved)
	for _, secret := range []string{"private-settings-llm-key", "embedding-settings-key", "private-webhook-path"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("settings response leaked a secret")
		}
	}
	c.call("PATCH", "/api/app/v1/settings", update, 409)
	var generation string
	wait(t, "hot-enabled embedding generation", func() bool {
		index, status, err := h.request(h.base, "/api/ext/v1/search/index", "GET", h.reader, nil)
		if err != nil || status != 200 || index["ready"] != true || documentCalls.Load() == 0 {
			return false
		}
		generation = index["active"].(map[string]any)["id"].(string)
		return true
	})
	// LLM 变更保留已发布的向量代次。
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"settings-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
	}))
	defer model.Close()
	c.call("PATCH", "/api/app/v1/settings", map[string]any{
		"version": 2, "settings": map[string]any{"llm": map[string]any{
			"enabled": true, "model": "settings-model", "base_url": model.URL + "/v1",
		}},
	}, 200)
	index := h.call(h.base, "/api/ext/v1/search/index", "GET", h.reader, nil, 200)
	if index["ready"] != true || index["active"].(map[string]any)["id"] != generation {
		t.Fatal("LLM update rebuilt the embedding generation")
	}
	h.call(h.base, "/api/app/v1/settings", "GET", h.operator, nil, 403)
	h.call(h.base, "/api/ext/v1/settings", "PATCH", h.operator, update, 403)
	c.call("PATCH", "/api/app/v1/settings", map[string]any{
		"version": 3, "settings": map[string]any{"embedding": map[string]any{"dimensions": 512}},
	}, 200)
	wait(t, "new embedding space generation", func() bool {
		index, status, err := h.request(h.base, "/api/ext/v1/search/index", "GET", h.reader, nil)
		if err != nil || status != 200 || index["ready"] != true {
			return false
		}
		active := index["active"].(map[string]any)
		return active["id"] != generation && active["dimensions"] == float64(512)
	})
	h.stop()
	stored, err := moerise.LoadSettings(h.config)
	if err != nil {
		t.Fatal(err)
	}
	app, err := moerise.OpenApp(stored)
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.AI.Provider.Generate(t.Context(), []moerise.LLMMessage{{Role: "user", Content: "虚构设置检查"}}, nil)
	app.Close()
	if err != nil || result.Content != "OK" {
		t.Fatalf("saved LLM provider was not usable: %v", err)
	}
	// 以旧部署值重启，再重建可丢弃索引。
	h.settings.Review.Count = 1
	h.settings.Timezone = "invalid/deployment-timezone"
	stale, _ := json.Marshal(h.settings)
	if err := os.WriteFile(h.config, stale, 0600); err != nil {
		t.Fatal(err)
	}
	h.command(true, "check-config")
	h.start()
	restarted := c.call("GET", "/api/app/v1/settings", nil, 200)
	if restarted["version"] != float64(4) ||
		restarted["settings"].(map[string]any)["review"].(map[string]any)["count"] != float64(5) {
		t.Fatal("restart or deployment configuration replaced saved settings")
	}
	h.stop()
	h.command(true, "rebuild-index")
	h.start()
	rebuilt := c.call("GET", "/api/app/v1/settings", nil, 200)
	if rebuilt["version"] != float64(4) || !rebuilt["secrets"].(map[string]any)["llm_api_key"].(map[string]any)["configured"].(bool) {
		t.Fatal("index rebuild changed product settings or credentials")
	}
	state := h.database("state")
	defer state.Close()
	var ciphertext []byte
	if err := state.QueryRow("SELECT ciphertext FROM product_settings").Scan(&ciphertext); err != nil ||
		strings.Contains(string(ciphertext), "private-settings-llm-key") {
		t.Fatalf("product state is not encrypted: %v", err)
	}
	c.call("DELETE", "/api/app/v1/session", nil, 204)
	c.call("GET", "/api/app/v1/settings", nil, 401)
}

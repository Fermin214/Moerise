//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	moerise "moerise/internal"
)

func (h *harness) vectorStatus() *moerise.VectorIndexStatus {
	h.t.Helper()
	response, code, err := h.request(h.base, "/api/ext/v1/sync", "GET", h.operator, nil)
	if err != nil || code != 200 {
		return nil
	}
	data, _ := json.Marshal(response["vector_index"])
	var status *moerise.VectorIndexStatus
	if err := json.Unmarshal(data, &status); err != nil {
		h.t.Fatal(err)
	}
	return status
}

func (h *harness) vectorsReady() *moerise.VectorIndexStatus {
	h.t.Helper()
	var status *moerise.VectorIndexStatus
	wait(h.t, "vector index publication", func() bool {
		status = h.vectorStatus()
		return status != nil && status.Ready && status.Active != nil && status.Building == nil
	})
	return status
}

func TestVectorIndexAcceptance(t *testing.T) {
	var mu sync.Mutex
	remaining := 1
	successes := map[string]int{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input struct {
				Texts []string `json:"texts"`
			} `json:"input"`
			Parameters struct {
				Dimensions int    `json:"dimension"`
				Purpose    string `json:"text_type"`
			} `json:"parameters"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Input.Texts) == 0 ||
			len(request.Input.Texts) > 2 || request.Parameters.Purpose != "document" {
			t.Error("invalid document batch")
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if remaining == 0 {
			w.WriteHeader(503)
			return
		}
		if remaining > 0 {
			remaining--
		}
		items := make([]any, len(request.Input.Texts))
		for i, text := range request.Input.Texts {
			key := fmt.Sprintf("%d:%s", request.Parameters.Dimensions, text)
			successes[key]++
			vector := make([]float32, request.Parameters.Dimensions)
			vector[0] = float32(len(text))
			items[len(items)-i-1] = map[string]any{"text_index": i, "embedding": vector}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": map[string]any{"embeddings": items}, "usage": map[string]int{"total_tokens": len(items)}})
	}))
	defer model.Close()
	h := newHarness(t)
	names := []string{}
	bodies := []string{"fictional balcony herbs", "fictional bicycle brakes", "fictional reading list", "fictional archived note",
		strings.Repeat("\u865a", 1100) + "\n\n" + strings.Repeat("\u6784", 1100)}
	for i, body := range bodies {
		note := h.record(fmt.Sprintf("vector-%d", i), h.operator, map[string]string{"content": body}, 201)
		names = append(names, note["name"].(string))
	}
	h.sync("full")
	h.stop()
	config := h.settings.Embedding
	config.Enabled, config.Dimensions, config.BatchSize, config.MaxRetries = true, 256, 2, 0
	address, key := model.URL+"/api/v1", "acceptance-model-credential"
	config.BaseURL, config.APIKey = &address, &key
	h.settings.Embedding, h.settings.Sync.RetrySeconds = config, 1
	saveConfig := func() {
		t.Helper()
		data, _ := json.Marshal(h.settings)
		if err := os.WriteFile(h.config, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	saveConfig()
	h.start()
	h.ready()
	var partial *moerise.VectorIndexStatus
	wait(t, "persisted model failure after first batch", func() bool {
		partial = h.vectorStatus()
		return partial != nil && partial.Building != nil && partial.Building.CompletedChunks == 2 &&
			partial.Building.LastError != nil && *partial.Building.LastError == "EMBEDDING_UNAVAILABLE"
	})
	if partial.Active != nil || partial.Ready || partial.Building.Chunks != 6 {
		t.Fatalf("partial index became active: %+v", partial)
	}
	h.stop()
	mu.Lock()
	remaining = -1
	mu.Unlock()
	h.start()
	h.ready()
	active := h.vectorsReady()
	if active.Active.ID != partial.Building.ID || active.Active.Notes != 5 || active.Active.Chunks != 6 {
		t.Fatalf("restart did not resume the generation: %+v", active)
	}
	mu.Lock()
	for _, count := range successes {
		if count != 1 {
			t.Error("restart repeated a committed model text")
		}
	}
	initialCount := len(successes)
	mu.Unlock()
	t.Log("PASS automatic chunk indexing, persisted batches, model failure, and process restart")

	h.call(h.memos, "/api/v1/"+names[3]+"?updateMask=state", "PATCH", h.upstream, map[string]string{"state": "ARCHIVED"}, 200)
	h.sync("incremental")
	active = h.vectorsReady()
	mu.Lock()
	if len(successes) != initialCount {
		t.Error("archive recomputed body vectors")
	}
	mu.Unlock()
	edited := strings.Repeat("\u865a", 1100) + "\n\n" + strings.Repeat("\u65b0", 1100)
	h.call(h.memos, "/api/v1/"+names[4]+"?updateMask=content", "PATCH", h.upstream, map[string]string{"content": edited}, 200)
	h.sync("incremental")
	active = h.vectorsReady()
	mu.Lock()
	if len(successes) != initialCount+1 {
		t.Errorf("body edit did not reuse the unchanged paragraph: %d", len(successes))
	}
	mu.Unlock()
	h.call(h.memos, "/api/v1/"+names[2], "DELETE", h.upstream, nil, 200)
	h.sync("full")
	active = h.vectorsReady()
	if active.Active.Notes != 4 || active.Active.Chunks != 5 {
		t.Fatal("deleted source remains in the published vector index")
	}
	t.Log("PASS metadata reuse, paragraph-level incremental update, and source deletion")

	oldID := active.Active.ID
	h.stop()
	h.settings.Embedding.Dimensions = 512
	mu.Lock()
	remaining = 0
	mu.Unlock()
	saveConfig()
	h.start()
	h.ready()
	wait(t, "replacement model failure", func() bool {
		status := h.vectorStatus()
		return status != nil && status.Building != nil && status.Building.LastError != nil
	})
	replacement := h.vectorStatus()
	if replacement.Ready || replacement.Active.ID != oldID || replacement.Active.Dimensions != 256 ||
		replacement.Building.Dimensions != 512 || replacement.Building.CompletedChunks != 0 {
		t.Fatal("space replacement discarded or exposed incompatible vectors")
	}
	mu.Lock()
	remaining = -1
	mu.Unlock()
	active = h.vectorsReady()
	if active.Active.Dimensions != 512 || active.Active.ID == oldID {
		t.Fatal("replacement did not switch to the complete new space")
	}
	t.Log("PASS old index retention on failure and atomic vector-space replacement")

	snapshot := filepath.Join(h.root, "vector-state-snapshot")
	h.command(true, "backup", "--output", snapshot)
	for _, operation := range []string{"rebuild-index", "restore"} {
		h.stop()
		if operation == "restore" {
			h.command(true, operation, "--source", snapshot, "--safety-backup", filepath.Join(h.root, "vector-before-restore"))
		} else {
			h.command(true, operation)
		}
		h.start()
		h.ready()
		rebuilt := h.vectorsReady()
		if rebuilt.Active.ID == active.Active.ID || rebuilt.Active.Notes != 4 {
			t.Fatal("derived index was not rebuilt from Memos")
		}
		note := h.record("vector-0", h.operator, map[string]string{"content": bodies[0]}, 200)
		if note["name"] != names[0] {
			t.Fatal("vector rebuild or restore lost product idempotency")
		}
		active = rebuilt
	}
	t.Log("PASS index rebuild and backup restore retain product state")
}

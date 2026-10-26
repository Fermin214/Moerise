package moerise

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestEmbeddingProtocolAndValidation(t *testing.T) {
	for _, test := range []struct {
		name, response, code string
	}{
		{"reordered", `{"data":[{"index":1,"embedding":[0,3]},{"index":0,"embedding":[4,0]}]}`, ""},
		{"duplicate", `{"data":[{"index":0,"embedding":[1,0]},{"index":0,"embedding":[0,1]}]}`, "EMBEDDING_PROTOCOL"},
		{"missing_index", `{"data":[{"embedding":[1,0]},{"index":1,"embedding":[0,1]}]}`, "EMBEDDING_PROTOCOL"},
		{"missing_result", `{"data":[{"index":0,"embedding":[1,0]}]}`, "EMBEDDING_PROTOCOL"},
		{"dimension", `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[0,1]}]}`, "EMBEDDING_PROTOCOL"},
		{"zero", `{"data":[{"index":0,"embedding":[0,0]},{"index":1,"embedding":[0,1]}]}`, "EMBEDDING_PROTOCOL"},
		{"overflow", `{"data":[{"index":0,"embedding":[1e100,0]},{"index":1,"embedding":[0,1]}]}`, "EMBEDDING_PROTOCOL"},
		{"trailing_json", `{"data":[{"index":0,"embedding":[1,0]},{"index":1,"embedding":[0,1]}]} {}`, "EMBEDDING_PROTOCOL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer embedding-secret" {
					t.Error("embedding endpoint or credential was not independent")
				}
				var body struct {
					Model      string   `json:"model"`
					Input      []string `json:"input"`
					Dimensions int      `json:"dimensions"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "vector-model" || body.Dimensions != 2 || !slices.Equal(body.Input, []string{"甲", "乙"}) {
					t.Error("invalid embedding request")
				}
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()
			config := DefaultSettings().Embedding
			config.BaseURL, config.APIKey, config.Model, config.Dimensions = stringPointer(server.URL+"/v1"), stringPointer("embedding-secret"), stringPointer("vector-model"), 2
			vectors, err := NewEmbeddingClient(config).Embed(context.Background(), []string{"甲", "乙"})
			if test.code != "" {
				if errorCode(err) != test.code {
					t.Fatalf("expected %s, got %v", test.code, err)
				}
			} else if err != nil || !slices.Equal(vectors[0], []float32{1, 0}) || !slices.Equal(vectors[1], []float32{0, 1}) {
				t.Fatalf("response mapping/normalization: %v %v", vectors, err)
			}
		})
	}
	for _, vector := range [][]float32{{float32(math.NaN())}, {float32(math.Inf(1))}, {0}, {}} {
		if normalizeVector(vector, 1) == nil {
			t.Fatal("untrusted vector accepted")
		}
	}
}

func TestEmbeddingRedirectDoesNotForwardCredentials(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("embedding redirect followed") }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer origin.Close()
	c := DefaultSettings().Embedding
	c.BaseURL, c.Model, c.APIKey = stringPointer(origin.URL), stringPointer("model"), stringPointer("secret")
	_, err := NewEmbeddingClient(c).Embed(context.Background(), []string{"private"})
	if errorCode(err) != "EMBEDDING_UNAVAILABLE" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect: %v", err)
	}
}

func TestChunkMemoPreservesUnicodePositions(t *testing.T) {
	for _, content := range []string{"短笔记🧠\n第二行", strings.Repeat("中文🧠段落。", 90) + "\n\n" + strings.Repeat("第二段落🙂", 400), strings.Repeat("x", 2501), "  \n\n  "} {
		chunks := chunkMemo(content)
		runes, last := []rune(content), 0
		for _, chunk := range chunks {
			if chunk.Start < last || chunk.End-chunk.Start > 1000 || chunk.Text != string(runes[chunk.Start:chunk.End]) {
				t.Fatalf("invalid chunk: %+v", chunk)
			}
			last = chunk.End
		}
		if strings.TrimSpace(content) != "" && last != len(runes) {
			t.Fatal("chunking dropped source text")
		}
		if len(runes) <= 1000 && strings.TrimSpace(content) != "" && len(chunks) != 1 {
			t.Fatal("short memo was split")
		}
	}
}

func TestEmbeddingConfiguration(t *testing.T) {
	for _, mutate := range []func(*EmbeddingConfig){
		func(c *EmbeddingConfig) { c.Protocol = "unknown" },
		func(c *EmbeddingConfig) { c.Dimensions = -1 },
		func(c *EmbeddingConfig) { c.Dimensions = 4097 },
		func(c *EmbeddingConfig) { c.BatchSize = 0 },
		func(c *EmbeddingConfig) { c.TimeoutSeconds = 91 },
		func(c *EmbeddingConfig) { c.MaxChunks = 0 },
		func(c *EmbeddingConfig) { c.Model = stringPointer("m") },
		func(c *EmbeddingConfig) { c.BaseURL = stringPointer("https://model.example/v1") },
		func(c *EmbeddingConfig) {
			c.Model, c.BaseURL = stringPointer("m"), stringPointer("https://user:secret@model.example/v1")
		},
		func(c *EmbeddingConfig) {
			c.Model, c.BaseURL = stringPointer("m"), stringPointer("https://model.example/v1?q=1")
		},
	} {
		c := DefaultSettings().Embedding
		mutate(&c)
		if c.validate() == nil {
			t.Fatalf("accepted invalid embedding config: %+v", c)
		}
	}
}

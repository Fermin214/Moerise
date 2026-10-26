package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func embeddingTestConfig(address string) EmbeddingConfig {
	config := DefaultSettings().Embedding
	config.Enabled = true
	key, base := "embedding-test-credential", address+"/api/v1"
	config.APIKey, config.BaseURL = &key, &base
	return config
}

func embeddingFixture(count, dimensions int) map[string]any {
	items := make([]any, count)
	for i := range count {
		vector := make([]float64, dimensions)
		vector[0] = float64(i + 1)
		items[count-i-1] = map[string]any{"text_index": i, "embedding": vector}
	}
	return map[string]any{"output": map[string]any{"embeddings": items}, "usage": map[string]any{"total_tokens": count * 7}}
}

func TestBailianEmbeddingFlow(t *testing.T) {
	var batches [][]string
	var purposes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/services/embeddings/text-embedding/text-embedding" ||
			r.Header.Get("Authorization") != "Bearer embedding-test-credential" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("invalid embedding request")
		}
		var request struct {
			Model string `json:"model"`
			Input struct {
				Texts []string `json:"texts"`
			} `json:"input"`
			Parameters struct {
				Dimension int    `json:"dimension"`
				Output    string `json:"output_type"`
				Purpose   string `json:"text_type"`
				Instruct  string `json:"instruct"`
			} `json:"parameters"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "qwen3.7-text-embedding" ||
			request.Parameters.Dimension != 1024 || request.Parameters.Output != "dense" {
			t.Error("invalid embedding parameters")
		}
		if request.Parameters.Purpose == "query" && request.Parameters.Instruct != "Retrieve relevant personal notes" ||
			request.Parameters.Purpose == "document" && request.Parameters.Instruct != "" {
			t.Error("instruction did not follow the query purpose")
		}
		batches = append(batches, request.Input.Texts)
		purposes = append(purposes, request.Parameters.Purpose)
		fixture := embeddingFixture(len(request.Input.Texts), 1024)
		for _, raw := range fixture["output"].(map[string]any)["embeddings"].([]any) {
			item := raw.(map[string]any)
			item["embedding"].([]float64)[0] = float64(len(request.Input.Texts[item["text_index"].(int)]))
		}
		_ = json.NewEncoder(w).Encode(fixture)
	}))
	defer server.Close()
	config := embeddingTestConfig(server.URL)
	instruction := "Retrieve relevant personal notes"
	config.QueryInstruction, config.BatchSize = &instruction, 2
	provider, err := NewEmbeddingProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	texts := []string{"  original whitespace  ", "\u4eca\u5929\u79cd\u4e86\u7f57\u52d2", "third document"}
	result, err := provider.Embed(context.Background(), texts, EmbeddingDocument)
	if err != nil || len(result.Vectors) != 3 || result.TotalTokens != 21 {
		t.Fatalf("embedding result: %+v, %v", result, err)
	}
	for i, text := range texts {
		if len(result.Vectors[i]) != 1024 || result.Vectors[i][0] != float32(len(text)) {
			t.Fatal("vectors did not retain input positions")
		}
	}
	query, err := provider.Embed(context.Background(), []string{"herbs"}, EmbeddingQuery)
	if err != nil || query.TotalTokens != 7 || query.Vectors[0][0] != 5 || len(batches) != 3 ||
		!slices.Equal(batches[0], texts[:2]) || !slices.Equal(batches[1], texts[2:]) ||
		!slices.Equal(purposes, []string{"document", "document", "query"}) {
		t.Fatalf("batch or query mismatch: %v", err)
	}
}

func TestEmbeddingRejectsBrokenResponses(t *testing.T) {
	for _, name := range []string{"count", "dimension", "missing-index", "duplicate-index", "out-of-range", "null-number",
		"string-number", "zero-vector", "float32-overflow", "float32-underflow", "missing-usage", "negative-usage", "trailing-json", "error-body"} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				fixture := embeddingFixture(2, 1024)
				items := fixture["output"].(map[string]any)["embeddings"].([]any)
				first := items[0].(map[string]any)
				switch name {
				case "count":
					fixture["output"].(map[string]any)["embeddings"] = items[:1]
				case "dimension":
					first["embedding"] = []float64{1}
				case "missing-index":
					delete(first, "text_index")
				case "duplicate-index":
					items[1].(map[string]any)["text_index"] = first["text_index"]
				case "out-of-range":
					first["text_index"] = 2
				case "null-number", "string-number":
					vector := make([]any, 1024)
					for i := range vector {
						vector[i] = float64(1)
					}
					if name == "null-number" {
						vector[0] = nil
					} else {
						vector[0] = "1"
					}
					first["embedding"] = vector
				case "zero-vector":
					first["embedding"] = make([]float64, 1024)
				case "float32-overflow":
					first["embedding"].([]float64)[0] = 1e100
				case "float32-underflow":
					first["embedding"].([]float64)[0] = 1e-100
				case "missing-usage":
					delete(fixture, "usage")
				case "negative-usage":
					fixture["usage"].(map[string]any)["total_tokens"] = -1
				case "error-body":
					fixture["code"] = "InvalidParameter"
				}
				_ = json.NewEncoder(w).Encode(fixture)
				if name == "trailing-json" {
					_, _ = io.WriteString(w, "{}")
				}
			}))
			defer server.Close()
			provider, err := NewEmbeddingProvider(embeddingTestConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			result, err := provider.Embed(context.Background(), []string{"one", "two"}, EmbeddingDocument)
			if errorCode(err) != "EMBEDDING_PROTOCOL" || result.Vectors != nil || calls.Load() != 1 {
				t.Fatalf("untrusted response returned or retried: %v", err)
			}
		})
	}
}

func TestEmbeddingFailureRecovery(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 || status < 500 {
					w.WriteHeader(status)
					_, _ = io.WriteString(w, "embedding-test-credential and private input")
					return
				}
				_ = json.NewEncoder(w).Encode(embeddingFixture(1, 1024))
			}))
			defer server.Close()
			config := embeddingTestConfig(server.URL)
			config.MaxRetries = 1
			provider, err := NewEmbeddingProvider(config)
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			_, err = provider.Embed(context.Background(), []string{"private input"}, EmbeddingDocument)
			if status >= 500 {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("transient failure did not recover: %v", err)
				}
			} else if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "embedding-test-credential") ||
				strings.Contains(err.Error(), "private input") {
				t.Fatalf("permanent failure retried or leaked content: %v", err)
			}
		})
	}

	t.Run("rate-limit-cancellation", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()
		provider, err := NewEmbeddingProvider(embeddingTestConfig(server.URL))
		if err != nil {
			t.Fatal(err)
		}
		defer provider.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err = provider.Embed(ctx, []string{"one"}, EmbeddingQuery)
		if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
			t.Fatalf("rate limit did not respect cancellation: %v", err)
		}
	})

	t.Run("request-timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
		}))
		defer server.Close()
		config := embeddingTestConfig(server.URL)
		config.TimeoutSeconds = 1
		provider, err := NewEmbeddingProvider(config)
		if err != nil {
			t.Fatal(err)
		}
		defer provider.Close()
		_, err = provider.Embed(context.Background(), []string{"one"}, EmbeddingDocument)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("configured timeout was not enforced: %v", err)
		}
	})

	t.Run("retry-limit", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()
		config := embeddingTestConfig(server.URL)
		config.MaxRetries = 1
		provider, err := NewEmbeddingProvider(config)
		if err != nil {
			t.Fatal(err)
		}
		defer provider.Close()
		_, err = provider.Embed(context.Background(), []string{"one"}, EmbeddingQuery)
		if errorCode(err) != "EMBEDDING_UNAVAILABLE" || calls.Load() != 2 {
			t.Fatalf("retry limit was not enforced: %v", err)
		}
	})

	t.Run("redirect", func(t *testing.T) {
		var redirected atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1) }))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		provider, err := NewEmbeddingProvider(embeddingTestConfig(server.URL))
		if err != nil {
			t.Fatal(err)
		}
		defer provider.Close()
		_, err = provider.Embed(context.Background(), []string{"one"}, EmbeddingDocument)
		if errorCode(err) != "EMBEDDING_REJECTED" || redirected.Load() != 0 {
			t.Fatalf("redirect forwarded embedding data: %v", err)
		}
	})
}

func TestEmbeddingConcurrencyAndPreflight(t *testing.T) {
	var active, peak, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(embeddingFixture(1, 1024))
	}))
	defer server.Close()
	config := embeddingTestConfig(server.URL)
	config.BatchSize, config.MaxConcurrency = 1, 2
	provider, err := NewEmbeddingProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	for _, texts := range [][]string{nil, {"one", "two", " "}, {strings.Repeat("x", 65537)}, {string([]byte{0xff})}, make([]string, 1001)} {
		if _, err := provider.Embed(context.Background(), texts, EmbeddingDocument); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("invalid input accepted: %v", err)
		}
	}
	if _, err := provider.Embed(context.Background(), []string{"one"}, "unsupported"); errorCode(err) != "INVALID_ARGUMENT" || calls.Load() != 0 {
		t.Fatal("preflight validation sent a partial request")
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if _, err := provider.Embed(context.Background(), []string{"one"}, EmbeddingDocument); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if calls.Load() != 8 || peak.Load() > 2 {
		t.Fatalf("model concurrency exceeded: peak=%d, requests=%d", peak.Load(), calls.Load())
	}
}

func TestEmbeddingConfiguration(t *testing.T) {
	for _, field := range []string{"provider", "protocol", "base_url", "api_key", "model", "dimensions", "batch_size", "timeout_seconds", "max_concurrency", "max_retries"} {
		t.Run(field, func(t *testing.T) {
			settings := testSettings(t)
			settings.Embedding = embeddingTestConfig("https://workspace.cn-beijing.maas.aliyuncs.com")
			switch field {
			case "provider":
				settings.Embedding.Provider = "unsupported"
			case "protocol":
				settings.Embedding.Protocol = "openai-compatible"
			case "base_url":
				address := "https://workspace.cn-beijing.maas.aliyuncs.com/api/v1?query=1"
				settings.Embedding.BaseURL = &address
			case "api_key":
				settings.Embedding.APIKey = nil
			case "model":
				settings.Embedding.Model = nil
			case "dimensions":
				settings.Embedding.Dimensions = 1537
			case "batch_size":
				settings.Embedding.BatchSize = 21
			case "timeout_seconds":
				settings.Embedding.TimeoutSeconds = 0
			case "max_concurrency":
				settings.Embedding.MaxConcurrency = 0
			case "max_retries":
				settings.Embedding.MaxRetries = -1
			}
			if err := settings.Validate(); err == nil || !strings.Contains(err.Error(), "embedding."+field) {
				t.Fatalf("invalid enabled embedding configuration accepted: %v", err)
			}
		})
	}
}

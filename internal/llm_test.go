package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func llmTestConfig(address string) LLMConfig {
	key, model, base := "llm-test-credential", "route/test-model", address+"/v1/"
	return LLMConfig{Enabled: true, Protocol: "openai-compatible", BaseURL: &base, APIKey: &key, Model: &model,
		TimeoutSeconds: 90, MaxConcurrency: 2, MaxRetries: 2, MaxOutputTokens: 2048, ContextBytes: 24000}
}

func llmTestProvider(t *testing.T, config LLMConfig) LLMProvider {
	t.Helper()
	provider, err := NewLLMProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	return provider
}

var llmTestMessages = []LLMMessage{{Role: "user", Content: "Fictional input for the protocol test."}}

const llmTestJSON = `{"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK","refusal":null},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8,"prompt_tokens_details":{"cached_tokens":0}}}`
const llmTestChunk = `{"model":"test-model","choices":[{"index":0,"delta":{"role":null,"content":"OK","refusal":null,"function_call":null},"finish_reason":""}]}`
const llmTestStop = `{"model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8,"prompt_tokens_details":{"cached_tokens":0}}}`
const llmTestStream = "data: " + llmTestChunk + "\n\ndata: " + llmTestStop + "\n\ndata: [DONE]\n\n"

func writeLLMJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, llmTestJSON)
}

func TestLLMGenerateAndStreamingCheck(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			delivered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" ||
					r.Header.Get("Authorization") != "Bearer llm-test-credential" ||
					r.Header.Get("Content-Type") != "application/json" {
					t.Error("invalid chat request")
				}
				var request struct {
					Model         string       `json:"model"`
					Messages      []LLMMessage `json:"messages"`
					OutputTokens  int          `json:"max_completion_tokens"`
					N             int          `json:"n"`
					Stream        bool         `json:"stream"`
					StreamOptions *struct {
						IncludeUsage bool `json:"include_usage"`
					} `json:"stream_options"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "route/test-model" ||
					request.OutputTokens != 2048 || request.N != 1 || len(request.Messages) != 1 ||
					request.Messages[0] != llmTestMessages[0] || request.Stream != stream {
					t.Error("invalid chat parameters")
				}
				if !stream {
					if request.StreamOptions != nil {
						t.Error("non-streaming request has streaming options")
					}
					writeLLMJSON(w)
					return
				}
				if request.StreamOptions == nil || !request.StreamOptions.IncludeUsage {
					t.Error("stream usage was not requested")
				}
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				_, _ = io.WriteString(w, ": keepalive\r\ndata: {\"model\":\"test-model\",\r\ndata: \"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\r\n\r\n")
				_, _ = io.WriteString(w, "data: "+llmTestChunk+"\r\n\r\n")
				w.(http.Flusher).Flush()
				select {
				case <-delivered:
				case <-time.After(2 * time.Second):
					t.Error("content was buffered until the stream finished")
				}
				// Usage may share the final stop frame; [DONE] may end at EOF.
				_, _ = io.WriteString(w, "data: "+llmTestStop+"\r\n\r\ndata: [DONE]")
			}))
			defer server.Close()
			provider := llmTestProvider(t, llmTestConfig(server.URL))
			var callback func(string) error
			var text strings.Builder
			if stream {
				callback = func(delta string) error {
					text.WriteString(delta)
					delivered <- struct{}{}
					return nil
				}
			}
			result, err := provider.Generate(context.Background(), llmTestMessages, callback)
			if err != nil || result.Content != "OK" || result.Model != "test-model" || result.FinishReason != "stop" ||
				result.Usage.InputTokens == nil || *result.Usage.InputTokens != 7 ||
				result.Usage.CachedTokens == nil || *result.Usage.CachedTokens != 0 ||
				(stream && text.String() != "OK") {
				t.Fatalf("chat result: %+v, %v", result, err)
			}
		})
	}

	t.Run("check-uses-stream-without-content", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request map[string]any
			if json.NewDecoder(r.Body).Decode(&request) != nil || request["stream"] != true {
				t.Error("connection check did not request streaming")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, llmTestStream)
		}))
		defer server.Close()
		result, err := CheckLLM(context.Background(), llmTestConfig(server.URL))
		if err != nil || result.Status != "ok" || result.Protocol != "openai-compatible" ||
			result.Model != "test-model" || result.FinishReason != "stop" || result.Usage.TotalTokens == nil {
			t.Fatalf("connection check: %+v, %v", result, err)
		}
		data, _ := json.Marshal(result)
		if strings.Contains(string(data), `"content"`) {
			t.Fatal("connection check exposed generated text")
		}
	})
}

func TestLLMTransientFailuresRecover(t *testing.T) {
	for _, status := range []int{429, 503, 0} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					if status == 0 {
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = connection.Close()
						return
					}
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, "llm-test-credential private upstream failure")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, llmTestStream)
			}))
			defer server.Close()
			config := llmTestConfig(server.URL)
			config.MaxRetries = 1
			provider := llmTestProvider(t, config)
			var text strings.Builder
			result, err := provider.Generate(context.Background(), llmTestMessages, func(delta string) error {
				text.WriteString(delta)
				return nil
			})
			if err != nil || result.Content != "OK" || text.String() != "OK" || calls.Load() != 2 {
				t.Fatalf("transient failure did not recover once: %d, %+v, %v", calls.Load(), result, err)
			}
		})
	}

	t.Run("retry-budget", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()
		config := llmTestConfig(server.URL)
		config.MaxRetries = 1
		provider := llmTestProvider(t, config)
		_, err := provider.Generate(context.Background(), llmTestMessages, nil)
		if errorCode(err) != "LLM_UNAVAILABLE" || calls.Load() != 2 {
			t.Fatalf("retry budget was not enforced: %v, calls=%d", err, calls.Load())
		}
	})
}

func TestLLMNeverReplaysDeliveredContent(t *testing.T) {
	for _, mode := range []string{"truncated", "network", "protocol", "callback", "callback-cancel", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "network" {
					connection, buffer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = fmt.Fprintf(buffer, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 999999\r\n\r\ndata: %s\n\n", llmTestChunk)
					_ = buffer.Flush()
					_ = connection.Close()
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+llmTestChunk+"\n\n")
				if mode == "protocol" {
					_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"private upstream failure\"}}\n\n")
				}
			}))
			defer server.Close()
			provider := llmTestProvider(t, llmTestConfig(server.URL))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			callbackError := failure("SOURCE_CHANGED", "Source changed", 409, false)
			var chunks int
			result, err := provider.Generate(ctx, llmTestMessages, func(string) error {
				chunks++
				if mode == "cancel" || mode == "callback-cancel" {
					cancel()
				}
				if mode == "callback" || mode == "callback-cancel" {
					return callbackError
				}
				return nil
			})
			if err == nil || calls.Load() != 1 || chunks != 1 || result.Content != "" {
				t.Fatalf("incomplete output succeeded or was replayed: %d calls, %d chunks, %+v, %v", calls.Load(), chunks, result, err)
			}
			if strings.HasPrefix(mode, "callback") && !errors.Is(err, callbackError) {
				t.Fatalf("callback error lost its identity: %v", err)
			}
			if mode != "callback" && strings.Contains(err.Error(), "private") {
				t.Fatalf("upstream detail leaked: %v", err)
			}
			var service *ServiceError
			if errors.As(err, &service) && service.Retryable {
				t.Fatalf("delivered stream advertised a retry: %+v", service)
			}
		})
	}
}

func TestLLMRejectsInvalidProviderData(t *testing.T) {
	cases := []struct {
		name   string
		stream bool
		body   string
		code   string
	}{
		{"extra-json", false, llmTestJSON + `{}`, "LLM_PROTOCOL"},
		{"wrong-content-type", false, llmTestJSON, "LLM_PROTOCOL"},
		{"invalid-utf8", false, strings.Replace(llmTestJSON, "OK", "\xff", 1), "LLM_PROTOCOL"},
		{"missing-model", false, strings.Replace(llmTestJSON, `"model":"test-model",`, "", 1), "LLM_PROTOCOL"},
		{"missing-index", false, strings.Replace(llmTestJSON, `"index":0,`, "", 1), "LLM_PROTOCOL"},
		{"wrong-index", false, strings.Replace(llmTestJSON, `"index":0`, `"index":1`, 1), "LLM_PROTOCOL"},
		{"multiple-choices", false, `{"model":"test-model","choices":[{},{}]}`, "LLM_PROTOCOL"},
		{"content-array", false, strings.Replace(llmTestJSON, `"content":"OK"`, `"content":[]`, 1), "LLM_PROTOCOL"},
		{"content-null", false, strings.Replace(llmTestJSON, `"content":"OK"`, `"content":null`, 1), "LLM_PROTOCOL"},
		{"empty-output", false, strings.Replace(llmTestJSON, `"OK"`, `""`, 1), "LLM_PROTOCOL"},
		{"tool-call", false, strings.Replace(llmTestJSON, `"refusal":null`, `"tool_calls":[{"id":"call"}]`, 1), "LLM_PROTOCOL"},
		{"function-call", false, strings.Replace(llmTestJSON, `"refusal":null`, `"function_call":{"name":"lookup","arguments":""}`, 1), "LLM_PROTOCOL"},
		{"function-arguments", false, strings.Replace(llmTestJSON, `"refusal":null`, `"function_call":{"name":"","arguments":"{}"}`, 1), "LLM_PROTOCOL"},
		{"unknown-function-field", false, strings.Replace(llmTestJSON, `"refusal":null`, `"function_call":{"unknown":""}`, 1), "LLM_PROTOCOL"},
		{"wrong-tool-type", false, strings.Replace(llmTestJSON, `"refusal":null`, `"tool_calls":{}`, 1), "LLM_PROTOCOL"},
		{"refusal", false, strings.Replace(llmTestJSON, `"refusal":null`, `"refusal":"private refusal"`, 1), "LLM_PROTOCOL"},
		{"failure", false, strings.Replace(llmTestJSON, `"refusal":null`, `"failure":{}`, 1), "LLM_PROTOCOL"},
		{"error-object", false, `{"error":{"message":"llm-test-credential private failure"}}`, "LLM_PROTOCOL"},
		{"token-limit", false, strings.Replace(llmTestJSON, `"stop"`, `"length"`, 1), "LLM_OUTPUT_LIMIT"},
		{"filtered", false, strings.Replace(llmTestJSON, `"stop"`, `"content_filter"`, 1), "LLM_REJECTED"},
		{"negative-usage", false, strings.Replace(llmTestJSON, `"total_tokens":8`, `"total_tokens":-1`, 1), "LLM_PROTOCOL"},
		{"negative-cache", false, strings.Replace(llmTestJSON, `"cached_tokens":0`, `"cached_tokens":-1`, 1), "LLM_PROTOCOL"},
		{"fractional-usage", false, strings.Replace(llmTestJSON, `"total_tokens":8`, `"total_tokens":1.5`, 1), "LLM_PROTOCOL"},
		{"overflow-usage", false, strings.Replace(llmTestJSON, `"total_tokens":8`, `"total_tokens":9223372036854775808`, 1), "LLM_PROTOCOL"},
		{"missing-done", true, "data: " + llmTestChunk + "\n\ndata: " + llmTestStop + "\n\n", "LLM_PROTOCOL"},
		{"missing-stop", true, "data: " + llmTestChunk + "\n\ndata: [DONE]\n\n", "LLM_PROTOCOL"},
		{"empty-finish-is-not-stop", true, "data: " + llmTestChunk + "\n\ndata: [DONE]", "LLM_PROTOCOL"},
		{"broken-event", true, "data: {not-json}\n\n", "LLM_PROTOCOL"},
		{"invalid-stream-utf8", true, "data: \xff\n\n", "LLM_PROTOCOL"},
		{"stream-error", true, "event: error\ndata: {\"message\":\"private failure\"}\n\n", "LLM_PROTOCOL"},
		{"changed-model", true, "data: " + llmTestChunk + "\n\ndata: " + strings.Replace(llmTestStop, "test-model", "other-model", 1) + "\n\n", "LLM_PROTOCOL"},
		{"stream-tool-call", true, "data: " + strings.Replace(llmTestChunk, `"function_call":null`, `"tool_calls":[{}]`, 1) + "\n\n", "LLM_PROTOCOL"},
		{"stream-limit", true, "data: " + strings.Replace(llmTestStop, `"stop"`, `"length"`, 1) + "\n\n", "LLM_OUTPUT_LIMIT"},
		{"stream-negative-usage", true, "data: " + strings.Replace(llmTestStop, `"total_tokens":8`, `"total_tokens":-1`, 1) + "\n\n", "LLM_PROTOCOL"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if item.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				if item.name == "wrong-content-type" {
					w.Header().Set("Content-Type", "text/html")
				}
				_, _ = io.WriteString(w, item.body)
			}))
			defer server.Close()
			provider := llmTestProvider(t, llmTestConfig(server.URL))
			var callback func(string) error
			if item.stream {
				callback = func(string) error { return nil }
			}
			result, err := provider.Generate(context.Background(), llmTestMessages, callback)
			if errorCode(err) != item.code || calls.Load() != 1 || result.Content != "" ||
				strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "credential") {
				t.Fatalf("invalid response was accepted, leaked, or retried: %+v, %v, %d", result, err, calls.Load())
			}
		})
	}
}

func TestLLMOptionalUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+llmTestChunk+"\n\ndata: "+strings.Split(llmTestStop, `,"usage"`)[0]+"}\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, strings.Split(llmTestJSON, `,"usage"`)[0]+"}")
				}
			}))
			defer server.Close()
			provider := llmTestProvider(t, llmTestConfig(server.URL))
			var callback func(string) error
			if stream {
				callback = func(string) error { return nil }
			}
			result, err := provider.Generate(context.Background(), llmTestMessages, callback)
			if err != nil || result.Content != "OK" || result.Usage.InputTokens != nil || result.Usage.OutputTokens != nil ||
				result.Usage.TotalTokens != nil || result.Usage.CachedTokens != nil {
				t.Fatalf("missing usage was fabricated: %+v, %v", result, err)
			}
		})
	}

	t.Run("separate-usage-frame", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			stop := strings.Split(llmTestStop, `,"usage"`)[0] + "}"
			usage := `{"model":"test-model","choices":[],"usage":{"prompt_tokens":0}}`
			_, _ = io.WriteString(w, "data: "+llmTestChunk+"\n\ndata: "+stop+"\n\ndata: "+usage+"\n\ndata: [DONE]\n\n")
		}))
		defer server.Close()
		provider := llmTestProvider(t, llmTestConfig(server.URL))
		result, err := provider.Generate(context.Background(), llmTestMessages, func(string) error { return nil })
		if err != nil || result.Content != "OK" || result.Usage.InputTokens == nil || *result.Usage.InputTokens != 0 ||
			result.Usage.OutputTokens != nil || result.Usage.TotalTokens != nil || result.Usage.CachedTokens != nil {
			t.Fatalf("separate optional usage was not preserved: %+v, %v", result, err)
		}
	})
}

func TestLLMEmptyRefusalAndCallPlaceholders(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !stream {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, strings.Replace(llmTestJSON, `"refusal":null`, `"refusal":"","tool_calls":[],"function_call":{}`, 1))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				chunk := strings.Replace(llmTestChunk, `"refusal":null`, `"refusal":"","tool_calls":[]`, 1)
				stop := strings.Replace(llmTestStop, `"delta":{}`, `"delta":{"refusal":"","function_call":{"name":"","arguments":""}}`, 1)
				_, _ = io.WriteString(w, "data: "+chunk+"\n\ndata: "+stop+"\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			provider := llmTestProvider(t, llmTestConfig(server.URL))
			var callback func(string) error
			if stream {
				callback = func(string) error { return nil }
			}
			result, err := provider.Generate(context.Background(), llmTestMessages, callback)
			if err != nil || result.Content != "OK" || result.FinishReason != "stop" {
				t.Fatalf("empty optional fields rejected text generation: %+v, %v", result, err)
			}
		})
	}
}

func TestLLMCancellationTimeoutAndConcurrency(t *testing.T) {
	t.Run("queue-shares-timeout", func(t *testing.T) {
		var calls atomic.Int32
		entered, release := make(chan struct{}), make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, llmTestStream)
		}))
		defer server.Close()
		defer close(release)
		config := llmTestConfig(server.URL)
		config.TimeoutSeconds, config.MaxConcurrency = 1, 1
		provider := llmTestProvider(t, config)
		first := make(chan error, 1)
		firstContext, cancelFirst := context.WithCancel(context.Background())
		defer cancelFirst()
		go func() {
			_, err := provider.Generate(firstContext, llmTestMessages, func(string) error {
				close(entered)
				<-release
				return nil
			})
			first <- err
		}()
		<-entered
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := provider.Generate(ctx, llmTestMessages, nil); errorCode(err) != "LLM_CANCELED" {
			t.Fatalf("queue cancellation: %v", err)
		}
		start := time.Now()
		_, err := provider.Generate(context.Background(), llmTestMessages, nil)
		if errorCode(err) != "LLM_TIMEOUT" || time.Since(start) > 2*time.Second || calls.Load() != 1 {
			t.Fatalf("queue exceeded the single request budget or admitted extra requests: %v, %d", err, calls.Load())
		}
		cancelFirst()
		release <- struct{}{}
		if err := <-first; errorCode(err) != "LLM_TIMEOUT" && errorCode(err) != "LLM_CANCELED" {
			t.Fatalf("first request cancellation: %v", err)
		}
	})

	for _, mode := range []string{"read-stream", "retry-backoff"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "retry-backoff" {
					w.Header().Set("Retry-After", "100")
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			config := llmTestConfig(server.URL)
			config.TimeoutSeconds = 1
			provider := llmTestProvider(t, config)
			start := time.Now()
			_, err := provider.Generate(context.Background(), llmTestMessages, func(string) error { return nil })
			if errorCode(err) != "LLM_TIMEOUT" || calls.Load() != 1 || time.Since(start) > 2*time.Second {
				t.Fatalf("timeout did not cover %s: %v, %d", mode, err, calls.Load())
			}
		})
	}
}

func TestLLMPermanentFailuresAndRedirects(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		writeLLMJSON(w)
	}))
	defer target.Close()
	for _, status := range []int{301, 302, 307, 308, 400, 401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "llm-test-credential private upstream message")
			}))
			defer server.Close()
			provider := llmTestProvider(t, llmTestConfig(server.URL))
			_, err := provider.Generate(context.Background(), llmTestMessages, nil)
			code := "LLM_REJECTED"
			if status == 401 || status == 403 {
				code = "LLM_AUTH"
			}
			if errorCode(err) != code || calls.Load() != 1 || targetCalls.Load() != 0 ||
				strings.Contains(err.Error(), "private") ||
				strings.Contains(err.Error(), "llm-test-credential") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("unsafe provider failure: %v, calls=%d, redirected=%d", err, calls.Load(), targetCalls.Load())
			}
		})
	}
}

func TestLLMResponseMemoryBounds(t *testing.T) {
	for _, mode := range []string{"json-body", "json-output", "event", "stream-output", "stream-body"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if strings.HasPrefix(mode, "json") {
					w.Header().Set("Content-Type", "application/json")
					content := llmOutputBytes + 1
					if mode == "json-body" {
						content = llmBodyBytes + 1
					}
					_, _ = io.WriteString(w, strings.Replace(llmTestJSON, `"OK"`, `"`+strings.Repeat("x", content)+`"`, 1))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch mode {
				case "event":
					// Each line is short, but one multiline event exceeds the event budget.
					_, _ = io.WriteString(w, strings.Repeat("data: {}\n", llmEventBytes/8+1)+"\n")
				case "stream-output":
					chunk := strings.Replace(llmTestChunk, `"OK"`, `"`+strings.Repeat("x", 128*1024)+`"`, 1)
					for range 9 {
						if _, err := io.WriteString(w, "data: "+chunk+"\n\n"); err != nil {
							return
						}
					}
				case "stream-body":
					padding := ":" + strings.Repeat("x", 4096) + "\n\n"
					for range llmBodyBytes/4096 + 2 {
						if _, err := io.WriteString(w, padding); err != nil {
							return
						}
					}
				}
			}))
			defer server.Close()
			provider := llmTestProvider(t, llmTestConfig(server.URL))
			var callback func(string) error
			if !strings.HasPrefix(mode, "json") {
				callback = func(string) error { return nil }
			}
			result, err := provider.Generate(context.Background(), llmTestMessages, callback)
			code := "LLM_PROTOCOL"
			if mode == "json-output" || mode == "stream-output" {
				code = "LLM_OUTPUT_LIMIT"
			}
			if errorCode(err) != code || calls.Load() != 1 || result.Content != "" {
				t.Fatalf("response memory bound failed: %+v, %v, calls=%d", result, err, calls.Load())
			}
		})
	}
}

func TestLLMValidationBoundaries(t *testing.T) {
	for _, mutate := range []func(*LLMConfig){
		func(c *LLMConfig) { c.Protocol = "unexpected" },
		func(c *LLMConfig) { value := "http://user@example.com/v1"; c.BaseURL = &value },
		func(c *LLMConfig) { value := "https://example.com/v1?secret=value"; c.BaseURL = &value },
		func(c *LLMConfig) { value := "https://example.com/v1#"; c.BaseURL = &value },
		func(c *LLMConfig) { value := "https://example.com/v1?"; c.BaseURL = &value },
		func(c *LLMConfig) { value := "file:///private"; c.BaseURL = &value },
		func(c *LLMConfig) { value := "secret\n"; c.APIKey = &value },
		func(c *LLMConfig) { value := "\u4e2d"; c.APIKey = &value },
		func(c *LLMConfig) { value := "model\n"; c.Model = &value },
		func(c *LLMConfig) { value := strings.Repeat("\u4e2d", 67); c.Model = &value },
		func(c *LLMConfig) { c.TimeoutSeconds = math.NaN() },
		func(c *LLMConfig) { c.TimeoutSeconds = 111 },
		func(c *LLMConfig) { c.MaxConcurrency = 9 },
		func(c *LLMConfig) { c.MaxRetries = 4 },
		func(c *LLMConfig) { c.MaxOutputTokens = 16385 },
		func(c *LLMConfig) { c.ContextBytes = 262145 },
	} {
		config := llmTestConfig("https://example.com")
		mutate(&config)
		if err := config.Validate(); errorCode(err) != "INVALID_CONFIG" || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid settings accepted or exposed: %v", err)
		}
	}
	disabled := LLMConfig{}
	if err := disabled.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLLMProvider(disabled); errorCode(err) != "LLM_DISABLED" {
		t.Fatalf("disabled provider: %v", err)
	}
	config := llmTestConfig("https://example.com")
	config.ContextBytes = 1024
	provider := llmTestProvider(t, config)
	_, err := provider.Generate(context.Background(), []LLMMessage{{Role: "user", Content: strings.Repeat("x", 1025)}}, nil)
	if errorCode(err) != "LLM_CONTEXT_LIMIT" {
		t.Fatalf("oversized input was sent: %v", err)
	}
}

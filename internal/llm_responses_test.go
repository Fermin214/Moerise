package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func responsesFixture(text string) map[string]any {
	return map[string]any{"model": "fictional-responses-model", "status": "completed", "error": nil,
		"output": []any{map[string]any{"id": "msg-fictional", "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}},
		"usage": map[string]any{"input_tokens": 17, "output_tokens": 2, "total_tokens": 19,
			"input_tokens_details": map[string]any{"cached_tokens": 5}}}
}

func responsesEvents(text string) []map[string]any {
	return []map[string]any{
		{"type": "response.created", "response": map[string]any{"status": "in_progress", "model": "fictional-responses-model"}},
		{"type": "response.output_item.added", "output_index": 0,
			"item": map[string]any{"id": "msg-fictional", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}},
		{"type": "response.content_part.added", "output_index": 0, "content_index": 0, "item_id": "msg-fictional",
			"part": map[string]any{"type": "output_text", "text": ""}},
		{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": "msg-fictional", "delta": text},
		{"type": "response.output_text.done", "output_index": 0, "content_index": 0, "item_id": "msg-fictional", "text": text},
		{"type": "response.content_part.done", "output_index": 0, "content_index": 0, "item_id": "msg-fictional",
			"part": map[string]any{"type": "output_text", "text": text}},
		{"type": "response.output_item.done", "output_index": 0,
			"item": responsesFixture(text)["output"].([]any)[0]},
		{"type": "response.completed", "response": responsesFixture(text)},
	}
}

func writeResponsesEvents(w http.ResponseWriter, events []map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "event: %s\r\ndata: %s\r\n\r\n", event["type"], data)
	}
}

func responsesConfig(address string) LLMConfig {
	config := DefaultSettings().LLM
	config.Enabled, config.Protocol, config.MaxRetries = true, "openai-responses", 1
	base, key, model := address+"/v1", "fictional-responses-key", "route-or-model"
	config.BaseURL, config.APIKey, config.Model = &base, &key, &model
	return config
}

func TestResponsesProtocolFlow(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/responses" ||
			r.Header.Get("Authorization") != "Bearer fictional-responses-key" {
			t.Error("invalid Responses request")
		}
		var request struct {
			Model  string       `json:"model"`
			Input  []LLMMessage `json:"input"`
			Max    int          `json:"max_output_tokens"`
			Stream bool         `json:"stream"`
			Store  *bool        `json:"store"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "route-or-model" ||
			request.Max != 2048 || request.Store == nil || *request.Store || len(request.Input) != 2 ||
			request.Input[0].Role != "system" || request.Input[1].Content != "fictional basil" {
			t.Error("invalid Responses body")
		}
		if request.Stream {
			writeResponsesEvents(w, responsesEvents("basil"))
		} else {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(responsesFixture("basil"))
		}
	}))
	defer server.Close()
	provider, err := NewLLMProvider(responsesConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	messages := []LLMMessage{{Role: "system", Content: "Use fictional data only."}, {Role: "user", Content: "fictional basil"}}
	for _, stream := range []bool{false, true} {
		var received strings.Builder
		var callback func(string) error
		if stream {
			callback = func(text string) error { received.WriteString(text); return nil }
		}
		result, err := provider.Generate(context.Background(), messages, callback)
		if err != nil || result.Content != "basil" || result.FinishReason != "stop" ||
			result.Usage.CachedTokens == nil || *result.Usage.CachedTokens != 5 ||
			result.Usage.InputTokens == nil || *result.Usage.InputTokens != 17 ||
			stream && received.String() != result.Content {
			t.Fatalf("Responses result: %+v, %v", result, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("protocol flow unexpectedly retried")
	}
}

func TestResponsesFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"truncated", "mismatch", "tool", "limit", "bad-position", "bad-usage", "callback", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempt := calls.Add(1)
				if mode == "unavailable" && attempt == 1 {
					w.WriteHeader(503)
					fmt.Fprint(w, "fictional-responses-key private input")
					return
				}
				events := responsesEvents("basil")
				switch mode {
				case "truncated":
					events = events[:4]
				case "mismatch":
					events[len(events)-1]["response"] = responsesFixture("different")
				case "tool":
					events[1]["item"] = map[string]any{"id": "call-fictional", "type": "function_call"}
				case "limit":
					events[len(events)-1] = map[string]any{"type": "response.incomplete",
						"response": map[string]any{"model": "fictional-responses-model", "status": "incomplete",
							"incomplete_details": map[string]any{"reason": "max_output_tokens"}}}
				case "bad-position":
					events[3]["content_index"] = 9
				case "bad-usage":
					events[len(events)-1]["response"].(map[string]any)["usage"] = map[string]any{"input_tokens": -1}
				}
				writeResponsesEvents(w, events)
			}))
			defer server.Close()
			provider, err := NewLLMProvider(responsesConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			sentinel := errors.New("consumer canceled")
			var output strings.Builder
			_, err = provider.Generate(context.Background(), []LLMMessage{{Role: "user", Content: "fictional basil"}}, func(text string) error {
				output.WriteString(text)
				if mode == "callback" {
					return sentinel
				}
				return nil
			})
			if mode == "unavailable" {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("transient failure did not recover: %v", err)
				}
				return
			}
			if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "fictional-responses-key") {
				t.Fatalf("invalid response accepted, replayed, or leaked: %v", err)
			}
			if mode == "callback" && !errors.Is(err, sentinel) {
				t.Fatalf("callback error lost: %v", err)
			}
			if mode == "limit" && errorCode(err) != "LLM_OUTPUT_LIMIT" {
				t.Fatalf("incomplete output not reported: %v", err)
			}
		})
	}
}

func TestResponsesStructuredFailures(t *testing.T) {
	for _, mode := range []string{"json-failed", "json-rejected", "stream-failed", "stream-error",
		"delivered-failed", "delivered-error", "quota", "rate-limit"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			stream := strings.HasPrefix(mode, "stream-") || strings.HasPrefix(mode, "delivered-")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) > 1 {
					if stream {
						writeResponsesEvents(w, responsesEvents("basil"))
					} else {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(responsesFixture("basil"))
					}
					return
				}
				providerCode := "server_error"
				if mode == "json-rejected" {
					providerCode = "invalid_prompt"
				}
				failed := map[string]any{"status": "failed",
					"error": map[string]any{"code": providerCode, "message": "private input fictional-responses-key"}}
				switch mode {
				case "quota", "rate-limit":
					code := "insufficient_quota"
					if mode == "rate-limit" {
						code = "rate_limit_exceeded"
					}
					w.WriteHeader(429)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
						"code": code, "message": "private input fictional-responses-key"}})
				case "json-failed", "json-rejected":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(failed)
				default:
					event := map[string]any{"type": "response.failed", "response": failed}
					if strings.HasSuffix(mode, "-error") {
						event = map[string]any{"type": "error", "code": "rate_limit_exceeded",
							"message": "private input fictional-responses-key"}
					}
					events := []map[string]any{event}
					if strings.HasPrefix(mode, "delivered-") {
						events = append(responsesEvents("basil")[:4], event)
					}
					writeResponsesEvents(w, events)
				}
			}))
			defer server.Close()
			provider, err := NewLLMProvider(responsesConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			var output strings.Builder
			var callback func(string) error
			if stream {
				callback = func(text string) error { output.WriteString(text); return nil }
			}
			result, err := provider.Generate(context.Background(), []LLMMessage{{Role: "user", Content: "fictional basil"}}, callback)
			if mode == "quota" || mode == "json-rejected" || strings.HasPrefix(mode, "delivered-") {
				want := "LLM_REJECTED"
				if mode == "delivered-failed" {
					want = "LLM_UNAVAILABLE"
				} else if mode == "delivered-error" {
					want = "LLM_RATE_LIMITED"
				}
				var service *ServiceError
				if !errors.As(err, &service) || service.Code != want || service.Retryable || calls.Load() != 1 ||
					strings.Contains(err.Error(), "private input") || strings.Contains(err.Error(), "fictional-responses-key") {
					t.Fatalf("permanent or delivered failure: calls=%d, err=%+v", calls.Load(), err)
				}
				if strings.HasPrefix(mode, "delivered-") && output.String() != "basil" {
					t.Fatalf("unexpected delivered output: %q", output.String())
				}
				return
			}
			if err != nil || calls.Load() != 2 || result.Content != "basil" || stream && output.String() != "basil" {
				t.Fatalf("structured transient failure did not recover: calls=%d, result=%+v, err=%v", calls.Load(), result, err)
			}
		})
	}
}

func TestResponsesCallbackErrorIdentity(t *testing.T) {
	for _, cancelDuringCallback := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelDuringCallback), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writeResponsesEvents(w, responsesEvents("basil"))
			}))
			defer server.Close()
			provider, err := NewLLMProvider(responsesConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := failure("LLM_UNAVAILABLE", "consumer stopped", 503, true)
			_, err = provider.Generate(ctx, []LLMMessage{{Role: "user", Content: "fictional basil"}}, func(string) error {
				if cancelDuringCallback {
					cancel()
				}
				return sentinel
			})
			if err != sentinel || calls.Load() != 1 {
				t.Fatalf("callback error changed or request replayed: calls=%d, err=%v", calls.Load(), err)
			}
		})
	}
}

func TestResponsesReasoningPartLifecycle(t *testing.T) {
	for _, mode := range []string{"valid", "foreign-item", "missing-part", "double-done", "unclosed-item", "late-part",
		"wrong-part-type", "body-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			reasoning := map[string]any{"id": "reason-fictional", "type": "reasoning", "status": "completed", "summary": []any{}}
			events := []map[string]any{
				{"type": "response.output_item.added", "output_index": 0,
					"item": map[string]any{"id": "reason-fictional", "type": "reasoning"}},
				{"type": "response.content_part.added", "output_index": 0, "content_index": 0, "item_id": "reason-fictional",
					"part": map[string]any{"type": "reasoning_text", "text": ""}},
				{"type": "response.reasoning_text.delta", "output_index": 0, "content_index": 0, "item_id": "reason-fictional",
					"delta": "private reasoning"},
				{"type": "response.reasoning_text.done", "output_index": 0, "content_index": 0, "item_id": "reason-fictional",
					"text": "private reasoning"},
				{"type": "response.content_part.done", "output_index": 0, "content_index": 0, "item_id": "reason-fictional",
					"part": map[string]any{"type": "reasoning_text", "text": "private reasoning"}},
				{"type": "response.output_item.done", "output_index": 0, "item": reasoning},
			}
			switch mode {
			case "foreign-item":
				events[4]["item_id"] = "other"
			case "missing-part":
				events = append(events[:1], events[2:]...)
			case "double-done":
				events = append(events[:5], events[4], events[5])
			case "unclosed-item":
				events = append(events[:4], events[5])
			case "late-part":
				events = append(events, events[1])
			case "wrong-part-type":
				events[1]["part"].(map[string]any)["type"] = "output_text"
			}
			bodyEvents := responsesEvents("basil")
			for _, event := range bodyEvents {
				if _, ok := event["output_index"]; ok {
					event["output_index"] = 1
				}
			}
			if mode == "body-mismatch" {
				bodyEvents[5]["part"].(map[string]any)["text"] = "different"
			}
			final := bodyEvents[len(bodyEvents)-1]["response"].(map[string]any)
			final["output"] = append([]any{reasoning}, final["output"].([]any)...)
			events = append(events, bodyEvents[1:]...)
			recorder := httptest.NewRecorder()
			writeResponsesEvents(recorder, events)
			var output strings.Builder
			result, _, err := readResponsesStream(context.Background(), recorder.Body, func(text string) error {
				output.WriteString(text)
				return nil
			})
			if mode == "valid" {
				if err != nil || result.Content != "basil" || output.String() != "basil" {
					t.Fatalf("reasoning interfered with answer: result=%+v, output=%q, err=%v", result, output.String(), err)
				}
			} else if errorCode(err) != "LLM_PROTOCOL" {
				t.Fatalf("invalid lifecycle accepted: %v", err)
			}
			if strings.Contains(output.String(), "private reasoning") {
				t.Fatalf("reasoning leaked to callback: %q", output.String())
			}
		})
	}
}

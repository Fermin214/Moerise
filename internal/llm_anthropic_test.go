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
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const anthropicTestJSON = `{"type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"Hello"},{"type":"text","text":"!"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":3,"cache_read_input_tokens":7,"cache_creation_input_tokens":5}}`

func anthropicTestConfig(base string) LLMConfig {
	key, model := "test-key", "test-model"
	return LLMConfig{Enabled: true, Protocol: "anthropic", BaseURL: &base, APIKey: &key, Model: &model,
		TimeoutSeconds: 5, MaxConcurrency: 1, MaxRetries: 1, MaxOutputTokens: 256, ContextBytes: 4096}
}

func anthropicTestProvider(t *testing.T, config LLMConfig) *anthropicLLMClient {
	t.Helper()
	provider, err := newAnthropicLLMProvider(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	return provider.(*anthropicLLMClient)
}

func anthropicTestEvent(kind, body string) string {
	return "event: " + kind + "\ndata: " + body + "\n\n"
}

func anthropicTestStart(usage string) string {
	if usage != "" {
		usage = `,"usage":` + usage
	}
	return anthropicTestEvent("message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null`+usage+`}}`)
}

func anthropicTestBlock(index int, text string) string {
	encoded, _ := json.Marshal(text)
	return anthropicTestEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, index)) +
		anthropicTestEvent("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, index, encoded)) +
		anthropicTestEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index))
}

func anthropicTestFinish(reason, usage string) string {
	if usage != "" {
		usage = `,"usage":` + usage
	}
	return anthropicTestEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+reason+`","stop_sequence":null}`+usage+`}`) +
		anthropicTestEvent("message_stop", `{"type":"message_stop"}`)
}

func anthropicTestStream() string {
	return anthropicTestStart(`{"input_tokens":11,"output_tokens":1,"cache_read_input_tokens":7,"cache_creation_input_tokens":5}`) +
		anthropicTestEvent("ping", `{"type":"ping"}`) +
		anthropicTestBlock(0, "Hello") + anthropicTestBlock(1, "!") +
		anthropicTestFinish("end_turn", `{"output_tokens":3}`)
}

func TestAnthropicLLMRequest(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" ||
					r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") != "2023-06-01" ||
					r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" {
					t.Error("invalid Anthropic request headers or path")
				}
				var body struct {
					Model     string             `json:"model"`
					MaxTokens int                `json:"max_tokens"`
					System    []anthropicText    `json:"system"`
					Messages  []anthropicMessage `json:"messages"`
					Stream    bool               `json:"stream"`
				}
				decoder := json.NewDecoder(r.Body)
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "test-model" || body.MaxTokens != 256 || body.Stream != stream ||
					!reflect.DeepEqual(body.System, []anthropicText{{Type: "text", Text: "system instruction"}, {Type: "text", Text: "developer instruction"}}) ||
					!reflect.DeepEqual(body.Messages, []anthropicMessage{
						{Role: "user", Content: []anthropicText{{Type: "text", Text: "question"}}},
						{Role: "assistant", Content: []anthropicText{{Type: "text", Text: "answer"}}},
						{Role: "user", Content: []anthropicText{{Type: "text", Text: "continue"}}},
					}) {
					t.Errorf("invalid request shape: %+v", body)
				}
				if stream {
					if r.Header.Get("Accept") != "text/event-stream" {
						t.Error("missing SSE accept header")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, anthropicTestStream())
				} else {
					if r.Header.Get("Accept") != "application/json" {
						t.Error("missing JSON accept header")
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, anthropicTestJSON)
				}
			}))
			defer server.Close()
			config := anthropicTestConfig(server.URL + "/v1/")
			provider := anthropicTestProvider(t, config)
			*config.Model, *config.APIKey, *config.BaseURL = "changed-model", "changed-key", "http://invalid"
			var callback func(string) error
			var received strings.Builder
			if stream {
				callback = func(text string) error { received.WriteString(text); return nil }
			}
			result, err := provider.Generate(context.Background(), []LLMMessage{
				{Role: "system", Content: "system instruction"}, {Role: "developer", Content: "developer instruction"},
				{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "continue"},
			}, callback)
			if err != nil || result.Content != "Hello!" || result.Model != "test-model" || result.FinishReason != "end_turn" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if stream && received.String() != result.Content {
				t.Fatalf("callback=%q result=%q", received.String(), result.Content)
			}
			if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 23 ||
				result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 3 ||
				result.Usage.TotalTokens == nil || *result.Usage.TotalTokens != 26 ||
				result.Usage.CachedTokens == nil || *result.Usage.CachedTokens != 7 ||
				result.Usage.CacheCreationTokens == nil || *result.Usage.CacheCreationTokens != 5 {
				t.Fatalf("usage=%+v", result.Usage)
			}
			usageJSON, err := json.Marshal(result.Usage)
			if err != nil {
				t.Fatal(err)
			}
			aiAPIValidateJSON(t, aiAPISchema(t, "LLMUsage"), usageJSON)
		})
	}
}

func TestAnthropicLLMUsage(t *testing.T) {
	for _, test := range []struct {
		name, usage string
		want        LLMUsage
		code        string
	}{
		{name: "missing"},
		{name: "null", usage: `null`},
		{name: "output only", usage: `{"output_tokens":3}`, want: LLMUsage{OutputTokens: anthropicTestInt(3)}},
		{name: "zero", usage: `{"input_tokens":0,"output_tokens":0}`, want: LLMUsage{InputTokens: anthropicTestInt(0), OutputTokens: anthropicTestInt(0), TotalTokens: anthropicTestInt(0)}},
		{name: "no cache", usage: `{"input_tokens":25,"output_tokens":1}`,
			want: LLMUsage{InputTokens: anthropicTestInt(25), OutputTokens: anthropicTestInt(1), TotalTokens: anthropicTestInt(26)}},
		{name: "cache read", usage: `{"input_tokens":11,"output_tokens":3,"cache_read_input_tokens":7}`,
			want: LLMUsage{InputTokens: anthropicTestInt(18), OutputTokens: anthropicTestInt(3), TotalTokens: anthropicTestInt(21), CachedTokens: anthropicTestInt(7)}},
		{name: "cache write", usage: `{"input_tokens":11,"output_tokens":3,"cache_creation_input_tokens":5}`,
			want: LLMUsage{InputTokens: anthropicTestInt(16), OutputTokens: anthropicTestInt(3), TotalTokens: anthropicTestInt(19), CacheCreationTokens: anthropicTestInt(5)}},
		{name: "cache read and write", usage: `{"input_tokens":11,"output_tokens":3,"cache_read_input_tokens":7,"cache_creation_input_tokens":5}`,
			want: LLMUsage{InputTokens: anthropicTestInt(23), OutputTokens: anthropicTestInt(3), TotalTokens: anthropicTestInt(26),
				CachedTokens: anthropicTestInt(7), CacheCreationTokens: anthropicTestInt(5)}},
		{name: "reported zero cache", usage: `{"input_tokens":11,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`,
			want: LLMUsage{InputTokens: anthropicTestInt(11), OutputTokens: anthropicTestInt(3), TotalTokens: anthropicTestInt(14),
				CachedTokens: anthropicTestInt(0), CacheCreationTokens: anthropicTestInt(0)}},
		{name: "cache without raw input", usage: `{"output_tokens":3,"cache_read_input_tokens":7,"cache_creation_input_tokens":5}`,
			want: LLMUsage{OutputTokens: anthropicTestInt(3), CachedTokens: anthropicTestInt(7), CacheCreationTokens: anthropicTestInt(5)}},
		{name: "raw total", usage: `{"total_tokens":17}`, want: LLMUsage{TotalTokens: anthropicTestInt(17)}},
		{name: "reported total takes precedence", usage: `{"input_tokens":11,"output_tokens":3,"cache_read_input_tokens":7,"cache_creation_input_tokens":5,"total_tokens":99}`,
			want: LLMUsage{InputTokens: anthropicTestInt(23), OutputTokens: anthropicTestInt(3), TotalTokens: anthropicTestInt(99),
				CachedTokens: anthropicTestInt(7), CacheCreationTokens: anthropicTestInt(5)}},
		{name: "overflow", usage: `{"input_tokens":9223372036854775807,"output_tokens":1}`,
			want: LLMUsage{InputTokens: anthropicTestInt(math.MaxInt64), OutputTokens: anthropicTestInt(1)}},
		{name: "cache sum at limit", usage: `{"input_tokens":9223372036854775795,"output_tokens":0,"cache_read_input_tokens":7,"cache_creation_input_tokens":5}`,
			want: LLMUsage{InputTokens: anthropicTestInt(math.MaxInt64), OutputTokens: anthropicTestInt(0), TotalTokens: anthropicTestInt(math.MaxInt64),
				CachedTokens: anthropicTestInt(7), CacheCreationTokens: anthropicTestInt(5)}},
		{name: "cache read overflow", usage: `{"input_tokens":9223372036854775807,"output_tokens":3,"cache_read_input_tokens":1}`,
			want: LLMUsage{OutputTokens: anthropicTestInt(3), CachedTokens: anthropicTestInt(1)}},
		{name: "cache overflow preserves reported total", usage: `{"input_tokens":9223372036854775807,"output_tokens":3,"cache_read_input_tokens":1,"total_tokens":17}`,
			want: LLMUsage{OutputTokens: anthropicTestInt(3), TotalTokens: anthropicTestInt(17), CachedTokens: anthropicTestInt(1)}},
		{name: "cache write overflow", usage: `{"input_tokens":9223372036854775806,"output_tokens":3,"cache_read_input_tokens":1,"cache_creation_input_tokens":1}`,
			want: LLMUsage{OutputTokens: anthropicTestInt(3), CachedTokens: anthropicTestInt(1), CacheCreationTokens: anthropicTestInt(1)}},
		{name: "cached total overflow", usage: `{"input_tokens":9223372036854775806,"output_tokens":1,"cache_read_input_tokens":1}`,
			want: LLMUsage{InputTokens: anthropicTestInt(math.MaxInt64), OutputTokens: anthropicTestInt(1), CachedTokens: anthropicTestInt(1)}},
		{name: "negative input", usage: `{"input_tokens":-1}`, code: "LLM_PROTOCOL"},
		{name: "negative output", usage: `{"output_tokens":-1}`, code: "LLM_PROTOCOL"},
		{name: "negative cache", usage: `{"cache_read_input_tokens":-1}`, code: "LLM_PROTOCOL"},
		{name: "negative creation", usage: `{"cache_creation_input_tokens":-1}`, code: "LLM_PROTOCOL"},
		{name: "negative total", usage: `{"total_tokens":-1}`, code: "LLM_PROTOCOL"},
		{name: "fraction", usage: `{"input_tokens":1.5}`, code: "LLM_PROTOCOL"},
		{name: "integer overflow", usage: `{"input_tokens":9223372036854775808}`, code: "LLM_PROTOCOL"},
	} {
		for _, format := range []string{"JSON", "SSE"} {
			t.Run(test.name+"/"+format, func(t *testing.T) {
				var result LLMResult
				var err error
				if format == "JSON" {
					wire := `{"type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn"`
					if test.usage != "" {
						wire += `,"usage":` + test.usage
					}
					result, err = readAnthropicLLMJSON(strings.NewReader(wire + `}`))
				} else {
					wire := anthropicTestStart(test.usage) + anthropicTestBlock(0, "OK") + anthropicTestFinish("end_turn", "")
					result, _, err = readAnthropicLLMStream(context.Background(), strings.NewReader(wire), func(string) error { return nil })
				}
				if test.code != "" {
					if errorCode(err) != test.code {
						t.Fatalf("error=%v", err)
					}
				} else if err != nil || !reflect.DeepEqual(result.Usage, test.want) {
					t.Fatalf("usage=%+v want=%+v err=%v", result.Usage, test.want, err)
				}
			})
		}
	}
	for _, test := range []struct {
		start string
		total int64
	}{
		{`{"input_tokens":11}`, 26},
		{`{"input_tokens":11,"cache_read_input_tokens":7,"cache_creation_input_tokens":5,"total_tokens":99}`, 99},
		{`{"input_tokens":9,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}`, 26},
	} {
		// Snapshots replace native fields; absent and null fields retain earlier reports.
		stream := anthropicTestStart(test.start) + anthropicTestBlock(0, "OK") +
			anthropicTestEvent("message_delta", `{"type":"message_delta","delta":{},"usage":{"output_tokens":2,"input_tokens":11,"cache_read_input_tokens":7,"cache_creation_input_tokens":5}}`) +
			anthropicTestEvent("message_delta", `{"type":"message_delta","delta":{},"usage":{"output_tokens":2,"cache_read_input_tokens":7,"cache_creation_input_tokens":5}}`) +
			anthropicTestFinish("end_turn", `{"input_tokens":null,"output_tokens":3,"cache_read_input_tokens":null,"cache_creation_input_tokens":null}`)
		result, _, err := readAnthropicLLMStream(context.Background(), strings.NewReader(stream), func(string) error { return nil })
		if err != nil || !reflect.DeepEqual(result.Usage, LLMUsage{InputTokens: anthropicTestInt(23),
			OutputTokens: anthropicTestInt(3), TotalTokens: anthropicTestInt(test.total),
			CachedTokens: anthropicTestInt(7), CacheCreationTokens: anthropicTestInt(5)}) {
			t.Fatalf("merged usage=%+v err=%v", result.Usage, err)
		}
	}
}

func anthropicTestInt(value int64) *int64 { return &value }

func TestAnthropicLLMJSONRejects(t *testing.T) {
	for _, test := range []struct{ name, wire, code string }{
		{"truncated", anthropicTestJSON[:len(anthropicTestJSON)-1], "LLM_PROTOCOL"},
		{"multiple JSON", anthropicTestJSON + `{}`, "LLM_PROTOCOL"},
		{"invalid UTF8", strings.Replace(anthropicTestJSON, "Hello", string([]byte{0xff}), 1), "LLM_PROTOCOL"},
		{"wrong role", strings.Replace(anthropicTestJSON, `"assistant"`, `"user"`, 1), "LLM_PROTOCOL"},
		{"wrong type", strings.Replace(anthropicTestJSON, `"message"`, `"error"`, 1), "LLM_PROTOCOL"},
		{"missing stop", strings.Replace(anthropicTestJSON, `"end_turn"`, `null`, 1), "LLM_PROTOCOL"},
		{"token limit", strings.Replace(anthropicTestJSON, `"end_turn"`, `"max_tokens"`, 1), "LLM_OUTPUT_LIMIT"},
		{"refusal", strings.Replace(anthropicTestJSON, `"end_turn"`, `"refusal"`, 1), "LLM_REJECTED"},
		{"tool stop", strings.Replace(anthropicTestJSON, `"end_turn"`, `"tool_use"`, 1), "LLM_REJECTED"},
		{"tool block", strings.Replace(anthropicTestJSON, `"type":"text"`, `"type":"tool_use"`, 1), "LLM_PROTOCOL"},
		{"thinking block", strings.Replace(anthropicTestJSON, `"type":"text"`, `"type":"thinking"`, 1), "LLM_PROTOCOL"},
		{"unsupported block", strings.Replace(anthropicTestJSON, `"type":"text"`, `"type":"image"`, 1), "LLM_PROTOCOL"},
		{"null text", strings.Replace(anthropicTestJSON, `"Hello"`, `null`, 1), "LLM_PROTOCOL"},
		{"empty content", `{"type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":"end_turn"}`, "LLM_PROTOCOL"},
		{"output limit", strings.Replace(anthropicTestJSON, "Hello", strings.Repeat("x", llmOutputBytes), 1), "LLM_OUTPUT_LIMIT"},
		{"body limit", strings.Repeat(" ", llmBodyBytes+1), "LLM_PROTOCOL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := readAnthropicLLMJSON(strings.NewReader(test.wire))
			if errorCode(err) != test.code || result.Content != "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestAnthropicLLMStreamRejects(t *testing.T) {
	start, block, finish := anthropicTestStart(""), anthropicTestBlock(0, "OK"), anthropicTestFinish("end_turn", "")
	for _, test := range []struct{ name, wire, code string }{
		{"missing start", block + finish, "LLM_PROTOCOL"},
		{"duplicate start", start + start + block + finish, "LLM_PROTOCOL"},
		{"delta without block", start + anthropicTestEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`), "LLM_PROTOCOL"},
		{"wrong delta index", start + strings.Replace(block, `"type":"content_block_delta","index":0`, `"type":"content_block_delta","index":1`, 1) + finish, "LLM_PROTOCOL"},
		{"wrong stop index", start + strings.Replace(block, `"type":"content_block_stop","index":0`, `"type":"content_block_stop","index":1`, 1) + finish, "LLM_PROTOCOL"},
		{"skipped index", start + anthropicTestBlock(1, "secret") + finish, "LLM_PROTOCOL"},
		{"reused index", start + block + block + finish, "LLM_PROTOCOL"},
		{"unclosed block", start + strings.Replace(block, anthropicTestEvent("content_block_stop", `{"type":"content_block_stop","index":0}`), "", 1) + finish, "LLM_PROTOCOL"},
		{"missing message stop", start + block + strings.TrimSuffix(finish, anthropicTestEvent("message_stop", `{"type":"message_stop"}`)), "LLM_PROTOCOL"},
		{"missing reason", start + block + anthropicTestEvent("message_stop", `{"type":"message_stop"}`), "LLM_PROTOCOL"},
		{"duplicate reason", start + block + strings.TrimSuffix(finish, anthropicTestEvent("message_stop", `{"type":"message_stop"}`)) + finish, "LLM_PROTOCOL"},
		{"block after finish", start + block + strings.TrimSuffix(finish, anthropicTestEvent("message_stop", `{"type":"message_stop"}`)) + anthropicTestBlock(1, "x") + finish, "LLM_PROTOCOL"},
		{"tool block", start + strings.Replace(block, `"type":"text"`, `"type":"tool_use"`, 1) + finish, "LLM_PROTOCOL"},
		{"thinking block", start + strings.Replace(block, `"type":"text"`, `"type":"thinking"`, 1) + finish, "LLM_PROTOCOL"},
		{"thinking delta", start + strings.Replace(block, `"type":"text_delta"`, `"type":"thinking_delta"`, 1) + finish, "LLM_PROTOCOL"},
		{"wrong event name", strings.Replace(start, "event: message_start", "event: ping", 1) + block + finish, "LLM_PROTOCOL"},
		{"single JSON", strings.Replace(start, `"type":"message_start","message"`, `"type":"message_start"} {} {"message"`, 1), "LLM_PROTOCOL"},
		{"UTF8", start + anthropicTestBlock(0, "OK") + string([]byte{0xff}), "LLM_PROTOCOL"},
		{"token limit", start + block + anthropicTestFinish("max_tokens", ""), "LLM_OUTPUT_LIMIT"},
		{"refusal", start + block + anthropicTestFinish("refusal", ""), "LLM_REJECTED"},
		{"tool stop", start + block + anthropicTestFinish("tool_use", ""), "LLM_REJECTED"},
		{"negative usage", start + block + anthropicTestFinish("end_turn", `{"output_tokens":-1}`), "LLM_PROTOCOL"},
		{"event limit", ": " + strings.Repeat("x", llmEventBytes) + "\n\n", "LLM_PROTOCOL"},
		{"multiline event limit", strings.Repeat(": "+strings.Repeat("x", 1024)+"\n", 256) + "\n", "LLM_PROTOCOL"},
		{"body limit", strings.Repeat(": "+strings.Repeat("x", 1024)+"\n\n", 8192), "LLM_PROTOCOL"},
		{"error", anthropicTestEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"private"}}`), "LLM_UNAVAILABLE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, _, err := readAnthropicLLMStream(context.Background(), strings.NewReader(test.wire), func(string) error { return nil })
			if errorCode(err) != test.code || result.Content != "" || strings.Contains(err.Error(), "private") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	t.Run("output limit across events", func(t *testing.T) {
		wire := start
		for index := 0; index < 9; index++ {
			wire += anthropicTestBlock(index, strings.Repeat("x", llmOutputBytes/8))
		}
		_, _, err := readAnthropicLLMStream(context.Background(), strings.NewReader(wire), func(string) error { return nil })
		if errorCode(err) != "LLM_OUTPUT_LIMIT" {
			t.Fatal(err)
		}
	})
}

func TestAnthropicLLMStreamFraming(t *testing.T) {
	for _, wire := range []string{
		anthropicTestStream(),
		anthropicTestStart("") + anthropicTestBlock(0, "Hello!") + anthropicTestFinish("end_turn", ""),
		strings.ReplaceAll(anthropicTestStream(), "\n", "\r\n"),
		strings.TrimRight(anthropicTestStream(), "\n"),
		strings.Replace(anthropicTestStream(), `data: {"type":"message_stop"}`, "data: {\ndata: \"type\":\"message_stop\"}", 1),
	} {
		var text strings.Builder
		result, _, err := readAnthropicLLMStream(context.Background(), strings.NewReader(wire), func(value string) error { text.WriteString(value); return nil })
		if err != nil || result.Content != "Hello!" || text.String() != result.Content {
			t.Fatalf("result=%+v text=%q err=%v", result, text.String(), err)
		}
	}
}

type anthropicTestTransport func(*http.Request) (*http.Response, error)

func (f anthropicTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type anthropicTestBrokenReader struct{ io.Reader }

func (r anthropicTestBrokenReader) Read(data []byte) (int, error) {
	n, err := r.Reader.Read(data)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func TestAnthropicLLMRetryAndNoReplay(t *testing.T) {
	callbackError := failure("LLM_UNAVAILABLE", "caller error", 503, true)
	for _, test := range []struct {
		name, wire, code string
		status           int
		network, broken  bool
		exhaust          bool
		callbackError    error
		wantCalls        int32
	}{
		{name: "network retry", network: true, wire: anthropicTestStream(), wantCalls: 2},
		{name: "429 retry", status: 429, wire: anthropicTestStream(), wantCalls: 2},
		{name: "529 retry", status: 529, wire: anthropicTestStream(), wantCalls: 2},
		{name: "500 retry", status: 500, wire: anthropicTestStream(), wantCalls: 2},
		{name: "retry budget exhausted", status: 529, exhaust: true, code: "LLM_UNAVAILABLE", wantCalls: 2},
		{name: "stream overload retry", wire: anthropicTestEvent("error", `{"type":"error","error":{"type":"overloaded_error"}}`), wantCalls: 2},
		{name: "callback error", wire: anthropicTestStream(), callbackError: callbackError, code: "LLM_UNAVAILABLE", wantCalls: 1},
		{name: "error after text", wire: anthropicTestStart("") + anthropicTestBlock(0, "partial") + anthropicTestEvent("error", `{"type":"error","error":{"type":"overloaded_error"}}`), code: "LLM_UNAVAILABLE", wantCalls: 1},
		{name: "truncated before text", wire: anthropicTestStart(""), code: "LLM_PROTOCOL", wantCalls: 1},
		{name: "network interruption", wire: anthropicTestStart(""), broken: true, code: "LLM_UNAVAILABLE", wantCalls: 1},
		{name: "error after finish", wire: anthropicTestStart("") + anthropicTestEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) + anthropicTestEvent("error", `{"type":"error","error":{"type":"overloaded_error"}}`), code: "LLM_UNAVAILABLE", wantCalls: 1},
		{name: "missing stop", wire: strings.TrimSuffix(anthropicTestStream(), anthropicTestEvent("message_stop", `{"type":"message_stop"}`)), code: "LLM_PROTOCOL", wantCalls: 1},
		{name: "tool", wire: anthropicTestStart("") + anthropicTestEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use"}}`), code: "LLM_PROTOCOL", wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := anthropicTestProvider(t, anthropicTestConfig("http://unused/v1"))
			var calls atomic.Int32
			provider.client.Transport = anthropicTestTransport(func(*http.Request) (*http.Response, error) {
				count := calls.Add(1)
				if test.network && count == 1 {
					return nil, errors.New("network")
				}
				status, wire := http.StatusOK, test.wire
				if (count == 1 || test.exhaust) && test.status != 0 {
					status = test.status
				}
				if count > 1 {
					wire = anthropicTestStream()
				}
				var reader io.Reader = strings.NewReader(wire)
				if test.broken {
					reader = anthropicTestBrokenReader{reader}
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}},
					Body: io.NopCloser(reader)}, nil
			})
			result, err := provider.Generate(context.Background(), []LLMMessage{{Role: "user", Content: "question"}}, func(string) error { return test.callbackError })
			if calls.Load() != test.wantCalls {
				t.Fatalf("calls=%d want=%d err=%v", calls.Load(), test.wantCalls, err)
			}
			if test.code == "" {
				if err != nil || result.Content != "Hello!" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if errorCode(err) != test.code {
				t.Fatalf("err=%v", err)
			}
			if test.callbackError != nil && err != test.callbackError {
				t.Fatalf("callback error identity lost: %v", err)
			}
		})
	}
}

func TestAnthropicLLMCancelAndTimeout(t *testing.T) {
	t.Run("concurrency wait", func(t *testing.T) {
		provider := anthropicTestProvider(t, anthropicTestConfig("http://unused/v1"))
		provider.config.TimeoutSeconds = 0.03
		provider.requests <- struct{}{}
		defer func() { <-provider.requests }()
		var calls atomic.Int32
		provider.client.Transport = anthropicTestTransport(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("must not request")
		})
		_, err := provider.Generate(context.Background(), []LLMMessage{{Role: "user", Content: "question"}}, nil)
		if errorCode(err) != "LLM_TIMEOUT" || calls.Load() != 0 {
			t.Fatalf("calls=%d err=%v", calls.Load(), err)
		}
	})
	t.Run("retry wait", func(t *testing.T) {
		provider := anthropicTestProvider(t, anthropicTestConfig("http://unused/v1"))
		provider.config.TimeoutSeconds = 0.03
		var calls atomic.Int32
		provider.client.Transport = anthropicTestTransport(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"120"}},
				Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		_, err := provider.Generate(context.Background(), []LLMMessage{{Role: "user", Content: "question"}}, nil)
		if errorCode(err) != "LLM_TIMEOUT" || calls.Load() != 1 {
			t.Fatalf("calls=%d err=%v", calls.Load(), err)
		}
	})
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("reading deadline=%t", deadline), func(t *testing.T) {
			entered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, anthropicTestStart(""))
				w.(http.Flusher).Flush()
				entered <- struct{}{}
				<-r.Context().Done()
			}))
			defer server.Close()
			config := anthropicTestConfig(server.URL)
			if deadline {
				config.TimeoutSeconds = 0.15
			}
			provider := anthropicTestProvider(t, config)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := provider.Generate(ctx, []LLMMessage{{Role: "user", Content: "question"}}, func(string) error { return nil })
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("request did not start")
			}
			want := "LLM_TIMEOUT"
			if !deadline {
				cancel()
				want = "LLM_CANCELED"
			}
			select {
			case err := <-done:
				if errorCode(err) != want {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("read did not stop")
			}
		})
	}
	t.Run("callback cancels with own error", func(t *testing.T) {
		provider := anthropicTestProvider(t, anthropicTestConfig("http://unused/v1"))
		provider.client.Transport = anthropicTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(anthropicTestStream()))}, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		want := errors.New("callback canceled")
		_, err := provider.Generate(ctx, []LLMMessage{{Role: "user", Content: "question"}}, func(string) error { cancel(); return want })
		if err != want {
			t.Fatalf("error identity lost: %v", err)
		}
	})
}

func TestAnthropicLLMArgumentsAndRedirect(t *testing.T) {
	provider := anthropicTestProvider(t, anthropicTestConfig("http://unused/v1"))
	for _, test := range []struct {
		name     string
		messages []LLMMessage
		code     string
	}{
		{"empty", nil, "INVALID_ARGUMENT"},
		{"system only", []LLMMessage{{Role: "system", Content: "secret"}}, "INVALID_ARGUMENT"},
		{"assistant first", []LLMMessage{{Role: "assistant", Content: "answer"}}, "INVALID_ARGUMENT"},
		{"tool role", []LLMMessage{{Role: "tool", Content: "tool result"}}, "INVALID_ARGUMENT"},
		{"blank", []LLMMessage{{Role: "user", Content: " "}}, "INVALID_ARGUMENT"},
		{"UTF8", []LLMMessage{{Role: "user", Content: string([]byte{0xff})}}, "INVALID_ARGUMENT"},
		{"too many", make([]LLMMessage, 257), "INVALID_ARGUMENT"},
		{"context", []LLMMessage{{Role: "system", Content: strings.Repeat("x", 2048)}, {Role: "user", Content: strings.Repeat("\u754c", 1024)}}, "LLM_CONTEXT_LIMIT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := provider.Generate(context.Background(), test.messages, nil)
			if errorCode(err) != test.code || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error=%v", err)
			}
		})
	}
	// Consecutive user turns are accepted by the standard Messages API.
	if _, err := provider.payload([]LLMMessage{{Role: "user", Content: "a"}, {Role: "user", Content: "b"}}, false); err != nil {
		t.Fatal(err)
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	redirectProvider := anthropicTestProvider(t, anthropicTestConfig(server.URL))
	_, err := redirectProvider.Generate(context.Background(), []LLMMessage{{Role: "user", Content: "question"}}, nil)
	if errorCode(err) != "LLM_REJECTED" || redirected.Load() != 0 {
		t.Fatalf("redirects=%d err=%v", redirected.Load(), err)
	}
}

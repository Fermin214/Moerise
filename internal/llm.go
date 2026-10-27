package moerise

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	llmBodyBytes   = 8 * 1024 * 1024
	llmEventBytes  = 256 * 1024
	llmOutputBytes = 1024 * 1024
)

type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type LLMUsage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	TotalTokens  *int64 `json:"total_tokens"`
	CachedTokens *int64 `json:"cached_tokens"`
}

type LLMResult struct {
	Content      string   `json:"content"`
	Model        string   `json:"model"`
	FinishReason string   `json:"finish_reason"`
	Usage        LLMUsage `json:"usage"`
}

type LLMProvider interface {
	Generate(context.Context, []LLMMessage, func(string) error) (LLMResult, error)
	Close()
}

func (c LLMConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	bad := func(field string) error {
		return failure("INVALID_CONFIG", "Invalid configuration at llm."+field, 422, false)
	}
	if !slices.Contains([]string{"openai-compatible", "openai-responses", "anthropic"}, c.Protocol) {
		return bad("protocol")
	}
	address, err := url.Parse(configured(c.BaseURL))
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Hostname() == "" ||
		address.User != nil || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" ||
		address.Opaque != "" || strings.Contains(configured(c.BaseURL), "#") {
		return bad("base_url")
	}
	if c.APIKey == nil || *c.APIKey == "" ||
		strings.ContainsFunc(*c.APIKey, func(r rune) bool { return r < 33 || r > 126 }) {
		return bad("api_key")
	}
	if c.Model == nil || !validLLMName(*c.Model) {
		return bad("model")
	}
	if math.IsNaN(c.TimeoutSeconds) || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 110 {
		return bad("timeout_seconds")
	}
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 8 {
		return bad("max_concurrency")
	}
	if c.MaxRetries < 0 || c.MaxRetries > 3 {
		return bad("max_retries")
	}
	if c.MaxOutputTokens < 32 || c.MaxOutputTokens > 16384 {
		return bad("max_output_tokens")
	}
	if c.ContextBytes < 1024 || c.ContextBytes > 262144 {
		return bad("context_bytes")
	}
	return nil
}

func validLLMName(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) &&
		len(value) <= 200 && !strings.ContainsFunc(value, unicode.IsControl)
}

type chatCompletionsClient struct {
	config   LLMConfig
	client   *http.Client
	requests chan struct{}
}

func NewLLMProvider(config LLMConfig) (LLMProvider, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !config.Enabled {
		return nil, failure("LLM_DISABLED", "LLM is disabled", 503, false)
	}
	// Freeze pointer-backed settings so later caller edits cannot change a live client.
	base, key, model := configured(config.BaseURL), *config.APIKey, configured(config.Model)
	config.BaseURL, config.APIKey, config.Model = &base, &key, &model
	if config.Protocol == "anthropic" {
		return newAnthropicLLMProvider(config)
	}
	if config.Protocol == "openai-responses" {
		return newResponsesLLMProvider(config)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = config.MaxConcurrency
	return &chatCompletionsClient{config: config, requests: make(chan struct{}, config.MaxConcurrency),
		client: &http.Client{Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *chatCompletionsClient) Close() { c.client.CloseIdleConnections() }

func (c *chatCompletionsClient) Generate(ctx context.Context, messages []LLMMessage, callback func(string) error) (LLMResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.config.TimeoutSeconds*float64(time.Second)))
	defer cancel()
	size := 0
	if len(messages) < 1 || len(messages) > 256 {
		return LLMResult{}, failure("INVALID_ARGUMENT", "LLM requires 1 to 256 messages", 422, false)
	}
	for _, message := range messages {
		if !slices.Contains([]string{"system", "developer", "user", "assistant"}, message.Role) ||
			!utf8.ValidString(message.Content) {
			return LLMResult{}, failure("INVALID_ARGUMENT", "LLM requires text messages with a supported role", 422, false)
		}
		if len(message.Content) > c.config.ContextBytes-size {
			return LLMResult{}, failure("LLM_CONTEXT_LIMIT", "LLM context exceeds the configured byte limit", 422, false)
		}
		size += len(message.Content)
	}
	if size == 0 {
		return LLMResult{}, failure("INVALID_ARGUMENT", "LLM context must not be empty", 422, false)
	}
	select {
	case c.requests <- struct{}{}:
		defer func() { <-c.requests }()
	case <-ctx.Done():
		return LLMResult{}, llmContextError(ctx)
	}
	body := map[string]any{"model": configured(c.config.Model), "messages": messages,
		"max_completion_tokens": c.config.MaxOutputTokens, "n": 1}
	if callback != nil {
		body["stream"] = true
		body["stream_options"] = map[string]bool{"include_usage": true}
	}
	var callbackError error
	deliver := callback
	if callback != nil {
		deliver = func(text string) error {
			callbackError = callback(text)
			return callbackError
		}
	}
	payload, _ := json.Marshal(body)
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return LLMResult{}, llmContextError(ctx)
		}
		result, delivered, retryAfter, err := c.request(ctx, payload, deliver)
		if callbackError != nil {
			return LLMResult{}, callbackError
		}
		if err == nil {
			if ctx.Err() != nil {
				return LLMResult{}, llmContextError(ctx)
			}
			return result, nil
		}
		if ctx.Err() != nil {
			return LLMResult{}, llmContextError(ctx)
		}
		if delivered || attempt >= c.config.MaxRetries ||
			!slices.Contains([]string{"LLM_RATE_LIMITED", "LLM_UNAVAILABLE"}, errorCode(err)) {
			return LLMResult{}, err
		}
		timer := time.NewTimer(max(time.Second<<attempt, retryAfter))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return LLMResult{}, llmContextError(ctx)
		}
	}
}

func llmContextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return failure("LLM_TIMEOUT", "LLM request timed out", 504, false)
	}
	return failure("LLM_CANCELED", "LLM request was canceled", 499, false)
}

func llmProtocolError() error {
	return failure("LLM_PROTOCOL", "Invalid LLM response", 503, false)
}

func llmUnavailable(delivered bool) error {
	return failure("LLM_UNAVAILABLE", "LLM response could not be read", 503, !delivered)
}

func (c *chatCompletionsClient) request(ctx context.Context, body []byte, callback func(string) error) (LLMResult, bool, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(configured(c.config.BaseURL), "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return LLMResult{}, false, 0, llmProtocolError()
	}
	req.Header.Set("Authorization", "Bearer "+*c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	if callback != nil {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	response, err := c.client.Do(req)
	if err != nil {
		return LLMResult{}, false, 0, failure("LLM_UNAVAILABLE", "LLM service is unavailable", 503, true)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return LLMResult{}, false, embeddingRetryAfter(response.Header.Get("Retry-After")), llmHTTPFailure(response)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	expected := "application/json"
	if callback != nil {
		expected = "text/event-stream"
	}
	if err != nil || mediaType != expected {
		return LLMResult{}, false, 0, llmProtocolError()
	}
	if callback != nil {
		result, delivered, err := readLLMStream(ctx, response.Body, callback)
		return result, delivered, 0, err
	}
	result, err := readLLMJSON(response.Body)
	return result, false, 0, err
}

type llmWireMessage struct {
	Role         string          `json:"role"`
	Content      *string         `json:"content"`
	ToolCalls    json.RawMessage `json:"tool_calls"`
	FunctionCall json.RawMessage `json:"function_call"`
	Refusal      json.RawMessage `json:"refusal"`
	Failure      json.RawMessage `json:"failure"`
}

func llmPresent(value json.RawMessage) bool {
	return len(value) != 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func llmEmptyRefusal(value json.RawMessage) bool {
	if !llmPresent(value) {
		return true
	}
	var text string
	return json.Unmarshal(value, &text) == nil && text == ""
}

func llmEmptyToolCalls(value json.RawMessage) bool {
	if !llmPresent(value) {
		return true
	}
	var calls []json.RawMessage
	return json.Unmarshal(value, &calls) == nil && len(calls) == 0
}

func llmEmptyFunctionCall(value json.RawMessage) bool {
	if !llmPresent(value) {
		return true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(value, &fields) != nil {
		return false
	}
	for key, raw := range fields {
		if key != "name" && key != "arguments" {
			return false
		}
		var text string
		if json.Unmarshal(raw, &text) != nil || text != "" {
			return false
		}
	}
	return true
}

func (m llmWireMessage) valid() bool {
	return (m.Role == "" || m.Role == "assistant") && llmEmptyToolCalls(m.ToolCalls) &&
		llmEmptyFunctionCall(m.FunctionCall) && llmEmptyRefusal(m.Refusal) && !llmPresent(m.Failure)
}

type llmWireUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
	PromptDetails    *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u llmWireUsage) usage() (LLMUsage, error) {
	result := LLMUsage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
	if u.PromptDetails != nil {
		result.CachedTokens = u.PromptDetails.CachedTokens
	}
	for _, value := range []*int64{result.InputTokens, result.OutputTokens, result.TotalTokens, result.CachedTokens} {
		if value != nil && *value < 0 {
			return LLMUsage{}, llmProtocolError()
		}
	}
	return result, nil
}

type llmWireResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Index        *int            `json:"index"`
		Message      *llmWireMessage `json:"message"`
		Delta        *llmWireMessage `json:"delta"`
		FinishReason *string         `json:"finish_reason"`
		Failure      json.RawMessage `json:"failure"`
	} `json:"choices"`
	Usage   *llmWireUsage   `json:"usage"`
	Error   json.RawMessage `json:"error"`
	Failure json.RawMessage `json:"failure"`
}

func decodeLLMResponse(data []byte) (llmWireResponse, error) {
	var response llmWireResponse
	if !utf8.Valid(data) || json.Unmarshal(data, &response) != nil ||
		llmPresent(response.Error) || llmPresent(response.Failure) {
		return llmWireResponse{}, llmProtocolError()
	}
	return response, nil
}

func llmFinishError(reason string) error {
	if reason == "length" {
		return failure("LLM_OUTPUT_LIMIT", "LLM output reached the token limit", 503, false)
	}
	return failure("LLM_REJECTED", "LLM generation did not finish normally", 503, false)
}

func readLLMJSON(reader io.Reader) (LLMResult, error) {
	data, err := io.ReadAll(io.LimitReader(reader, llmBodyBytes+1))
	if len(data) > llmBodyBytes {
		return LLMResult{}, llmProtocolError()
	}
	if err != nil {
		return LLMResult{}, llmUnavailable(false)
	}
	response, err := decodeLLMResponse(data)
	if err != nil {
		return LLMResult{}, err
	}
	if !validLLMName(response.Model) || len(response.Choices) != 1 {
		return LLMResult{}, llmProtocolError()
	}
	choice := response.Choices[0]
	if choice.Index == nil || *choice.Index != 0 || choice.Message == nil || choice.Delta != nil ||
		!choice.Message.valid() || choice.Message.Content == nil || choice.FinishReason == nil ||
		llmPresent(choice.Failure) {
		return LLMResult{}, llmProtocolError()
	}
	if *choice.FinishReason != "stop" {
		return LLMResult{}, llmFinishError(*choice.FinishReason)
	}
	content := *choice.Message.Content
	if len(content) > llmOutputBytes {
		return LLMResult{}, failure("LLM_OUTPUT_LIMIT", "LLM output exceeds the byte limit", 503, false)
	}
	if strings.TrimSpace(content) == "" {
		return LLMResult{}, llmProtocolError()
	}
	result := LLMResult{Content: content, Model: response.Model, FinishReason: "stop"}
	if response.Usage != nil {
		result.Usage, err = response.Usage.usage()
		if err != nil {
			return LLMResult{}, err
		}
	}
	return result, nil
}

func readLLMStream(ctx context.Context, reader io.Reader, callback func(string) error) (LLMResult, bool, error) {
	limited := &io.LimitedReader{R: reader, N: llmBodyBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), llmEventBytes+1)
	var content strings.Builder
	var event []byte
	var result LLMResult
	delivered, stopped, usageSeen, hasData := false, false, false, false
	eventSize := 0
	consume := func() (bool, error) {
		if !hasData {
			return false, nil
		}
		data := bytes.TrimSuffix(event, []byte("\n"))
		if bytes.Equal(data, []byte("[DONE]")) {
			if !stopped || strings.TrimSpace(content.String()) == "" {
				return false, llmProtocolError()
			}
			return true, nil
		}
		response, err := decodeLLMResponse(data)
		if err != nil {
			return false, err
		}
		if response.Model != "" {
			if !validLLMName(response.Model) || (result.Model != "" && response.Model != result.Model) {
				return false, llmProtocolError()
			}
			result.Model = response.Model
		}
		if result.Model == "" {
			return false, llmProtocolError()
		}
		if response.Usage != nil {
			if usageSeen {
				return false, llmProtocolError()
			}
			result.Usage, err = response.Usage.usage()
			if err != nil {
				return false, err
			}
			usageSeen = true
		}
		if len(response.Choices) == 0 {
			if !stopped || response.Usage == nil {
				return false, llmProtocolError()
			}
			return false, nil
		}
		if stopped || len(response.Choices) != 1 {
			return false, llmProtocolError()
		}
		choice := response.Choices[0]
		if choice.Index == nil || *choice.Index != 0 || choice.Delta == nil || choice.Message != nil ||
			!choice.Delta.valid() || llmPresent(choice.Failure) {
			return false, llmProtocolError()
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			if *choice.FinishReason != "stop" {
				return false, llmFinishError(*choice.FinishReason)
			}
			stopped, result.FinishReason = true, "stop"
		}
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			text := *choice.Delta.Content
			if len(text) > llmOutputBytes-content.Len() {
				return false, failure("LLM_OUTPUT_LIMIT", "LLM output exceeds the byte limit", 503, false)
			}
			if ctx.Err() != nil {
				return false, llmContextError(ctx)
			}
			// Mark delivery before invoking user code: a failing callback may have sent bytes.
			delivered = true
			if err := callback(text); err != nil {
				return false, err
			}
			content.WriteString(text)
		}
		return false, nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return LLMResult{}, delivered, llmContextError(ctx)
		}
		if limited.N <= 0 {
			return LLMResult{}, delivered, llmProtocolError()
		}
		line := scanner.Bytes()
		if !utf8.Valid(line) {
			return LLMResult{}, delivered, llmProtocolError()
		}
		if len(line) == 0 {
			done, err := consume()
			if err != nil {
				return LLMResult{}, delivered, err
			}
			if done {
				result.Content = content.String()
				return result, delivered, nil
			}
			event, hasData, eventSize = event[:0], false, 0
			continue
		}
		eventSize += len(line) + 1
		if eventSize > llmEventBytes {
			return LLMResult{}, delivered, llmProtocolError()
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		if bytes.Equal(field, []byte("event")) && !bytes.Equal(value, []byte("message")) && len(value) != 0 {
			return LLMResult{}, delivered, llmProtocolError()
		}
		if bytes.Equal(field, []byte("data")) {
			event = append(event, value...)
			event = append(event, '\n')
			hasData = true
		}
	}
	if limited.N <= 0 || errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return LLMResult{}, delivered, llmProtocolError()
	}
	if scanner.Err() != nil {
		return LLMResult{}, delivered, llmUnavailable(delivered)
	}
	// A complete terminal marker may end directly at EOF; other EOFs are truncation.
	done, err := consume()
	if err != nil {
		return LLMResult{}, delivered, err
	}
	if !done {
		return LLMResult{}, delivered, llmProtocolError()
	}
	result.Content = content.String()
	return result, delivered, nil
}

type LLMCheck struct {
	Status       string   `json:"status"`
	Protocol     string   `json:"protocol"`
	Model        string   `json:"model"`
	FinishReason string   `json:"finish_reason"`
	Usage        LLMUsage `json:"usage"`
	DurationMS   int64    `json:"duration_ms"`
}

func CheckLLM(ctx context.Context, config LLMConfig) (LLMCheck, error) {
	provider, err := NewLLMProvider(config)
	if err != nil {
		return LLMCheck{}, err
	}
	defer provider.Close()
	started := time.Now()
	result, err := provider.Generate(ctx, []LLMMessage{
		{Role: "user", Content: "A fictional gardener planted basil on an imaginary balcony. Reply with OK."},
	}, func(string) error { return nil })
	if err != nil {
		return LLMCheck{}, err
	}
	return LLMCheck{Status: "ok", Protocol: config.Protocol, Model: result.Model, FinishReason: result.FinishReason, Usage: result.Usage,
		DurationMS: time.Since(started).Milliseconds()}, nil
}

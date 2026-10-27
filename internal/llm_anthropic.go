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
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type anthropicLLMClient struct {
	config   LLMConfig
	client   *http.Client
	requests chan struct{}
}

func newAnthropicLLMProvider(config LLMConfig) (LLMProvider, error) {
	base, key, model := configured(config.BaseURL), configured(config.APIKey), configured(config.Model)
	config.BaseURL, config.APIKey, config.Model = &base, &key, &model
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = config.MaxConcurrency
	return &anthropicLLMClient{config: config, requests: make(chan struct{}, config.MaxConcurrency),
		client: &http.Client{Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *anthropicLLMClient) Close() { c.client.CloseIdleConnections() }

type anthropicText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content []anthropicText `json:"content"`
}

func (c *anthropicLLMClient) payload(messages []LLMMessage, stream bool) ([]byte, error) {
	if len(messages) == 0 || len(messages) > 256 {
		return nil, failure("INVALID_ARGUMENT", "LLM requires 1 to 256 messages", 422, false)
	}
	body := struct {
		Model     string             `json:"model"`
		MaxTokens int                `json:"max_tokens"`
		Messages  []anthropicMessage `json:"messages"`
		System    []anthropicText    `json:"system,omitempty"`
		Stream    bool               `json:"stream,omitempty"`
	}{Model: configured(c.config.Model), MaxTokens: c.config.MaxOutputTokens, Stream: stream}
	size := 0
	for _, message := range messages {
		if !slices.Contains([]string{"system", "developer", "user", "assistant"}, message.Role) ||
			!utf8.ValidString(message.Content) || strings.TrimSpace(message.Content) == "" {
			return nil, failure("INVALID_ARGUMENT", "LLM requires nonempty text messages with a supported role", 422, false)
		}
		if len(message.Content) > c.config.ContextBytes-size {
			return nil, failure("LLM_CONTEXT_LIMIT", "LLM context exceeds the configured byte limit", 422, false)
		}
		size += len(message.Content)
		text := anthropicText{Type: "text", Text: message.Content}
		if message.Role == "system" || message.Role == "developer" {
			// Messages has one system tier; keep developer instructions out of conversation turns.
			body.System = append(body.System, text)
		} else {
			body.Messages = append(body.Messages, anthropicMessage{Role: message.Role, Content: []anthropicText{text}})
		}
	}
	if len(body.Messages) == 0 || body.Messages[0].Role != "user" {
		return nil, failure("INVALID_ARGUMENT", "Anthropic requires a user message before assistant messages", 422, false)
	}
	return json.Marshal(body)
}

func (c *anthropicLLMClient) Generate(ctx context.Context, messages []LLMMessage, callback func(string) error) (LLMResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.config.TimeoutSeconds*float64(time.Second)))
	defer cancel()
	payload, err := c.payload(messages, callback != nil)
	if err != nil {
		return LLMResult{}, err
	}
	select {
	case c.requests <- struct{}{}:
		defer func() { <-c.requests }()
	case <-ctx.Done():
		return LLMResult{}, llmContextError(ctx)
	}
	callbackFailed := false
	var emit func(string) error
	if callback != nil {
		emit = func(text string) error {
			err := callback(text)
			callbackFailed = err != nil
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return LLMResult{}, llmContextError(ctx)
		}
		result, noReplay, retryAfter, err := c.request(ctx, payload, emit)
		// Callback errors belong to the caller, including when it also canceled ctx.
		if callbackFailed {
			return LLMResult{}, err
		}
		if ctx.Err() != nil {
			return LLMResult{}, llmContextError(ctx)
		}
		if err == nil {
			return result, nil
		}
		if noReplay || attempt >= c.config.MaxRetries ||
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

func (c *anthropicLLMClient) request(ctx context.Context, body []byte, callback func(string) error) (LLMResult, bool, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(configured(c.config.BaseURL), "/")+"/messages", bytes.NewReader(body))
	if err != nil {
		return LLMResult{}, false, 0, llmProtocolError()
	}
	req.Header.Set("x-api-key", *c.config.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
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
		result, noReplay, err := readAnthropicLLMStream(ctx, response.Body, callback)
		return result, noReplay, 0, err
	}
	result, err := readAnthropicLLMJSON(response.Body)
	return result, false, 0, err
}

type anthropicUsage struct {
	InputTokens         *int64 `json:"input_tokens"`
	OutputTokens        *int64 `json:"output_tokens"`
	TotalTokens         *int64 `json:"total_tokens"`
	CacheReadTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationTokens *int64 `json:"cache_creation_input_tokens"`
}

func (u anthropicUsage) merge(result *LLMUsage) error {
	for _, value := range []*int64{u.InputTokens, u.OutputTokens, u.TotalTokens, u.CacheReadTokens, u.CacheCreationTokens} {
		if value != nil && *value < 0 {
			return llmProtocolError()
		}
	}
	if u.InputTokens != nil {
		result.InputTokens = u.InputTokens
	}
	if u.OutputTokens != nil {
		result.OutputTokens = u.OutputTokens
	}
	if u.TotalTokens != nil {
		result.TotalTokens = u.TotalTokens
	}
	if u.CacheReadTokens != nil {
		result.CachedTokens = u.CacheReadTokens
	}
	return nil
}

func anthropicTotal(usage *LLMUsage) {
	if usage.TotalTokens == nil && usage.InputTokens != nil && usage.OutputTokens != nil &&
		*usage.InputTokens <= math.MaxInt64-*usage.OutputTokens {
		total := *usage.InputTokens + *usage.OutputTokens
		usage.TotalTokens = &total
	}
}

type anthropicBlock struct {
	Type    string          `json:"type"`
	Text    *string         `json:"text"`
	Refusal json.RawMessage `json:"refusal"`
}

type anthropicResponse struct {
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Model        string           `json:"model"`
	Content      []anthropicBlock `json:"content"`
	StopReason   *string          `json:"stop_reason"`
	StopSequence json.RawMessage  `json:"stop_sequence"`
	Usage        *anthropicUsage  `json:"usage"`
	Error        json.RawMessage  `json:"error"`
	Refusal      json.RawMessage  `json:"refusal"`
}

func anthropicFinish(reason *string) error {
	if reason == nil || *reason == "" {
		return llmProtocolError()
	}
	if *reason == "end_turn" {
		return nil
	}
	if *reason == "max_tokens" {
		return llmFinishError("length")
	}
	return llmFinishError(*reason)
}

func readAnthropicLLMJSON(reader io.Reader) (LLMResult, error) {
	data, err := io.ReadAll(io.LimitReader(reader, llmBodyBytes+1))
	if len(data) > llmBodyBytes {
		return LLMResult{}, llmProtocolError()
	}
	if err != nil {
		return LLMResult{}, llmUnavailable(false)
	}
	var response anthropicResponse
	if !utf8.Valid(data) || json.Unmarshal(data, &response) != nil ||
		response.Type != "message" || response.Role != "assistant" || !validLLMName(response.Model) ||
		llmPresent(response.Error) || llmPresent(response.Refusal) || llmPresent(response.StopSequence) {
		return LLMResult{}, llmProtocolError()
	}
	if err := anthropicFinish(response.StopReason); err != nil {
		return LLMResult{}, err
	}
	var content strings.Builder
	for _, block := range response.Content {
		if block.Type != "text" || block.Text == nil || llmPresent(block.Refusal) {
			return LLMResult{}, llmProtocolError()
		}
		if len(*block.Text) > llmOutputBytes-content.Len() {
			return LLMResult{}, failure("LLM_OUTPUT_LIMIT", "LLM output exceeds the byte limit", 503, false)
		}
		content.WriteString(*block.Text)
	}
	if strings.TrimSpace(content.String()) == "" {
		return LLMResult{}, llmProtocolError()
	}
	result := LLMResult{Content: content.String(), Model: response.Model, FinishReason: "end_turn"}
	if response.Usage != nil {
		if err := response.Usage.merge(&result.Usage); err != nil {
			return LLMResult{}, err
		}
	}
	anthropicTotal(&result.Usage)
	return result, nil
}

type anthropicEvent struct {
	Type         string             `json:"type"`
	Message      *anthropicResponse `json:"message"`
	Index        *int               `json:"index"`
	ContentBlock *anthropicBlock    `json:"content_block"`
	Delta        *struct {
		Type         string          `json:"type"`
		Text         *string         `json:"text"`
		StopReason   *string         `json:"stop_reason"`
		StopSequence json.RawMessage `json:"stop_sequence"`
		Refusal      json.RawMessage `json:"refusal"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *struct {
		Type string `json:"type"`
	} `json:"error"`
	Refusal json.RawMessage `json:"refusal"`
}

func anthropicStreamError(kind string, noReplay bool) error {
	switch kind {
	case "rate_limit_error":
		return failure("LLM_RATE_LIMITED", "LLM service rate limit exceeded", 503, !noReplay)
	case "overloaded_error", "api_error":
		return failure("LLM_UNAVAILABLE", "LLM service request failed", 503, !noReplay)
	case "authentication_error", "permission_error":
		return failure("LLM_AUTH", "LLM service rejected the configured credential", 503, false)
	default:
		return failure("LLM_REJECTED", "LLM service rejected the request", 503, false)
	}
}

func readAnthropicLLMStream(ctx context.Context, reader io.Reader, callback func(string) error) (LLMResult, bool, error) {
	limited := &io.LimitedReader{R: reader, N: llmBodyBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), llmEventBytes+1)
	var result LLMResult
	var content strings.Builder
	var data []byte
	name, eventSize := "", 0
	started, open, finalPhase, finished, delivered := false, false, false, false, false
	nextIndex := 0
	emit := func(text string) error {
		if len(text) > llmOutputBytes-content.Len() {
			return failure("LLM_OUTPUT_LIMIT", "LLM output exceeds the byte limit", 503, false)
		}
		if ctx.Err() != nil {
			return llmContextError(ctx)
		}
		if text != "" {
			delivered = true
			if err := callback(text); err != nil {
				return err
			}
			content.WriteString(text)
		}
		return nil
	}
	consume := func() (bool, error) {
		if len(data) == 0 {
			return false, nil
		}
		var event anthropicEvent
		if json.Unmarshal(data, &event) != nil || event.Type == "" || name != event.Type ||
			llmPresent(event.Refusal) || (event.Type != "error" && event.Error != nil) {
			return false, llmProtocolError()
		}
		switch event.Type {
		case "ping":
			return false, nil
		case "error":
			if event.Error == nil || event.Error.Type == "" {
				return false, llmProtocolError()
			}
			return false, anthropicStreamError(event.Error.Type, delivered || finalPhase)
		case "message_start":
			message := event.Message
			if started || message == nil || message.Type != "message" || message.Role != "assistant" ||
				!validLLMName(message.Model) || message.Content == nil || len(message.Content) != 0 ||
				message.StopReason != nil || llmPresent(message.StopSequence) ||
				llmPresent(message.Error) || llmPresent(message.Refusal) {
				return false, llmProtocolError()
			}
			started, result.Model = true, message.Model
			if message.Usage != nil {
				if err := message.Usage.merge(&result.Usage); err != nil {
					return false, err
				}
			}
		case "content_block_start":
			if !started || open || finalPhase || event.Index == nil || *event.Index != nextIndex ||
				event.ContentBlock == nil || event.ContentBlock.Type != "text" ||
				event.ContentBlock.Text == nil || llmPresent(event.ContentBlock.Refusal) {
				return false, llmProtocolError()
			}
			open = true
			if err := emit(*event.ContentBlock.Text); err != nil {
				return false, err
			}
		case "content_block_delta":
			if !started || !open || finalPhase || event.Index == nil || *event.Index != nextIndex ||
				event.Delta == nil || event.Delta.Type != "text_delta" || event.Delta.Text == nil ||
				event.Delta.StopReason != nil || llmPresent(event.Delta.StopSequence) || llmPresent(event.Delta.Refusal) {
				return false, llmProtocolError()
			}
			if err := emit(*event.Delta.Text); err != nil {
				return false, err
			}
		case "content_block_stop":
			if !started || !open || finalPhase || event.Index == nil || *event.Index != nextIndex {
				return false, llmProtocolError()
			}
			open = false
			nextIndex++
		case "message_delta":
			if !started || open || event.Delta == nil || event.Delta.Type != "" ||
				event.Delta.Text != nil || llmPresent(event.Delta.StopSequence) || llmPresent(event.Delta.Refusal) {
				return false, llmProtocolError()
			}
			finalPhase = true
			if event.Delta.StopReason != nil {
				if finished {
					return false, llmProtocolError()
				}
				if err := anthropicFinish(event.Delta.StopReason); err != nil {
					return false, err
				}
				finished, result.FinishReason = true, "end_turn"
			}
			if event.Usage != nil {
				if err := event.Usage.merge(&result.Usage); err != nil {
					return false, err
				}
			}
		case "message_stop":
			if !started || open || !finished || strings.TrimSpace(content.String()) == "" {
				return false, llmProtocolError()
			}
			anthropicTotal(&result.Usage)
			return true, nil
		default:
			return false, llmProtocolError()
		}
		return false, nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return LLMResult{}, true, llmContextError(ctx)
		}
		if limited.N <= 0 {
			return LLMResult{}, true, llmProtocolError()
		}
		line := scanner.Bytes()
		if !utf8.Valid(line) {
			return LLMResult{}, true, llmProtocolError()
		}
		if len(line) == 0 {
			done, err := consume()
			if err != nil {
				return LLMResult{}, delivered || finalPhase, err
			}
			if done {
				result.Content = content.String()
				return result, true, nil
			}
			data, name, eventSize = data[:0], "", 0
			continue
		}
		eventSize += len(line) + 1
		if eventSize > llmEventBytes {
			return LLMResult{}, true, llmProtocolError()
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			if name != "" {
				return LLMResult{}, true, llmProtocolError()
			}
			name = string(value)
		case "data":
			data = append(data, value...)
			data = append(data, '\n')
		}
	}
	if ctx.Err() != nil {
		return LLMResult{}, true, llmContextError(ctx)
	}
	if limited.N <= 0 || errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return LLMResult{}, true, llmProtocolError()
	}
	if scanner.Err() != nil {
		// An interrupted SSE is never replayed, even before the first text delta.
		return LLMResult{}, true, llmUnavailable(true)
	}
	done, err := consume()
	if err != nil {
		return LLMResult{}, delivered || finalPhase, err
	}
	if !done {
		return LLMResult{}, true, llmProtocolError()
	}
	result.Content = content.String()
	return result, true, nil
}

package moerise

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type responsesLLMClient struct {
	config   LLMConfig
	client   *http.Client
	requests chan struct{}
}

func newResponsesLLMProvider(config LLMConfig) (LLMProvider, error) {
	base, key, model := configured(config.BaseURL), *config.APIKey, configured(config.Model)
	config.BaseURL, config.APIKey, config.Model = &base, &key, &model
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = config.MaxConcurrency
	return &responsesLLMClient{config: config, requests: make(chan struct{}, config.MaxConcurrency),
		client: &http.Client{Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *responsesLLMClient) Close() { c.client.CloseIdleConnections() }

func (c *responsesLLMClient) Generate(ctx context.Context, messages []LLMMessage, callback func(string) error) (LLMResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.config.TimeoutSeconds*float64(time.Second)))
	defer cancel()
	size := 0
	if len(messages) == 0 || len(messages) > 256 {
		return LLMResult{}, failure("INVALID_ARGUMENT", "LLM requires 1 to 256 messages", 422, false)
	}
	for _, message := range messages {
		if !slices.Contains([]string{"system", "developer", "user", "assistant"}, message.Role) ||
			!utf8.ValidString(message.Content) {
			return LLMResult{}, failure("INVALID_ARGUMENT", "Invalid LLM text message", 422, false)
		}
		size += len(message.Content)
		if size > c.config.ContextBytes {
			return LLMResult{}, failure("LLM_CONTEXT_LIMIT", "LLM context exceeds the configured byte limit", 422, false)
		}
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
	body, _ := json.Marshal(map[string]any{"model": configured(c.config.Model), "input": messages,
		"max_output_tokens": c.config.MaxOutputTokens, "stream": callback != nil, "store": false})
	var callbackError error
	deliver := callback
	if callback != nil {
		deliver = func(text string) error {
			callbackError = callback(text)
			return callbackError
		}
	}
	for attempt := 0; ; attempt++ {
		result, delivered, retryAfter, err := c.request(ctx, body, deliver)
		if callbackError != nil {
			return LLMResult{}, callbackError
		}
		if ctx.Err() != nil {
			return LLMResult{}, llmContextError(ctx)
		}
		if err == nil {
			return result, nil
		}
		if delivered || attempt >= c.config.MaxRetries ||
			!slices.Contains([]string{"LLM_UNAVAILABLE", "LLM_RATE_LIMITED"}, errorCode(err)) {
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

func (c *responsesLLMClient) request(ctx context.Context, body []byte, callback func(string) error) (LLMResult, bool, time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(configured(c.config.BaseURL), "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return LLMResult{}, false, 0, llmProtocolError()
	}
	request.Header.Set("Authorization", "Bearer "+*c.config.APIKey)
	request.Header.Set("Content-Type", "application/json")
	media := "application/json"
	if callback != nil {
		media = "text/event-stream"
	}
	request.Header.Set("Accept", media)
	response, err := c.client.Do(request)
	if err != nil {
		return LLMResult{}, false, 0, upstreamError("LLM_UNAVAILABLE", "LLM service is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return LLMResult{}, false, embeddingRetryAfter(response.Header.Get("Retry-After")), llmHTTPFailure(response)
	}
	actual, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || actual != media {
		return LLMResult{}, false, 0, llmProtocolError()
	}
	if callback != nil {
		result, delivered, err := readResponsesStream(ctx, response.Body, callback)
		return result, delivered, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, llmBodyBytes+1))
	if err != nil {
		return LLMResult{}, false, 0, llmUnavailable(false)
	}
	if len(data) > llmBodyBytes {
		return LLMResult{}, false, 0, llmProtocolError()
	}
	result, err := decodeResponsesResult(data)
	return result, false, 0, err
}

type responsesPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type responsesItem struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Status  string          `json:"status"`
	Content []responsesPart `json:"content"`
}

type responsesWire struct {
	Model      string          `json:"model"`
	Status     string          `json:"status"`
	Output     []responsesItem `json:"output"`
	Error      json.RawMessage `json:"error"`
	Incomplete *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		Input   *int64 `json:"input_tokens"`
		Output  *int64 `json:"output_tokens"`
		Total   *int64 `json:"total_tokens"`
		Details *struct {
			Cached *int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

func decodeResponsesResult(data []byte) (LLMResult, error) {
	var response responsesWire
	if !utf8.Valid(data) || json.Unmarshal(data, &response) != nil {
		return LLMResult{}, llmProtocolError()
	}
	if response.Status == "failed" {
		var providerError llmProviderError
		if llmPresent(response.Error) && json.Unmarshal(response.Error, &providerError) != nil {
			return LLMResult{}, llmProtocolError()
		}
		return LLMResult{}, llmProviderFailure(providerError.Code)
	}
	if !validLLMName(response.Model) {
		return LLMResult{}, llmProtocolError()
	}
	if response.Status == "incomplete" && response.Incomplete != nil && response.Incomplete.Reason == "max_output_tokens" {
		return LLMResult{}, llmFinishError("length")
	}
	if response.Status != "completed" || llmPresent(response.Error) || response.Incomplete != nil {
		return LLMResult{}, failure("LLM_REJECTED", "LLM response did not complete normally", 503, false)
	}
	var content strings.Builder
	for _, item := range response.Output {
		if item.Type == "reasoning" {
			continue
		}
		if item.Type != "message" || item.Role != "assistant" || item.Status != "completed" {
			return LLMResult{}, failure("LLM_REJECTED", "LLM returned unsupported output", 503, false)
		}
		for _, part := range item.Content {
			if part.Type != "output_text" || part.Refusal != "" {
				return LLMResult{}, failure("LLM_REJECTED", "LLM returned unsupported content", 503, false)
			}
			if len(part.Text) > llmOutputBytes-content.Len() {
				return LLMResult{}, failure("LLM_OUTPUT_LIMIT", "LLM output exceeds the byte limit", 503, false)
			}
			content.WriteString(part.Text)
		}
	}
	if strings.TrimSpace(content.String()) == "" {
		return LLMResult{}, llmProtocolError()
	}
	result := LLMResult{Content: content.String(), Model: response.Model, FinishReason: "stop"}
	if usage := response.Usage; usage != nil {
		result.Usage = LLMUsage{InputTokens: usage.Input, OutputTokens: usage.Output, TotalTokens: usage.Total}
		if usage.Details != nil {
			result.Usage.CachedTokens = usage.Details.Cached
		}
		for _, count := range []*int64{result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.TotalTokens, result.Usage.CachedTokens} {
			if count != nil && *count < 0 {
				return LLMResult{}, llmProtocolError()
			}
		}
	}
	return result, nil
}

type responsesStreamEvent struct {
	Type         string          `json:"type"`
	Code         string          `json:"code"`
	Response     json.RawMessage `json:"response"`
	OutputIndex  *int            `json:"output_index"`
	ContentIndex *int            `json:"content_index"`
	ItemID       string          `json:"item_id"`
	Item         *responsesItem  `json:"item"`
	Part         *responsesPart  `json:"part"`
	Delta        string          `json:"delta"`
	Text         string          `json:"text"`
}

func readResponsesStream(ctx context.Context, reader io.Reader, callback func(string) error) (out LLMResult, delivered bool, err error) {
	var callbackError error
	defer func() {
		if delivered && err != nil && callbackError == nil {
			var service *ServiceError
			if errors.As(err, &service) {
				copy := *service
				copy.Retryable = false
				err = &copy
			}
		}
	}()
	limited := &io.LimitedReader{R: reader, N: llmBodyBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), llmEventBytes+1)
	items := map[int]responsesItem{}
	finishedItems := map[int]bool{}
	type partKey struct{ output, content int }
	parts := map[partKey]string{}
	closed := map[partKey]bool{}
	var text strings.Builder
	var data []byte
	eventName := ""
	eventSize := 0
	emit := func(value string) error {
		if len(value) > llmOutputBytes-text.Len() {
			return failure("LLM_OUTPUT_LIMIT", "LLM output exceeds the byte limit", 503, false)
		}
		if ctx.Err() != nil {
			return llmContextError(ctx)
		}
		if value != "" {
			delivered = true
			if callbackError = callback(value); callbackError != nil {
				return callbackError
			}
			text.WriteString(value)
		}
		return nil
	}
	consume := func() (*LLMResult, error) {
		if len(data) == 0 {
			return nil, nil
		}
		var event responsesStreamEvent
		payload := bytes.TrimSuffix(data, []byte("\n"))
		if !utf8.Valid(payload) || json.Unmarshal(payload, &event) != nil ||
			event.Type == "" || (eventName != "" && eventName != event.Type) {
			return nil, llmProtocolError()
		}
		switch event.Type {
		case "response.created", "response.in_progress":
			return nil, nil
		case "response.output_item.added":
			if event.OutputIndex == nil || *event.OutputIndex < 0 || event.Item == nil ||
				event.Item.ID == "" || items[*event.OutputIndex].ID != "" {
				return nil, llmProtocolError()
			}
			item := *event.Item
			for _, existing := range items {
				if existing.ID == item.ID {
					return nil, llmProtocolError()
				}
			}
			if item.Type != "reasoning" && (item.Type != "message" || item.Role != "assistant") {
				return nil, failure("LLM_REJECTED", "LLM returned unsupported output", 503, false)
			}
			items[*event.OutputIndex] = item
		case "response.content_part.added", "response.output_text.delta", "response.output_text.done", "response.content_part.done",
			"response.reasoning_text.delta", "response.reasoning_text.done":
			if event.OutputIndex == nil || event.ContentIndex == nil || *event.OutputIndex < 0 || *event.ContentIndex < 0 {
				return nil, llmProtocolError()
			}
			key := partKey{*event.OutputIndex, *event.ContentIndex}
			item, exists := items[key.output]
			if !exists || item.ID != event.ItemID || finishedItems[key.output] {
				return nil, llmProtocolError()
			}
			previous, exists := parts[key]
			if item.Type == "reasoning" {
				switch event.Type {
				case "response.content_part.added":
					if exists || event.Part == nil || event.Part.Type != "reasoning_text" || event.Part.Refusal != "" {
						return nil, llmProtocolError()
					}
					parts[key] = ""
				case "response.content_part.done":
					if !exists || closed[key] || event.Part == nil || event.Part.Type != "reasoning_text" || event.Part.Refusal != "" {
						return nil, llmProtocolError()
					}
					closed[key] = true
				case "response.reasoning_text.delta", "response.reasoning_text.done":
					if !exists || closed[key] {
						return nil, llmProtocolError()
					}
				default:
					return nil, llmProtocolError()
				}
				break
			}
			switch event.Type {
			case "response.content_part.added":
				if exists || event.Part == nil || event.Part.Type != "output_text" || event.Part.Refusal != "" {
					return nil, llmProtocolError()
				}
				if err := emit(event.Part.Text); err != nil {
					return nil, err
				}
				parts[key] = event.Part.Text
			case "response.output_text.delta":
				if !exists || closed[key] {
					return nil, llmProtocolError()
				}
				if err := emit(event.Delta); err != nil {
					return nil, err
				}
				parts[key] = previous + event.Delta
			case "response.output_text.done":
				if !exists || closed[key] || event.Text != previous {
					return nil, llmProtocolError()
				}
			case "response.content_part.done":
				if !exists || closed[key] || event.Part == nil || event.Part.Type != "output_text" ||
					event.Part.Text != previous || event.Part.Refusal != "" {
					return nil, llmProtocolError()
				}
				closed[key] = true
			default:
				return nil, llmProtocolError()
			}
		case "response.output_item.done":
			if event.OutputIndex == nil || event.Item == nil || event.Item.ID == "" {
				return nil, llmProtocolError()
			}
			item, exists := items[*event.OutputIndex]
			if !exists || item.ID != event.Item.ID || item.Type != event.Item.Type ||
				item.Role != event.Item.Role || finishedItems[*event.OutputIndex] {
				return nil, llmProtocolError()
			}
			for key := range parts {
				if key.output == *event.OutputIndex && !closed[key] {
					return nil, llmProtocolError()
				}
			}
			finishedItems[*event.OutputIndex] = true
		case "response.output_text.annotation.added",
			"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
			"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
			return nil, nil
		case "response.completed":
			result, err := decodeResponsesResult(event.Response)
			if err != nil {
				return nil, err
			}
			if result.Content != text.String() || len(parts) == 0 {
				return nil, llmProtocolError()
			}
			for key := range parts {
				if !closed[key] {
					return nil, llmProtocolError()
				}
			}
			for index := range items {
				if !finishedItems[index] {
					return nil, llmProtocolError()
				}
			}
			return &result, nil
		case "response.incomplete":
			_, err := decodeResponsesResult(event.Response)
			if err == nil {
				err = llmProtocolError()
			}
			return nil, err
		case "response.failed":
			var response responsesWire
			if json.Unmarshal(event.Response, &response) != nil || response.Status != "failed" {
				return nil, llmProtocolError()
			}
			_, err := decodeResponsesResult(event.Response)
			return nil, err
		case "error":
			return nil, llmProviderFailure(event.Code)
		default:
			return nil, llmProtocolError()
		}
		return nil, nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return LLMResult{}, delivered, llmContextError(ctx)
		}
		line := scanner.Bytes()
		eventSize += len(line) + 1
		if limited.N <= 0 || eventSize > llmEventBytes || !utf8.Valid(line) {
			return LLMResult{}, delivered, llmProtocolError()
		}
		if len(line) == 0 {
			result, err := consume()
			if err != nil {
				return LLMResult{}, delivered, err
			}
			if result != nil {
				return *result, delivered, nil
			}
			data, eventName, eventSize = data[:0], "", 0
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			eventName = string(value)
		case "data":
			data = append(append(data, value...), '\n')
		}
	}
	if scanner.Err() != nil {
		return LLMResult{}, delivered, llmUnavailable(delivered)
	}
	result, err := consume()
	if err != nil {
		return LLMResult{}, delivered, err
	}
	if result == nil {
		return LLMResult{}, delivered, llmProtocolError()
	}
	return *result, delivered, nil
}

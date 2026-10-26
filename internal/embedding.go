package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type EmbeddingPurpose string

const (
	EmbeddingDocument EmbeddingPurpose = "document"
	EmbeddingQuery    EmbeddingPurpose = "query"
)

type EmbeddingResult struct {
	Vectors     [][]float32
	TotalTokens int64
}

type EmbeddingProvider interface {
	Embed(context.Context, []string, EmbeddingPurpose) (EmbeddingResult, error)
	Close()
}

func (c EmbeddingConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	bad := func(field string) error { return fmt.Errorf("invalid configuration at embedding.%s", field) }
	if c.Provider != "bailian" {
		return bad("provider")
	}
	if c.Protocol != "dashscope" {
		return bad("protocol")
	}
	address, err := url.Parse(configured(c.BaseURL))
	if err != nil || (address.Scheme != "https" && address.Scheme != "http") || address.Hostname() == "" ||
		address.User != nil || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" ||
		address.RawPath != "" || !strings.HasSuffix(strings.TrimRight(address.Path, "/"), "/api/v1") {
		return bad("base_url")
	}
	key := configured(c.APIKey)
	if key == "" || strings.ContainsFunc(key, func(r rune) bool { return r < 33 || r > 126 }) {
		return bad("api_key")
	}
	if configured(c.Model) != "qwen3.7-text-embedding" {
		return bad("model")
	}
	if !slices.Contains([]int{256, 512, 768, 1024, 1536, 2048, 2560}, c.Dimensions) {
		return bad("dimensions")
	}
	if c.BatchSize < 1 || c.BatchSize > 20 {
		return bad("batch_size")
	}
	if math.IsNaN(c.TimeoutSeconds) || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 120 {
		return bad("timeout_seconds")
	}
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 8 {
		return bad("max_concurrency")
	}
	if c.MaxRetries < 0 || c.MaxRetries > 3 {
		return bad("max_retries")
	}
	if instruction := configured(c.QueryInstruction); len(instruction) > 4096 || !utf8.ValidString(instruction) {
		return bad("query_instruction")
	}
	return nil
}

type bailianEmbeddingClient struct {
	config   EmbeddingConfig
	client   *http.Client
	requests chan struct{}
}

func NewEmbeddingProvider(config EmbeddingConfig) (EmbeddingProvider, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !config.Enabled {
		return nil, failure("EMBEDDING_DISABLED", "Embedding is disabled", 503, false)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = config.MaxConcurrency
	return &bailianEmbeddingClient{config: config, requests: make(chan struct{}, config.MaxConcurrency),
		client: &http.Client{Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *bailianEmbeddingClient) Close() { c.client.CloseIdleConnections() }

func (c *bailianEmbeddingClient) Embed(ctx context.Context, texts []string, purpose EmbeddingPurpose) (EmbeddingResult, error) {
	if purpose != EmbeddingDocument && purpose != EmbeddingQuery {
		return EmbeddingResult{}, failure("INVALID_ARGUMENT", "Embedding purpose must be document or query", 422, false)
	}
	if len(texts) < 1 || len(texts) > 1000 {
		return EmbeddingResult{}, failure("INVALID_ARGUMENT", "Embedding requires 1 to 1000 texts", 422, false)
	}
	for _, text := range texts {
		if strings.TrimSpace(text) == "" || len(text) > 65536 || !utf8.ValidString(text) {
			return EmbeddingResult{}, failure("INVALID_ARGUMENT", "Embedding text must be nonempty UTF-8 of at most 65536 bytes", 422, false)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.config.TimeoutSeconds*float64(time.Second)))
	defer cancel()
	result := EmbeddingResult{Vectors: make([][]float32, 0, len(texts))}
	for start := 0; start < len(texts); start += c.config.BatchSize {
		batch, err := c.batch(ctx, texts[start:min(start+c.config.BatchSize, len(texts))], purpose)
		if err != nil {
			return EmbeddingResult{}, err
		}
		result.Vectors = append(result.Vectors, batch.Vectors...)
		if batch.TotalTokens > math.MaxInt64-result.TotalTokens {
			return EmbeddingResult{}, failure("EMBEDDING_PROTOCOL", "Invalid embedding token usage", 503, false)
		}
		result.TotalTokens += batch.TotalTokens
	}
	return result, nil
}

func (c *bailianEmbeddingClient) batch(ctx context.Context, texts []string, purpose EmbeddingPurpose) (EmbeddingResult, error) {
	select {
	case c.requests <- struct{}{}:
		defer func() { <-c.requests }()
	case <-ctx.Done():
		return EmbeddingResult{}, ctx.Err()
	}
	parameters := map[string]any{"dimension": c.config.Dimensions, "output_type": "dense", "text_type": purpose}
	if purpose == EmbeddingQuery && configured(c.config.QueryInstruction) != "" {
		parameters["instruct"] = configured(c.config.QueryInstruction)
	}
	body, _ := json.Marshal(map[string]any{"model": configured(c.config.Model),
		"input": map[string]any{"texts": texts}, "parameters": parameters})
	for attempt := 0; ; attempt++ {
		result, retryAfter, err := c.request(ctx, body, len(texts))
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return EmbeddingResult{}, ctx.Err()
		}
		if attempt >= c.config.MaxRetries || !retryableEmbeddingError(err) {
			return EmbeddingResult{}, err
		}
		delay := max(time.Second<<attempt, retryAfter)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return EmbeddingResult{}, ctx.Err()
		}
	}
}

func retryableEmbeddingError(err error) bool {
	return slices.Contains([]string{"EMBEDDING_UNAVAILABLE", "EMBEDDING_RATE_LIMITED"}, errorCode(err))
}

func (c *bailianEmbeddingClient) request(ctx context.Context, body []byte, count int) (EmbeddingResult, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(configured(c.config.BaseURL), "/")+"/services/embeddings/text-embedding/text-embedding", bytes.NewReader(body))
	if err != nil {
		return EmbeddingResult{}, 0, failure("EMBEDDING_PROTOCOL", "Invalid embedding endpoint", 503, false)
	}
	req.Header.Set("Authorization", "Bearer "+configured(c.config.APIKey))
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return EmbeddingResult{}, 0, upstreamError("EMBEDDING_UNAVAILABLE", "Embedding service is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		code, message, retryable := "EMBEDDING_REJECTED", "Embedding service rejected the request", false
		switch {
		case response.StatusCode == 401 || response.StatusCode == 403:
			code, message = "EMBEDDING_AUTH", "Embedding service rejected the configured credential"
		case response.StatusCode == 429:
			code, message, retryable = "EMBEDDING_RATE_LIMITED", "Embedding service rate limit exceeded", true
		case response.StatusCode >= 500:
			code, message, retryable = "EMBEDDING_UNAVAILABLE", "Embedding service request failed", true
		}
		return EmbeddingResult{}, embeddingRetryAfter(response.Header.Get("Retry-After")), failure(code, message, 503, retryable)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil {
		return EmbeddingResult{}, 0, upstreamError("EMBEDDING_UNAVAILABLE", "Embedding response could not be read")
	}
	invalid := func() (EmbeddingResult, time.Duration, error) {
		return EmbeddingResult{}, 0, failure("EMBEDDING_PROTOCOL", "Invalid embedding response", 503, false)
	}
	if len(data) > 8*1024*1024 {
		return invalid()
	}
	var raw struct {
		Code   string `json:"code"`
		Output struct {
			Embeddings []struct {
				Index  *int       `json:"text_index"`
				Vector []*float64 `json:"embedding"`
			} `json:"embeddings"`
		} `json:"output"`
		Usage struct {
			TotalTokens *int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&raw) != nil || decoder.Decode(new(any)) != io.EOF || raw.Code != "" ||
		len(raw.Output.Embeddings) != count || raw.Usage.TotalTokens == nil || *raw.Usage.TotalTokens < 0 {
		return invalid()
	}
	result := EmbeddingResult{Vectors: make([][]float32, count), TotalTokens: *raw.Usage.TotalTokens}
	// Returned positions, not response order, identify the corresponding input text.
	for _, item := range raw.Output.Embeddings {
		if item.Index == nil || *item.Index < 0 || *item.Index >= count || result.Vectors[*item.Index] != nil ||
			len(item.Vector) != c.config.Dimensions {
			return invalid()
		}
		vector := make([]float32, len(item.Vector))
		var norm float64
		for i, value := range item.Vector {
			if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || math.Abs(*value) > math.MaxFloat32 {
				return invalid()
			}
			vector[i] = float32(*value)
			norm += float64(vector[i]) * float64(vector[i])
		}
		if norm == 0 {
			return invalid()
		}
		result.Vectors[*item.Index] = vector
	}
	return result, 0, nil
}

func embeddingRetryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(min(seconds, 120)) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil {
		return min(max(time.Until(until), 0), 120*time.Second)
	}
	return 0
}

type EmbeddingCheck struct {
	Status          string `json:"status"`
	Provider        string `json:"provider"`
	Protocol        string `json:"protocol"`
	Model           string `json:"model"`
	Dimensions      int    `json:"dimensions"`
	DocumentVectors int    `json:"document_vectors"`
	QueryVectors    int    `json:"query_vectors"`
	TotalTokens     int64  `json:"total_tokens"`
	DurationMS      int64  `json:"duration_ms"`
}

func CheckEmbedding(ctx context.Context, config EmbeddingConfig) (EmbeddingCheck, error) {
	provider, err := NewEmbeddingProvider(config)
	if err != nil {
		return EmbeddingCheck{}, err
	}
	defer provider.Close()
	started := time.Now()
	documents, err := provider.Embed(ctx, []string{"I planted basil on the balcony.", "I repaired the bicycle brake."}, EmbeddingDocument)
	if err != nil {
		return EmbeddingCheck{}, err
	}
	query, err := provider.Embed(ctx, []string{"How can I grow herbs at home?"}, EmbeddingQuery)
	if err != nil {
		return EmbeddingCheck{}, err
	}
	if query.TotalTokens > math.MaxInt64-documents.TotalTokens {
		return EmbeddingCheck{}, failure("EMBEDDING_PROTOCOL", "Invalid embedding token usage", 503, false)
	}
	return EmbeddingCheck{Status: "ok", Provider: config.Provider, Protocol: config.Protocol,
		Model: configured(config.Model), Dimensions: config.Dimensions, DocumentVectors: len(documents.Vectors),
		QueryVectors: len(query.Vectors), TotalTokens: documents.TotalTokens + query.TotalTokens,
		DurationMS: time.Since(started).Milliseconds()}, nil
}

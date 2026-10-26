package moerise

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const chunkVersion = "paragraph-runes-v1-1000"
const maxVectorBytes = 96 * 1024 * 1024

type Embedder interface {
	Embed(context.Context, []string) ([][]float32, error)
}

type EmbeddingClient struct {
	config EmbeddingConfig
	client *http.Client
	gate   chan struct{}
}

func NewEmbeddingClient(config EmbeddingConfig) *EmbeddingClient {
	return &EmbeddingClient{config, &http.Client{Timeout: time.Duration(config.TimeoutSeconds) * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, make(chan struct{}, 1)}
}

func (c *EmbeddingClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if len(texts) == 0 || len(texts) > c.config.BatchSize {
		return nil, failure("INVALID_ARGUMENT", "Invalid embedding batch size", 422, false)
	}
	body := map[string]any{"model": configured(c.config.Model), "input": texts, "encoding_format": "float"}
	if c.config.Dimensions > 0 {
		body["dimensions"] = c.config.Dimensions
	}
	data, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(configured(c.config.BaseURL), "/")+"/embeddings", bytes.NewReader(data))
	if err != nil {
		return nil, upstreamError("EMBEDDING_PROTOCOL", "Invalid embedding endpoint")
	}
	request.Header.Set("Content-Type", "application/json")
	if key := configured(c.config.APIKey); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, upstreamError("EMBEDDING_UNAVAILABLE", "Embedding provider is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, upstreamError("EMBEDDING_UNAVAILABLE", "Embedding provider rejected the request")
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, 16*1024*1024+1))
	if err != nil || len(data) > 16*1024*1024 {
		return nil, upstreamError("EMBEDDING_PROTOCOL", "Invalid embedding response size")
	}
	var result struct {
		Data []struct {
			Index     *int      `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.Data) != len(texts) {
		return nil, upstreamError("EMBEDDING_PROTOCOL", "Invalid embedding response")
	}
	vectors, seen, dimensions := make([][]float32, len(texts)), map[int]bool{}, c.config.Dimensions
	for _, item := range result.Data {
		if item.Index == nil || *item.Index < 0 || *item.Index >= len(texts) || seen[*item.Index] {
			return nil, upstreamError("EMBEDDING_PROTOCOL", "Invalid embedding response indices")
		}
		if dimensions == 0 {
			dimensions = len(item.Embedding)
		}
		if err := normalizeVector(item.Embedding, dimensions); err != nil {
			return nil, err
		}
		seen[*item.Index], vectors[*item.Index] = true, item.Embedding
	}
	return vectors, nil
}

func normalizeVector(vector []float32, dimensions int) error {
	bad := upstreamError("EMBEDDING_PROTOCOL", "Embedding dimensions or values are invalid")
	if dimensions < 1 || dimensions > 4096 || len(vector) != dimensions {
		return bad
	}
	var norm float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return bad
		}
		norm += float64(value) * float64(value)
	}
	if norm <= 0 || math.IsInf(norm, 0) {
		return bad
	}
	norm = math.Sqrt(norm)
	for i := range vector {
		vector[i] = float32(float64(vector[i]) / norm)
	}
	return nil
}

func vectorBytes(vector []float32) []byte {
	data := make([]byte, 4*len(vector))
	for i, value := range vector {
		binary.LittleEndian.PutUint32(data[4*i:], math.Float32bits(value))
	}
	return data
}

type memoChunk struct {
	Start int
	End   int
	Text  string
}

// Offsets are Unicode code points in the unchanged source, with an exclusive end.
func chunkMemo(content string) []memoChunk {
	runes := []rune(content)
	chunks := []memoChunk{}
	for start := 0; start < len(runes); {
		end := min(start+1000, len(runes))
		if end < len(runes) {
			for i := end - 1; i > start+250; i-- {
				if runes[i] == '\n' && runes[i-1] == '\n' {
					end = i + 1
					break
				}
			}
		}
		text := string(runes[start:end])
		if strings.TrimSpace(text) != "" {
			chunks = append(chunks, memoChunk{start, end, text})
		}
		start = end
	}
	return chunks
}

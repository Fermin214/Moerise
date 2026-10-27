package moerise

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// Build and warm the published matrix before measuring semantic queries.
func BenchmarkSemanticCache10000x1024(b *testing.B) {
	settings := DefaultSettings()
	settings.Memos.InstanceID, settings.Memos.User = "benchmark", "users/owner"
	settings.Memos.BaseURL = "http://memos.test"
	settings.Embedding = embeddingTestConfig("http://embedding.test")
	settings.Embedding.Dimensions = 1024
	root := b.TempDir()
	settings.StateDir, settings.IndexDir = filepath.Join(root, "state"), filepath.Join(root, "index")
	store, err := NewStore(settings)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	memos := make(map[string]map[string]any, 10000)
	for i := range 10000 {
		raw := rawMemo(fmt.Sprintf("%05d", i), "\u82b1 semantic benchmark")
		memo, err := normalizeMemo(raw)
		if err != nil {
			b.Fatal(err)
		}
		memos[memo.Name] = raw
		if err := store.Refresh(memo.Name, &memo); err != nil {
			b.Fatal(err)
		}
	}
	if err := store.Advance(nowText(), true); err != nil {
		b.Fatal(err)
	}
	source := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		raw := memos[strings.TrimPrefix(r.URL.Path, "/api/v1/")]
		if raw == nil {
			return 404, nil, nil
		}
		return 200, raw, nil
	})
	b.Cleanup(source.Close)
	provider := &searchEmbedding{dims: settings.Embedding.Dimensions}
	index := NewVectorIndexService(store, source, provider)
	for {
		worked, err := index.Step(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if !worked {
			break
		}
	}
	service := NewSearchService(store, source, provider)
	matrix, err := service.vectors(context.Background())
	if err != nil || len(matrix.entries) != 10000 {
		b.Fatalf("published matrix: %v", err)
	}
	request := SearchRequest{Query: "benchmark query", Mode: "semantic", State: "NORMAL"}
	b.ReportAllocs()
	b.SetBytes(int64(len(matrix.entries) * settings.Embedding.Dimensions * 4))
	b.ResetTimer()
	for b.Loop() {
		candidates, status, err := service.semanticCandidates(context.Background(), request)
		if err != nil || !status.Used || len(candidates) != searchCandidates || service.matrix != matrix {
			b.Fatalf("semantic cache query: candidates=%d status=%+v err=%v", len(candidates), status, err)
		}
	}
}

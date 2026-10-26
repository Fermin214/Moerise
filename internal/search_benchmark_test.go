package moerise

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// The immutable matrix is allocated once, outside the measured query loop.
func BenchmarkExactVectors10000x1024(b *testing.B) {
	matrix := &vectorMatrix{Dimensions: 1024}
	allowed := map[string]bool{}
	for i := range 10000 {
		name := fmt.Sprintf("memos/%05d", i)
		matrix.Entries = append(matrix.Entries, vectorEntry{Name: name, ContentHash: "hash", End: 10})
		allowed[name] = true
		for range 1024 {
			matrix.Values = append(matrix.Values, 0.03125)
		}
	}
	query := make([]float32, 1024)
	for i := range query {
		query[i] = 0.03125
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(matrix.Values) * 4))
	b.ResetTimer()
	for b.Loop() {
		if _, err := matrix.nearest(context.Background(), query, allowed, 200); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInitialEmbedding1000(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		settings := testSettings(b)
		settings.Embedding.Model, settings.Embedding.BaseURL = stringPointer("fixture-model"), stringPointer("https://embedding.example/v1")
		settings.Embedding.BatchSize = 64
		store := testStore(b, settings)
		memos := map[string]map[string]any{}
		for i := range 1000 {
			raw := rawMemo(fmt.Sprintf("%04d", i), "学习与复习，间隔帮助长期记忆。")
			memo, err := normalizeMemo(raw)
			if err != nil {
				b.Fatal(err)
			}
			memos[memo.Name] = raw
			if err := store.Refresh(memo.Name, &memo); err != nil {
				b.Fatal(err)
			}
		}
		if err := store.Advance("2026-01-01T00:00:00Z", true); err != nil {
			b.Fatal(err)
		}
		source := fakeClient(settings, func(r *http.Request) (int, any, error) {
			if status, body, ok := identityResponse(r); ok {
				return status, body, nil
			}
			return 200, memos[r.URL.Path[len("/api/v1/"):]], nil
		})
		service := NewSearchService(store, source)
		service.Embedder = embedFunc(func(_ context.Context, texts []string) ([][]float32, error) {
			vectors := [][]float32{}
			for range texts {
				vector := make([]float32, 1024)
				for i := range vector {
					vector[i] = 0.03125
				}
				vectors = append(vectors, vector)
			}
			return vectors, nil
		})
		b.StartTimer()
		for {
			if err := service.IndexStep(context.Background()); err != nil {
				b.Fatal(err)
			}
			status, err := service.IndexStatus()
			if err != nil {
				b.Fatal(err)
			}
			if status.Status == "active" {
				break
			}
		}
		b.StopTimer()
		store.Close()
		source.Close()
	}
}

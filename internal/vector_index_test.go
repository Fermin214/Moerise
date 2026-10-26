package moerise

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

type indexEmbedding struct {
	dimensions int
	texts      []string
	after      func()
}

func (p *indexEmbedding) Close() {}
func (p *indexEmbedding) Embed(ctx context.Context, texts []string, purpose EmbeddingPurpose) (EmbeddingResult, error) {
	if purpose != EmbeddingDocument {
		return EmbeddingResult{}, errors.New("unexpected query embedding")
	}
	p.texts = append(p.texts, texts...)
	result := EmbeddingResult{}
	for _, text := range texts {
		vector := make([]float32, p.dimensions)
		vector[0] = float32(len(text))
		result.Vectors = append(result.Vectors, vector)
	}
	if p.after != nil {
		p.after()
	}
	return result, ctx.Err()
}

func TestChunkTextSourceLocations(t *testing.T) {
	short := "  \u77ed\u7b14\u8bb0\r\n\r\nsecond paragraph\n"
	chunks := ChunkText(short)
	if len(chunks) != 1 || chunks[0].Content != short || chunks[0].ByteStart != 0 ||
		chunks[0].ByteEnd != len(short) || chunks[0].LineStart != 1 || chunks[0].LineEnd != 3 {
		t.Fatalf("short body changed: %+v", chunks)
	}
	paragraph := strings.Repeat("\u6c49", 1000) + "\r\n \t\r\n"
	body := paragraph + paragraph + strings.Repeat("\u5b57", 4500) + "\nend"
	chunks = ChunkText(body)
	if chunks[0].Content != paragraph+paragraph || len(chunks) != 4 {
		t.Fatalf("paragraph packing: chunks=%d first=%d", len(chunks), utf8.RuneCountInString(chunks[0].Content))
	}
	var rebuilt strings.Builder
	last := 0
	for i, chunk := range chunks {
		if chunk.Ordinal != i || chunk.ByteStart != last || !utf8.ValidString(chunk.Content) ||
			utf8.RuneCountInString(chunk.Content) > chunkRunes || chunk.Content != body[chunk.ByteStart:chunk.ByteEnd] ||
			chunk.LineStart != 1+strings.Count(body[:chunk.ByteStart], "\n") ||
			chunk.ContentHash != hashBytes([]byte(chunk.Content)) {
			t.Fatalf("invalid source position: %+v", chunk)
		}
		rebuilt.WriteString(chunk.Content)
		last = chunk.ByteEnd
	}
	if rebuilt.String() != body || len(ChunkText(" \r\n\t")) != 0 {
		t.Fatal("body was lost or whitespace was embedded")
	}
}

func indexFixture(t *testing.T) (*Store, *VectorIndexService, map[string]map[string]any, *indexEmbedding) {
	t.Helper()
	settings := testSettings(t)
	settings.Embedding = embeddingTestConfig("http://embedding.test")
	settings.Embedding.BatchSize = 2
	store := testStore(t, settings)
	memos := map[string]map[string]any{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		raw := rawMemo(name, "body "+name)
		if name == "e" {
			raw["state"] = "ARCHIVED"
		}
		memos["memos/"+name] = raw
		memo, _ := normalizeMemo(raw)
		if err := store.Refresh(memo.Name, &memo); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Advance(nowText(), true); err != nil {
		t.Fatal(err)
	}
	source := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if raw := memos[strings.TrimPrefix(r.URL.Path, "/api/v1/")]; raw != nil {
			return 200, raw, nil
		}
		return 404, nil, nil
	})
	provider := &indexEmbedding{dimensions: settings.Embedding.Dimensions}
	return store, NewVectorIndexService(store, source, provider), memos, provider
}

func finishIndex(t *testing.T, service *VectorIndexService) *VectorIndexStatus {
	t.Helper()
	for range 30 {
		worked, err := service.Step(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		status, err := service.Store.VectorStatus()
		if err != nil {
			t.Fatal(err)
		}
		if status.Ready {
			return status
		}
		if !worked {
			t.Fatal("index stopped before publication")
		}
	}
	t.Fatal("index did not finish")
	return nil
}

func refreshIndexMemo(t *testing.T, store *Store, raw map[string]any) {
	t.Helper()
	memo, err := normalizeMemo(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Refresh(memo.Name, &memo); err != nil {
		t.Fatal(err)
	}
}

func TestVectorIndexConcurrentProjectionAndCancellation(t *testing.T) {
	store, service, memos, provider := indexFixture(t)
	active := finishIndex(t, service)
	memos["memos/a"]["content"] = "first revision"
	refreshIndexMemo(t, store, memos["memos/a"])
	provider.after = func() {
		memos["memos/a"]["content"] = "second revision"
		refreshIndexMemo(t, store, memos["memos/a"])
		provider.after = nil
	}
	if _, err := service.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	building, _ := store.VectorStatus()
	if published, err := service.publish(building.Building.ID); err != nil || published {
		t.Fatalf("stale snapshot published: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	provider.after = cancel
	if _, err := service.Step(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	cancelled, _ := store.VectorStatus()
	if cancelled.Active.ID != active.Active.ID || cancelled.Building.CompletedChunks != 4 {
		t.Fatal("cancelled model batch was saved or published")
	}
	provider.after = nil
	finishIndex(t, service)
	if !slices.Contains(provider.texts, "second revision") {
		t.Fatal("latest projection was not indexed")
	}
}

func TestVectorIndexSourceChecksAndInvalidVectors(t *testing.T) {
	for _, mode := range []string{"deleted", "changed", "identity", "invalid-vector"} {
		t.Run(mode, func(t *testing.T) {
			store, service, memos, provider := indexFixture(t)
			switch mode {
			case "deleted":
				delete(memos, "memos/a")
			case "changed":
				memos["memos/a"]["content"] = "fresh source"
			case "identity":
				service.Source = fakeClient(store.Settings, func(*http.Request) (int, any, error) {
					return 200, map[string]string{"version": "0.31.0"}, nil
				})
			case "invalid-vector":
				provider.dimensions = 2
			}
			_, err := service.Step(context.Background())
			status, _ := store.VectorStatus()
			if status.Active != nil || status.Building.CompletedChunks != 0 {
				t.Fatal("untrusted batch saved or published")
			}
			if mode != "invalid-vector" && len(provider.texts) != 0 {
				t.Fatal("unverified source was sent to the model")
			}
			if mode == "identity" && err == nil || mode == "invalid-vector" && errorCode(err) != "EMBEDDING_PROTOCOL" {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode == "changed" {
				finishIndex(t, service)
				if !slices.Contains(provider.texts, "fresh source") || slices.Contains(provider.texts, "body a") {
					t.Fatal("source change did not replace stale content")
				}
			}
		})
	}
}

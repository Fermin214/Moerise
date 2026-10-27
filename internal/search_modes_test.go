package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestSearchModesValidation(t *testing.T) {
	for _, mode := range []string{"", "keyword", "semantic", "hybrid", "HYBRID", " keyword", "unknown"} {
		var request SearchRequest
		data, err := json.Marshal(map[string]any{"query": " \u82b1 ", "mode": mode})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &request); err != nil {
			t.Fatal(err)
		}
		got, err := normalizeSearch(request)
		valid := slices.Contains([]string{"", "keyword", "semantic", "hybrid"}, mode)
		if !valid {
			if errorCode(err) != "INVALID_ARGUMENT" {
				t.Fatalf("mode %q: %v", mode, err)
			}
			continue
		}
		want := mode
		if want == "" {
			want = "hybrid"
		}
		if err != nil || got.Mode != want || got.Query != "\u82b1" || got.State != "NORMAL" || got.Limit != 10 {
			t.Fatalf("mode %q: %+v %v", mode, got, err)
		}
	}
}

func TestSearchModesChannelSelectionAndMatches(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	memos["memos/a"]["content"] = "\u9633\u53f0\u79cd\u82b1"
	memos["memos/a"]["tags"] = []string{"plant", "exact"}
	memos["memos/b"]["content"] = "topic"
	memos["memos/c"]["tags"] = []string{"topic"}
	memos["memos/d"]["content"] = "\u82b1"
	memos["memos/d"]["tags"] = []string{"plant/child", "exact"}
	memos["memos/e"]["content"] = "\u82b1"
	memos["memos/e"]["tags"] = []string{"plant"}
	for _, raw := range memos {
		refreshIndexMemo(t, store, raw)
	}
	provider := &searchEmbedding{dims: store.Settings.Embedding.Dimensions}
	index.Provider = provider
	finishIndex(t, index)
	provider.purposes = nil
	service := NewSearchService(store, index.Source, provider)

	provider.err = upstreamError("EMBEDDING_RATE_LIMITED", "rate limited")
	keyword, err := service.Search(context.Background(), SearchRequest{Query: "topic", Mode: "keyword"})
	if err != nil || keyword.Mode != "keyword" || keyword.Semantic.Used || keyword.Semantic.Reason != "KEYWORD_MODE" ||
		len(keyword.Results) != 2 || len(provider.purposes) != 0 || service.matrix != nil {
		t.Fatalf("keyword mode called embeddings or lost lexical results: %+v %v", keyword, err)
	}
	var signals []string
	for _, hit := range keyword.Results {
		for _, match := range hit.Matches {
			signals = append(signals, match.Signals...)
		}
	}
	if !slices.Contains(signals, "keyword") || !slices.Contains(signals, "tag") || slices.Contains(signals, "semantic") {
		t.Fatalf("keyword channels: %v", signals)
	}
	provider.err = nil
	semantic, err := service.Search(context.Background(), SearchRequest{Query: "topic", Mode: "semantic"})
	if err != nil || semantic.Mode != "semantic" || !semantic.Semantic.Used || semantic.Semantic.Reason != "" ||
		len(semantic.Results) != 2 || len(provider.purposes) != 1 || provider.purposes[0] != EmbeddingQuery {
		t.Fatalf("semantic selection: %+v %v", semantic, err)
	}
	for _, hit := range semantic.Results {
		if hit.Note.Name == "memos/b" || hit.Note.Name == "memos/c" || len(hit.Matches) == 0 {
			t.Fatalf("semantic mode included a lexical-only hit: %+v", hit)
		}
		for _, match := range hit.Matches {
			if !slices.Equal(match.Signals, []string{"semantic"}) ||
				match.Content != hit.Note.Content[match.ByteStart:match.ByteEnd] || match.LineStart != 1 || match.LineEnd != 1 {
				t.Fatalf("semantic matches lost the main protocol: %+v", match)
			}
		}
	}
	implicit, err := service.Search(context.Background(), SearchRequest{Query: "topic"})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := service.Search(context.Background(), SearchRequest{Query: "topic", Mode: "hybrid"})
	if err != nil || !reflect.DeepEqual(implicit, explicit) || explicit.Mode != "hybrid" ||
		!explicit.Semantic.Used || len(explicit.Results) != 4 {
		t.Fatalf("default hybrid changed: implicit=%+v explicit=%+v err=%v", implicit, explicit, err)
	}
	for _, mode := range []string{"keyword", "semantic", "hybrid"} {
		filtered, err := service.Search(context.Background(), SearchRequest{
			Query: "\u82b1", Mode: mode, Tags: []string{"plant", "exact"},
		})
		if err != nil || len(filtered.Results) != 1 || filtered.Results[0].Note.Name != "memos/a" {
			t.Fatalf("%s lost exact AND filters: %+v %v", mode, filtered, err)
		}
		archived, err := service.Search(context.Background(), SearchRequest{Query: "\u82b1", Mode: mode, State: "ARCHIVED"})
		if err != nil || len(archived.Results) != 1 || archived.Results[0].Note.Name != "memos/e" {
			t.Fatalf("%s lost archive filter: %+v %v", mode, archived, err)
		}
	}
}

func TestSearchModesSemanticErrorsAndHybridFallback(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	memos["memos/a"]["content"] = "\u82b1"
	refreshIndexMemo(t, store, memos["memos/a"])
	provider := &searchEmbedding{dims: store.Settings.Embedding.Dimensions}
	index.Provider = provider
	finishIndex(t, index)
	for _, reason := range []string{"EMBEDDING_DISABLED", "INDEX_NOT_READY", "EMBEDDING_UNAVAILABLE", "EMBEDDING_RATE_LIMITED", "EMBEDDING_PROTOCOL"} {
		t.Run(reason, func(t *testing.T) {
			service := NewSearchService(store, index.Source, provider)
			switch reason {
			case "EMBEDDING_DISABLED":
				store.Settings.Embedding.Enabled = false
				defer func() { store.Settings.Embedding.Enabled = true }()
			case "INDEX_NOT_READY":
				model := "different-space"
				previous := store.Settings.Embedding.Model
				store.Settings.Embedding.Model = &model
				defer func() { store.Settings.Embedding.Model = previous }()
			case "EMBEDDING_UNAVAILABLE":
				service.Provider = nil
			case "EMBEDDING_RATE_LIMITED":
				provider.err = upstreamError(reason, "rate limited")
				defer func() { provider.err = nil }()
			case "EMBEDDING_PROTOCOL":
				provider.dims--
				defer func() { provider.dims++ }()
			}
			result, err := service.Search(context.Background(), SearchRequest{Query: "\u82b1", Mode: "semantic"})
			var failure *ServiceError
			if !errors.As(err, &failure) || failure.Code != reason || failure.Status != 503 || len(result.Results) != 0 {
				t.Fatalf("semantic mode silently degraded: %+v %v", result, err)
			}
			for _, mode := range []string{"", "hybrid"} {
				result, err := service.Search(context.Background(), SearchRequest{Query: "\u82b1", Mode: mode})
				if err != nil || result.Mode != "keyword" || result.Semantic.Used || result.Semantic.Reason != reason ||
					len(result.Results) != 1 || result.Results[0].Note.Name != "memos/a" {
					t.Fatalf("hybrid fallback: %+v %v", result, err)
				}
			}
		})
	}
	for _, mode := range []string{"semantic", "hybrid"} {
		ctx, cancel := context.WithCancel(context.Background())
		provider.after = cancel
		result, err := NewSearchService(store, index.Source, provider).Search(ctx, SearchRequest{Query: "\u82b1", Mode: mode})
		cancel()
		if !errors.Is(err, context.Canceled) || len(result.Results) != 0 {
			t.Fatalf("%s swallowed cancellation: %+v %v", mode, result, err)
		}
	}
	provider.after = nil
}

func TestSearchModesProjectionChangeDuringQuery(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	memos["memos/a"]["content"] = "\u82b1"
	refreshIndexMemo(t, store, memos["memos/a"])
	provider := &searchEmbedding{dims: store.Settings.Embedding.Dimensions}
	index.Provider = provider
	service := NewSearchService(store, index.Source, provider)
	for _, mode := range []string{"semantic", "hybrid"} {
		finishIndex(t, index)
		provider.after = func() {
			memos["memos/b"]["content"] = "changed " + mode
			refreshIndexMemo(t, store, memos["memos/b"])
		}
		result, err := service.Search(context.Background(), SearchRequest{Query: "\u82b1", Mode: mode})
		provider.after = nil
		if mode == "semantic" {
			if errorCode(err) != "INDEX_NOT_READY" || len(result.Results) != 0 {
				t.Fatalf("semantic used a stale matrix: %+v %v", result, err)
			}
		} else if err != nil || result.Mode != "keyword" || result.Semantic.Reason != "INDEX_NOT_READY" || len(result.Results) != 1 {
			t.Fatalf("hybrid used a stale matrix: %+v %v", result, err)
		}
	}
}

package moerise

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func discoveryFixture(t *testing.T) (*SearchService, *VectorIndexService, map[string]map[string]any) {
	t.Helper()
	store, index, memos, _ := indexFixture(t)
	for name, body := range map[string]string{
		"a": "seed", "b": "near", "c": "angle", "d": "other", "e": "archive",
	} {
		raw := memos["memos/"+name]
		raw["content"] = body
		raw["tags"] = []string{"other"}
	}
	memos["memos/a"]["tags"] = []string{"study/base"}
	memos["memos/b"]["tags"] = []string{"study/recall"}
	memos["memos/e"]["tags"] = []string{"study/archive"}
	for _, raw := range memos {
		refreshIndexMemo(t, store, raw)
	}
	index.Provider = &relatedEmbedding{dimensions: store.Settings.Embedding.Dimensions, vectors: map[string][]float32{
		"seed": {1, 0}, "near": {1, 0}, "angle": {0.9, 0.4}, "other": {0, 1}, "archive": {1, 0},
	}}
	finishIndex(t, index)
	return NewSearchService(store, index.Source, nil), index, memos
}

func TestDiscoveryStartWalk(t *testing.T) {
	service, _, memos := discoveryFixture(t)
	ctx := context.Background()
	for _, name := range []string{"memos/a", "memos/b"} {
		start, err := service.StartWalk(ctx, name)
		if err != nil || start.Item == nil || start.Item.Note.Name != name || start.Exhausted ||
			!slices.Equal(start.Path, []string{name}) || start.Mode != "continue" ||
			start.Item.Note.Content != memos[name]["content"] || start.Item.Score != 0 ||
			len(start.Item.Matches) != 0 || start.Item.Matches == nil || start.Warnings == nil {
			t.Fatalf("specified start must return the exact normal memo: %+v %v", start, err)
		}
	}
	if result, err := service.StartWalk(ctx, "memos/e"); errorCode(err) != "WALK_ARCHIVED" || result.Item != nil {
		t.Fatalf("archived starting memo entered a walk: %+v %v", result, err)
	}
	start, err := service.StartWalk(ctx, "")
	if err != nil || start.Item == nil || start.Exhausted || start.Mode != "random" ||
		!slices.Equal(start.Path, []string{start.Item.Note.Name}) || start.Item.Note.State != "NORMAL" {
		t.Fatalf("random start must return exactly one current memo: %+v %v", start, err)
	}
	next, err := service.Walk(ctx, WalkRequest{Name: "memos/a", Path: []string{"memos/a"}})
	if err != nil || next.Item == nil || next.Item.Note.Name == "memos/a" || len(next.Path) != 2 {
		t.Fatalf("start path could not continue: %+v %v", next, err)
	}
	if result, err := service.StartWalk(ctx, "bad-name"); errorCode(err) != "INVALID_ARGUMENT" || result.Item != nil {
		t.Fatalf("invalid start: %+v %v", result, err)
	}
	if result, err := service.StartWalk(ctx, "memos/missing"); errorCode(err) != "SOURCE_NOT_FOUND" || result.Item != nil {
		t.Fatalf("missing start: %+v %v", result, err)
	}
}

func TestDiscoveryStartWalkRechecksSourceAndIdentity(t *testing.T) {
	for _, change := range []string{"content", "delete", "author", "identity", "offline", "cancel"} {
		t.Run(change, func(t *testing.T) {
			service, _, memos := discoveryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads, identities := 0, 0
			service.Source = fakeClient(service.Store.Settings, func(r *http.Request) (int, any, error) {
				if strings.HasSuffix(r.URL.Path, "/auth/me") {
					identities++
					if change == "identity" && identities == 2 {
						return 200, map[string]any{"user": map[string]string{"name": "users/other"}}, nil
					}
				}
				if status, body, ok := identityResponse(r); ok {
					return status, body, nil
				}
				reads++
				if reads == 2 {
					switch change {
					case "content":
						memos["memos/a"]["content"] = "changed at start"
					case "delete":
						return 404, nil, nil
					case "author":
						memos["memos/a"]["creator"] = "users/other"
					case "offline":
						return 503, nil, nil
					case "cancel":
						cancel()
					}
				}
				return 200, memos["memos/a"], nil
			})
			result, err := service.StartWalk(ctx, "memos/a")
			code := map[string]string{"content": "SOURCE_CHANGED", "delete": "SOURCE_NOT_FOUND",
				"author": "SOURCE_NOT_FOUND", "identity": "IDENTITY_MISMATCH", "offline": "UPSTREAM_UNAVAILABLE"}[change]
			if result.Item != nil || result.Exhausted || err == nil ||
				change != "cancel" && errorCode(err) != code || change == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("start source check failed: %+v %v", result, err)
			}
		})
	}
}

func TestDiscoveryWalkModesAndPath(t *testing.T) {
	service, _, _ := discoveryFixture(t)
	ctx := context.Background()
	first := func(int) int { return 0 }
	last := func(n int) int { return n - 1 }
	cont, err := service.walk(ctx, WalkRequest{Name: "memos/a"}, first)
	if err != nil || cont.Mode != "continue" || cont.Item == nil || cont.Item.Note.Name != "memos/b" ||
		!slices.Equal(cont.Path, []string{"memos/a", "memos/b"}) ||
		!slices.Equal(cont.Item.Matches[0].Signals, []string{"semantic"}) || cont.Item.Score != 1 {
		t.Fatalf("continue must return an unweighted semantic draw: %+v %v", cont, err)
	}
	for slot, want := range []string{"memos/b", "memos/c"} {
		draw := func(n int) int {
			if n != 2 {
				t.Fatalf("expected exactly two positive-cosine notes, got %d", n)
			}
			return slot
		}
		item, err := service.walk(ctx, WalkRequest{Name: "memos/a", Mode: "continue"}, draw)
		if err != nil || item.Item == nil || item.Item.Note.Name != want {
			t.Fatalf("continue slot %d: %+v %v", slot, item, err)
		}
	}
	weaker, err := service.walk(ctx, WalkRequest{Name: "memos/a"}, last)
	if err != nil || weaker.Item == nil || weaker.Item.Note.Name != "memos/c" ||
		math.Abs(weaker.Item.Score-0.9/math.Sqrt(0.97)) > 1e-6 {
		t.Fatalf("a weaker semantic neighbor must remain selectable: %+v %v", weaker, err)
	}
	jump, err := service.Walk(ctx, WalkRequest{Name: "memos/a", Mode: "random",
		Path: []string{"memos/b", "memos/c", "memos/b"}})
	if err != nil || jump.Item == nil || jump.Item.Note.Name != "memos/d" ||
		!slices.Equal(jump.Path, []string{"memos/b", "memos/c", "memos/a", "memos/d"}) {
		t.Fatalf("random path deduplication: %+v %v", jump, err)
	}
	match := jump.Item.Matches[0]
	if match.Content != jump.Item.Note.Content[match.ByteStart:match.ByteEnd] ||
		!slices.Equal(match.Signals, []string{"random"}) {
		t.Fatal("random item lost its source match")
	}
	exhausted, err := service.Walk(ctx, WalkRequest{Mode: "random", Path: jump.Path})
	if err != nil || !exhausted.Exhausted || exhausted.Item != nil {
		t.Fatalf("path exhaustion: %+v %v", exhausted, err)
	}
	archived, err := service.Walk(ctx, WalkRequest{Mode: "random", Path: jump.Path, IncludeArchived: true})
	if err != nil || archived.Item == nil || archived.Item.Note.Name != "memos/e" {
		t.Fatalf("archive opt-in: %+v %v", archived, err)
	}
	archived, err = service.Walk(ctx, WalkRequest{Name: "memos/a", Path: []string{"memos/b", "memos/c"},
		IncludeArchived: true})
	if err != nil || archived.Item == nil || archived.Item.Note.Name != "memos/e" {
		t.Fatalf("semantic archive opt-in: %+v %v", archived, err)
	}
}

func TestDiscoveryNormalizeAndLongPath(t *testing.T) {
	input := []string{"memos/b", "memos/b"}
	normalized, err := normalizeWalk(WalkRequest{Name: "memos/a", Path: input})
	if err != nil || normalized.Mode != "continue" || !slices.Equal(normalized.Path, []string{"memos/b"}) {
		t.Fatalf("walk normalization: %+v %v", normalized, err)
	}
	normalized.Path[0] = "memos/c"
	if input[0] != "memos/b" {
		t.Fatal("normalization mutated its caller's path")
	}
	for _, request := range []WalkRequest{
		{}, {Mode: "other"}, {Mode: "random", Name: "a"},
		{Name: "memos/a/b"}, {Mode: "random", Path: []string{"memos/"}},
	} {
		if _, err := normalizeWalk(request); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("invalid walk accepted: %+v %v", request, err)
		}
	}
	repeated, err := normalizeWalk(WalkRequest{Mode: "random", Path: slices.Repeat([]string{"memos/a"}, 1001)})
	if err != nil || !slices.Equal(repeated.Path, []string{"memos/a"}) {
		t.Fatalf("long repeated input: %+v %v", repeated, err)
	}
	service, _, _ := discoveryFixture(t)
	path := make([]string, 1001)
	for i := range path {
		path[i] = fmt.Sprintf("memos/visited-%03d", i)
	}
	full, err := service.Walk(context.Background(), WalkRequest{Name: "memos/a", Path: path})
	if err != nil || full.Item == nil || full.Exhausted || len(full.Path) != 1003 || len(path) != 1001 {
		t.Fatalf("long semantic path stopped early: %+v %v", full, err)
	}
	next, err := service.Walk(context.Background(), WalkRequest{Name: full.Item.Note.Name, Path: full.Path})
	if err != nil || next.Item == nil || next.Exhausted || len(next.Path) != 1004 {
		t.Fatalf("long path could not continue: %+v %v", next, err)
	}
	random, err := service.Walk(context.Background(), WalkRequest{Mode: "random", Path: next.Path})
	if err != nil || random.Item == nil || random.Exhausted || len(random.Path) != 1005 {
		t.Fatalf("long random-start path stopped early: %+v %v", random, err)
	}
	exhausted, err := service.Walk(context.Background(), WalkRequest{Mode: "random", Path: random.Path})
	if err != nil || !exhausted.Exhausted || exhausted.Item != nil || len(exhausted.Path) != 1005 {
		t.Fatalf("actual full-pool exhaustion: %+v %v", exhausted, err)
	}
}

func TestDiscoverySemanticReadinessForAllModes(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	service := NewSearchService(store, index.Source, nil)
	for _, mode := range []string{"continue", "random"} {
		if result, err := service.Walk(context.Background(), WalkRequest{Name: "memos/a", Mode: mode}); errorCode(err) != "INDEX_NOT_READY" || result.Item != nil {
			t.Fatalf("%s silently fell back without a published index: %+v %v", mode, result, err)
		}
	}
	for _, name := range []string{"", "memos/a"} {
		if result, err := service.StartWalk(context.Background(), name); errorCode(err) != "INDEX_NOT_READY" || result.Item != nil {
			t.Fatalf("start %q accepted an unpublished index: %+v %v", name, result, err)
		}
	}
	finishIndex(t, index)
	store.Settings.Embedding.Enabled = false
	for _, mode := range []string{"continue", "random"} {
		if result, err := service.Walk(context.Background(), WalkRequest{Name: "memos/a", Mode: mode}); errorCode(err) != "EMBEDDING_DISABLED" || result.Item != nil {
			t.Fatalf("%s silently fell back without embedding: %+v %v", mode, result, err)
		}
	}
	for _, name := range []string{"", "memos/a"} {
		if result, err := service.StartWalk(context.Background(), name); errorCode(err) != "EMBEDDING_DISABLED" || result.Item != nil {
			t.Fatalf("start %q accepted disabled embedding: %+v %v", name, result, err)
		}
	}
	store.Settings.Embedding.Enabled = true
	empty := rawMemo("empty", " \n\t")
	memos["memos/empty"] = empty
	refreshIndexMemo(t, store, empty)
	finishIndex(t, index)
	if result, err := service.Walk(context.Background(), WalkRequest{Name: "memos/empty"}); errorCode(err) != "NO_SOURCE_VECTORS" || result.Item != nil {
		t.Fatalf("source without document vectors silently fell back: %+v %v", result, err)
	}
}

func TestDiscoveryRandomBoundsSourceRequests(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	store.Settings.Embedding.BatchSize = 100
	for i := range 120 {
		raw := rawMemo(fmt.Sprintf("random-%03d", i), "random content")
		memos[raw["name"].(string)] = raw
		refreshIndexMemo(t, store, raw)
	}
	finishIndex(t, index)
	client := index.Source.(*MemosClient)
	transport := client.client.Transport
	reads := 0
	client.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/memos/") {
			reads++
		}
		return transport.RoundTrip(r)
	})
	service := NewSearchService(store, client, nil)
	candidates, err := service.randomWalkCandidates(context.Background(), WalkRequest{}, nil)
	if err != nil || len(candidates) != 124 {
		t.Fatalf("random start truncated its complete pool: %d %v", len(candidates), err)
	}
	result, err := service.Walk(context.Background(), WalkRequest{Mode: "random"})
	if err != nil || result.Item == nil || reads != 1 {
		t.Fatalf("random fetched %d sources: %+v %v", reads, result, err)
	}
}

func TestDiscoveryFullSemanticPoolAndOneSlotPerNote(t *testing.T) {
	service, index, memos := discoveryFixture(t)
	body := strings.Repeat(strings.Repeat("p", 2046)+"\n\n", 105)
	raw := rawMemo("many", body)
	memos["memos/many"] = raw
	refreshIndexMemo(t, service.Store, raw)
	provider := index.Provider.(*relatedEmbedding)
	for _, chunk := range ChunkText(body) {
		provider.vectors[chunk.Content] = []float32{1, 0}
	}
	index.Store.Settings.Embedding.BatchSize = 100
	finishIndex(t, index)

	ctx := context.Background()
	seed, err := service.liveSearchMemo(ctx, "memos/a")
	if err != nil {
		t.Fatal(err)
	}
	top, semantic, err := service.relatedSemanticCandidates(ctx, *seed, SearchRequest{State: "NORMAL"})
	if err != nil || !semantic.Used || len(top) != 100 {
		t.Fatalf("Related must retain its top-100 chunk contract: %d %+v %v", len(top), semantic, err)
	}
	for _, item := range top {
		if item.name == "memos/c" {
			t.Fatal("fixture must put the weaker note beyond Related's top 100")
		}
	}
	full, err := service.Walk(ctx, WalkRequest{Name: "memos/a", Path: []string{"memos/b", "memos/many"}})
	if err != nil || full.Item == nil || full.Item.Note.Name != "memos/c" || full.Exhausted {
		t.Fatalf("a candidate beyond the strongest 100 chunks was lost: %+v %v", full, err)
	}
	for slot, want := range []string{"memos/b", "memos/c", "memos/many"} {
		result, err := service.walk(ctx, WalkRequest{Name: "memos/a"}, func(n int) int {
			if n != 3 {
				t.Fatalf("chunk count changed note probability: %d slots", n)
			}
			return slot
		})
		if err != nil || result.Item == nil || result.Item.Note.Name != want {
			t.Fatalf("note slot %d: %+v %v", slot, result, err)
		}
	}
}

func TestDiscoveryRandomScansPastHundredStaleCandidates(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	store.Settings.Embedding.BatchSize = 100
	for i := range 120 {
		raw := rawMemo(fmt.Sprintf("stale-%03d", i), "random content")
		memos[raw["name"].(string)] = raw
		refreshIndexMemo(t, store, raw)
	}
	finishIndex(t, index)
	for name := range memos {
		if name != "memos/a" {
			delete(memos, name)
		}
	}
	service := NewSearchService(store, index.Source, nil)
	checked := 0
	result, err := service.walk(context.Background(), WalkRequest{Mode: "random"}, func(n int) int {
		checked++
		return n - 1
	})
	if err != nil || result.Item == nil || result.Item.Note.Name != "memos/a" || result.Exhausted || checked != 124 {
		t.Fatalf("stale candidates caused truncated exhaustion after %d checks: %+v %v", checked, result, err)
	}
}

func TestDiscoveryCancellationAndDeadlineDoNotReportExhaustion(t *testing.T) {
	for _, stage := range []string{"before", "scan", "candidate", "finish", "deadline"} {
		t.Run(stage, func(t *testing.T) {
			service, _, _ := discoveryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := context.Canceled
			if stage == "deadline" {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				want = context.DeadlineExceeded
			}
			if stage == "before" {
				cancel()
			}
			client := service.Source.(*MemosClient)
			transport := client.client.Transport
			identities := 0
			client.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/auth/me") {
					identities++
					if stage == "finish" && identities == 2 {
						cancel()
					}
				}
				if stage == "candidate" && strings.HasSuffix(r.URL.Path, "/memos/b") {
					cancel()
				}
				if stage == "scan" && strings.HasSuffix(r.URL.Path, "/memos/a") {
					cancel()
				}
				return transport.RoundTrip(r)
			})
			result, err := service.walk(ctx, WalkRequest{Name: "memos/a"}, func(int) int { return 0 })
			if !errors.Is(err, want) || result.Item != nil || result.Exhausted {
				t.Fatalf("interruption became success or exhaustion: %+v %v, want %v", result, err, want)
			}
		})
	}
}

func TestDiscoveryRejectsChangedCandidates(t *testing.T) {
	for _, change := range []string{"delete", "content", "tags", "author", "archive"} {
		t.Run(change, func(t *testing.T) {
			service, _, memos := discoveryFixture(t)
			switch change {
			case "delete":
				delete(memos, "memos/b")
			case "content":
				memos["memos/b"]["content"] = "changed"
			case "tags":
				memos["memos/b"]["tags"] = []string{"different"}
			case "author":
				memos["memos/b"]["creator"] = "users/other"
			case "archive":
				memos["memos/b"]["state"] = "ARCHIVED"
			}
			result, err := service.walk(context.Background(), WalkRequest{Name: "memos/a"}, func(int) int { return 0 })
			if err != nil || result.Item == nil || result.Item.Note.Name != "memos/c" {
				t.Fatalf("stale candidate was returned: %+v %v", result, err)
			}
			if change == "delete" || change == "author" {
				names, err := service.Store.Names()
				if err != nil || names["memos/b"] {
					t.Fatalf("invalid source remained in the projection: %v %v", names, err)
				}
			}
		})
	}
}

func TestDiscoverySourceChangesAndFailuresAreAtomic(t *testing.T) {
	for _, test := range []struct {
		change, code string
	}{
		{"content", "SOURCE_CHANGED"}, {"tags", "SOURCE_CHANGED"},
		{"delete", "SOURCE_NOT_FOUND"}, {"author", "SOURCE_NOT_FOUND"},
		{"identity", "IDENTITY_MISMATCH"}, {"offline", "UPSTREAM_UNAVAILABLE"},
	} {
		t.Run(test.change, func(t *testing.T) {
			service, _, memos := discoveryFixture(t)
			identityCalls := 0
			service.Source = fakeClient(service.Store.Settings, func(r *http.Request) (int, any, error) {
				if strings.HasSuffix(r.URL.Path, "/auth/me") {
					identityCalls++
					if test.change == "identity" && identityCalls >= 2 {
						return 200, map[string]any{"user": map[string]string{"name": "users/other"}}, nil
					}
				}
				if status, body, ok := identityResponse(r); ok {
					return status, body, nil
				}
				if strings.HasSuffix(r.URL.Path, "/memos/b") {
					switch test.change {
					case "content":
						memos["memos/a"]["content"] = "changed while selecting"
					case "tags":
						memos["memos/a"]["tags"] = []string{"changed"}
					case "delete":
						delete(memos, "memos/a")
					case "author":
						memos["memos/a"]["creator"] = "users/other"
					case "offline":
						return 503, nil, nil
					}
				}
				if raw := memos[strings.TrimPrefix(r.URL.Path, "/api/v1/")]; raw != nil {
					return 200, raw, nil
				}
				return 404, nil, nil
			})
			result, err := service.walk(context.Background(), WalkRequest{Name: "memos/a"}, func(int) int { return 0 })
			if errorCode(err) != test.code || result.Item != nil {
				t.Fatalf("source failure exposed a partial result: %+v %v", result, err)
			}
			if test.code == "SOURCE_CHANGED" {
				var serviceError *ServiceError
				if !errors.As(err, &serviceError) || serviceError.Status != 409 || !serviceError.Retryable {
					t.Fatalf("source change must be retryable: %v", err)
				}
			}
		})
	}
}

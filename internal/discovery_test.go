package moerise

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
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

func TestDiscoveryWalkModesPathAndExplicitRead(t *testing.T) {
	service, _, _ := discoveryFixture(t)
	ctx := context.Background()
	cont, err := service.Walk(ctx, WalkRequest{Name: "memos/a"})
	if err != nil || cont.Mode != "continue" || cont.Item == nil || cont.Item.Note.Name != "memos/b" ||
		!slices.Equal(cont.Path, []string{"memos/a", "memos/b"}) ||
		!slices.Equal(cont.Item.Matches[0].Signals, []string{"semantic", "tag"}) {
		t.Fatalf("continue must favor a shared top-level topic: %+v %v", cont, err)
	}
	angle, err := service.Walk(ctx, WalkRequest{Name: "memos/a", Mode: "angle"})
	if err != nil || angle.Item == nil || angle.Item.Note.Name != "memos/c" {
		t.Fatalf("angle must favor a different topic: %+v %v", angle, err)
	}
	recent, err := service.recentReads()
	if err != nil || len(recent) != 0 {
		t.Fatalf("displaying a walk item marked it as read: %v %v", recent, err)
	}
	if note, err := service.Open(ctx, "memos/b"); err != nil || note.Name != "memos/b" {
		t.Fatalf("explicit open: %+v %v", note, err)
	}
	cont, err = service.Walk(ctx, WalkRequest{Name: "memos/a"})
	if err != nil || cont.Item == nil || cont.Item.Note.Name != "memos/c" {
		t.Fatalf("recent explicit read did not affect ranking: %+v %v", cont, err)
	}
	penalized, err := service.Walk(ctx, WalkRequest{Name: "memos/a", Path: []string{"memos/c"}})
	if err != nil || penalized.Item == nil || penalized.Item.Note.Name != "memos/b" ||
		math.Abs(penalized.Item.Score-0.8) > 1e-6 ||
		!slices.Contains(penalized.Item.Matches[0].Signals, "recent_read") {
		t.Fatalf("recent read must remain selectable with an explicit penalty: %+v %v", penalized, err)
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

func TestDiscoveryNormalizeAndPathLimit(t *testing.T) {
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
		{}, {Mode: "other"}, {Mode: "angle"}, {Mode: "random", Name: "a"},
		{Name: "memos/a/b"}, {Mode: "random", Path: []string{"memos/"}},
		{Mode: "random", Path: slices.Repeat([]string{"memos/a"}, 101)},
	} {
		if _, err := normalizeWalk(request); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("invalid walk accepted: %+v %v", request, err)
		}
	}
	settings := testSettings(t)
	store := testStore(t, settings)
	raw := rawMemo("available", "a valid random item")
	refreshIndexMemo(t, store, raw)
	client := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		return 200, raw, nil
	})
	service := NewSearchService(store, client, nil)
	path := make([]string, 99)
	for i := range path {
		path[i] = fmt.Sprintf("memos/visited-%03d", i)
	}
	full, err := service.Walk(context.Background(), WalkRequest{Mode: "random", Path: path})
	if err != nil || full.Item == nil || len(full.Path) != 100 || len(path) != 99 {
		t.Fatalf("walk must append at most the hundredth resource: %+v %v", full, err)
	}
	exhausted, err := service.Walk(context.Background(), WalkRequest{Mode: "random", Path: full.Path})
	if err != nil || !exhausted.Exhausted || exhausted.Item != nil || len(exhausted.Path) != 100 {
		t.Fatalf("full path grew beyond its limit: %+v %v", exhausted, err)
	}
	fullSeed, err := service.Walk(context.Background(), WalkRequest{Mode: "random", Name: "memos/available",
		Path: append(slices.Clone(path), "memos/last")})
	if err != nil || fullSeed.Item != nil || !fullSeed.Exhausted || len(fullSeed.Path) != 100 {
		t.Fatalf("adding a seed exceeded a full path: %+v %v", fullSeed, err)
	}
}

func TestDiscoverySemanticReadinessAndModelFreeRandom(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	service := NewSearchService(store, index.Source, nil)
	for _, mode := range []string{"continue", "angle"} {
		if result, err := service.Walk(context.Background(), WalkRequest{Name: "memos/a", Mode: mode}); errorCode(err) != "INDEX_NOT_READY" || result.Item != nil {
			t.Fatalf("%s silently fell back without a published index: %+v %v", mode, result, err)
		}
	}
	finishIndex(t, index)
	store.Settings.Embedding.Enabled = false
	for _, mode := range []string{"continue", "angle"} {
		if result, err := service.Walk(context.Background(), WalkRequest{Name: "memos/a", Mode: mode}); errorCode(err) != "EMBEDDING_DISABLED" || result.Item != nil {
			t.Fatalf("%s silently fell back without embedding: %+v %v", mode, result, err)
		}
	}
	if result, err := service.Walk(context.Background(), WalkRequest{Mode: "random"}); err != nil || result.Item == nil {
		t.Fatalf("random required a model: %+v %v", result, err)
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
	for i := range 120 {
		raw := rawMemo(fmt.Sprintf("random-%03d", i), "random content")
		memos[raw["name"].(string)] = raw
		refreshIndexMemo(t, store, raw)
	}
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
	if err != nil || len(candidates) != searchCandidates {
		t.Fatalf("random candidate cap: %d %v", len(candidates), err)
	}
	result, err := service.Walk(context.Background(), WalkRequest{Mode: "random"})
	if err != nil || result.Item == nil || reads != 1 {
		t.Fatalf("random fetched %d sources: %+v %v", reads, result, err)
	}
}

func TestDiscoveryRecentReviewReadsAndSevenDayWindow(t *testing.T) {
	reviews, _ := reviewFixture(t, 9)
	service := NewSearchService(reviews.Store, reviews.Source, nil)
	ctx, at := context.Background(), time.Now().UTC()
	batch, err := reviews.Daily(ctx, at)
	if err != nil || len(batch.Items) < 3 {
		t.Fatalf("review batch: %+v %v", batch, err)
	}
	opened, skipped, shown := batch.Items[0].Name, batch.Items[1].Name, batch.Items[2].Name
	if _, err := reviews.Mark(ctx, batch.ID, opened, "open", at); err != nil {
		t.Fatal(err)
	}
	if _, err := reviews.Mark(ctx, batch.ID, skipped, "skip", at); err != nil {
		t.Fatal(err)
	}
	recent, err := service.recentReads()
	if err != nil || !recent[opened] || recent[skipped] || recent[shown] {
		t.Fatalf("only opened reviews should count as reads: %v %v", recent, err)
	}
	path := []string{}
	names, err := service.Store.Names()
	if err != nil {
		t.Fatal(err)
	}
	for name := range names {
		if name != opened && name != shown {
			path = append(path, name)
		}
	}
	walk, err := service.Walk(ctx, WalkRequest{Mode: "random", Path: path})
	if err != nil || walk.Item == nil || walk.Item.Note.Name != shown {
		t.Fatalf("opened review did not lower walk priority: %+v %v", walk, err)
	}
	if _, err := service.Open(ctx, skipped); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.state.Exec(`UPDATE discovery_reads SET opened_at = ?`,
		at.Add(-8*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.state.Exec(`UPDATE review_items SET opened_at = ? WHERE memo_name = ?`,
		at.Add(-8*24*time.Hour).Format(time.RFC3339Nano), opened); err != nil {
		t.Fatal(err)
	}
	recent, err = service.recentReads()
	if err != nil || len(recent) != 0 {
		t.Fatalf("expired reads still affect discovery: %v %v", recent, err)
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
			result, err := service.Walk(context.Background(), WalkRequest{Name: "memos/a"})
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
			result, err := service.Walk(context.Background(), WalkRequest{Name: "memos/a"})
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

func TestDiscoveryOpenFailureDoesNotRecordRead(t *testing.T) {
	for _, cause := range []string{"missing", "author", "identity", "offline"} {
		t.Run(cause, func(t *testing.T) {
			settings := testSettings(t)
			store := testStore(t, settings)
			identities := 0
			client := fakeClient(settings, func(r *http.Request) (int, any, error) {
				if strings.HasSuffix(r.URL.Path, "/auth/me") {
					identities++
					if cause == "identity" && identities == 2 {
						return 200, map[string]any{"user": map[string]string{"name": "users/other"}}, nil
					}
				}
				if status, body, ok := identityResponse(r); ok {
					return status, body, nil
				}
				switch cause {
				case "missing":
					return 404, nil, nil
				case "offline":
					return 503, nil, nil
				}
				raw := rawMemo("a", "source")
				if cause == "author" {
					raw["creator"] = "users/other"
				}
				return 200, raw, nil
			})
			service := NewSearchService(store, client, nil)
			note, err := service.Open(context.Background(), "memos/a")
			if err == nil || note.Name != "" {
				t.Fatalf("failed open exposed a note: %+v %v", note, err)
			}
			reads, err := service.recentReads()
			if err != nil || len(reads) != 0 {
				t.Fatalf("failed open recorded a read: %v %v", reads, err)
			}
		})
	}
}

func TestDiscoveryReadsSurviveRestartRebuildAndRestore(t *testing.T) {
	service, _, _ := discoveryFixture(t)
	if _, err := service.Open(context.Background(), "memos/b"); err != nil {
		t.Fatal(err)
	}
	settings := service.Store.Settings
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if _, err := Backup(settings, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Open(context.Background(), "memos/c"); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := testStore(t, settings)
	reads, err := NewSearchService(restarted, service.Source, nil).recentReads()
	if err != nil || !reads["memos/b"] || !reads["memos/c"] {
		t.Fatalf("restart lost explicit reads: %v %v", reads, err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildIndex(settings); err != nil {
		t.Fatal(err)
	}
	rebuilt := testStore(t, settings)
	reads, err = NewSearchService(rebuilt, service.Source, nil).recentReads()
	if err != nil || !reads["memos/b"] || !reads["memos/c"] {
		t.Fatalf("index rebuild lost explicit reads: %v %v", reads, err)
	}
	if err := rebuilt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(settings, snapshot, filepath.Join(t.TempDir(), "safety")); err != nil {
		t.Fatal(err)
	}
	restored := NewSearchService(testStore(t, settings), service.Source, nil)
	reads, err = restored.recentReads()
	if err != nil || !reads["memos/b"] || reads["memos/c"] {
		t.Fatalf("restore did not recover the snapshot's read history: %v %v", reads, err)
	}
}

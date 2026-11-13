package moerise

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func mapServiceFixture(t *testing.T) (*MapService, *VectorIndexService, map[string]map[string]any, *indexEmbedding) {
	t.Helper()
	store, index, memos, provider := indexFixture(t)
	return NewMapService(store), index, memos, provider
}

func readyMapService(t *testing.T) (*MapService, *VectorIndexService, map[string]map[string]any, *indexEmbedding, MapResponse) {
	t.Helper()
	service, index, memos, provider := mapServiceFixture(t)
	finishIndex(t, index)
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, err := service.Read(t.Context())
	if err != nil || first.Status != "ready" || len(first.Points) != 5 || first.GeneratedAt == "" {
		t.Fatalf("published map: %+v %v", first, err)
	}
	return service, index, memos, provider, first
}

func TestMapServiceReadStatusesAndEmpty(t *testing.T) {
	service, index, memos, _ := mapServiceFixture(t)
	service.Store.Settings.Embedding.Enabled = false
	result, err := service.Read(t.Context())
	if err != nil || result.Status != "disabled" || result.Points == nil || len(result.Points) != 0 {
		t.Fatalf("disabled: %+v %v", result, err)
	}
	service.Store.Settings.Embedding.Enabled = true
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "waiting" {
		t.Fatalf("waiting: %+v %v", result, err)
	}
	id, err := index.prepare()
	if err != nil {
		t.Fatal(err)
	}
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "building" {
		t.Fatalf("building: %+v %v", result, err)
	}
	if _, err := service.Store.index.Exec("UPDATE vector_generations SET last_error = 'EMBEDDING_AUTH' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "unavailable" || len(result.Points) != 0 {
		t.Fatalf("failed: %+v %v", result, err)
	}
	for name := range memos {
		delete(memos, name)
		if err := service.Store.Refresh(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	finishIndex(t, index)
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "building" {
		t.Fatalf("read performed background work: %+v %v", result, err)
	}
	service.Reset()
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "empty" || result.LayoutVersion == "" || result.VectorGeneration == "" || result.Points == nil {
		t.Fatalf("empty: %+v %v", result, err)
	}

}

func TestMapReadsAndUnchangedDailyChecksNeverScanVectorsOrCallModels(t *testing.T) {
	service, _, _, provider, first := readyMapService(t)
	calls := len(provider.texts)
	// A corrupt vector makes any accidental full scan fail, including after restart.
	if _, err := service.Store.index.Exec("UPDATE vector_chunks SET vector = ?", []byte{1}); err != nil {
		t.Fatal(err)
	}
	for _, reader := range []*MapService{service, NewMapService(service.Store)} {
		result, err := reader.Read(t.Context())
		if err != nil || !reflect.DeepEqual(first, result) {
			t.Fatalf("snapshot read rescanned vectors: %v", err)
		}
		stamp, _ := time.Parse(time.RFC3339Nano, first.GeneratedAt)
		if err := reader.update(t.Context(), stamp.Add(2*mapSnapshotInterval)); err != nil {
			t.Fatal(err)
		}
	}
	if len(provider.texts) != calls {
		t.Fatal("snapshot called embedding model")
	}
}

func TestMapDailySnapshotCombinesChangesAndPersistsSchedule(t *testing.T) {
	service, index, memos, _, first := readyMapService(t)
	stamp, _ := time.Parse(time.RFC3339Nano, first.GeneratedAt)
	tags := []string{"R&D", "C++", "中文"}
	memos["memos/a"]["tags"] = tags
	changed, _ := normalizeMemo(memos["memos/a"])
	if err := service.Store.Refresh(changed.Name, &changed); err != nil {
		t.Fatal(err)
	}
	delete(memos, "memos/b")
	if err := service.Store.Refresh("memos/b", nil); err != nil {
		t.Fatal(err)
	}
	added := testMemo(t, "new", "new note")
	memos[added.Name] = rawMemo("new", "new note")
	if err := service.Store.Refresh(added.Name, &added); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, index)
	restarted := NewMapService(service.Store)
	for _, worker := range []*MapService{service, restarted} {
		if err := worker.update(t.Context(), stamp.Add(23*time.Hour)); err != nil {
			t.Fatal(err)
		}
		result, err := worker.Read(t.Context())
		if err != nil || !reflect.DeepEqual(first, result) {
			t.Fatalf("ordinary changes bypassed daily interval: %v", err)
		}
	}
	if err := service.update(t.Context(), stamp.Add(mapSnapshotInterval)); err != nil {
		t.Fatal(err)
	}
	second, err := service.Read(t.Context())
	if err != nil || second.LayoutVersion == first.LayoutVersion || second.LayoutFrame != first.LayoutFrame || len(second.Points) != len(first.Points) || !reflect.DeepEqual(second.Points[0].Tags, tags) {
		t.Fatalf("daily combined snapshot: %+v %v", second, err)
	}
	for _, old := range first.Points {
		for _, point := range second.Points {
			if point.Name == old.Name && (point.X != old.X || point.Y != old.Y) {
				t.Fatal("unchanged vectors moved")
			}
			if point.Name == "memos/b" {
				t.Fatal("daily snapshot retained deleted note")
			}
		}
	}
}

func TestMapOldSnapshotSurvivesIndexingAndNewSpaceNeverReadsOldPoints(t *testing.T) {
	service, index, _, _, first := readyMapService(t)
	stamp, _ := time.Parse(time.RFC3339Nano, first.GeneratedAt)
	added := testMemo(t, "new", "pending note")
	if err := service.Store.Refresh(added.Name, &added); err != nil {
		t.Fatal(err)
	}
	if err := service.update(t.Context(), stamp.Add(mapSnapshotInterval)); err != nil {
		t.Fatal(err)
	}
	result, err := service.Read(t.Context())
	if err != nil || !reflect.DeepEqual(result, first) {
		t.Fatalf("indexing hid available snapshot: %v", err)
	}
	model := "another-space"
	service.Store.Settings.Embedding.Model = &model
	result, err = service.Read(t.Context())
	if err != nil || len(result.Points) != 0 || result.LayoutVersion != "" {
		t.Fatalf("mixed spaces: %+v %v", result, err)
	}
	finishIndex(t, index)
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "ready" || result.LayoutFrame == first.LayoutFrame {
		t.Fatalf("new space waited for daily interval: %+v %v", result, err)
	}
	service.Store.Settings.Embedding.Enabled = false
	result, err = service.Read(t.Context())
	if err != nil || result.Status != "disabled" || len(result.Points) != 0 {
		t.Fatalf("disabled retained snapshot: %+v %v", result, err)
	}
}

func TestMapConcurrentUpdatesCoalesceAndReadsDoNotWaitForGeneration(t *testing.T) {
	service, _, _, _, first := readyMapService(t)
	stamp, _ := time.Parse(time.RFC3339Nano, first.GeneratedAt)
	held, err := service.Store.index.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.update(ctx, stamp.Add(mapSnapshotInterval)) }()
	result, err := service.Read(t.Context())
	if err != nil || !reflect.DeepEqual(first, result) {
		t.Fatalf("read blocked behind builder: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("builder ignored cancellation: %v", err)
	}
	held.Rollback()
	service.Reset()
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if err := service.Update(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	result, err = service.Read(t.Context())
	if err != nil || !reflect.DeepEqual(result, first) {
		t.Fatalf("concurrent worker changed snapshot: %v", err)
	}
}

func TestMapColdDatabaseReadRespectsBudget(t *testing.T) {
	service, _, _, _, _ := readyMapService(t)
	service.Reset()
	held, err := service.Store.index.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := service.Read(ctx)
	if time.Since(start) > 500*time.Millisecond || errorCode(err) != "MAP_RESOURCE_LIMIT" || result.Points != nil {
		t.Fatalf("cold read ignored budget: %+v %v", result, err)
	}
}

func clientMapIndex(t *testing.T, api *ClientAPI) *VectorIndexService {
	t.Helper()
	store := api.app.Store
	store.Settings.Embedding = embeddingTestConfig("http://embedding.test")
	for _, name := range []string{"a", "b", "archived"} {
		raw := rawMemo(name, "private body "+name)
		if name == "archived" {
			raw["state"] = "ARCHIVED"
		}
		memo, _ := normalizeMemo(raw)
		if err := store.Refresh(memo.Name, &memo); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Advance(nowText(), true); err != nil {
		t.Fatal(err)
	}
	source := fakeClient(store.Settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if r.URL.Path == "/api/v1/memos" {
			items := []any{}
			for _, name := range []string{"a", "b", "archived"} {
				raw := rawMemo(name, "private body "+name)
				if name == "archived" {
					raw["state"] = "ARCHIVED"
				}
				if raw["state"] == r.URL.Query().Get("state") {
					items = append(items, raw)
				}
			}
			return 200, map[string]any{"memos": items}, nil
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/memos/")
		raw := rawMemo(name, "private body "+name)
		if name == "archived" {
			raw["state"] = "ARCHIVED"
		}
		return 200, raw, nil
	})
	t.Cleanup(source.Close)
	api.app.Notes.Source = source
	index := NewVectorIndexService(store, source, &indexEmbedding{dimensions: store.Settings.Embedding.Dimensions})
	finishIndex(t, index)
	if err := api.app.Map.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	return index
}

package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func starsFixture(t *testing.T, wait func()) (*MapService, *VectorIndexService, map[string]map[string]any, *atomic.Int32) {
	t.Helper()
	store, index, memos, _ := indexFixture(t)
	finishIndex(t, index)
	service, calls := testStarsService(t, store, wait)
	return service, index, memos, calls
}

func testStarsService(t *testing.T, store *Store, wait func()) (*MapService, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input starRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("invalid worker request")
			w.WriteHeader(400)
			return
		}
		call := calls.Add(1)
		if wait != nil {
			wait()
		}
		frame := input.Frame
		if frame == "" {
			frame = fmt.Sprintf("%032x", call)
		}
		result := starResult{Algorithm: starsAlgorithm, Frame: frame, Points: []MapPoint{}, Topics: []MapTopic{}, Links: []MapLink{}}
		old := map[string]starPreviousPoint{}
		for _, point := range input.PreviousPoints {
			old[point.Name] = point
		}
		for i, document := range input.Documents {
			point := MapPoint{Name: document.Name, X: float64(i) / 10, Y: float64(i) / 7,
				Stellar: &MapStellar{Seed: .5, Role: "ordinary", Radius: 1, Luminosity: .7}}
			if previous, ok := old[document.Name]; ok && previous.VectorHash == document.VectorHash {
				point.X, point.Y, point.Stellar = previous.X, previous.Y, previous.Stellar
			}
			result.Points = append(result.Points, point)
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(worker.Close)
	service := NewMapService(store)
	service.workerURL = worker.URL
	return service, calls
}

func TestStarsSnapshotRestartIncrementalAndRebuild(t *testing.T) {
	service, index, memos, calls := starsFixture(t, nil)
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, err := service.Read(t.Context())
	if err != nil || first.Algorithm != starsAlgorithm || first.Status != "ready" {
		t.Fatalf("initial stars: %+v %v", first, err)
	}
	restarted := NewMapService(service.Store)
	restarted.workerURL = service.workerURL
	cold, err := restarted.Read(t.Context())
	if err != nil || !reflect.DeepEqual(cold, first) || calls.Load() != 1 {
		t.Fatalf("restart recomputed layout: %v", err)
	}
	added := testMemo(t, "stars-added", "a new semantic note")
	memos[added.Name] = rawMemo("stars-added", "a new semantic note")
	if err := service.Store.Refresh(added.Name, &added); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, index)
	stamp, _ := time.Parse(time.RFC3339Nano, first.GeneratedAt)
	if err := restarted.update(t.Context(), stamp.Add(mapSnapshotInterval)); err != nil {
		t.Fatal(err)
	}
	second, err := restarted.Read(t.Context())
	if err != nil || second.LayoutFrame != first.LayoutFrame || len(second.Points) != len(first.Points)+1 {
		t.Fatalf("incremental frame: %+v %v", second, err)
	}
	for _, before := range first.Points {
		for _, after := range second.Points {
			if before.Name == after.Name && (before.X != after.X || before.Y != after.Y || !reflect.DeepEqual(before.Stellar, after.Stellar)) {
				t.Fatal("unchanged star moved or changed appearance")
			}
		}
	}
	if err := restarted.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	third, err := restarted.Read(t.Context())
	if err != nil || third.LayoutFrame == second.LayoutFrame {
		t.Fatalf("rebuild retained the frame: %v", err)
	}
	var count int
	if err := service.Store.index.QueryRow("SELECT COUNT(*) FROM star_snapshots").Scan(&count); err != nil || count != 2 {
		t.Fatalf("snapshot retention: %d %v", count, err)
	}
}

func TestStarsWorkerOwnsNoRuntimeOrIndexLockAndRejectsNewSpace(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	service, _, _, _ := starsFixture(t, func() { close(entered); <-release })
	service.Store.runtimeMu = &sync.RWMutex{}
	done := make(chan error, 1)
	go func() { done <- service.Update(t.Context()) }()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	unlock, err := service.Store.lockBackground(ctx)
	if err != nil {
		close(release)
		t.Fatal("layout computation held the indexing lock")
	}
	unlock()
	if !service.Store.runtimeMu.TryLock() {
		close(release)
		t.Fatal("layout computation held the settings lock")
	}
	model := "replacement-model"
	service.Store.Settings.Embedding.Model = &model
	service.Store.runtimeMu.Unlock()
	close(release)
	if err := <-done; errorCode(err) != "MAP_INDEX_CHANGED" {
		t.Fatalf("stale model space published: %v", err)
	}
	var count int
	if err := service.Store.index.QueryRow("SELECT COUNT(*) FROM star_snapshots").Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale snapshot retained: %d %v", count, err)
	}
}

func TestStarsDisableAndReenableRejectsInFlightResult(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	service, _, _, calls := starsFixture(t, func() {
		if blocked.Load() {
			close(entered)
			<-release
		}
	})
	defer unblock()
	service.Store.runtimeMu = &sync.RWMutex{}
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, err := service.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	blocked.Store(true)
	done := make(chan error, 1)
	go func() { done <- service.Update(t.Context()) }()
	<-entered
	service.Store.runtimeMu.Lock()
	service.Store.Settings.Stargazing.Enabled = false
	service.Reset()
	service.Store.runtimeMu.Unlock()
	disabled, err := service.Read(t.Context())
	if err != nil || disabled.Status != "disabled" || disabled.DisabledReason != "stargazing" || len(disabled.Points) != 0 {
		t.Fatalf("disabled stars: %+v %v", disabled, err)
	}
	service.Store.runtimeMu.Lock()
	service.Store.Settings.Stargazing.Enabled = true
	service.Reset()
	service.Store.runtimeMu.Unlock()
	unblock()
	if err := <-done; errorCode(err) != "MAP_INDEX_CHANGED" {
		t.Fatalf("result from before disabling was published: %v", err)
	}
	restored, err := service.Read(t.Context())
	if err != nil || !reflect.DeepEqual(first, restored) || calls.Load() != 2 {
		t.Fatalf("saved snapshot changed: %+v %v", restored, err)
	}
}

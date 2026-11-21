package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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
	if err := restarted.update(t.Context(), nextMapSchedule(stamp, restarted.Store.Settings.Timezone)); err != nil {
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

func TestStarsBatchLLMNamingFallbackBoundsAndExcerptInvalidation(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	longBody := "# 周末研究\n\n- **Rust** [tokio](https://tokio.rs) ![图](img.png) " + strings.Repeat("异步并发模型与运行时架构探讨。", 12)
	edited, _ := normalizeMemo(rawMemo("a", longBody))
	memos[edited.Name] = rawMemo("a", longBody)
	if err := store.Refresh(edited.Name, &edited); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, index)

	service := NewMapService(store)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input starRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			w.WriteHeader(400)
			return
		}
		points := make([]MapPoint, len(input.Documents))
		members := make([]string, len(input.Documents))
		for i, doc := range input.Documents {
			points[i] = MapPoint{Name: doc.Name, X: float64(i), Y: 0.5,
				Stellar: &MapStellar{Seed: 0.5, Role: "ordinary", Radius: 1, Luminosity: 0.8}}
			members[i] = doc.Name
		}
		topics := []MapTopic{{
			ID: "topic-0123456789abcdef", Label: "tokio · rust", X: 1, Y: 0.5, Members: members,
			Districts: []MapDistrict{{ID: "district-0123456789abcdef", Label: "runtime", X: 1, Y: 0.5, Members: members[:2]}},
		}}
		_ = json.NewEncoder(w).Encode(starResult{Algorithm: starsAlgorithm, Frame: strings.Repeat("a", 32), Points: points, Topics: topics, Links: []MapLink{}})
	}))
	defer worker.Close()
	service.workerURL = worker.URL

	llmCalls := atomic.Int32{}
	var capturedPrompt string
	var currentProvider LLMProvider = aiLLMFunc(func(ctx context.Context, messages []LLMMessage, _ func(string) error) (LLMResult, error) {
		llmCalls.Add(1)
		capturedPrompt = messages[1].Content
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			t.Fatal("LLM cluster naming missed 15s deadline")
		}
		return LLMResult{Content: "```json\n{\"topic-0123456789abcdef\":\"异步运行时架构\"}\n```"}, nil
	})
	service.acquireLLM = func() (LLMProvider, func()) { return currentProvider, func() {} }
	store.Settings.LLM.Enabled = true
	store.Settings.Stargazing.LLMNaming = true

	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	if llmCalls.Load() != 1 || strings.Contains(capturedPrompt, "https://tokio.rs") {
		t.Fatalf("unexpected LLM naming calls=%d prompt=%s", llmCalls.Load(), capturedPrompt)
	}
	snap, err := service.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Topics[0].Label != "异步运行时架构" || snap.Topics[0].Districts[0].Label != "runtime" {
		t.Fatalf("LLM naming or c-TF-IDF partial fallback failed: %+v", snap.Topics)
	}
	var pointA MapPoint
	for _, p := range snap.Points {
		if p.Name == "memos/a" {
			pointA = p
		}
	}
	if pointA.CreateTime == "" || len([]rune(pointA.Excerpt)) > 91 || strings.Contains(pointA.Excerpt, "**") || strings.Contains(pointA.Excerpt, "https://") {
		t.Fatalf("unexpected excerpt formatting: %+v", pointA)
	}

	added := testMemo(t, "added", "增量笔记正文")
	memos[added.Name] = rawMemo("added", "增量笔记正文")
	if err := store.Refresh(added.Name, &added); err != nil {
		t.Fatal(err)
	}
	finishIndex(t, index)
	stamp, _ := time.Parse(time.RFC3339Nano, snap.GeneratedAt)
	if err := service.update(t.Context(), nextMapSchedule(stamp, store.Settings.Timezone)); err != nil {
		t.Fatal(err)
	}
	if llmCalls.Load() != 1 {
		t.Fatalf("incremental update re-called LLM: %d", llmCalls.Load())
	}

	archived := rawMemo("a", longBody)
	archived["state"] = "ARCHIVED"
	archivedMemo, _ := normalizeMemo(archived)
	if err := store.Refresh("memos/a", &archivedMemo); err != nil {
		t.Fatal(err)
	}
	if err := store.Refresh("memos/added", nil); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := NewMapService(store).Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range afterDelete.Points {
		if (p.Name == "memos/a" || p.Name == "memos/added") && p.Excerpt != "" {
			t.Fatalf("deleted or archived note kept stale excerpt: %+v", p)
		}
	}

	currentProvider = aiLLMFunc(func(context.Context, []LLMMessage, func(string) error) (LLMResult, error) {
		llmCalls.Add(1)
		return LLMResult{}, context.DeadlineExceeded
	})
	if err := service.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	fallbackSnap, err := service.Read(t.Context())
	if err != nil || fallbackSnap.Topics[0].Label != "tokio · rust" {
		t.Fatalf("timeout fallback failed: %+v %v", fallbackSnap.Topics, err)
	}
}

func TestMapScheduleDailyThreeAMTimezoneAndSnapshotSizes(t *testing.T) {
	base := time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)
	hkDue := nextMapSchedule(base, "Asia/Hong_Kong")
	wantHK := time.Date(2026, 11, 21, 19, 0, 0, 0, time.UTC)
	if !hkDue.Equal(wantHK) {
		t.Fatalf("hk due=%v want=%v", hkDue, wantHK)
	}
	afterThree := wantHK.Add(time.Minute)
	nextDayHK := nextMapSchedule(afterThree, "Asia/Hong_Kong")
	if !nextDayHK.Equal(wantHK.Add(24 * time.Hour)) {
		t.Fatalf("next day hk=%v want=%v", nextDayHK, wantHK.Add(24*time.Hour))
	}

	excerpt := strings.Repeat("中文星图摘要测试内容示例", 7) + "。"
	for _, n := range []int{1000, 5000} {
		points := make([]MapPoint, n)
		for i := range points {
			points[i] = MapPoint{
				Name:       fmt.Sprintf("memos/n%05d", i),
				SourceHash: strings.Repeat("a", 64),
				X:          0.1234567, Y: -0.7654321, Semantic: true,
				Tags:       []string{"开发", "架构"},
				Excerpt:    excerpt,
				CreateTime: "2026-11-21T08:00:00Z",
				Stellar:    &MapStellar{Seed: 0.5, Role: "ordinary", Radius: 1.1, Luminosity: 0.72},
			}
		}
		resp := MapResponse{
			Status: "ready", LayoutVersion: strings.Repeat("b", 64), LayoutFrame: strings.Repeat("c", 64),
			Algorithm: starsAlgorithm, VectorGeneration: "gen-1", ProjectionRevision: 1,
			GeneratedAt: "2026-11-21T03:00:00Z", Points: points, Topics: []MapTopic{}, Links: []MapLink{},
		}
		encoded, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("map snapshot points=%d bytes=%d (%.2f MiB)", n, len(encoded), float64(len(encoded))/(1024*1024))
		if len(encoded) > 8*1024*1024 {
			t.Fatalf("5000-point snapshot exceeded 8 MiB bound: %d", len(encoded))
		}
	}
}

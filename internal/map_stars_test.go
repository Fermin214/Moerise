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
	store.Settings.Stargazing.Enabled = true
	calls := &atomic.Int32{}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wait != nil {
			wait()
		}
		_ = json.NewEncoder(w).Encode(testStarsResult(t, r, calls.Add(1)))
	}))
	t.Cleanup(worker.Close)
	service := NewMapService(store)
	service.workerURL = worker.URL
	return service, calls
}

func testStarsResult(t *testing.T, request *http.Request, call int32) starResult {
	t.Helper()
	var input starRequest
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		t.Errorf("invalid worker request: %v", err)
	}
	frame := input.Frame
	if frame == "" {
		frame = fmt.Sprintf("%032x", call)
	}
	result := starResult{Algorithm: starsAlgorithm, NamingVersion: starsNamingVersion,
		Frame: frame, Points: []MapPoint{}, Topics: []MapTopic{}, Links: []MapLink{}}
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
	return result
}

func TestStarsHealthKeepsSnapshotDuringOutage(t *testing.T) {
	service, _, _, calls := starsFixture(t, nil)
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, err := service.Read(t.Context())
	if err != nil || first.Status != "ready" {
		t.Fatalf("initial stars: %+v %v", first, err)
	}
	var probes, status atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/health" {
			t.Errorf("unexpected layout request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		probes.Add(1)
		w.WriteHeader(int(status.Load()))
		fmt.Fprintf(w, `{"status":"ready","naming_version":%d}`, starsNamingVersion)
	}))
	t.Cleanup(worker.Close)
	service.workerURL = worker.URL
	status.Store(503)
	service.checkLayout(t.Context())
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	down, err := service.Read(t.Context())
	if err != nil || down.UpdateState != "" || down.UnavailableReason != layoutUnavailable {
		t.Fatalf("layout outage: %+v %v", down, err)
	}
	down.UnavailableReason = ""
	if !reflect.DeepEqual(first, down) {
		t.Fatal("layout outage changed the published snapshot")
	}
	status.Store(200)
	service.checkLayout(t.Context())
	restored, err := service.Read(t.Context())
	if err != nil || !reflect.DeepEqual(first, restored) || calls.Load() != 1 {
		t.Fatalf("layout recovery changed the snapshot: %+v %v", restored, err)
	}
	service.Store.Settings.Stargazing.Enabled = false
	service.Reset()
	service.checkLayout(t.Context())
	if probes.Load() != 2 {
		t.Fatalf("disabled stars issued a health request: %d", probes.Load())
	}
}

func TestStarsOldLayoutReportsIncompatibilityAndResumesAfterUpgrade(t *testing.T) {
	service, _, _, _ := starsFixture(t, nil)
	var calls atomic.Int32
	var upgraded atomic.Bool
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			if upgraded.Load() {
				fmt.Fprintf(w, `{"status":"ready","naming_version":%d}`, starsNamingVersion)
			} else {
				fmt.Fprint(w, `{"status":"ready","last_job":{}}`)
			}
			return
		}
		result := testStarsResult(t, r, calls.Add(1))
		if upgraded.Load() {
			_ = json.NewEncoder(w).Encode(result)
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"algorithm": result.Algorithm, "frame": result.Frame,
				"points": result.Points, "topics": result.Topics, "links": result.Links})
		}
	}))
	t.Cleanup(worker.Close)
	service.workerURL = worker.URL
	if err := service.Update(t.Context()); errorCode(err) != "MAP_LAYOUT_INCOMPATIBLE" {
		t.Fatalf("old response not diagnosed: %v", err)
	}
	service.checkLayout(t.Context())
	response, err := service.Read(t.Context())
	if err != nil || response.Status != "unavailable" || response.UnavailableReason != layoutIncompatible {
		t.Fatalf("unexpected old service state: %+v %v", response, err)
	}
	if err := service.update(t.Context(), time.Now().Add(time.Minute)); err != nil || calls.Load() != 1 {
		t.Fatalf("incompatible worker retried: %v, calls=%d", err, calls.Load())
	}
	upgraded.Store(true)
	service.checkLayout(t.Context())
	select {
	case <-service.wake:
	default:
		t.Fatal("compatible worker did not wake the scheduler")
	}
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	response, err = service.Read(t.Context())
	if err != nil || response.Status != "ready" || response.UnavailableReason != "" || response.UpdateState != "" {
		t.Fatalf("unexpected upgraded service state: %+v %v", response, err)
	}
}

func TestStarsRebuildClearsOutageBeforeNextHealthProbe(t *testing.T) {
	service, _, _, _ := starsFixture(t, nil)
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	initial, err := service.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			close(entered)
			<-release
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(testStarsResult(t, r, 2))
	}))
	t.Cleanup(worker.Close)
	t.Cleanup(unblock)
	service.workerURL = worker.URL
	service.layoutHealth.Store(&layoutHealth{key: service.cacheKey(), reason: layoutUnavailable})
	checked := make(chan struct{})
	go func() { defer close(checked); service.checkLayout(t.Context()) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("layout health probe did not arrive")
	}
	if err := service.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	unblock()
	<-checked
	result, err := service.Read(t.Context())
	if err != nil || result.Status != "ready" || result.LayoutFrame == initial.LayoutFrame ||
		result.UpdateState != "" || result.UnavailableReason != "" {
		t.Fatalf("stale outage masked a new frame: %+v %v", result, err)
	}
}

func TestStarsLegacyNamingUpdatesWithoutIndexChanges(t *testing.T) {
	service, _, _, calls := starsFixture(t, nil)
	if err := service.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := publishedStars(t, service)
	if _, err := service.Store.index.Exec("UPDATE star_snapshots SET naming_version = 0"); err != nil {
		t.Fatal(err)
	}
	restarted := NewMapService(service.Store)
	restarted.workerURL = service.workerURL
	if err := restarted.Update(t.Context()); err != nil {
		t.Fatal(err)
	}
	second := publishedStars(t, restarted)
	if calls.Load() != 2 || second.namingVersion != starsNamingVersion || first.frame != second.frame ||
		!reflect.DeepEqual(first.points, second.points) {
		t.Fatal("legacy naming did not republish the existing layout")
	}
	if err := restarted.Update(t.Context()); err != nil || calls.Load() != 2 {
		t.Fatalf("current naming triggered an extra generation: %v", err)
	}
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

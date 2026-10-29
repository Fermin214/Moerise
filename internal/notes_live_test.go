package moerise

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type notesLiveHTTPFixture struct {
	service       *NoteService
	mu            sync.Mutex
	rows          []map[string]any
	failAt        string
	failureHit    bool
	requests      int
	profileChecks int
}

func newNotesLiveHTTPFixture(t *testing.T) *notesLiveHTTPFixture {
	t.Helper()
	settings := testSettings(t)
	f := &notesLiveHTTPFixture{rows: []map[string]any{
		liveRawMemo("z", "list version"), liveRawMemo("a", "another memo"),
	}}
	client := liveProbeClient(t, settings, func(r *http.Request) (int, any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		switch r.URL.Path {
		case "/api/v1/instance/profile":
			f.profileChecks++
			if f.failAt == "identity" || (f.failAt == "final-identity" && f.profileChecks > 1) {
				f.failureHit = true
				return 503, map[string]any{}, nil
			}
			return 200, map[string]string{"version": "0.31.0"}, nil
		case "/api/v1/auth/me":
			return 200, map[string]any{"user": map[string]string{"name": settings.Memos.User}}, nil
		case "/api/v1/memos":
			if f.failAt == "list" {
				f.failureHit = true
				return 503, map[string]any{}, nil
			}
			if r.URL.Query().Get("orderBy") != "create_time desc" ||
				!strings.HasPrefix(r.URL.Query().Get("filter"), "creator == '"+settings.Memos.User+"'") {
				t.Errorf("unexpected live source query: %s", r.URL)
			}
			// 模拟官方 offset 分页；同时间条目由来源适配器统一排序。
			offset, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
			size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
			end := min(offset+size, len(f.rows))
			if offset > end || size < 1 {
				return 400, map[string]any{}, nil
			}
			next := ""
			if end < len(f.rows) {
				next = strconv.Itoa(end)
			}
			return 200, map[string]any{"memos": f.rows[offset:end], "nextPageToken": next}, nil
		}
		if (f.failAt == "get" && r.URL.Path == "/api/v1/memos/z") ||
			(f.failAt == "get-after-one" && r.URL.Path == "/api/v1/memos/a") {
			f.failureHit = true
			return 503, map[string]any{}, nil
		}
		for _, row := range f.rows {
			if r.URL.Path == "/api/v1/"+row["name"].(string) {
				fresh := map[string]any{}
				for key, value := range row {
					fresh[key] = value
				}
				fresh["content"] = "  fresh from Get\n#tag  "
				return 200, fresh, nil
			}
		}
		return 404, map[string]any{}, nil
	})
	settings.Memos.BaseURL = client.config.BaseURL
	f.service = NewNoteService(settings, testStore(t, settings), client)
	return f
}

func (f *notesLiveHTTPFixture) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func notesLivePageNames(page NotePage) []string {
	names := []string{}
	for _, note := range page.Notes {
		names = append(names, note.Name)
	}
	return names
}

type notesLiveGetSource struct {
	Source
	get func(context.Context, string) (*Memo, error)
}

func (s *notesLiveGetSource) Get(ctx context.Context, name string) (*Memo, error) {
	return s.get(ctx, name)
}

func (s *notesLiveGetSource) ListLivePage(ctx context.Context, state string, size int, token string) ([]Memo, string, error) {
	return s.Source.(LiveSource).ListLivePage(ctx, state, size, token)
}

func waitNotesLiveSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for live source workers")
	}
}

func TestNotesLiveBoundedConcurrentOrder(t *testing.T) {
	f := newNotesLiveHTTPFixture(t)
	f.rows = nil
	for i := range 21 {
		row := liveRawMemo(strconv.Itoa(100+i), "candidate")
		row["createTime"] = time.Date(2020, 1, 1, 0, 0, 30-i/2, 0, time.UTC).Format(time.RFC3339)
		f.rows = append(f.rows, row)
	}
	want := []string{}
	for i := 0; i < 20; i += 2 {
		want = append(want, f.rows[i+1]["name"].(string), f.rows[i]["name"].(string))
	}
	ctx, cancel := context.WithCancel(context.Background())
	started, completed := make(chan struct{}, 20), make(chan struct{}, 20)
	release, releaseFirst := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	active, peak := 0, 0
	source := f.service.Source
	f.service.Source = &notesLiveGetSource{Source: source, get: func(ctx context.Context, name string) (*Memo, error) {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
			completed <- struct{}{}
		}()
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if name == want[0] {
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		memo, err := source.Get(ctx, name)
		if err == nil && memo != nil {
			switch name {
			case want[3]:
				memo.Creator = "users/other"
			case want[7]:
				memo.State = "ARCHIVED"
			case want[9]:
				memo = nil
			}
		}
		return memo, err
	}}
	var page NotePage
	var err error
	done := make(chan struct{})
	go func() {
		page, err = f.service.ListLive(ctx, NoteList{})
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		waitNotesLiveSignal(t, done)
	})
	for range 4 {
		waitNotesLiveSignal(t, started)
	}
	close(release)
	// 让第一页首条最后完成，验证响应不能按网络完成顺序拼接。
	for range 19 {
		waitNotesLiveSignal(t, completed)
	}
	close(releaseFirst)
	waitNotesLiveSignal(t, done)
	wantNotes := []string{}
	for i, name := range want {
		if i != 3 && i != 7 && i != 9 {
			wantNotes = append(wantNotes, name)
		}
	}
	if err != nil || !reflect.DeepEqual(notesLivePageNames(page), wantNotes) || page.NextPageToken == "" {
		t.Fatalf("concurrent checks changed order, scope, or cursor: %+v %v", page, err)
	}
	if peak != 4 || active != 0 {
		t.Fatalf("unexpected source concurrency: peak=%d active=%d", peak, active)
	}
	for _, note := range page.Notes {
		if note.Content != "  fresh from Get\n#tag  " {
			t.Fatal("concurrent checks returned candidate text")
		}
	}
	f.mu.Lock()
	checks := f.profileChecks
	f.mu.Unlock()
	if checks != 2 {
		t.Fatalf("expected identity checks before and after all source checks, got %d", checks)
	}
}

func TestNotesLiveCancellationStopsAndJoinsWorkers(t *testing.T) {
	for _, point := range []string{"source-error", "creation-changed", "request-canceled"} {
		t.Run(point, func(t *testing.T) {
			f := newNotesLiveHTTPFixture(t)
			f.rows = nil
			for _, name := range []string{"g", "f", "e", "d", "c", "b", "a"} {
				f.rows = append(f.rows, liveRawMemo(name, "candidate"))
			}
			ctx, cancel := context.WithCancel(context.Background())
			started, canceled := make(chan struct{}, 6), make(chan struct{}, 6)
			completeOne, triggerFailure, allowExit := make(chan struct{}), make(chan struct{}), make(chan struct{})
			releaseExit := sync.OnceFunc(func() { close(allowExit) })
			source := f.service.Source
			sourceErr := upstreamError("UPSTREAM_AUTH", "Memos rejected the configured credential")
			f.service.Source = &notesLiveGetSource{Source: source, get: func(ctx context.Context, name string) (*Memo, error) {
				started <- struct{}{}
				if name == "memos/g" {
					select {
					case <-completeOne:
						return source.Get(ctx, name)
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				if name == "memos/f" && point != "request-canceled" {
					select {
					case <-triggerFailure:
						if point == "source-error" {
							return nil, sourceErr
						}
						memo, err := source.Get(ctx, name)
						if err == nil && memo != nil {
							memo.CreateTime = "2019-01-01T00:00:00.000000+00:00"
						}
						return memo, err
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				<-ctx.Done()
				canceled <- struct{}{}
				<-allowExit
				return nil, ctx.Err()
			}}
			var page NotePage
			var err error
			done := make(chan struct{})
			go func() {
				page, err = f.service.ListLive(ctx, NoteList{PageSize: 6})
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				releaseExit()
				waitNotesLiveSignal(t, done)
			})
			for range 4 {
				waitNotesLiveSignal(t, started)
			}
			close(completeOne)
			// 第五个任务开始说明已有一条复查成功，失败仍须丢弃整页。
			waitNotesLiveSignal(t, started)
			waiting := 3
			if point == "request-canceled" {
				waiting = 4
				cancel()
			} else {
				close(triggerFailure)
			}
			for range waiting {
				waitNotesLiveSignal(t, canceled)
			}
			select {
			case <-done:
				t.Fatal("live list returned before canceled workers exited")
			default:
			}
			releaseExit()
			waitNotesLiveSignal(t, done)
			switch point {
			case "source-error":
				if !errors.Is(err, sourceErr) {
					t.Fatalf("source failure was masked by sibling cancellation: %v", err)
				}
			case "creation-changed":
				if errorCode(err) != "SOURCE_CHANGED" {
					t.Fatalf("creation time change was not rejected: %v", err)
				}
			case "request-canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("request cancellation was not propagated: %v", err)
				}
			}
			if len(page.Notes) != 0 || page.NextPageToken != "" {
				t.Fatalf("failed live list leaked partial data: %+v", page)
			}
			if len(started) != 0 {
				t.Fatal("live list dispatched more source requests after cancellation")
			}
		})
	}
}

func TestNotesLiveCanceledHTTPPreservesSourceHealth(t *testing.T) {
	for _, point := range []string{"creation-changed", "source-error", "request-canceled"} {
		t.Run(point, func(t *testing.T) {
			settings := testSettings(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			siblingStarted := make(chan struct{})
			client := liveProbeClient(t, settings, func(r *http.Request) (int, any, error) {
				switch r.URL.Path {
				case "/api/v1/instance/profile":
					return 200, map[string]string{"version": "0.31.0"}, nil
				case "/api/v1/auth/me":
					return 200, map[string]any{"user": map[string]string{"name": settings.Memos.User}}, nil
				case "/api/v1/memos":
					return 200, map[string]any{"memos": []any{
						liveRawMemo("z", "candidate"), liveRawMemo("a", "candidate"),
					}}, nil
				case "/api/v1/memos/z":
					select {
					case <-siblingStarted:
					case <-r.Context().Done():
						return 200, liveRawMemo("z", "candidate"), nil
					}
					if point == "request-canceled" {
						<-r.Context().Done()
						return 200, liveRawMemo("z", "candidate"), nil
					}
					if point == "source-error" {
						return 401, map[string]any{}, nil
					}
					changed := liveRawMemo("z", "changed")
					changed["createTime"], changed["updateTime"] = "2020-01-02T00:00:00Z", "2020-01-02T00:00:00Z"
					return 200, changed, nil
				case "/api/v1/memos/a":
					close(siblingStarted)
					<-r.Context().Done()
					return 200, liveRawMemo("a", "candidate"), nil
				}
				return 404, map[string]any{}, nil
			})
			settings.Memos.BaseURL = client.config.BaseURL
			store := testStore(t, settings)
			service := NewNoteService(settings, store, client)
			var page NotePage
			var err error
			done := make(chan struct{})
			go func() {
				page, err = service.ListLive(ctx, NoteList{})
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				waitNotesLiveSignal(t, done)
			})
			if point == "request-canceled" {
				waitNotesLiveSignal(t, siblingStarted)
				cancel()
			}
			waitNotesLiveSignal(t, done)
			switch point {
			case "creation-changed":
				if errorCode(err) != "SOURCE_CHANGED" {
					t.Fatalf("creation time change was not preserved: %v", err)
				}
			case "source-error":
				if errorCode(err) != "UPSTREAM_AUTH" {
					t.Fatalf("source error was masked by cancellation: %v", err)
				}
			case "request-canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("request cancellation was not preserved: %v", err)
				}
			}
			if len(page.Notes) != 0 || page.NextPageToken != "" {
				t.Fatalf("failed list returned partial data: %+v", page)
			}
			if _, err := client.Identity(t.Context()); err != nil {
				t.Fatalf("source identity is unavailable: %v", err)
			}
			status, err := store.Status()
			if err != nil {
				t.Fatal(err)
			}
			if point == "source-error" {
				if status.SourceOK || status.LastError == nil || *status.LastError != "UPSTREAM_AUTH" {
					t.Fatalf("source authentication failure was overwritten: %+v", status)
				}
			} else if !status.SourceOK || status.LastError != nil {
				t.Fatalf("cancellation changed healthy source status: %+v", status)
			}
		})
	}
}

func TestNotesLiveReadsUndiscoveredSource(t *testing.T) {
	f := newNotesLiveHTTPFixture(t)
	ctx := context.Background()
	before, err := f.service.Store.Status()
	if err != nil {
		t.Fatal(err)
	}
	if names, err := f.service.Store.Names(); err != nil || len(names) != 0 {
		t.Fatalf("index not empty: %v %v", names, err)
	}
	page, err := f.service.ListLive(ctx, NoteList{PageSize: 1})
	if err != nil || !reflect.DeepEqual(notesLivePageNames(page), []string{"memos/z"}) {
		t.Fatalf("undiscovered source missing: %+v %v", page, err)
	}
	if page.Notes[0].Content != "  fresh from Get\n#tag  " {
		t.Fatal("live list returned candidate text instead of rechecking Get")
	}
	f.mu.Lock()
	f.rows = append(f.rows, liveRawMemo("zz-new", "new at source"))
	f.mu.Unlock()
	if names, err := f.service.Store.Names(); err != nil || names["memos/zz-new"] {
		t.Fatalf("new memo was already indexed: %v %v", names, err)
	}
	refreshed, err := f.service.ListLive(ctx, NoteList{PageSize: 1})
	if err != nil || !reflect.DeepEqual(notesLivePageNames(refreshed), []string{"memos/zz-new"}) {
		t.Fatalf("refresh depended on index discovery: %+v %v", refreshed, err)
	}
	after, err := f.service.Store.Status()
	if err != nil || !reflect.DeepEqual(before.Watermark, after.Watermark) ||
		!reflect.DeepEqual(before.LastFull, after.LastFull) {
		t.Fatalf("live reads advanced synchronization: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestNotesLiveRejectsForeignCursors(t *testing.T) {
	f := newNotesLiveHTTPFixture(t)
	ctx := context.Background()
	first, err := f.service.ListLive(ctx, NoteList{PageSize: 1})
	if err != nil || first.NextPageToken == "" {
		t.Fatalf("live cursor setup: %+v %v", first, err)
	}
	memo, err := normalizeMemo(liveRawMemo("a", "indexed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.Refresh(memo.Name, &memo); err != nil {
		t.Fatal(err)
	}
	indexed, err := f.service.ListIndexed(ctx, NoteList{PageSize: 1})
	if err != nil || indexed.NextPageToken == "" {
		t.Fatalf("indexed cursor setup: %+v %v", indexed, err)
	}
	var raw liveListCursor
	if err := f.service.readCursor(first.NextPageToken, &raw); err != nil {
		t.Fatal(err)
	}
	wrongOrder := raw
	wrongOrder.Order = "updated_desc"
	for _, tc := range []struct {
		name   string
		token  string
		state  string
		mutate func(*NoteService)
	}{
		{"indexed-to-live", indexed.NextPageToken, "", nil},
		{"adapter-to-live", raw.Token, "", nil},
		{"state", first.NextPageToken, "ARCHIVED", nil},
		{"origin", first.NextPageToken, "", func(s *NoteService) { s.Settings.Memos.BaseURL = "http://other:5230" }},
		{"instance", first.NextPageToken, "", func(s *NoteService) { s.Settings.Memos.InstanceID = "other-instance" }},
		{"author", first.NextPageToken, "", func(s *NoteService) { s.Settings.Memos.User = "users/other" }},
		{"order", f.service.signCursor(wrongOrder), "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := *f.service
			if tc.mutate != nil {
				tc.mutate(&service)
			}
			before := f.requestCount()
			page, err := service.ListLive(ctx, NoteList{PageSize: 1, State: tc.state, PageToken: tc.token})
			if errorCode(err) != "INVALID_PAGE_TOKEN" || len(page.Notes) != 0 || page.NextPageToken != "" {
				t.Fatalf("foreign live cursor accepted: %+v %v", page, err)
			}
			if f.requestCount() != before {
				t.Fatal("invalid cursor reached the source")
			}
		})
	}
	before := f.requestCount()
	page, err := f.service.ListIndexed(ctx, NoteList{PageSize: 1, PageToken: first.NextPageToken})
	if errorCode(err) != "INVALID_PAGE_TOKEN" || len(page.Notes) != 0 || page.NextPageToken != "" || f.requestCount() != before {
		t.Fatalf("live cursor accepted by indexed list: %+v %v", page, err)
	}
}

func TestNotesLiveCursorSurvivesIndexRebuild(t *testing.T) {
	f := newNotesLiveHTTPFixture(t)
	ctx := context.Background()
	first, err := f.service.ListLive(ctx, NoteList{PageSize: 1})
	if err != nil || first.NextPageToken == "" {
		t.Fatalf("cursor setup: %+v %v", first, err)
	}
	before, err := f.service.Store.Generation()
	if err != nil {
		t.Fatal(err)
	}
	settings := f.service.Settings
	if err := f.service.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildIndex(settings); err != nil {
		t.Fatal(err)
	}
	rebuilt := testStore(t, settings)
	after, err := rebuilt.Generation()
	if err != nil || before == after {
		t.Fatalf("index generation unchanged: %q %q %v", before, after, err)
	}
	if names, err := rebuilt.Names(); err != nil || len(names) != 0 {
		t.Fatalf("rebuilt index not empty: %v %v", names, err)
	}
	service := NewNoteService(settings, rebuilt, f.service.Source)
	next, err := service.ListLive(ctx, NoteList{PageSize: 1, PageToken: first.NextPageToken})
	if err != nil || !reflect.DeepEqual(notesLivePageNames(next), []string{"memos/a"}) || next.NextPageToken != "" {
		t.Fatalf("live cursor invalidated by rebuild: %+v %v", next, err)
	}
}

func TestNotesLiveSourceFailuresAreErrors(t *testing.T) {
	for _, point := range []string{"identity", "list", "get", "get-after-one", "final-identity"} {
		t.Run(point, func(t *testing.T) {
			f := newNotesLiveHTTPFixture(t)
			for _, name := range []string{"z", "a"} {
				memo, err := normalizeMemo(liveRawMemo(name, "cached private text"))
				if err != nil {
					t.Fatal(err)
				}
				if err := f.service.Store.Refresh(memo.Name, &memo); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Lock()
			f.failAt = point
			f.mu.Unlock()
			page, err := f.service.ListLive(context.Background(), NoteList{PageSize: 2})
			if errorCode(err) != "UPSTREAM_UNAVAILABLE" || len(page.Notes) != 0 || page.NextPageToken != "" {
				t.Fatalf("source failure became empty success or leaked partial data: %+v %v", page, err)
			}
			f.mu.Lock()
			hit := f.failureHit
			f.mu.Unlock()
			if !hit {
				t.Fatalf("test did not reach the %s failure", point)
			}
		})
	}
}

package moerise

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
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

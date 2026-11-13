package moerise

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseLiveNoteFilters(t *testing.T) {
	args, err := parseNoteList(url.Values{
		"query": {"  关键词  "}, "tag": {"b", "a/子", "b"}, "date": {"2024-02-29"},
	}, true)
	if err != nil || args.Query != "关键词" || !reflect.DeepEqual(args.Tags, []string{"a/子", "b"}) ||
		args.State != "NORMAL" || args.Order != "created_desc" || args.PageSize != 20 || args.Date != "2024-02-29" {
		t.Fatalf("filters: %+v %v", args, err)
	}
	for _, query := range []url.Values{
		{"query": {"one", "two"}}, {"date": {"2024-02-29", "2024-02-29"}},
		{"state": {"NORMAL", "NORMAL"}}, {"page_size": {"1", "2"}}, {"unknown": {"x"}},
		{"tag": {}}, {"tag": {""}},
		{"tag": {strings.Repeat("a", 101)}}, {"tag": slices.Repeat([]string{"a"}, 21)},
		{"query": {string([]byte{0xff})}}, {"tag": {string([]byte{0xff})}},
		{"query": {strings.Repeat("文", 257)}}, {"query": {strings.Repeat(" ", 1025)}},
		{"date": {"2023-02-29"}}, {"date": {"2024-2-29"}}, {"date": {"2024-02-30"}},
		{"date": {"2024-02-29T00:00:00Z"}}, {"date": {"0000-01-01"}},
	} {
		if _, err := parseNoteList(query, true); errorCode(err) != "INVALID_ARGUMENT" {
			t.Errorf("accepted invalid filters %q: %v", query, err)
		}
	}
	for _, query := range []url.Values{
		{"query": {""}}, {"query": {strings.Repeat("文", 256)}},
		{"tag": {"标签/子", "Other"}}, {"date": {""}},
		{"tag": {"C++", "R&D", "e\u0301", "👩‍💻", "#tag", "tag/", "a b"}},
		{"tag": {strings.Repeat("😀", 100)}},
	} {
		if _, err := parseNoteList(query, true); err != nil {
			t.Errorf("rejected valid filters %q: %v", query, err)
		}
		if _, err := parseNoteList(query, false); errorCode(err) != "INVALID_ARGUMENT" {
			t.Errorf("indexed endpoint accepted new filters %q: %v", query, err)
		}
	}
}

type notesFilterSource struct {
	Source
	mu          sync.Mutex
	rows        []Memo
	fresh       map[string]*Memo
	checks      int
	failCheck   int
	pages       int
	failPage    int
	getError    string
	perPage     int
	repeatToken bool
	gets        int
	onPage      func(int)
}

func (s *notesFilterSource) Identity(context.Context) (string, error) {
	s.checks++
	if s.checks == s.failCheck {
		return "", upstreamError("UPSTREAM_AUTH", "identity failed")
	}
	return "0.31.0", nil
}

func (s *notesFilterSource) Get(_ context.Context, name string) (*Memo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if name == s.getError {
		return nil, upstreamError("UPSTREAM_UNAVAILABLE", "get failed")
	}
	if memo, ok := s.fresh[name]; ok {
		return memo, nil
	}
	for _, memo := range s.rows {
		if memo.Name == name {
			return &memo, nil
		}
	}
	return nil, nil
}

func (s *notesFilterSource) ListLivePage(_ context.Context, state string, size int, token string) ([]Memo, string, error) {
	s.pages++
	if s.onPage != nil {
		s.onPage(s.pages)
	}
	if s.pages == s.failPage {
		return nil, "", upstreamError("UPSTREAM_UNAVAILABLE", "list failed")
	}
	rows := []Memo{}
	for _, memo := range s.rows {
		if memo.State == state {
			rows = append(rows, memo)
		}
	}
	start, _ := strconv.Atoi(token)
	end := min(start+min(size, s.perPage), len(rows))
	next := ""
	if end < len(rows) {
		next = strconv.Itoa(end)
	}
	if s.repeatToken {
		next = "repeat"
	}
	return rows[start:end], next, nil
}

func newNotesFilterFixture(t *testing.T) (*NoteService, *notesFilterSource) {
	t.Helper()
	settings := testSettings(t)
	settings.Memos.User = "users/owner"
	source := &notesFilterSource{perPage: 2, fresh: map[string]*Memo{}}
	for _, row := range []struct {
		name, content, created, state string
		tags                          []string
	}{
		{"z", "unrelated", "2026-03-02T12:00:00Z", "NORMAL", []string{"outside"}},
		{"y", "Needle", "2026-03-01T16:00:00Z", "NORMAL", []string{"Work", "Work/Child"}},
		{"x", "needle", "2026-03-01T15:59:59Z", "NORMAL", []string{"Work", "Work/Child"}},
		{"w", "NEEDLE", "2026-03-01T12:00:00Z", "NORMAL", []string{"Work"}},
		{"v", "needle", "2026-03-01T11:00:00Z", "NORMAL", []string{"Work", "Work/Child"}},
		{"u", "needle", "2026-02-28T16:00:00Z", "NORMAL", []string{"Work", "Work/Child"}},
		{"t", "needle", "2026-02-28T15:59:59Z", "NORMAL", []string{"outside", "outside"}},
		{"s", "needle", "2026-02-28T14:00:00Z", "ARCHIVED", []string{"archive"}},
	} {
		raw := liveRawMemo(row.name, row.content)
		raw["createTime"], raw["state"], raw["tags"] = row.created, row.state, row.tags
		memo, err := normalizeMemo(raw)
		if err != nil {
			t.Fatal(err)
		}
		source.rows = append(source.rows, memo)
	}
	return NewNoteService(settings, testStore(t, settings), source), source
}

func TestNotesFilteredFallbackAcrossPagesAndCursorBinding(t *testing.T) {
	service, source := newNotesFilterFixture(t)
	args := NoteList{Query: "needle", Tags: []string{"Work/Child", "Work"}, Date: "2026-03-01", PageSize: 1}
	first, err := service.ListLive(t.Context(), args)
	if err != nil || !reflect.DeepEqual(notesLivePageNames(first), []string{"memos/x"}) || first.NextPageToken == "" || source.pages < 4 {
		t.Fatalf("cross-page filter: %+v %v (%d source pages)", first, err, source.pages)
	}
	args.PageToken = first.NextPageToken
	for _, change := range []func(*NoteList){
		func(a *NoteList) { a.Query = "other" },
		func(a *NoteList) { a.Tags = []string{"Work"} },
		func(a *NoteList) { a.Date = "2026-03-02" },
	} {
		other := args
		change(&other)
		before := source.checks
		if _, err := service.ListLive(t.Context(), other); errorCode(err) != "INVALID_PAGE_TOKEN" || source.checks != before {
			t.Fatalf("filter change accepted: %+v %v", other, err)
		}
	}
	args.Tags = []string{"Work", "Work/Child", "Work"}
	next, err := service.ListLive(t.Context(), args)
	if err != nil || !reflect.DeepEqual(notesLivePageNames(next), []string{"memos/v"}) || next.NextPageToken == "" {
		t.Fatalf("continuation: %+v %v", next, err)
	}
	args.PageToken = next.NextPageToken
	last, err := service.ListLive(t.Context(), args)
	if err != nil || !reflect.DeepEqual(notesLivePageNames(last), []string{"memos/u"}) || last.NextPageToken != "" {
		t.Fatalf("last page: %+v %v", last, err)
	}
}

func TestNotesFilteredFreshConditions(t *testing.T) {
	for _, change := range []func(*Memo){
		func(m *Memo) { m.Content = "changed" },
		func(m *Memo) { m.Tags = []string{"Work"} },
		func(m *Memo) { m.Creator = "users/other" },
		func(m *Memo) { m.State = "ARCHIVED" },
	} {
		service, source := newNotesFilterFixture(t)
		fresh := source.rows[2]
		change(&fresh)
		source.fresh[fresh.Name] = &fresh
		result, err := service.ListLive(t.Context(), NoteList{
			Query: "needle", Tags: []string{"Work/Child"}, Date: "2026-03-01", PageSize: 1,
		})
		if err != nil || len(result.Notes) != 0 || result.NextPageToken == "" {
			t.Fatalf("changed source escaped filter or lost continuation: %+v %v", result, err)
		}
	}
}

func TestNotesFullSourceAggregates(t *testing.T) {
	service, source := newNotesFilterFixture(t)
	tags, err := service.Tags(t.Context(), "")
	if err != nil || !reflect.DeepEqual(tags, []TagCount{{"Work", 5}, {"Work/Child", 4}, {"outside", 2}}) {
		t.Fatalf("all tags: %+v %v", tags, err)
	}
	if source.pages != 8 || source.gets != 0 || source.checks != 2 {
		t.Fatalf("aggregation missed source pages or identity: %+v", source)
	}
	if names, err := service.Store.Names(); err != nil || len(names) != 0 {
		t.Fatalf("aggregation persisted source content: %v %v", names, err)
	}
	days, err := service.Calendar(t.Context(), "NORMAL", "2026-03")
	if err != nil || !reflect.DeepEqual(days, []DayCount{{"2026-03-01", 4}, {"2026-03-02", 2}}) {
		t.Fatalf("timezone calendar: %+v %v", days, err)
	}
	archived, err := service.Tags(t.Context(), "ARCHIVED")
	if err != nil || !reflect.DeepEqual(archived, []TagCount{{"archive", 1}}) {
		t.Fatalf("archive scope: %+v %v", archived, err)
	}
	empty, err := service.Calendar(t.Context(), "", "2026-04")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty month: %+v %v", empty, err)
	}
}

func TestNotesAggregatesFreshSourceAndFailures(t *testing.T) {
	for _, point := range []string{"identity", "final-identity", "list", "verification-page", "page-budget", "loop", "changed", "deleted", "foreign"} {
		t.Run(point, func(t *testing.T) {
			service, source := newNotesFilterFixture(t)
			want := "UPSTREAM_UNAVAILABLE"
			switch point {
			case "identity":
				source.failCheck, want = 1, "UPSTREAM_AUTH"
			case "final-identity":
				source.failCheck, want = 2, "UPSTREAM_AUTH"
			case "list":
				source.failPage = 2
			case "verification-page":
				source.failPage = 5
			case "page-budget":
				service.Settings.Memos.MaxPages, want = 1, "UPSTREAM_PAGINATION_LIMIT"
			case "loop":
				source.rows, source.repeatToken, want = nil, true, "UPSTREAM_PROTOCOL"
			case "changed", "deleted", "foreign":
				want = "SOURCE_CHANGED"
				source.onPage = func(page int) {
					if page == 5 {
						switch point {
						case "changed":
							source.rows[0].Tags = []string{"fresh"}
						case "deleted":
							source.rows = source.rows[1:]
						case "foreign":
							source.rows[0].Creator = "users/other"
							want = "UPSTREAM_PROTOCOL"
						}
					}
				}
			}
			tags, err := service.Tags(t.Context(), "")
			if tags != nil || errorCode(err) != want {
				t.Fatalf("partial aggregate escaped failure: %+v %v (want %s)", tags, err, want)
			}
		})
	}
}

func TestNotesScanBudgetsAndValidation(t *testing.T) {
	for _, limits := range []memosLiveLimits{{2, 1 << 20}, {100, 16}} {
		service, _ := newNotesFilterFixture(t)
		err := service.walkLive(t.Context(), "NORMAL", LiveFilter{}, limits, func([]Memo, *int) error { return nil })
		if errorCode(err) != "LIVE_LIST_LIMIT" {
			t.Fatalf("scan escaped budget %+v: %v", limits, err)
		}
	}
	service, source := newNotesFilterFixture(t)
	for _, month := range []string{"", "2026-13", "2026-2", "0000-01", "2026-03-01"} {
		if _, err := service.Calendar(t.Context(), "", month); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("invalid month accepted: %s %v", month, err)
		}
	}
	if _, err := service.Tags(t.Context(), "ALL"); errorCode(err) != "INVALID_ARGUMENT" || source.checks != 0 {
		t.Fatalf("invalid state reached source: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := service.walkLive(ctx, "NORMAL", LiveFilter{}, memosLiveLimits{100, 1 << 20}, func([]Memo, *int) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scan ignored cancellation: %v", err)
	}
	for _, refresh := range []bool{false, true} {
		checked, used, err := service.checkLiveMemos(t.Context(), source.rows[:2], "NORMAL", LiveFilter{}, refresh, 64)
		if errorCode(err) != "LIVE_LIST_LIMIT" || checked != nil || used != 0 {
			t.Fatalf("fresh source bypassed byte budget: %+v %d %v", checked, used, err)
		}
	}
}

func TestNotesAggregatesLargeCollectionUsesPagesAndNoPerNoteReads(t *testing.T) {
	service, source := newNotesFilterFixture(t)
	service.Settings.Memos.PageSize = 100
	template := source.rows[0]
	source.rows, source.perPage = nil, 100
	for i := range 10000 {
		memo := template
		memo.Name = "memos/scale_" + strconv.Itoa(i)
		memo.Tags = []string{"all", "all"}
		source.rows = append(source.rows, memo)
	}
	tags, err := service.Tags(t.Context(), "NORMAL")
	if err != nil || !reflect.DeepEqual(tags, []TagCount{{"all", 10000}}) || source.gets != 0 ||
		source.pages != 200 || source.checks != 2 {
		t.Fatalf("large aggregate exceeded source request budget: tags=%v pages=%d gets=%d identities=%d err=%v",
			tags, source.pages, source.gets, source.checks, err)
	}
	source.pages, source.checks = 0, 0
	days, err := service.Calendar(t.Context(), "NORMAL", "2026-03")
	if err != nil || !reflect.DeepEqual(days, []DayCount{{"2026-03-02", 10000}}) || source.gets != 0 ||
		source.pages != 200 || source.checks != 2 {
		t.Fatalf("large calendar escaped source budget: days=%v pages=%d gets=%d err=%v", days, source.pages, source.gets, err)
	}
}

func TestNotesFilterDSTAndExactSemantics(t *testing.T) {
	service, _ := newNotesFilterFixture(t)
	service.Settings.Timezone = "America/New_York"
	for date, hours := range map[string]int{"2026-03-08": 23, "2026-11-01": 25} {
		filter, err := service.liveFilter(NoteList{Date: date})
		if err != nil || filter.CreatedUntil.Sub(filter.CreatedFrom) != time.Duration(hours)*time.Hour {
			t.Fatalf("DST bounds for %s: %+v %v", date, filter, err)
		}
	}
	for _, tc := range []struct {
		zone, date, from, until string
	}{
		{"America/Sao_Paulo", "2018-11-04", "2018-11-04T03:00:00Z", "2018-11-05T02:00:00Z"},
		{"Pacific/Apia", "2011-12-30", "2011-12-30T10:00:00Z", "2011-12-30T10:00:00Z"},
	} {
		service.Settings.Timezone = tc.zone
		filter, err := service.liveFilter(NoteList{Date: tc.date})
		if err != nil || filter.CreatedFrom.Format(time.RFC3339) != tc.from || filter.CreatedUntil.Format(time.RFC3339) != tc.until {
			t.Fatalf("local date bounds for %+v: %+v %v", tc, filter, err)
		}
	}
	filter := LiveFilter{Query: "Ä_%\\'", Tags: []string{"Work"}}
	memo := Memo{Content: "ä_%\\'", Tags: []string{"Work/Child"}}
	if filter.matches(&memo) {
		t.Fatal("tag prefix matched an exact tag")
	}
	memo.Tags = []string{"Work"}
	if !filter.matches(&memo) {
		t.Fatal("literal Unicode keyword failed")
	}
	memo.Content = "äxx\\'"
	if filter.matches(&memo) {
		t.Fatal("keyword LIKE metacharacters became wildcards")
	}
}

type pagedAggregateSource struct {
	Source
	LiveSource
	AggregateSource
}

func TestNotesAggregatesMemosAdapterUsesLinearOfficialPagination(t *testing.T) {
	for _, pageSize := range []int{100, 500} {
		for _, sameTime := range []bool{false, true} {
			t.Run(strconv.Itoa(pageSize)+"/"+strconv.FormatBool(sameTime), func(t *testing.T) {
				settings := testSettings(t)
				settings.Memos.User, settings.Memos.PageSize = "users/owner", pageSize
				settings.Timezone = "UTC"
				total := 10000
				if sameTime {
					total = 1000
				}
				lists, gets := 0, 0
				source := fakeClient(settings, func(r *http.Request) (int, any, error) {
					if status, body, ok := identityResponse(r); ok {
						return status, body, nil
					}
					if r.URL.Path != "/api/v1/memos" {
						gets++
						return 500, nil, nil
					}
					lists++
					query := r.URL.Query()
					if query.Get("orderBy") != "create_time desc" || query.Get("pageSize") != strconv.Itoa(pageSize) ||
						!strings.Contains(query.Get("filter"), "creator == 'users/owner'") {
						t.Errorf("unexpected aggregate page: %s", r.URL)
					}
					offset, _ := strconv.Atoi(query.Get("pageToken"))
					rows := []any{}
					for i := offset; i < min(offset+pageSize, total); i++ {
						raw := liveRawMemo("same_"+strconv.Itoa(i), "#all")
						raw["tags"] = []string{"all", "all"}
						created := time.Date(2020, 1, 2, 12, 0, 0, 0, time.UTC)
						if !sameTime {
							created = created.Add(-time.Duration(i) * time.Second)
						}
						raw["createTime"], raw["updateTime"] = created.Format(time.RFC3339Nano), created.Format(time.RFC3339Nano)
						rows = append(rows, raw)
					}
					next := ""
					if offset+pageSize < total {
						next = strconv.Itoa(offset + pageSize)
					}
					return 200, map[string]any{"memos": rows, "nextPageToken": next}, nil
				})
				service := NewNoteService(settings, testStore(t, settings), pagedAggregateSource{source, source, source})
				tags, err := service.Tags(t.Context(), "NORMAL")
				if err != nil || !reflect.DeepEqual(tags, []TagCount{{"all", total}}) || lists != 2*((total+pageSize-1)/pageSize) || gets != 0 {
					t.Fatalf("same-time collection rescanned: tags=%v lists=%d gets=%d err=%v", tags, lists, gets, err)
				}
				lists = 0
				days, err := service.Calendar(t.Context(), "NORMAL", "2020-01")
				if err != nil || !reflect.DeepEqual(days, []DayCount{{"2020-01-02", total}}) || lists != 2*((total+pageSize-1)/pageSize) || gets != 0 {
					t.Fatalf("same-time calendar rescanned: days=%v lists=%d gets=%d err=%v", days, lists, gets, err)
				}
			})
		}
	}
}

func TestNotesAggregatesMemosAdapterRejectsPartialOrChangingPages(t *testing.T) {
	for _, operation := range []string{"tags", "calendar"} {
		for _, issue := range []string{"changed", "missing", "scope", "state", "duplicate", "token", "rate-limit"} {
			t.Run(operation+"/"+issue, func(t *testing.T) {
				settings := testSettings(t)
				settings.Memos.User, settings.Memos.PageSize = "users/owner", 1
				settings.Timezone = "UTC"
				pages := 0
				source := fakeClient(settings, func(r *http.Request) (int, any, error) {
					if status, body, ok := identityResponse(r); ok {
						return status, body, nil
					}
					pages++
					raw := liveRawMemo("one", "#all")
					raw["tags"] = []string{"all"}
					next := "next"
					if pages == 2 {
						raw["name"], next = "memos/two", ""
						switch issue {
						case "scope":
							raw["creator"] = "users/other"
						case "state":
							raw["state"] = "ARCHIVED"
						case "duplicate":
							raw["name"] = "memos/one"
						case "token":
							next = "next"
						case "rate-limit":
							return 429, map[string]string{"message": "private"}, nil
						}
					}
					if pages == 3 {
						if issue == "missing" {
							return 200, map[string]any{"memos": []any{}}, nil
						}
						raw["content"] = "changed"
					}
					return 200, map[string]any{"memos": []any{raw}, "nextPageToken": next}, nil
				})
				service := NewNoteService(settings, testStore(t, settings), pagedAggregateSource{source, source, source})
				var err error
				if operation == "tags" {
					var tags []TagCount
					tags, err = service.Tags(t.Context(), "NORMAL")
					if tags != nil {
						t.Fatalf("partial tags returned: %v", tags)
					}
				} else {
					var days []DayCount
					days, err = service.Calendar(t.Context(), "NORMAL", "2020-01")
					if days != nil {
						t.Fatalf("partial calendar returned: %v", days)
					}
				}
				want := "SOURCE_CHANGED"
				if issue == "scope" || issue == "state" || issue == "token" {
					want = "UPSTREAM_PROTOCOL"
				} else if issue == "rate-limit" {
					want = "UPSTREAM_RATE_LIMITED"
				}
				if errorCode(err) != want {
					t.Fatalf("aggregate accepted %s: %v", issue, err)
				}
			})
		}
	}
}

func TestNotesSourceFailureCancelsHealthConnectionWait(t *testing.T) {
	service, _ := newNotesFilterFixture(t)
	service.Source = fakeClient(service.Settings, func(*http.Request) (int, any, error) {
		return 503, map[string]string{"message": "private"}, nil
	})
	connection, err := service.Store.state.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := service.fresh(ctx, "memos/one"); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("health wait returned the wrong error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		connection.Close()
		<-done
		t.Fatal("source failure kept waiting to write health after the deadline")
	}
	connection.Close()
	var code string
	if err := service.Store.state.QueryRow("SELECT COALESCE(last_error, '') FROM sync_state").Scan(&code); err != nil || code != "" {
		t.Fatalf("canceled source failure changed health: %q %v", code, err)
	}
}

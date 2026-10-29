package moerise

import (
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMemosFilteredCELAndCursor(t *testing.T) {
	settings := liveProbeSettings()
	filter := LiveFilter{
		Query: `Ä_%\"' || true`, Tags: []string{"Work", "Work/Child"},
		CreatedFrom:  time.Date(2019, 12, 31, 16, 0, 0, 0, time.UTC),
		CreatedUntil: time.Date(2020, 1, 1, 16, 0, 0, 0, time.UTC),
	}
	requests := 0
	client := liveProbeClient(t, settings, func(r *http.Request) (int, any, error) {
		requests++
		got := r.URL.Query().Get("filter")
		for _, part := range []string{
			`content.contains("Ä_%\\\"' || true")`, `"Work" in tags`, `"Work/Child" in tags`,
			`created_ts >= timestamp("2019-12-31T16:00:00Z")`,
			`created_ts < timestamp("2020-01-01T16:00:00Z")`,
		} {
			if !strings.Contains(got, part) {
				t.Errorf("CEL missing %q in %q", part, got)
			}
		}
		rows := []map[string]any{liveRawMemo("z", filter.Query), liveRawMemo("a", filter.Query)}
		for _, row := range rows {
			row["tags"] = filter.Tags
		}
		return 200, map[string]any{"memos": rows}, nil
	})
	items, token, err := client.ListFilteredLivePage(t.Context(), "NORMAL", 1, "", filter)
	if err != nil || token == "" || len(items) != 1 || items[0].Name != "memos/z" {
		t.Fatalf("filtered page: %+v %q %v", items, token, err)
	}
	changed := filter
	changed.Query += "x"
	before := requests
	if _, _, err := client.ListFilteredLivePage(t.Context(), "NORMAL", 1, token, changed); errorCode(err) != "INVALID_PAGE_TOKEN" || requests != before {
		t.Fatalf("adapter cursor ignored filter binding: %v", err)
	}
}

func TestMemosFilteredRejectsOutOfFilter(t *testing.T) {
	client := liveProbeClient(t, liveProbeSettings(), func(*http.Request) (int, any, error) {
		return 200, map[string]any{"memos": []any{liveRawMemo("z", "unrelated")}}, nil
	})
	items, next, err := client.ListFilteredLivePage(t.Context(), "NORMAL", 1, "", LiveFilter{Query: "needle"})
	if errorCode(err) != "UPSTREAM_PROTOCOL" || len(items) != 0 || next != "" {
		t.Fatalf("source filter violation escaped: %+v %q %v", items, next, err)
	}
}

func TestMemosFilteredRealProtocol(t *testing.T) {
	address := os.Getenv("MOERISE_FILTER_PROBE_URL")
	if address == "" {
		t.Skip("requires the isolated filter protocol probe")
	}
	settings := testSettings(t)
	settings.Memos.BaseURL, settings.Memos.User, settings.Memos.Token = address,
		os.Getenv("MOERISE_FILTER_PROBE_USER"), os.Getenv("MOERISE_FILTER_PROBE_TOKEN")
	settings.Memos.InstanceID, settings.Memos.PageSize = "isolated-filter-probe", 2
	client := NewMemosClient(settings.Memos)
	defer client.Close()
	service := NewNoteService(settings, testStore(t, settings), client)
	args := NoteList{Query: "needle", Tags: []string{"Work", "Work/Child"}, Date: "2026-03-01", PageSize: 1}
	names := []string{}
	for range 5 {
		page, err := service.ListLive(t.Context(), args)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, notesLivePageNames(page)...)
		args.PageToken = page.NextPageToken
		if args.PageToken == "" {
			break
		}
	}
	if !reflect.DeepEqual(names, []string{"memos/match-z", "memos/match-a"}) {
		t.Fatalf("real combined CEL pagination: %v", names)
	}
	for _, query := range []string{`Ä_%\"' || true`, "中文"} {
		page, err := service.ListLive(t.Context(), NoteList{Query: query})
		if err != nil || !reflect.DeepEqual(notesLivePageNames(page), []string{"memos/literal"}) {
			t.Fatalf("real Unicode/literal search %q: %+v %v", query, page, err)
		}
	}
	days, err := service.Calendar(t.Context(), "NORMAL", "2026-03")
	if err != nil || !reflect.DeepEqual(days, []DayCount{{"2026-03-01", 4}, {"2026-03-02", 1}}) {
		t.Fatalf("real calendar: %+v %v", days, err)
	}
	tags, err := service.Tags(t.Context(), "NORMAL")
	if err != nil || !reflect.DeepEqual(tags, []TagCount{{"Work", 4}, {"Work/Child", 3}}) {
		t.Fatalf("real tags: %+v %v", tags, err)
	}
}

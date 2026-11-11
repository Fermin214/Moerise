package moerise

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func statsFixture(user string) SourceStats {
	return SourceStats{Name: user + "/stats", Total: 2, Tags: map[string]int{"all": 2, "Work": 1},
		Created: []string{"2026-03-01T16:00:00Z", "2026-03-02T12:00:00Z"},
		Updated: []string{"2026-03-01T16:00:00Z", "2026-03-02T12:00:00Z"}}
}

func TestMemosNativeAggregatesScopeCalendarAndSourceChecks(t *testing.T) {
	for _, state := range []string{"NORMAL", "ARCHIVED"} {
		for _, operation := range []string{"tags", "calendar"} {
			t.Run(state+"/"+operation, func(t *testing.T) {
				settings := testSettings(t)
				lists, identities := 0, 0
				source := fakeClient(settings, func(r *http.Request) (int, any, error) {
					if status, body, ok := identityResponse(r); ok {
						identities++
						return status, body, nil
					}
					if r.URL.Path != "/api/v1/users:stats" || r.URL.Query().Get("state") != state ||
						!strings.Contains(r.URL.Query().Get("filter"), "creator == '"+settings.Memos.User+"'") {
						t.Fatalf("wrong source scope: %s", r.URL)
					}
					if operation == "calendar" && (!strings.Contains(r.URL.Query().Get("filter"), "created_ts >=") ||
						!strings.Contains(r.URL.Query().Get("filter"), "created_ts <")) {
						t.Fatalf("month was not applied at source: %s", r.URL)
					}
					lists++
					stats := statsFixture(settings.Memos.User)
					if lists == 2 {
						stats.Created[0], stats.Created[1] = stats.Created[1], stats.Created[0]
						stats.Updated[0], stats.Updated[1] = stats.Updated[1], stats.Updated[0]
					}
					return 200, map[string]any{"stats": []SourceStats{stats}}, nil
				})
				service := NewNoteService(settings, testStore(t, settings), source)
				if operation == "tags" {
					tags, err := service.Tags(t.Context(), state)
					if err != nil || !reflect.DeepEqual(tags, []TagCount{{"Work", 1}, {"all", 2}}) {
						t.Fatalf("native tags: %+v %v", tags, err)
					}
				} else {
					days, err := service.Calendar(t.Context(), state, "2026-03")
					if err != nil || !reflect.DeepEqual(days, []DayCount{{"2026-03-02", 2}}) {
						t.Fatalf("calendar timezone: %+v %v", days, err)
					}
				}
				if lists != 2 || identities != 4 {
					t.Fatalf("unexpected source budget: stats=%d identity HTTP=%d", lists, identities)
				}
			})
		}
	}
}

// 可控响应覆盖真实 Memos 难以稳定触发的截断、变更、跨作者及故障路径。
func TestMemosNativeAggregatesRejectPartialAndChangingStats(t *testing.T) {
	for _, mode := range []string{"owner", "duplicate", "truncated", "updated-missing", "count", "tag-count", "changed", "time-changed", "rate-limit", "identity", "month"} {
		t.Run(mode, func(t *testing.T) {
			settings := testSettings(t)
			statsCalls, identityCalls := 0, 0
			source := fakeClient(settings, func(r *http.Request) (int, any, error) {
				if status, body, ok := identityResponse(r); ok {
					identityCalls++
					if mode == "identity" && identityCalls == 4 {
						return 401, map[string]any{}, nil
					}
					return status, body, nil
				}
				statsCalls++
				stats := statsFixture(settings.Memos.User)
				switch mode {
				case "owner":
					stats.Name = "users/other/stats"
				case "duplicate":
					return 200, map[string]any{"stats": []SourceStats{stats, stats}}, nil
				case "truncated":
					stats.Created = stats.Created[:1]
				case "updated-missing":
					stats.Updated = nil
				case "count":
					stats.Total = -1
				case "tag-count":
					stats.Tags["all"] = 3
				case "changed":
					if statsCalls == 2 {
						stats.Tags["all"] = 1
					}
				case "time-changed":
					if statsCalls == 2 {
						stats.Updated[0] = "2026-03-03T00:00:00Z"
					}
				case "rate-limit":
					if statsCalls == 2 {
						return 429, map[string]any{}, nil
					}
				case "month":
					stats.Created[0] = "2020-01-01T00:00:00Z"
				}
				return 200, map[string]any{"stats": []SourceStats{stats}}, nil
			})
			service := NewNoteService(settings, testStore(t, settings), source)
			tags, err := service.Tags(t.Context(), "NORMAL")
			if mode == "month" {
				days, calendarErr := service.Calendar(t.Context(), "NORMAL", "2026-03")
				if len(days) != 0 || errorCode(calendarErr) != "UPSTREAM_PROTOCOL" {
					t.Fatalf("month scope escaped: %+v %v", days, calendarErr)
				}
				return
			}
			want := "UPSTREAM_PROTOCOL"
			switch mode {
			case "changed", "time-changed":
				want = "SOURCE_CHANGED"
			case "rate-limit":
				want = "UPSTREAM_RATE_LIMITED"
			case "identity":
				want = "UPSTREAM_AUTH"
			}
			if tags != nil || errorCode(err) != want {
				t.Fatalf("partial statistics escaped %s: %+v %v", mode, tags, err)
			}
		})
	}
}

func TestMemosNativeAggregatesEmpty(t *testing.T) {
	settings := testSettings(t)
	source := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		return 200, map[string]any{}, nil
	})
	service := NewNoteService(settings, testStore(t, settings), source)
	if tags, err := service.Tags(t.Context(), "ARCHIVED"); err != nil || len(tags) != 0 {
		t.Fatalf("empty native tags: %+v %v", tags, err)
	}
	if days, err := service.Calendar(t.Context(), "NORMAL", "2026-03"); err != nil || len(days) != 0 {
		t.Fatalf("empty native calendar: %+v %v", days, err)
	}
}

package moerise

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func liveProbeSettings() Settings {
	s := DefaultSettings()
	s.Memos.InstanceID, s.Memos.BaseURL = "live-test-instance", "http://memos:5230"
	s.Memos.User, s.Memos.Token, s.Memos.PageSize = "users/owner", strings.Repeat("u", 40), 2
	return s
}

func liveRawMemo(name, content string) map[string]any {
	return map[string]any{"name": "memos/" + name, "creator": "users/owner", "content": content,
		"state": "NORMAL", "visibility": "PRIVATE", "createTime": "2020-01-01T00:00:00Z", "updateTime": "2020-01-01T00:00:00Z"}
}

func liveProbeClient(t *testing.T, s Settings, handler func(*http.Request) (int, any, error)) *MemosClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, body, err := handler(r)
		if err != nil {
			t.Errorf("probe handler: %v", err)
			status = http.StatusInternalServerError
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	s.Memos.BaseURL = server.URL
	client := NewMemosClient(s.Memos)
	t.Cleanup(client.Close)
	return client
}

func liveProbeNames(items []Memo) []string {
	names := []string{}
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func TestMemosLiveStablePages(t *testing.T) {
	s := liveProbeSettings()
	rows := []map[string]any{}
	// 官方协议在创建时间相同时按插入 ID 排序。
	for _, name := range []string{"y", "b", "m", "a", "z"} {
		rows = append(rows, liveRawMemo(name, "  original\n#tag  "))
	}
	old := liveRawMemo("old", "edited")
	old["createTime"], old["updateTime"], old["pinned"] = "2019-01-01T00:00:00Z", "2026-10-29T00:00:00Z", true
	rows = append(rows, old)
	requests := 0
	client := liveProbeClient(t, s, func(r *http.Request) (int, any, error) {
		requests++
		q := r.URL.Query()
		if r.URL.Path != "/api/v1/memos" || q.Get("state") != "NORMAL" || q.Get("orderBy") != "create_time desc" ||
			q.Get("pageSize") != "2" || r.Header.Get("Authorization") != "Bearer "+s.Memos.Token {
			t.Errorf("unexpected live request: %s", r.URL)
		}
		filter := q.Get("filter")
		if !strings.HasPrefix(filter, "creator == 'users/owner'") {
			t.Errorf("author not fixed: %q", filter)
		}
		candidates := rows
		if bound, ok := strings.CutPrefix(filter, `creator == 'users/owner' && created_ts <= timestamp("`); ok {
			bound = strings.TrimSuffix(bound, `")`)
			candidates = nil
			for _, row := range rows {
				created, _ := time.Parse(time.RFC3339Nano, row["createTime"].(string))
				upper, err := time.Parse(time.RFC3339Nano, bound)
				if err != nil {
					t.Errorf("invalid timestamp filter: %s", filter)
				}
				if !created.After(upper) {
					candidates = append(candidates, row)
				}
			}
		}
		offset, _ := strconv.Atoi(q.Get("pageToken"))
		end := min(offset+2, len(candidates))
		if offset > end {
			t.Fatalf("invalid upstream offset: %d", offset)
		}
		next := ""
		if end < len(candidates) {
			next = strconv.Itoa(end)
		}
		return 200, map[string]any{"memos": candidates[offset:end], "nextPageToken": next}, nil
	})
	first, token, err := client.ListLivePage(context.Background(), "", 2, "")
	if err != nil || token == "" || !reflect.DeepEqual(liveProbeNames(first), []string{"memos/z", "memos/y"}) {
		t.Fatalf("first page: %v, %q, %v", liveProbeNames(first), token, err)
	}
	if first[0].Content != "  original\n#tag  " {
		t.Fatal("live list changed the source content")
	}
	// 新增及删除已返回的条目后，续页仍须读到全部剩余条目。
	newer := liveRawMemo("new", "new")
	newer["createTime"] = "2026-10-29T00:00:00Z"
	rows = append([]map[string]any{newer}, rows[1:]...)
	all := liveProbeNames(first)
	for token != "" {
		page, next, err := client.ListLivePage(context.Background(), "NORMAL", 2, token)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, liveProbeNames(page)...)
		token = next
	}
	if want := []string{"memos/z", "memos/y", "memos/m", "memos/b", "memos/a", "memos/old"}; !reflect.DeepEqual(all, want) {
		t.Fatalf("stable continuation: got %v, want %v", all, want)
	}
	refreshed, _, err := client.ListLivePage(context.Background(), "NORMAL", 2, "")
	if err != nil || !reflect.DeepEqual(liveProbeNames(refreshed), []string{"memos/new", "memos/z"}) {
		t.Fatalf("refresh did not see insertion: %v, %v", liveProbeNames(refreshed), err)
	}
	if requests < 6 {
		t.Fatalf("equal-time boundary was not fetched across upstream pages: %d requests", requests)
	}
}

func TestMemosLiveCursorBinding(t *testing.T) {
	s := liveProbeSettings()
	client := liveProbeClient(t, s, func(r *http.Request) (int, any, error) {
		return 200, map[string]any{"memos": []any{liveRawMemo("z", ""), liveRawMemo("a", "")}}, nil
	})
	_, token, err := client.ListLivePage(context.Background(), "NORMAL", 1, "")
	if err != nil || token == "" {
		t.Fatalf("cursor setup: %q %v", token, err)
	}
	for _, mutate := range []func(*MemosClient){
		func(m *MemosClient) { m.config.User = "users/other" },
		func(m *MemosClient) { m.config.InstanceID = "other" },
		func(m *MemosClient) { m.config.BaseURL = "http://other:5230" },
	} {
		other := *client
		mutate(&other)
		if _, _, err := other.ListLivePage(context.Background(), "NORMAL", 1, token); errorCode(err) != "INVALID_PAGE_TOKEN" {
			t.Fatalf("cross-binding cursor accepted: %v", err)
		}
	}
	if _, _, err := client.ListLivePage(context.Background(), "ARCHIVED", 1, token); errorCode(err) != "INVALID_PAGE_TOKEN" {
		t.Fatalf("cross-state cursor accepted: %v", err)
	}
	data, _ := base64.RawURLEncoding.DecodeString(token)
	var cursor map[string]any
	_ = json.Unmarshal(data, &cursor)
	for _, field := range []string{"kind", "created", "name", "unknown"} {
		copy := map[string]any{}
		for key, value := range cursor {
			copy[key] = value
		}
		copy[field] = "invalid"
		payload, _ := json.Marshal(copy)
		bad := base64.RawURLEncoding.EncodeToString(payload)
		if _, _, err := client.ListLivePage(context.Background(), "NORMAL", 1, bad); errorCode(err) != "INVALID_PAGE_TOKEN" {
			t.Fatalf("invalid cursor %s accepted: %v", field, err)
		}
	}
	for _, bad := range []string{"offset-from-memos", strings.Repeat("a", 2049), base64.RawURLEncoding.EncodeToString(append(data, []byte("{}")...))} {
		if _, _, err := client.ListLivePage(context.Background(), "NORMAL", 1, bad); errorCode(err) != "INVALID_PAGE_TOKEN" {
			t.Fatalf("invalid token accepted: %v", err)
		}
	}
	for _, state := range []string{"ALL", "NORMAL' || true"} {
		if _, _, err := client.ListLivePage(context.Background(), state, 1, ""); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("invalid state accepted: %v", err)
		}
	}
	for _, size := range []int{-1, 0, 1001} {
		if _, _, err := client.ListLivePage(context.Background(), "NORMAL", size, ""); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("invalid size accepted: %v", err)
		}
	}
}

func TestMemosLiveStopsAfterBoundaryGroup(t *testing.T) {
	s := liveProbeSettings()
	s.Memos.MaxPages = 1
	requests := 0
	client := liveProbeClient(t, s, func(r *http.Request) (int, any, error) {
		requests++
		newer := liveRawMemo("new", "new")
		newer["createTime"] = "2026-10-29T00:00:00Z"
		return 200, map[string]any{"memos": []any{newer, liveRawMemo("tie", "older")}, "nextPageToken": "many-older-ties"}, nil
	})
	items, token, err := client.ListLivePage(context.Background(), "NORMAL", 1, "")
	if err != nil || token == "" || requests != 1 || !reflect.DeepEqual(liveProbeNames(items), []string{"memos/new"}) {
		t.Fatalf("read past the returned boundary: %v %q %v (%d requests)", items, token, err, requests)
	}
}

func TestMemosLiveRetainedBudget(t *testing.T) {
	rowBytes, err := json.Marshal(liveRawMemo("z", "same-size"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		count   int
		limits  memosLiveLimits
		cursor  memosLiveCursor
		wantErr bool
	}{
		{"count-exact", 2, memosLiveLimits{2, 4096}, memosLiveCursor{}, false},
		{"count-over-cross-page", 3, memosLiveLimits{2, 4096}, memosLiveCursor{}, true},
		{"bytes-exact", 2, memosLiveLimits{10, 2 * len(rowBytes)}, memosLiveCursor{}, false},
		{"bytes-over", 2, memosLiveLimits{10, 2*len(rowBytes) - 1}, memosLiveCursor{}, true},
		{"bytes-over-cross-page", 3, memosLiveLimits{10, 2*len(rowBytes) + len("second")}, memosLiveCursor{}, true},
		{"excluded-names-bounded", 3, memosLiveLimits{2, 4096}, memosLiveCursor{Created: "2020-01-01T00:00:00.000000+00:00", Name: "memos/a"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := liveProbeSettings()
			s.Memos.MaxPages = 100000
			requests := 0
			client := liveProbeClient(t, s, func(r *http.Request) (int, any, error) {
				requests++
				if r.URL.Query().Get("pageToken") != "" {
					return 200, map[string]any{"memos": []any{liveRawMemo("x", "same-size")}}, nil
				}
				next := ""
				if tc.count > 2 {
					next = "second"
				}
				return 200, map[string]any{"memos": []any{liveRawMemo("z", "same-size"), liveRawMemo("y", "same-size")}, "nextPageToken": next}, nil
			})
			items, _, err := client.scanLivePage(context.Background(), "NORMAL", 1, tc.cursor, tc.limits)
			if tc.wantErr {
				if errorCode(err) != "LIVE_LIST_LIMIT" || items != nil {
					t.Fatalf("budget returned partial success: %v %v", liveProbeNames(items), err)
				}
				var service *ServiceError
				if !errors.As(err, &service) || service.Status != 503 {
					t.Fatalf("unexpected limit error: %v", err)
				}
			} else if err != nil || !reflect.DeepEqual(liveProbeNames(items), []string{"memos/z", "memos/y"}) {
				t.Fatalf("exact budget rejected: %v %v", liveProbeNames(items), err)
			}
			if requests > 2 {
				t.Fatalf("budget failed to stop scanning: %d requests", requests)
			}
		})
	}
}

func TestMemosLiveRejectsBrokenScan(t *testing.T) {
	for _, mode := range []string{"foreign-author", "wrong-state", "bad-order", "duplicate", "loop", "budget", "changed", "unavailable", "short-page"} {
		t.Run(mode, func(t *testing.T) {
			s := liveProbeSettings()
			s.Memos.MaxPages = 4
			calls := 0
			client := liveProbeClient(t, s, func(r *http.Request) (int, any, error) {
				calls++
				row := liveRawMemo("z", "first")
				next := "second"
				if calls > 1 {
					row = liveRawMemo("a", "second")
					next = ""
				}
				switch mode {
				case "foreign-author":
					row["creator"] = "users/other"
				case "wrong-state":
					row["state"] = "ARCHIVED"
				case "bad-order":
					if calls > 1 {
						row["createTime"] = "2026-01-01T00:00:00Z"
					}
				case "duplicate":
					row["name"] = "memos/z"
				case "loop":
					row["name"] = "memos/item" + strconv.Itoa(calls)
					next = "repeated"
				case "budget":
					row["name"] = "memos/item" + strconv.Itoa(calls)
					next = strconv.Itoa(calls)
				case "changed":
					if calls == 3 {
						row = liveRawMemo("m", "replacement")
						next = "second"
					}
				case "unavailable":
					if calls > 1 {
						return 503, map[string]any{}, nil
					}
				case "short-page":
					if r.URL.Query().Get("pageToken") == "" {
						return 200, map[string]any{"memos": []any{}, "nextPageToken": "second"}, nil
					}
				}
				return 200, map[string]any{"memos": []any{row}, "nextPageToken": next}, nil
			})
			items, token, err := client.ListLivePage(context.Background(), "NORMAL", 1, "")
			if mode == "short-page" {
				if err != nil || token != "" || !reflect.DeepEqual(liveProbeNames(items), []string{"memos/a"}) {
					t.Fatalf("short upstream page ended list: %v, %q, %v", items, token, err)
				}
				return
			}
			want := "UPSTREAM_PROTOCOL"
			switch mode {
			case "duplicate", "changed":
				want = "SOURCE_CHANGED"
			case "budget":
				want = "UPSTREAM_PAGINATION_LIMIT"
			case "unavailable":
				want = "UPSTREAM_UNAVAILABLE"
			}
			if errorCode(err) != want || items != nil || token != "" {
				t.Fatalf("broken scan published data: %v %q %v (want %s)", items, token, err, want)
			}
		})
	}
}

// 独立列表协议探针向此测试传入临时凭据。
func TestMemosLiveRealProtocol(t *testing.T) {
	address := os.Getenv("MOERISE_LIVE_PROBE_URL")
	if address == "" {
		t.Skip("requires the isolated list protocol probe")
	}
	settings := DefaultSettings().Memos
	settings.BaseURL, settings.User, settings.Token = address, os.Getenv("MOERISE_LIVE_PROBE_USER"), os.Getenv("MOERISE_LIVE_PROBE_TOKEN")
	settings.InstanceID, settings.PageSize = "isolated-live-probe", 2
	client := NewMemosClient(settings)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if version, err := client.Identity(ctx); err != nil || version != "0.31.0" {
		t.Fatalf("identity: %q %v", version, err)
	}
	all := []string{}
	token := ""
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("live cursor failed to terminate")
		}
		items, next, err := client.ListLivePage(ctx, "NORMAL", 2, token)
		if err != nil {
			t.Fatal(err)
		}
		for _, memo := range items {
			if memo.Creator != settings.User || memo.State != "NORMAL" {
				t.Fatalf("scope leak: %s %s", memo.Name, memo.Creator)
			}
		}
		all = append(all, liveProbeNames(items)...)
		if next == "" {
			break
		}
		token = next
	}
	want := []string{"memos/new-front-probe", "memos/z-probe", "memos/y-probe", "memos/m-probe", "memos/b-probe", "memos/a-probe", "memos/old-pinned-probe"}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("live created_desc: got %v, want %v", all, want)
	}
	archived, next, err := client.ListLivePage(ctx, "ARCHIVED", 2, "")
	if err != nil || next != "" || !reflect.DeepEqual(liveProbeNames(archived), []string{"memos/archived-probe"}) {
		t.Fatalf("archived scope: %v %q %v", liveProbeNames(archived), next, err)
	}
	// 修改分页大小沿用相同的键值边界。
	first, next, err := client.ListLivePage(ctx, "NORMAL", 1, "")
	if err != nil || next == "" {
		t.Fatalf("page size setup: %v %q %v", first, next, err)
	}
	rest, _, err := client.ListLivePage(ctx, "NORMAL", 20, next)
	if err != nil || !reflect.DeepEqual(liveProbeNames(rest), want[1:]) {
		t.Fatalf("page size continuation: %v %v", liveProbeNames(rest), err)
	}
	_, continuation, err := client.ListLivePage(ctx, "NORMAL", 2, "")
	if err != nil || continuation == "" {
		t.Fatalf("mutation setup: %q %v", continuation, err)
	}
	var response map[string]any
	for _, name := range []string{"memos/new-front-probe", "memos/z-probe"} {
		if err := client.do(ctx, http.MethodDelete, "/api/v1/"+name, nil, nil, &response); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ name, created string }{
		{"after-live-first", "2027-01-01T00:00:00Z"},
		{"w-live", "2026-01-02T00:00:00Z"},
		{"zz-live", "2026-01-02T00:00:00Z"},
	} {
		if _, err := client.Create(ctx, "memos/"+item.name, item.name); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]string{"createTime": item.created})
		if err := client.do(ctx, http.MethodPatch, "/api/v1/memos/"+item.name,
			map[string][]string{"updateMask": {"create_time"}}, payload, &response); err != nil {
			t.Fatal(err)
		}
	}
	remaining := []string{}
	for page := 0; continuation != ""; page++ {
		if page > 10 {
			t.Fatal("mutation continuation failed to terminate")
		}
		items, next, err := client.ListLivePage(ctx, "NORMAL", 2, continuation)
		if err != nil {
			t.Fatal(err)
		}
		remaining = append(remaining, liveProbeNames(items)...)
		continuation = next
	}
	if want := []string{"memos/y-probe", "memos/w-live", "memos/m-probe", "memos/b-probe", "memos/a-probe", "memos/old-pinned-probe"}; !reflect.DeepEqual(remaining, want) {
		t.Fatalf("mutation continuation: got %v, want %v", remaining, want)
	}
	refreshed, _, err := client.ListLivePage(ctx, "NORMAL", 2, "")
	if err != nil || !reflect.DeepEqual(liveProbeNames(refreshed), []string{"memos/after-live-first", "memos/zz-live"}) {
		t.Fatalf("mutation refresh: %v %v", liveProbeNames(refreshed), err)
	}
	t.Logf("real protocol: %d normal, %d archived; resource-name ties, pinning, edit and author isolation passed", len(all), len(archived))
	t.Log("real mutation: newer and equal-time insertions, returned-item deletions and first-page refresh passed")
}

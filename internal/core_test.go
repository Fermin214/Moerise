package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testSettings(t *testing.T) Settings {
	t.Helper()
	root := t.TempDir()
	s := DefaultSettings()
	s.Memos.InstanceID, s.Memos.BaseURL, s.Memos.PublicURL = "unit-instance", "http://memos:5230", "https://memos.example.com"
	s.Memos.User, s.Memos.Token, s.Memos.PageSize = "users/owner", strings.Repeat("u", 40), 2
	s.AccessTokens = []AccessToken{{"reader", strings.Repeat("r", 40), []string{"notes:read"}}, {"operator", strings.Repeat("o", 40), []string{"notes:read", "sync:read", "sync:write"}}}
	s.CursorSecret, s.StateDir, s.IndexDir = strings.Repeat("c", 40), filepath.Join(root, "state"), filepath.Join(root, "index")
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	return s
}

func testStore(t *testing.T, s Settings) *Store {
	t.Helper()
	store, err := NewStore(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func rawMemo(name, content string) map[string]any {
	return map[string]any{"name": "memos/" + name, "creator": "users/owner", "content": content,
		"state": "NORMAL", "visibility": "PRIVATE", "createTime": "2020-01-01T00:00:00Z", "updateTime": "2020-01-01T00:00:00Z"}
}

func testMemo(t *testing.T, name, content string) Memo {
	t.Helper()
	memo, err := normalizeMemo(rawMemo(name, content))
	if err != nil {
		t.Fatal(err)
	}
	return memo
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeClient(s Settings, handler func(*http.Request) (int, any, error)) *MemosClient {
	client := NewMemosClient(s.Memos)
	client.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status, body, err := handler(request)
		if err != nil {
			return nil, err
		}
		data, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}, Request: request}, nil
	})
	return client
}

func identityResponse(request *http.Request) (int, any, bool) {
	if strings.HasSuffix(request.URL.Path, "/profile") {
		return 200, map[string]string{"version": "0.31.0"}, true
	}
	if strings.HasSuffix(request.URL.Path, "/auth/me") {
		return 200, map[string]any{"user": map[string]string{"name": "users/owner"}}, true
	}
	return 0, nil, false
}

func claim(t *testing.T, store *Store, mode string) Job {
	t.Helper()
	if _, err := store.Enqueue(mode, true); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim()
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	return *job
}

func TestFailedPageDoesNotPublish(t *testing.T) {
	s := testSettings(t)
	store := testStore(t, s)
	original := testMemo(t, "original", "original")
	if err := store.Refresh(original.Name, &original); err != nil {
		t.Fatal(err)
	}
	if err := store.Advance("2020-01-02T00:00:00+00:00", true); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Status()
	client := fakeClient(s, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		if request.URL.Query().Get("pageToken") != "" {
			return 0, nil, context.DeadlineExceeded
		}
		return 200, map[string]any{"memos": []any{rawMemo("partial", "partial")}, "nextPageToken": "second"}, nil
	})
	_, err := NewSyncService(store, client).Sync(context.Background(), claim(t, store, "full"))
	if errorCode(err) != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("unexpected error: %v", err)
	}
	after, _ := store.Status()
	names, _ := store.Names()
	if len(names) != 1 || !names["memos/original"] || *before.Watermark != *after.Watermark || *before.LastFull != *after.LastFull {
		t.Fatal("failed scan published partial data")
	}
	var count int
	if err := store.index.QueryRow("SELECT COUNT(*) FROM scan_memos").Scan(&count); err != nil || count != 0 {
		t.Fatalf("staging not cleared: %v", err)
	}
}

func TestFullRepairRechecksMissingAndBackdated(t *testing.T) {
	s := testSettings(t)
	store := testStore(t, s)
	for _, name := range []string{"moved", "deleted"} {
		memo := testMemo(t, name, "original")
		if err := store.Refresh(memo.Name, &memo); err != nil {
			t.Fatal(err)
		}
	}
	filterSeen := false
	client := fakeClient(s, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		if strings.HasSuffix(request.URL.Path, "/memos") {
			if strings.Contains(request.URL.Query().Get("filter"), "timestamp(") {
				filterSeen = true
			}
			return 200, map[string]any{"memos": []any{rawMemo("backdated", "new")}}, nil
		}
		if strings.HasSuffix(request.URL.Path, "/moved") {
			memo := rawMemo("moved", "still visible")
			memo["state"] = "ARCHIVED"
			return 200, memo, nil
		}
		return 404, nil, nil
	})
	result, err := NewSyncService(store, client).Sync(context.Background(), claim(t, store, "full"))
	if err != nil {
		t.Fatal(err)
	}
	names, _ := store.Names()
	status, _ := store.Status()
	if len(names) != 2 || !names["memos/moved"] || !names["memos/backdated"] || result["removed"] != 1 || !filterSeen || status.Counts["ARCHIVED"] != 1 {
		t.Fatalf("incorrect full repair: %+v", result)
	}
}

func TestIdentityChangeCannotPublish(t *testing.T) {
	for _, duringScan := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[duringScan], func(t *testing.T) {
			s := testSettings(t)
			store := testStore(t, s)
			identities := 0
			client := fakeClient(s, func(request *http.Request) (int, any, error) {
				if strings.HasSuffix(request.URL.Path, "/profile") {
					return 200, map[string]string{"version": "0.31.0"}, nil
				}
				if strings.HasSuffix(request.URL.Path, "/auth/me") {
					identities++
					user := "users/other"
					if duringScan && identities == 1 {
						user = "users/owner"
					}
					return 200, map[string]any{"user": map[string]string{"name": user}}, nil
				}
				return 200, map[string]any{"memos": []any{rawMemo("partial", "private")}}, nil
			})
			_, err := NewSyncService(store, client).Sync(context.Background(), claim(t, store, "full"))
			names, _ := store.Names()
			status, _ := store.Status()
			if errorCode(err) != "IDENTITY_MISMATCH" || len(names) != 0 || status.Watermark != nil {
				t.Fatal("identity mismatch published data")
			}
		})
	}
}

func TestMergeRetryAndRestart(t *testing.T) {
	s := testSettings(t)
	store := testStore(t, s)
	original, err := store.Enqueue("incremental", true)
	if err != nil {
		t.Fatal(err)
	}
	merged, _ := store.Enqueue("full", true)
	if merged.ID != original.ID || merged.Mode != "full" {
		t.Fatal("pending tasks did not merge")
	}
	running, _ := store.Claim()
	pending, _ := store.Enqueue("incremental", true)
	if err := store.Retry(*running, "UPSTREAM_UNAVAILABLE"); err != nil {
		t.Fatal(err)
	}
	merged, _ = store.Job(pending.ID)
	retry, _ := store.Job(original.ID)
	if merged.Result["merged_into"] != original.ID || retry.AvailableAt <= nowSeconds() {
		t.Fatal("retry intent or backoff lost")
	}
	if err := NewSyncService(store, nil).Schedule(); err != nil {
		t.Fatal(err)
	}
	if job, err := store.Claim(); err != nil || job != nil {
		t.Fatal("schedule erased backoff")
	}
	if _, err := store.state.Exec("UPDATE jobs SET available_at = 0 WHERE id = ?", original.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(); err != nil {
		t.Fatal(err)
	}
	store.Close()
	restarted := testStore(t, s)
	restored, _ := restarted.Job(original.ID)
	if restored.Status != "pending" || restored.Mode != "full" {
		t.Fatal("running task was not recovered")
	}
	if _, err := restarted.Claim(); err != nil {
		t.Fatal(err)
	}
	next, _ := restarted.Enqueue("incremental", true)
	restarted.Close()
	again := testStore(t, s)
	restored, _ = again.Job(original.ID)
	next, _ = again.Job(next.ID)
	if restored.Result["merged_into"] != next.ID || next.Mode != "full" {
		t.Fatal("restart lost full repair intent")
	}
}

func TestStateAndBackupCompatibility(t *testing.T) {
	s := testSettings(t)
	s.Memos.InstanceID = "legacy-instance"
	fixture := filepath.Join("testdata", "backup-v1")
	if err := Restore(s, fixture, filepath.Join(t.TempDir(), "unused")); err != nil {
		t.Fatal(err)
	}
	store := testStore(t, s)
	var history string
	if err := store.state.QueryRow("SELECT value_json FROM product_state WHERE namespace = 'review'").Scan(&history); err != nil || !strings.Contains(history, "memos/example") {
		t.Fatalf("backup history lost: %v", err)
	}
	backupDir := filepath.Join(t.TempDir(), "snapshot")
	if _, err := Backup(s, backupDir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.state.Exec("DELETE FROM product_state"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := Restore(s, backupDir, filepath.Join(t.TempDir(), "safety")); err != nil {
		t.Fatal(err)
	}
	again := testStore(t, s)
	status, _ := again.Status()
	names, _ := again.Names()
	if status.Watermark != nil || len(names) != 0 {
		t.Fatal("restore did not request a fresh index")
	}
	if err := again.state.QueryRow("SELECT value_json FROM product_state").Scan(&history); err != nil {
		t.Fatal(err)
	}
	again.Close()
	if err := Restore(s, backupDir, filepath.Join(t.TempDir(), "safety-again")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil || len(entries) != 2 {
		t.Fatal("restore modified its backup")
	}
}

func TestOpenDatabaseRecoversRunningTask(t *testing.T) {
	settings := testSettings(t)
	settings.Memos.InstanceID = "legacy-instance"
	if err := os.MkdirAll(settings.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "backup-v1", "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.StateDir, "state.sqlite3"), data, 0600); err != nil {
		t.Fatal(err)
	}
	store := testStore(t, settings)
	status, err := store.Status()
	if err != nil || len(status.Jobs) != 1 || status.Jobs[0].Status != "pending" || status.Jobs[0].Count != 1 {
		t.Fatalf("persisted task not recovered: %+v %v", status.Jobs, err)
	}
	job, err := store.Claim()
	if err != nil || job == nil || job.Mode != "full" {
		t.Fatalf("persisted repair intent lost: %+v %v", job, err)
	}
}

func TestConcurrentWorkerReadsAndSyncRequests(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	client := fakeClient(settings, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		if request.URL.Path == "/api/v1/memos" {
			if request.URL.Query().Get("state") == "ARCHIVED" {
				return 200, map[string]any{"memos": []any{}}, nil
			}
			return 200, map[string]any{"memos": []any{rawMemo("example", "live")}}, nil
		}
		return 200, rawMemo("example", "live"), nil
	})
	worker := NewSyncService(store, client)
	api, err := NewAPI(settings, store, client, worker)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	var group sync.WaitGroup
	for i := 0; i < 6; i++ {
		group.Go(func() {
			for j := 0; j < 12; j++ {
				method, path, body := "GET", "/api/ext/v1/notes", ""
				if j%3 == 0 {
					method, path, body = "POST", "/api/ext/v1/sync", `{"mode":"full"}`
				}
				request := httptest.NewRequest(method, path, strings.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+settings.AccessTokens[1].Token)
				response := httptest.NewRecorder()
				api.ServeHTTP(response, request)
				if response.Code != 200 && response.Code != 202 {
					t.Errorf("concurrent request: %d %s", response.Code, response.Body.String())
					return
				}
			}
		})
	}
	group.Wait()
}

func TestRestoreRejectsCorruptionSchemaAndWriter(t *testing.T) {
	s := testSettings(t)
	store := testStore(t, s)
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if _, err := Backup(s, snapshot); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireWriter(s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	err = Restore(s, snapshot, filepath.Join(t.TempDir(), "blocked"))
	lock.Close()
	if err == nil || !strings.Contains(err.Error(), "Another Moerise writer") {
		t.Fatal("live restore was allowed")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(snapshot, "state.sqlite3"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("corruption")
	file.Close()
	if err := Restore(s, snapshot, filepath.Join(t.TempDir(), "safety")); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatal("corrupt backup was accepted")
	}
}

func TestAPIContractAndSourceIsolation(t *testing.T) {
	s := testSettings(t)
	store := testStore(t, s)
	client := fakeClient(s, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		name := strings.TrimPrefix(request.URL.Path, "/api/v1/memos/")
		if name == "other" {
			memo := rawMemo(name, "private")
			memo["creator"] = "users/other"
			return 200, memo, nil
		}
		return 200, rawMemo(name, "fresh"), nil
	})
	worker := NewSyncService(store, client)
	api, err := NewAPI(s, store, client, worker)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		memo := testMemo(t, name, "stale")
		if err := store.Refresh(memo.Name, &memo); err != nil {
			t.Fatal(err)
		}
	}
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		writer := httptest.NewRecorder()
		api.ServeHTTP(writer, request)
		return writer
	}
	for _, test := range []struct {
		method, path, token, body string
		status                    int
	}{
		{"GET", "/api/ext/v1/notes", "", "", 401},
		{"POST", "/api/ext/v1/sync", s.AccessTokens[0].Token, `{"mode":"full"}`, 403},
		{"POST", "/api/ext/v1/sync", s.AccessTokens[1].Token, `{"mode":"bad"}`, 422},
		{"POST", "/api/ext/v1/sync", s.AccessTokens[1].Token, `{"mode":"full","extra":true}`, 422},
		{"POST", "/api/ext/v1/sync", s.AccessTokens[1].Token, `{"mode":null}`, 422},
		{"GET", "/api/ext/v1/notes?page_size=0", s.AccessTokens[0].Token, "", 422},
		{"GET", "/api/ext/v1/notes/other", s.AccessTokens[0].Token, "", 404},
		{"GET", "/openapi.json", "", "", 200},
		{"GET", "/docs", "", "", 200},
		{"DELETE", "/api/ext/v1/sync", s.AccessTokens[1].Token, "", 405},
	} {
		response := call(test.method, test.path, test.token, test.body)
		if response.Code != test.status {
			t.Fatalf("%s %s: %d %s", test.method, test.path, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") == "" {
			t.Fatal("missing privacy or tracing headers")
		}
	}
	first := call("GET", "/api/ext/v1/notes?page_size=1", s.AccessTokens[0].Token, "")
	var page struct {
		Notes []Note `json:"notes"`
		Token string `json:"next_page_token"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || len(page.Notes) != 1 || page.Token == "" || page.Notes[0].Content != "fresh" {
		t.Fatalf("incorrect first page: %s", first.Body.String())
	}
	if _, err := api.decodeCursor(page.Token, "ARCHIVED"); errorCode(err) != "INVALID_PAGE_TOKEN" {
		t.Fatal("cursor state was not enforced")
	}
	if _, err := api.decodeCursor(page.Token+"x", "NORMAL"); errorCode(err) != "INVALID_PAGE_TOKEN" {
		t.Fatal("cursor signature was not enforced")
	}
	var spec map[string]any
	if err := json.Unmarshal(openAPIJSON, &spec); err != nil {
		t.Fatal(err)
	}
}

func TestConfigRejectsUnknownAndInvalidValues(t *testing.T) {
	s := testSettings(t)
	for _, mutate := range []func(*Settings){
		func(s *Settings) { s.Memos.BaseURL = "https://user:password@example.com" },
		func(s *Settings) { s.Memos.User = "users/../other" },
		func(s *Settings) { s.StateDir = s.IndexDir },
		func(s *Settings) { s.AccessTokens[0].Scopes = []string{"notes:write"} },
		func(s *Settings) { s.Sync.PollSeconds = 0 },
	} {
		var clone Settings
		data, _ := json.Marshal(s)
		_ = json.Unmarshal(data, &clone)
		mutate(&clone)
		if clone.Validate() == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	var target Settings
	if strictJSON(strings.NewReader(`{"unexpected":"secret"}`), &target) == nil {
		t.Fatal("unknown config field accepted")
	}
	if strictJSON(strings.NewReader(`{} {}`), &target) == nil {
		t.Fatal("multiple JSON values accepted")
	}
}

func TestTimestampAndCanonicalHash(t *testing.T) {
	memo := testMemo(t, "example", "original")
	if memo.UpdateTime != "2020-01-01T00:00:00.000000+00:00" {
		t.Fatal(memo.UpdateTime)
	}
	if got := string(canonicalJSON(map[string]any{"z": "中文<>&", "a": []any{true, 1, nil}})); got != `{"a": [true, 1, null], "z": "中文<>&"}` {
		t.Fatal(got)
	}
	if _, err := time.Parse(time.RFC3339Nano, memo.UpdateTime); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledScanDoesNotAdvance(t *testing.T) {
	s := testSettings(t)
	store := testStore(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := fakeClient(s, func(request *http.Request) (int, any, error) { return 0, nil, ctx.Err() })
	_, err := NewSyncService(store, client).Sync(ctx, claim(t, store, "full"))
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("canceled request succeeded")
	}
	state, _ := store.Status()
	if state.Watermark != nil {
		t.Fatal("cancellation advanced watermark")
	}
}

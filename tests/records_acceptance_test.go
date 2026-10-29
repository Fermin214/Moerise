//go:build integration

package acceptance

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

func requireRecordError(t *testing.T, result map[string]any, code string) {
	t.Helper()
	detail, ok := result["error"].(map[string]any)
	if !ok || detail["code"] != code {
		t.Fatalf("expected error %s, got %v", code, result)
	}
	if code == "WRITE_OUTCOME_UNKNOWN" && detail["retryable"] != true {
		t.Fatalf("unknown write outcome must be retryable: %v", result)
	}
}

func (h *harness) requireRecord(note map[string]any, name, content string, tags []string) string {
	h.t.Helper()
	actual, ok := note["name"].(string)
	if !ok || !strings.HasPrefix(actual, "memos/") || actual == "memos/" || (name != "" && actual != name) {
		h.t.Fatalf("unexpected record resource: %v", note)
	}
	if note["instance_id"] != h.settings.Memos.InstanceID || note["creator"] != h.settings.Memos.User ||
		note["visibility"] != "PRIVATE" || note["content"] != content || note["state"] != "NORMAL" ||
		note["url"] != h.settings.Memos.PublicURL+"/"+actual {
		h.t.Fatalf("incorrect record identity, content, visibility, or source URL: %v", note)
	}
	for _, field := range []string{"create_time", "update_time", "source_hash"} {
		if value, ok := note[field].(string); !ok || value == "" {
			h.t.Fatalf("record is missing %s: %v", field, note)
		}
	}
	values, ok := note["tags"].([]any)
	if !ok {
		h.t.Fatalf("record tags must be an array: %v", note)
	}
	actualTags := make([]string, 0, len(values))
	for _, value := range values {
		tag, ok := value.(string)
		if !ok {
			h.t.Fatalf("invalid record tag: %v", value)
		}
		actualTags = append(actualTags, tag)
	}
	slices.Sort(actualTags)
	expectedTags := slices.Clone(tags)
	slices.Sort(expectedTags)
	if !slices.Equal(actualTags, expectedTags) {
		h.t.Fatalf("record tags=%v expected=%v", actualTags, expectedTags)
	}
	source := h.call(h.memos, "/api/v1/"+actual, "GET", h.upstream, nil, 200)
	if source["name"] != actual || source["content"] != content || source["creator"] != h.settings.Memos.User ||
		(source["visibility"] != nil && source["visibility"] != "PRIVATE") {
		h.t.Fatalf("real Memos record differs: %v", source)
	}
	return actual
}

func (h *harness) requireSourceRecords(names ...string) {
	h.t.Helper()
	found := []string{}
	for _, state := range []string{"NORMAL", "ARCHIVED"} {
		cursor := ""
		visited := map[string]bool{}
		for {
			query := url.Values{
				"state":    {state},
				"pageSize": {"2"},
				"filter":   {"creator == '" + h.settings.Memos.User + "'"},
			}
			if cursor != "" {
				query.Set("pageToken", cursor)
			}
			page := h.call(h.memos, "/api/v1/memos?"+query.Encode(), "GET", h.upstream, nil, 200)
			if memos, ok := page["memos"].([]any); ok {
				for _, value := range memos {
					memo, ok := value.(map[string]any)
					if !ok || memo["creator"] != h.settings.Memos.User {
						h.t.Fatalf("unexpected source memo: %v", value)
					}
					name, ok := memo["name"].(string)
					if !ok {
						h.t.Fatalf("missing source memo name: %v", memo)
					}
					found = append(found, name)
				}
			} else if page["memos"] != nil {
				h.t.Fatalf("invalid source memo page: %v", page)
			}
			cursor, _ = page["nextPageToken"].(string)
			if cursor == "" {
				break
			}
			if visited[cursor] || len(visited) >= 1000 {
				h.t.Fatal("source pagination did not finish")
			}
			visited[cursor] = true
		}
	}
	slices.Sort(found)
	expected := slices.Clone(names)
	slices.Sort(expected)
	if !slices.Equal(found, expected) {
		h.t.Fatalf("real Memos records=%v expected=%v", found, expected)
	}
}

func TestRecordsAcceptance(t *testing.T) {
	h := newHarness(t)
	body := map[string]any{"content": "记录正文 #测试\nsecond line #parent/child"}
	key := "records.A-Z_a-z:0-9"
	requireRecordError(t, h.call(h.base, "/api/ext/v1/notes", "POST", h.reader, body, 403), "FORBIDDEN")
	requireRecordError(t, h.recordInProcess("", body, 422), "INVALID_ARGUMENT")
	requireRecordError(t, h.recordInProcess("bad/key", body, 422), "INVALID_ARGUMENT")
	requireRecordError(t, h.recordInProcess(strings.Repeat("k", 129), body, 422), "INVALID_ARGUMENT")
	requireRecordError(t, h.recordInProcess("invalid-tag",
		map[string]any{"content": "rejected", "tags": []string{""}}, 422), "INVALID_ARGUMENT")
	requireRecordError(t, h.recordInProcess("separate-tag",
		map[string]any{"content": "rejected", "tags": []string{"valid"}}, 422), "INVALID_ARGUMENT")
	h.requireSourceRecords()

	content := body["content"].(string)
	tags := []string{"parent", "parent/child", "测试"}
	name := h.requireRecord(h.recordInProcess(key, body, 201), "", content, tags)
	h.requireRecord(h.recordInProcess(key, body, 200), name, content, tags)
	conflict := map[string]any{"content": "different"}
	requireRecordError(t, h.recordInProcess(key, conflict, 409), "IDEMPOTENCY_CONFLICT")
	h.requireSourceRecords(name)

	plainBody := map[string]any{"content": "plain text without tags"}
	plainKey := strings.Repeat("k", 128)
	plainName := h.requireRecord(h.recordInProcess(plainKey, plainBody, 201), "", plainBody["content"].(string), nil)
	h.requireSourceRecords(name, plainName)
	snapshot := filepath.Join(h.root, "records-snapshot")
	h.command(true, "backup", "--output", snapshot)

	edited := "#edited\n\nedited in Memos"
	h.call(h.memos, "/api/v1/"+name+"?updateMask=content", "PATCH", h.upstream, map[string]string{"content": edited}, 200)
	fresh := h.recordInProcess(key, body, 200)
	h.requireRecord(fresh, name, edited, []string{"edited"})
	h.call(h.memos, "/api/v1/"+plainName, "DELETE", h.upstream, nil, 200)
	requireRecordError(t, h.recordInProcess(plainKey, plainBody, 404), "SOURCE_NOT_FOUND")

	checkPersisted := func() {
		t.Helper()
		h.requireRecord(h.recordInProcess(key, body, 200), name, edited, []string{"edited"})
		requireRecordError(t, h.recordInProcess(key, conflict, 409), "IDEMPOTENCY_CONFLICT")
		requireRecordError(t, h.recordInProcess(plainKey, plainBody, 404), "SOURCE_NOT_FOUND")
		h.requireSourceRecords(name)
	}
	for _, operation := range []string{"restart", "rebuild-index", "restore"} {
		h.stop()
		switch operation {
		case "rebuild-index":
			h.command(true, "rebuild-index")
		case "restore":
			h.command(true, "restore", "--source", snapshot, "--safety-backup", filepath.Join(h.root, "records-before-restore"))
		}
		h.start()
		h.ready()
		checkPersisted()
		t.Logf("PASS record replay, conflict, fresh source, and deleted source after %s", operation)
	}
}

func TestRecordsLegacyUpgradeAcceptance(t *testing.T) {
	h := newHarness(t)
	body := map[string]any{"content": "legacy body", "tags": []string{"z", "a", "a"}}
	content := "#a #z\n\nlegacy body"
	// This is the historical canonical digest, independent of the new preparer.
	digest := sha256.Sum256([]byte(`{"content": "legacy body", "tags": ["a", "z"]}`))
	names := []string{}
	h.stop()
	db := h.database("state")
	for _, status := range []string{"pending", "unknown", "succeeded"} {
		name := "memos/legacy-upgrade-" + status
		names = append(names, name)
		if status != "pending" {
			h.call(h.memos, "/api/v1/memos?memoId="+strings.TrimPrefix(name, "memos/"), "POST", h.upstream,
				map[string]string{"content": content, "visibility": "PRIVATE"}, 200)
		}
		_, err := db.Exec(`INSERT INTO note_writes
			(instance_id, user_name, idempotency_key, request_hash, memo_name, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, h.settings.Memos.InstanceID, h.settings.Memos.User,
			"legacy:"+status, fmt.Sprintf("%x", digest), name, status, "2026-10-28T00:00:00Z", "2026-10-28T00:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(h.root, "legacy-writes")
	h.command(true, "backup", "--output", snapshot)
	h.start()
	h.ready()
	replay := func() {
		t.Helper()
		for _, status := range []string{"pending", "unknown", "succeeded"} {
			h.requireRecord(h.recordInProcess("legacy:"+status, body, 200),
				"memos/legacy-upgrade-"+status, content, []string{"a", "z"})
		}
		h.requireSourceRecords(names...)
	}
	replay()
	h.stop()
	h.command(true, "restore", "--source", snapshot, "--safety-backup", filepath.Join(h.root, "before-legacy-restore"))
	h.start()
	h.ready()
	replay()
}

type recordUpstreamEvent struct {
	method string
	path   string
	memoID string
}

type recordFaultProxy struct {
	mu            sync.Mutex
	mode          string
	armed         bool
	events        []recordUpstreamEvent
	droppedStatus int
	dropError     error
}

func (p *recordFaultProxy) decorate(t *testing.T, address string) string {
	t.Helper()
	target, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := &http.Transport{Proxy: nil}
	proxy.Transport = transport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.events = append(p.events, recordUpstreamEvent{r.Method, r.URL.Path, r.URL.Query().Get("memoId")})
		mode := ""
		if p.armed && r.Method == "POST" && r.URL.Path == "/api/v1/memos" {
			mode, p.armed = p.mode, false
		}
		p.mu.Unlock()
		switch mode {
		case "unavailable":
			http.Error(w, "injected failure before source write", http.StatusServiceUnavailable)
		case "drop-response":
			// Buffer the real source response, then close the client connection without sending it.
			response := httptest.NewRecorder()
			proxy.ServeHTTP(response, r)
			conn, _, err := w.(http.Hijacker).Hijack()
			p.mu.Lock()
			p.droppedStatus, p.dropError = response.Code, err
			p.mu.Unlock()
			if err == nil {
				_ = conn.Close()
			}
		default:
			proxy.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(func() {
		server.Close()
		transport.CloseIdleConnections()
	})
	return server.URL
}

func (p *recordFaultProxy) snapshot() []recordUpstreamEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.events)
}

func recordPosts(events []recordUpstreamEvent) []recordUpstreamEvent {
	posts := []recordUpstreamEvent{}
	for _, event := range events {
		if event.method == "POST" && event.path == "/api/v1/memos" {
			posts = append(posts, event)
		}
	}
	return posts
}

func requireRecordLookup(t *testing.T, events []recordUpstreamEvent, memoID string, resend bool) {
	t.Helper()
	found := false
	for _, event := range events {
		if event.method == "GET" && event.path == "/api/v1/memos/"+memoID {
			found = true
		}
		if event.method == "POST" && event.path == "/api/v1/memos" {
			if !resend || !found || event.memoID != memoID {
				t.Fatalf("replay must look up persisted memoId before any resend: %v", events)
			}
		}
	}
	if !found {
		t.Fatalf("replay did not look up persisted memoId %q: %v", memoID, events)
	}
}

func TestRecordsFaultRecoveryAcceptance(t *testing.T) {
	for _, mode := range []string{"drop-response", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			fault := &recordFaultProxy{mode: mode}
			h := newHarnessWithUpstream(t, func(address string) string { return fault.decorate(t, address) })
			fault.mu.Lock()
			fault.armed = true
			fault.mu.Unlock()
			body := map[string]any{"content": "recover " + mode + "\n#recovery"}
			key := "recovery:" + mode
			requireRecordError(t, h.recordInProcess(key, body, 503), "WRITE_OUTCOME_UNKNOWN")
			posts := recordPosts(fault.snapshot())
			if len(posts) != 1 {
				t.Fatalf("unknown outcome must leave exactly one source attempt: %v", posts)
			}
			memoID := posts[0].memoID
			if !regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`).MatchString(memoID) {
				t.Fatalf("source Create must use query memoId=uuid: %v", posts[0])
			}
			name := "memos/" + memoID
			if mode == "drop-response" {
				fault.mu.Lock()
				status, dropErr := fault.droppedStatus, fault.dropError
				fault.mu.Unlock()
				if status != 200 || dropErr != nil {
					t.Fatalf("failed to drop a successful real Memos response: status=%d error=%v", status, dropErr)
				}
				h.requireSourceRecords(name)
			} else {
				h.requireSourceRecords()
			}

			snapshot := filepath.Join(h.root, "unknown-write-snapshot")
			h.command(true, "backup", "--output", snapshot)
			h.stop()
			h.start()
			h.ready()

			start := len(fault.snapshot())
			note := h.recordInProcess(key, body, 200)
			events := fault.snapshot()
			requireRecordLookup(t, events[start:], memoID, mode == "unavailable")
			h.requireRecord(note, name, body["content"].(string), []string{"recovery"})
			posts = recordPosts(events)
			expectedPosts := 1
			if mode == "unavailable" {
				expectedPosts = 2
			}
			if len(posts) != expectedPosts {
				t.Fatalf("source POST count=%d expected=%d: %v", len(posts), expectedPosts, posts)
			}
			for _, post := range posts {
				if post.memoID != memoID {
					t.Fatalf("source retry changed persisted memoId: %v", posts)
				}
			}
			h.requireSourceRecords(name)
			for _, operation := range []string{"rebuild-index", "restore"} {
				h.stop()
				if operation == "rebuild-index" {
					h.command(true, "rebuild-index")
				} else {
					h.command(true, "restore", "--source", snapshot, "--safety-backup", filepath.Join(h.root, "unknown-write-before-restore"))
				}
				h.start()
				h.ready()
				start = len(fault.snapshot())
				h.requireRecord(h.recordInProcess(key, body, 200), name, body["content"].(string), []string{"recovery"})
				requireRecordLookup(t, fault.snapshot()[start:], memoID, false)
				if posts := recordPosts(fault.snapshot()); len(posts) != expectedPosts {
					t.Fatalf("%s added source POSTs: %v", operation, posts)
				}
				h.requireSourceRecords(name)
			}
			t.Log("PASS unknown write recovery, persisted memoId lookup, and exactly one real Memos record")
		})
	}
}

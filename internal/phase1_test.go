package moerise

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPhase1ZeroHTTPClientReadsAndAuthEvictionOn401(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	_ = clientMapIndex(t, api)
	store := api.app.Store
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")
	if err := store.Advance(now, true); err != nil {
		t.Fatal(err)
	}
	vecProvider := &clientSearchEmbedding{dimensions: store.Settings.Embedding.Dimensions}
	api.app.Search.Provider = vecProvider

	var authFail atomic.Bool
	var upstreamHTTP atomic.Int32
	blocker := fakeClient(store.Settings, func(r *http.Request) (int, any, error) {
		upstreamHTTP.Add(1)
		if authFail.Load() && (strings.HasSuffix(r.URL.Path, "/auth/me") || r.URL.Path == "/api/v1/memos") {
			return 401, map[string]string{"message": "revoked"}, nil
		}
		if authFail.Load() {
			if status, body, ok := identityResponse(r); ok {
				return status, body, nil
			}
		}
		return 503, map[string]string{"message": "offline"}, nil
	})
	origAuthTransport := api.app.Sessions.auth.client.Transport
	api.app.Sessions.auth.client.Transport = blocker.client.Transport
	api.app.Source = blocker
	api.app.Notes.Source = blocker
	api.app.Records.Source = blocker
	api.app.Reviews.Source = blocker
	api.app.Search.Source = blocker

	origin := fixture.settings.Web.PublicOrigin
	clientAPI := api

	month := time.Now().UTC().Format("2006-01")
	requests := []struct{ method, path, body string }{
		{"GET", "/api/app/v1/notes", ""},
		{"GET", "/api/app/v1/notes/a", ""},
		{"GET", "/api/app/v1/tags", ""},
		{"GET", "/api/app/v1/calendar?month=" + month, ""},
		{"GET", "/api/app/v1/reviews/daily", ""},
		{"GET", "/api/app/v1/map", ""},
		{"POST", "/api/app/v1/search", `{"query":"alpha","mode":"keyword"}`},
		{"POST", "/api/app/v1/search", `{"query":"alpha","mode":"hybrid"}`},
		{"POST", "/api/app/v1/walks/start", `{}`},
		{"POST", "/api/app/v1/walks/next", `{"name":"memos/a","path":["memos/a"]}`},
	}

	var wg sync.WaitGroup
	errs := make(chan string, len(requests)*2)
	for _, req := range requests {
		wg.Go(func() {
			r := httptest.NewRequest(req.method, req.path, strings.NewReader(req.body))
			r.AddCookie(cookie)
			if req.method != http.MethodGet {
				r.Header.Set("Origin", origin)
				r.Header.Set("X-CSRF-Token", identity.CSRFToken)
				r.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			clientAPI.ServeHTTP(w, r)
			if w.Code != 200 {
				errs <- fmt.Sprintf("%s %s -> %d %s", req.method, req.path, w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatal(msg)
	}
	if calls := upstreamHTTP.Load(); calls != 0 {
		t.Fatalf("local client paths made %d Memos HTTP calls", calls)
	}

	authFail.Store(true)
	writeReq := httptest.NewRequest(http.MethodPost, "/api/app/v1/notes", strings.NewReader(`{"content":"boom"}`))
	writeReq.AddCookie(cookie)
	writeReq.Header.Set("Origin", origin)
	writeReq.Header.Set("X-CSRF-Token", identity.CSRFToken)
	writeReq.Header.Set("Idempotency-Key", "k-auth-401")
	writeReq.Header.Set("Content-Type", "application/json")
	writeResp := httptest.NewRecorder()
	clientAPI.ServeHTTP(writeResp, writeReq)
	after401 := clientCall(clientAPI, "GET", "/api/app/v1/notes", "", cookie, "", "")
	if after401.Code == 200 {
		t.Fatalf("upstream 401 write did not evict active session cache: %d %s", after401.Code, after401.Body.String())
	}

	authFail.Store(false)
	api.app.Sessions.auth.client.Transport = origAuthTransport
	cookie2, identity2 := settingsSession(t, api)
	if _, release, err := api.app.Sessions.BindRequest(t.Context(), identity2); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	var pageCalls atomic.Int32
	midScan401 := fakeClient(store.Settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if r.URL.Path == "/api/v1/memos" && pageCalls.Add(1) == 1 {
			return 200, map[string]any{"memos": []any{rawMemo("a", "alpha")}, "nextPageToken": "p2"}, nil
		}
		if r.URL.Path == "/api/v1/memos" {
			return 401, map[string]string{"message": "revoked mid scan"}, nil
		}
		return 404, nil, nil
	})
	syncSvc := NewSyncService(store, midScan401)
	syncSvc.onAuthFailure = api.app.Sessions.EvictAllAndCancel
	if _, err := syncSvc.Sync(t.Context(), Job{ID: "mid401", Mode: "full"}); errorCode(err) != "UPSTREAM_AUTH" {
		t.Fatalf("expected UPSTREAM_AUTH mid scan, got %v", err)
	}
	if _, release, err := api.app.Sessions.BindRequest(t.Context(), identity2); errorCode(err) != "SESSION_UNAUTHORIZED" {
		if release != nil {
			release()
		}
		t.Fatalf("stale identity bound after EvictAllAndCancel: %v", err)
	}
	_ = cookie2
}

func TestPhase1WriteThroughReceiptsAndScanSafetyGates(t *testing.T) {
	settings, store := settingsFixture(t)
	created := map[string]map[string]any{}
	attachMeta := map[string]map[string]any{}
	var postCount atomic.Int32
	source := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/memos":
			postCount.Add(1)
			id := r.URL.Query().Get("memoId")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			raw := rawMemo(id, body["content"].(string))
			raw["createTime"] = "2026-01-10T00:00:00Z"
			raw["updateTime"] = "2026-01-10T00:00:00Z"
			created["memos/"+id] = raw
			return 200, raw, nil
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/attachments":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			attID := "attachments/" + r.URL.Query().Get("attachmentId")
			att := map[string]any{
				"name": attID, "filename": body["filename"], "type": body["type"], "memo": body["memo"], "size": "5",
			}
			attachMeta[attID] = att
			memoName, _ := body["memo"].(string)
			m := created[memoName]
			atts, _ := m["attachments"].([]map[string]any)
			m["attachments"] = append(atts, att)
			return 200, att, nil
		case strings.HasPrefix(r.URL.Path, "/api/v1/attachments/"):
			key := strings.TrimPrefix(r.URL.Path, "/api/v1/")
			if a := attachMeta[key]; a != nil {
				return 200, a, nil
			}
			return 404, nil, nil
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/memos/"):
			key := strings.TrimPrefix(r.URL.Path, "/api/v1/")
			if m := created[key]; m != nil {
				return 200, m, nil
			}
			return 404, nil, nil
		}
		return 404, nil, nil
	})
	reviews := NewReviewService(store, source)
	if _, err := reviews.DailyLocal(t.Context(), time.Now()); errorCode(err) != "INDEX_NOT_READY" {
		t.Fatalf("expected first-sync completeness gate on daily review, got %v", err)
	}
	records := NewRecordService(store, source)
	payload := []byte("hello")
	note, status, err := records.Create(t.Context(), "receipt-key-1", CreateNote{
		Content:     "with attachment",
		Attachments: []CreateAttachment{{Filename: "a.bin", Type: "application/octet-stream", Content: payload}},
	})
	if err != nil || status != 201 || len(note.AttachmentReceipts) != 1 {
		t.Fatalf("create with receipt failed: status=%d note=%+v err=%v", status, note, err)
	}
	wantDigest := hashBytes(canonicalJSON([]any{"receipt-key-1", 0, note.AttachmentReceipts[0].Name, hashBytes(payload)}))
	if note.AttachmentReceipts[0].ReceiptDigest != wantDigest || note.AttachmentReceipts[0].ContentSHA256 != hashBytes(payload) {
		t.Fatalf("unexpected receipt digest: %+v want %s", note.AttachmentReceipts[0], wantDigest)
	}
	replay, replayStatus, err := records.Create(t.Context(), "receipt-key-1", CreateNote{
		Content:     "with attachment",
		Attachments: []CreateAttachment{{Filename: "a.bin", Type: "application/octet-stream", Content: payload}},
	})
	if err != nil || replayStatus != 200 || !reflect.DeepEqual(replay.AttachmentReceipts, note.AttachmentReceipts) || postCount.Load() != 1 {
		t.Fatalf("replay receipt mismatch: status=%d note=%+v err=%v", replayStatus, replay, err)
	}

	staleScan, _ := normalizeMemo(rawMemo(strings.TrimPrefix(note.Name, "memos/"), "older scan text"))
	staleScan.CreateTime = "2026-01-10T00:00:00Z"
	staleScan.UpdateTime = "2026-01-09T00:00:00Z"
	if err := store.Stage("stale-run", []Memo{staleScan}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishScan("stale-run", map[string]bool{}, 0); err != nil {
		t.Fatal(err)
	}
	localAfterScan, err := store.GetMemoLocal(t.Context(), note.Name)
	if err != nil || localAfterScan == nil || localAfterScan.Content != "with attachment" {
		t.Fatalf("older scan overwrote newer write-through memo: %+v %v", localAfterScan, err)
	}

	existing, _ := normalizeMemo(rawMemo("keep", "kept before failed archive page"))
	if err := store.Refresh("memos/keep", &existing); err != nil {
		t.Fatal(err)
	}
	if err := store.Advance("2026-01-01T00:00:00Z", true); err != nil {
		t.Fatal(err)
	}
	failArchiveSource := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if r.URL.Path == "/api/v1/memos" {
			if strings.Contains(r.URL.RawQuery, "ARCHIVED") {
				return 503, map[string]string{"message": "archive page failed"}, nil
			}
			return 200, map[string]any{"memos": []any{rawMemo("new-normal", "should not publish without archived")}}, nil
		}
		return 404, nil, nil
	})
	syncSvc := NewSyncService(store, failArchiveSource)
	if _, err := syncSvc.Sync(t.Context(), Job{ID: "job-arch-fail", Kind: "full_sync", Mode: "full"}); err == nil {
		t.Fatal("expected full sync to fail when archived page failed")
	}
	st, _ := store.Status()
	if st.Watermark == nil || *st.Watermark != "2026-01-01T00:00:00Z" {
		t.Fatalf("failed archived scan advanced watermark: %+v", st.Watermark)
	}
	if got, _ := store.GetMemoLocal(t.Context(), "memos/new-normal"); got != nil {
		t.Fatal("partial scan published without archived completeness")
	}
}

func TestPhase1SyncNotesConvergenceAndResetTriggers(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, _ := settingsSession(t, api)
	capResp := clientCall(api, "GET", "/api/app/v1/capabilities", "", nil, "", "")
	var caps struct {
		Sync struct {
			Notes bool `json:"notes"`
		} `json:"sync"`
	}
	if capResp.Code != 200 || json.Unmarshal(capResp.Body.Bytes(), &caps) != nil || !caps.Sync.Notes {
		t.Fatalf("missing sync capability: %d %s", capResp.Code, capResp.Body.String())
	}

	store := api.app.Store
	expected := map[string]string{}
	for i := range 120 {
		id := fmt.Sprintf("n-%03d", i)
		raw := rawMemo(id, fmt.Sprintf("body %03d", i))
		raw["createTime"] = time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC).Format(time.RFC3339)
		refreshIndexMemo(t, store, raw)
		expected["memos/"+id] = raw["content"].(string)
	}

	fetch := func(cursor string, limit int) SyncNotesResponse {
		url := fmt.Sprintf("/api/app/v1/sync/notes?limit=%d", limit)
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		resp := clientCall(api, "GET", url, "", cookie, "", "")
		if resp.Code != 200 {
			t.Fatalf("sync/notes %s: %d %s", url, resp.Code, resp.Body.String())
		}
		var out SyncNotesResponse
		if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	replica := map[string]string{}
	apply := func(res SyncNotesResponse) {
		if res.Mode == "reset" || (res.Mode == "full" && len(replica) == 0) {
			if res.Mode == "reset" {
				clear(replica)
			}
		}
		for _, item := range res.Items {
			replica[item.Name] = item.Content
		}
		for _, del := range res.Deleted {
			delete(replica, del)
		}
	}

	page1 := fetch("", 50)
	if page1.Mode != "full" || !page1.HasMore || len(page1.Items) != 50 {
		t.Fatalf("unexpected page1: mode=%s more=%v len=%d", page1.Mode, page1.HasMore, len(page1.Items))
	}
	apply(page1)

	mutated := rawMemo("n-005", "edited during full sync")
	mutated["createTime"] = time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC).Format(time.RFC3339)
	refreshIndexMemo(t, store, mutated)
	expected["memos/n-005"] = "edited during full sync"
	if err := store.Refresh("memos/n-110", nil); err != nil {
		t.Fatal(err)
	}
	delete(expected, "memos/n-110")
	added := rawMemo("n-120", "created during full sync")
	added["createTime"] = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	refreshIndexMemo(t, store, added)
	expected["memos/n-120"] = "created during full sync"

	cursor := page1.NextCursor
	for range 5 {
		page := fetch(cursor, 50)
		if page.Mode == "reset" {
			t.Fatal("mid-sync delta unexpectedly triggered reset")
		}
		apply(page)
		cursor = page.NextCursor
		if !page.HasMore {
			break
		}
	}
	delta := fetch(cursor, 50)
	if delta.Mode != "delta" {
		t.Fatalf("expected delta follow-up, got %s", delta.Mode)
	}
	apply(delta)
	cursor = delta.NextCursor

	for i := range 3 {
		m := rawMemo(fmt.Sprintf("d-%d", i), fmt.Sprintf("delta %d", i))
		refreshIndexMemo(t, store, m)
		expected[fmt.Sprintf("memos/d-%d", i)] = m["content"].(string)
	}
	d1 := fetch(cursor, 2)
	if d1.Mode != "delta" || !d1.HasMore || len(d1.Items) != 2 {
		t.Fatalf("expected paginated delta p1: %+v", d1)
	}
	_ = fetch(cursor, 2)
	apply(d1)
	refreshIndexMemo(t, store, rawMemo("d-3", "mid-delta create"))
	expected["memos/d-3"] = "mid-delta create"
	d2 := fetch(d1.NextCursor, 2)
	apply(d2)
	d3 := fetch(d2.NextCursor, 2)
	apply(d3)
	if !reflect.DeepEqual(replica, expected) {
		t.Fatalf("replica diverged from authoritative index: got=%d want=%d", len(replica), len(expected))
	}

	if _, err := store.index.Exec(`UPDATE projection_meta SET min_change_seq = ?`, store.MaxChangeSeqNoErr()+1); err != nil {
		t.Fatal(err)
	}
	expired := fetch(d3.NextCursor, 50)
	if expired.Mode != "reset" {
		t.Fatalf("expected reset on expired delta min_change_seq, got %s", expired.Mode)
	}
	if _, err := store.index.Exec(`UPDATE projection_meta SET min_change_seq = 1`); err != nil {
		t.Fatal(err)
	}
	fullWithMinSeq := fetch("", 50)
	fullP2 := fetch(fullWithMinSeq.NextCursor, 50)
	if fullP2.Mode != "full" {
		t.Fatalf("multi-page full sync loop on non-zero min_change_seq: %s", fullP2.Mode)
	}

	_ = fixture
	if err := os.Remove(filepath.Join(store.Settings.IndexDir, "index.sqlite3")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := store.index.Close(); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := NewStore(store.Settings)
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.Close()
	api.app.Store = rebuilt
	api.app.Notes.Store = rebuilt
	afterRebuild := fetch(d3.NextCursor, 50)
	if afterRebuild.Mode != "reset" || afterRebuild.SyncGeneration == d3.SyncGeneration {
		t.Fatalf("expected reset after rebuild-index: %+v", afterRebuild)
	}
}

func (s *Store) MaxChangeSeqNoErr() int64 {
	seq, _ := s.MaxChangeSeq()
	return seq
}

func seedV020StateDB(settings Settings) error {
	if err := os.MkdirAll(settings.StateDir, 0700); err != nil {
		return err
	}
	db, err := openDB(filepath.Join(settings.StateDir, "state.sqlite3"), false)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;
		CREATE TABLE IF NOT EXISTS schema_migrations
		(version TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	for _, version := range []string{"001", "002", "003", "004", "005", "006", "007"} {
		body, err := migrationSQL("state", version)
		if err != nil {
			return err
		}
		if err := transaction(db, func(tx *sql.Tx) error {
			if _, err := tx.Exec(string(body)); err != nil {
				return err
			}
			_, err := tx.Exec("INSERT INTO schema_migrations VALUES (?, ?, ?)", version, hashBytes(body), nowText())
			return err
		}); err != nil {
			return err
		}
	}
	_, err = db.Exec("INSERT OR IGNORE INTO bindings VALUES (?, ?, ?, ?)",
		settings.Memos.InstanceID, settings.Memos.BaseURL, settings.Memos.User, nowText())
	return err
}

func runPhase1AutoBackupAndRollback(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	settings := testSettings(t)
	settings.StateDir = filepath.Join(root, "state")
	settings.IndexDir = filepath.Join(root, "index")
	if err := os.MkdirAll(settings.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config.json")
	cfgBytes, _ := json.Marshal(settings)
	if err := os.WriteFile(cfgPath, cfgBytes, 0600); err != nil {
		t.Fatal(err)
	}

	if err := seedV020StateDB(settings); err != nil {
		t.Fatal(err)
	}

	preUpgradeDB, err := openDB(filepath.Join(settings.StateDir, "state.sqlite3"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preUpgradeDB.Exec(`INSERT INTO notifications
		(id, instance_id, user_name, channel_key, logical_key, kind, local_date, status, platform_uuid, available_at, created_at, updated_at)
		VALUES ('n-pending', ?, ?, 'tg:1', 'lk-1', 'review', '2026-10-30', 'pending', 'pu-1', 0, '2026-10-30T00:00:00Z', '2026-10-30T00:00:00Z')`,
		settings.Memos.InstanceID, settings.Memos.User); err != nil {
		t.Fatal(err)
	}
	if err := preUpgradeDB.Close(); err != nil {
		t.Fatal(err)
	}

	originalBackup := filepath.Join(t.TempDir(), "pre-v020.sqlite3")
	for i := range 7 {
		stateDB, err := openDB(filepath.Join(settings.StateDir, "state.sqlite3"), false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stateDB.Exec(`DELETE FROM schema_migrations WHERE version = '008'`); err != nil {
			t.Fatal(err)
		}
		if err := stateDB.Close(); err != nil {
			t.Fatal(err)
		}
		upgraded, err := NewStore(settings)
		if err != nil {
			t.Fatalf("upgrade run %d failed: %v", i, err)
		}
		if err := upgraded.Close(); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			backups, err := listAutoBackups(autoBackupDir(settings))
			if err != nil || len(backups) != 1 {
				t.Fatalf("initial backup: %v", err)
			}
			body, err := os.ReadFile(backups[0].path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(originalBackup, body, 0600); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	entries, err := listAutoBackups(autoBackupDir(settings))
	if err != nil || len(entries) != maxAutoMigrationBackups {
		t.Fatalf("expected max %d auto backups retained, got %d (%v)", maxAutoMigrationBackups, len(entries), err)
	}

	res, err := Rollback(settings, originalBackup)
	if err != nil || !res.CrossVersion || res.DowngradeVersion != "0.2.0" || res.MaxSnapshotMigration != "007" {
		t.Fatalf("rollback result unexpected: %+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(settings.IndexDir, "index.sqlite3")); !os.IsNotExist(err) {
		t.Fatal("rollback did not clear index.sqlite3")
	}
	postRollbackDB, err := openDB(filepath.Join(settings.StateDir, "state.sqlite3"), true)
	if err != nil {
		t.Fatal(err)
	}
	var notifStatus, notifErr string
	var restoreCount int
	if err := postRollbackDB.QueryRow(`SELECT status, COALESCE(last_error, '') FROM notifications WHERE id = 'n-pending'`).Scan(&notifStatus, &notifErr); err != nil {
		t.Fatal(err)
	}
	if err := postRollbackDB.QueryRow(`SELECT COUNT(*) FROM bot_restore WHERE instance_id = ?`, settings.Memos.InstanceID).Scan(&restoreCount); err != nil {
		t.Fatal(err)
	}
	applied, err := appliedMigrations(postRollbackDB)
	_ = postRollbackDB.Close()
	if err != nil {
		t.Fatal(err)
	}
	if notifStatus != "unknown" || notifErr != "STATE_RESTORED" || restoreCount != 1 {
		t.Fatalf("rollback did not reset notification/bot restore state: status=%s err=%s restores=%d", notifStatus, notifErr, restoreCount)
	}
	if applied["007"] == "" || applied["008"] != "" {
		t.Fatalf("unexpected migrations after rollback: %+v", applied)
	}
	if err := checkDatabase(filepath.Join(settings.StateDir, "state.sqlite3")); err != nil {
		t.Fatalf("state integrity check after rollback failed: %v", err)
	}

	reupgraded, err := NewStore(settings)
	if err != nil {
		t.Fatalf("upgrade after rollback failed: %v", err)
	}
	defer reupgraded.Close()
	if err := checkDatabase(filepath.Join(settings.StateDir, "state.sqlite3")); err != nil {
		t.Fatalf("reupgraded state integrity: %v", err)
	}
}

func TestAutoBackupRotationAndRollback(t *testing.T) {
	runPhase1AutoBackupAndRollback(t)
}

func TestPhase1Index004TagBackfillAndKeywordPaginationPast300(t *testing.T) {
	settings := testSettings(t)
	indexPath := filepath.Join(settings.IndexDir, "index.sqlite3")
	if err := os.MkdirAll(settings.IndexDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacyDB, err := openDB(indexPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.Exec(`PRAGMA journal_mode = WAL;
		CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"001", "002", "003"} {
		sqlBytes, readErr := migrationSQL("index", v)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, execErr := legacyDB.Exec(string(sqlBytes)); execErr != nil {
			t.Fatal(execErr)
		}
		if _, execErr := legacyDB.Exec(`INSERT INTO schema_migrations(version, checksum, applied_at) VALUES (?, ?, ?)`, v, hashBytes(sqlBytes), nowText()); execErr != nil {
			t.Fatal(execErr)
		}
	}
	legacyMemo := rawMemo("legacy-tags", "body with tags")
	legacyMemo["tags"] = []string{"work", "中文", "123"}
	payload, _ := json.Marshal(map[string]any{
		"name":        legacyMemo["name"],
		"creator":     legacyMemo["creator"],
		"content":     legacyMemo["content"],
		"state":       legacyMemo["state"],
		"visibility":  legacyMemo["visibility"],
		"create_time": legacyMemo["createTime"],
		"update_time": legacyMemo["updateTime"],
		"tags":        legacyMemo["tags"],
	})
	if _, err := legacyDB.Exec(`INSERT INTO memos(instance_id, user_name, name, state, update_time, source_hash, content_hash, payload_json, observed_at)
		VALUES (?, ?, ?, 'NORMAL', '2026-01-01T00:00:00Z', 'sh-legacy', 'ch-legacy', ?, '2026-01-01T00:00:00Z')`,
		settings.Memos.InstanceID, settings.Memos.User, "memos/legacy-tags", string(payload)); err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(settings)
	if err != nil {
		t.Fatalf("upgrading non-empty pre-004 index failed: %v", err)
	}
	defer func() { _ = store.Close() }()
	var backfilled []string
	rows, err := store.indexReader().Query(`SELECT tag FROM memo_tags WHERE memo_name = 'memos/legacy-tags' ORDER BY tag`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatal(err)
		}
		backfilled = append(backfilled, tag)
	}
	_ = rows.Close()
	if !reflect.DeepEqual(backfilled, []string{"123", "work", "中文"}) {
		t.Fatalf("unexpected memo_tags backfill: %v", backfilled)
	}

	const total = 305
	memos := make([]Memo, 0, total)
	for i := range total {
		text := fmt.Sprintf("kwneedle note %03d", i)
		if i == 0 {
			text += "\n\n" + strings.Repeat("extra kwneedle paragraph\n\n", 8)
		}
		m := testMemo(t, fmt.Sprintf("kw-%03d", i), text)
		memos = append(memos, m)
	}
	if err := store.Stage("r-305", memos); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishScan("r-305", nil, 0); err != nil {
		t.Fatal(err)
	}
	search := NewSearchService(store, fakeClient(settings, nil), nil)
	notes := NewNoteService(settings, store, search.Source)
	var seen []string
	var firstToken string
	req := ClientSearchRequest{Query: "kwneedle", Mode: "keyword", State: "NORMAL", PageSize: 100}
	for {
		page, err := search.ClientPage(t.Context(), notes, req)
		if err != nil {
			t.Fatalf("keyword page err: %v", err)
		}
		for _, n := range page.Notes {
			seen = append(seen, n.Name)
		}
		if page.NextPageToken == "" {
			break
		}
		if firstToken == "" {
			firstToken = page.NextPageToken
		}
		req.PageToken = page.NextPageToken
	}
	if len(seen) != total {
		t.Fatalf("keyword search pagination truncated: got %d want %d", len(seen), total)
	}
	var legacyCursor clientSearchCursor
	if err := notes.readCursorLimit(firstToken, &legacyCursor, clientSearchTokenBytes); err != nil {
		t.Fatal(err)
	}
	legacyCursor.Offset = 0
	legacyCursor.Sources = []clientSearchSource{{Name: "memos/kw-000", Hash: "legacy-hash"}}
	legacyReq := ClientSearchRequest{Query: "kwneedle", Mode: "keyword", State: "NORMAL", PageSize: 100, PageToken: notes.signCursor(legacyCursor)}
	if _, err := search.ClientPage(t.Context(), notes, legacyReq); errorCode(err) != "INVALID_PAGE_TOKEN" {
		t.Fatalf("legacy keyword cursor with Sources must be rejected, got: %v", err)
	}
}

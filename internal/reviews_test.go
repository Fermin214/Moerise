package moerise

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func reviewFixture(t *testing.T, count int) (*ReviewService, map[string]map[string]any) {
	t.Helper()
	settings := testSettings(t)
	store := testStore(t, settings)
	memos := map[string]map[string]any{}
	for i := range count {
		id := fmt.Sprintf("review-%02d", i)
		raw := rawMemo(id, "review content "+id)
		raw["tags"] = []string{fmt.Sprintf("topic-%d/child", i%3)}
		memos["memos/"+id] = raw
		memo, err := normalizeMemo(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Refresh(memo.Name, &memo); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.state.Exec("UPDATE sync_state SET last_full = ?", nowText()); err != nil {
		t.Fatal(err)
	}
	client := fakeClient(settings, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		if raw := memos[strings.TrimPrefix(request.URL.Path, "/api/v1/")]; raw != nil {
			return 200, raw, nil
		}
		return 404, nil, nil
	})
	return NewReviewService(store, client), memos
}

func TestReviewBatchesHistoryAndRecovery(t *testing.T) {
	service, memos := reviewFixture(t, 9)
	ctx := context.Background()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	first, err := service.Daily(ctx, at)
	if err != nil || first.Sequence != 1 || len(first.Items) != 3 {
		t.Fatalf("first batch: %+v %v", first, err)
	}
	seen := map[string]bool{}
	check := func(batch ReviewBatch) {
		t.Helper()
		for _, item := range batch.Items {
			if seen[item.Name] || item.Note.Creator != service.Store.Settings.Memos.User {
				t.Fatal("review repeated a recent memo or crossed the author boundary")
			}
			seen[item.Name] = true
		}
	}
	check(first)
	again, err := service.Daily(ctx, at.Add(time.Hour))
	if err != nil || again.ID != first.ID {
		t.Fatal("repeated daily read created another batch")
	}
	name := first.Items[0].Name
	opened, err := service.Mark(ctx, first.ID, name, "open", at)
	if err != nil || opened.Items[0].OpenedAt == nil || opened.Items[0].SkippedAt != nil {
		t.Fatalf("open: %+v %v", opened, err)
	}
	marked, err := service.Mark(ctx, first.ID, name, "skip", at.Add(time.Minute))
	if err != nil || marked.Items[0].OpenedAt == nil || marked.Items[0].SkippedAt == nil {
		t.Fatal("open and skip must be retained separately")
	}
	retry, err := service.Mark(ctx, first.ID, name, "open", at.Add(time.Hour))
	if err != nil || *retry.Items[0].OpenedAt != *opened.Items[0].OpenedAt {
		t.Fatal("repeated open changed its first timestamp")
	}
	if _, err := service.Replace(ctx, "replace-1", at); errorCode(err) != "REVIEW_REPLACEMENT_DISABLED" {
		t.Fatalf("fixed plans accepted replacement: %v", err)
	}
	second, err := service.Daily(ctx, at.AddDate(0, 0, 1))
	if err != nil || second.Sequence != 1 || len(second.Items) != 3 {
		t.Fatalf("second batch: %+v %v", second, err)
	}
	check(second)
	same, err := service.Daily(ctx, at.AddDate(0, 0, 1).Add(time.Minute))
	if err != nil || same.ID != second.ID {
		t.Fatal("replacement replay advanced the batch")
	}
	third, err := service.Daily(ctx, at.AddDate(0, 0, 2))
	if err != nil || third.Sequence != 1 || len(third.Items) != 3 {
		t.Fatalf("third batch: %+v %v", third, err)
	}
	check(third)
	emptyAt := at.AddDate(0, 0, 3)
	empty, err := service.Daily(ctx, emptyAt)
	if err != nil || len(empty.Items) != 0 {
		t.Fatal("exhausted candidates must produce a stable empty batch")
	}
	memos[name]["content"] = "edited at source"
	fresh, err := service.Get(ctx, first.ID)
	if err != nil || fresh.Items[0].Note.Content != "edited at source" {
		t.Fatal("review returned cached source content")
	}
	delete(memos, name)
	deleted, err := service.Get(ctx, first.ID)
	if err != nil || !deleted.Items[0].Unavailable || deleted.Items[0].Note != nil {
		t.Fatal("deleted source was returned or silently replaced")
	}
	settings := service.Store.Settings
	snapshot := filepath.Join(filepath.Dir(settings.StateDir), "review-snapshot")
	if _, err := Backup(settings, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := service.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildIndex(settings); err != nil {
		t.Fatal(err)
	}
	reopened := testStore(t, settings)
	recovered := NewReviewService(reopened, service.Source)
	persisted, err := recovered.Daily(ctx, emptyAt)
	if err != nil || persisted.ID != empty.ID {
		t.Fatalf("index rebuild changed the daily batch: %+v %v", persisted, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(settings, snapshot, filepath.Join(filepath.Dir(settings.StateDir), "before-review-restore")); err != nil {
		t.Fatal(err)
	}
	restored := NewReviewService(testStore(t, settings), service.Source)
	batch, err := restored.Get(ctx, first.ID)
	if err != nil || batch.Items[0].OpenedAt == nil || batch.Items[0].SkippedAt == nil {
		t.Fatalf("backup did not preserve review states: %+v %v", batch, err)
	}
}

func TestReviewRulesDatesAndSourceBoundary(t *testing.T) {
	service, memos := reviewFixture(t, 5)
	ctx := context.Background()
	at := time.Date(2026, 10, 26, 15, 59, 0, 0, time.UTC)
	memos["memos/review-00"]["createTime"] = at.Add(-time.Hour).Format(time.RFC3339)
	memos["memos/review-01"]["state"] = "ARCHIVED"
	memos["memos/review-02"]["creator"] = "users/other"
	batch, err := service.Daily(ctx, at)
	if err != nil || batch.Date != "2026-10-26" {
		t.Fatalf("timezone date: %+v %v", batch, err)
	}
	for _, item := range batch.Items {
		if slices.Contains([]string{"memos/review-00", "memos/review-01", "memos/review-02"}, item.Name) {
			t.Fatal("new, archived or foreign source entered review")
		}
	}
	next, err := service.Daily(ctx, at.Add(2*time.Minute))
	if err != nil || next.Date != "2026-10-27" || next.ID == batch.ID || len(next.Items) != 0 {
		t.Fatalf("local midnight or recent exclusion: %+v %v", next, err)
	}
	later, err := service.Daily(ctx, at.AddDate(0, 0, 31))
	if err != nil || len(later.Items) == 0 {
		t.Fatal("old selections did not become eligible again")
	}
	service.Store.Settings.Review.IncludeArchived = true
	changed, err := service.Daily(ctx, at)
	if err != nil || changed.ID != batch.ID {
		t.Fatal("changing rules replaced a fixed daily batch")
	}
	client := service.Source.(*MemosClient)
	client.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("offline")
	})
	failed, err := service.Get(ctx, batch.ID)
	if err == nil || len(failed.Items) != 0 {
		t.Fatal("source outage exposed review content")
	}
}

func TestRandomReviewHasNoTopicQuotaAndRespectsAge(t *testing.T) {
	service, memos := reviewFixture(t, 5)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for name, raw := range memos {
		raw["tags"] = []string{"shared/topic"}
		raw["createTime"] = at.Add(-72 * time.Hour).Format(time.RFC3339)
		memo, _ := normalizeMemo(raw)
		if err := service.Store.Refresh(name, &memo); err != nil {
			t.Fatal(err)
		}
	}
	memos["memos/review-00"]["createTime"] = at.Add(-72*time.Hour + time.Second).Format(time.RFC3339)
	memo, _ := normalizeMemo(memos["memos/review-00"])
	if err := service.Store.Refresh(memo.Name, &memo); err != nil {
		t.Fatal(err)
	}
	service.Store.Settings.Review.Count = 10
	batch, err := service.Daily(context.Background(), at)
	if err != nil || len(batch.Items) != 4 {
		t.Fatalf("age or hidden quota reduced the candidate pool: %+v %v", batch, err)
	}
	for _, item := range batch.Items {
		if item.Name == "memos/review-00" {
			t.Fatal("a note younger than 72 hours was selected")
		}
	}
}

func TestConcurrentDailyReadsShareOneBatch(t *testing.T) {
	service, _ := reviewFixture(t, 9)
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			entry := NewReviewService(service.Store, service.Source)
			batch, err := entry.Daily(context.Background(), time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
			if err != nil {
				t.Error(err)
				return
			}
			results <- batch.ID
		})
	}
	wg.Wait()
	close(results)
	id := ""
	for actual := range results {
		if id != "" && id != actual {
			t.Fatal("concurrent reads created different batches")
		}
		id = actual
	}
}

func TestReviewLatestBatchAcrossTimestampPrecisionAndRules(t *testing.T) {
	service, _ := reviewFixture(t, 15)
	ctx := context.Background()
	at := time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
	legacy := seedLegacyReview(t, service, at, "legacy-replace", "legacy-rules", 40)
	replacements := []ReviewBatch{}
	for i, offset := range []time.Duration{500 * time.Millisecond, 510 * time.Millisecond, 510 * time.Millisecond} {
		key := fmt.Sprintf("same-second-%d", i)
		batch := seedLegacyReview(t, service, at.Add(offset), key, "random-v1", i+1)
		replacements = append(replacements, batch)
		for _, read := range []struct {
			name string
			call func(context.Context, time.Time) (ReviewBatch, error)
		}{{"ReadDaily", service.ReadDaily}} {
			latest, err := read.call(ctx, at.Add(time.Second))
			if err != nil || latest.ID != batch.ID {
				t.Fatalf("%s did not return the latest replacement: %+v %v", read.name, latest, err)
			}
		}
	}
	for _, replay := range []struct {
		key string
		id  string
	}{{"legacy-replace", legacy.ID}, {"same-second-0", replacements[0].ID}} {
		batch, err := service.Replace(ctx, replay.key, at.Add(time.Minute))
		if err != nil || batch.ID != replay.id {
			t.Fatalf("replacement replay changed across rules or newer batches: %+v %v", batch, err)
		}
	}
	latest, err := service.Daily(ctx, at)
	if err != nil || latest.ID != replacements[len(replacements)-1].ID {
		t.Fatalf("replaying an earlier replacement changed the daily result: %+v %v", latest, err)
	}
	var count int
	if err := service.Store.state.QueryRow("SELECT COUNT(*) FROM review_batches").Scan(&count); err != nil || count != 4 {
		t.Fatalf("daily reads or replacement replays created batches: %d %v", count, err)
	}
}

func seedLegacyReview(t *testing.T, service *ReviewService, at time.Time, key, rule string, sequence int) ReviewBatch {
	t.Helper()
	location, _ := time.LoadLocation(service.Store.Settings.Timezone)
	id := newID()
	_, err := service.Store.state.Exec(`INSERT INTO review_batches
		(id, instance_id, user_name, local_date, timezone, rule_version, sequence, request_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, service.Store.Settings.Memos.InstanceID,
		service.Store.Settings.Memos.User, at.In(location).Format(time.DateOnly),
		service.Store.Settings.Timezone, rule, sequence, key, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := service.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

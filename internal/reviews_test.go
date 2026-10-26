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
		topics := map[string]bool{}
		for _, item := range batch.Items {
			topic := strings.Split(item.Note.Tags[0], "/")[0]
			if seen[item.Name] || topics[topic] || item.Note.Creator != service.Store.Settings.Memos.User {
				t.Fatal("review repeated a recent memo or exceeded a topic limit")
			}
			seen[item.Name], topics[topic] = true, true
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
	second, err := service.Replace(ctx, "replace-1", at)
	if err != nil || second.Sequence != 2 || len(second.Items) != 3 {
		t.Fatalf("second batch: %+v %v", second, err)
	}
	check(second)
	same, err := service.Replace(ctx, "replace-1", at.Add(time.Minute))
	if err != nil || same.ID != second.ID {
		t.Fatal("replacement replay advanced the batch")
	}
	third, err := service.Replace(ctx, "replace-2", at)
	if err != nil || third.Sequence != 3 || len(third.Items) != 3 {
		t.Fatalf("third batch: %+v %v", third, err)
	}
	check(third)
	empty, err := service.Replace(ctx, "replace-3", at)
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
	persisted, err := recovered.Daily(ctx, at)
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
	if err != nil || changed.RuleVersion == batch.RuleVersion {
		t.Fatal("changed rules reused another rule's batch")
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

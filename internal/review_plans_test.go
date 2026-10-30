package moerise

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// Slot boundaries and interrupted scheduling need a controlled clock and source.
func TestReviewPlanDisjointSlotsFreezeAndRecentExclusion(t *testing.T) {
	reviews, _ := reviewFixture(t, 8)
	store := reviews.Store
	store.Settings.Notifications.Times = []string{"09:00", "18:00"}
	store.Settings.Notifications.Enabled = true
	store.productVersion = 7
	service := NewNotificationService(store, reviews, &notificationPlatform{})
	ctx := t.Context()
	at := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	batch, err := reviews.Daily(ctx, at)
	if err != nil || len(batch.Items) != 6 || batch.SettingsVersion != 7 {
		t.Fatalf("whole-day plan: %+v %v", batch, err)
	}
	store.Settings.Review.Count = 1
	store.Settings.Notifications.Times = []string{"08:00", "12:00", "23:00"}
	store.productVersion = 8
	again, err := reviews.Daily(ctx, at.Add(time.Hour))
	if err != nil || again.ID != batch.ID || len(again.Items) != 6 || again.SettingsVersion != 7 {
		t.Fatalf("settings changed a frozen plan: %+v %v", again, err)
	}
	for _, clock := range []time.Duration{time.Hour, 10 * time.Hour} {
		if count, err := service.Schedule(ctx, at.Add(clock)); err != nil || count != 1 {
			t.Fatalf("slot scheduling: %d %v", count, err)
		}
	}
	notifications, err := service.List("")
	if err != nil || len(notifications) != 2 {
		t.Fatalf("queue: %+v %v", notifications, err)
	}
	names := map[string]bool{}
	for _, notification := range notifications {
		if notification.SlotID == nil {
			t.Fatal("notification has no allocation reference")
		}
		selected, err := reviews.forNotification(ctx, notification)
		if err != nil || len(selected.Items) != 3 {
			t.Fatalf("slot selection: %+v %v", selected, err)
		}
		for _, item := range selected.Items {
			if names[item.Name] {
				t.Fatal("two slots share a note")
			}
			names[item.Name] = true
		}
	}
	next, err := reviews.Daily(ctx, at.AddDate(0, 0, 1))
	if err != nil || len(next.Items) != 2 {
		t.Fatalf("selection exclusion depends on reading or delivery: %+v %v", next, err)
	}
	for _, item := range next.Items {
		if names[item.Name] {
			t.Fatal("an unread, undelivered selection was repeated")
		}
	}
}

func TestReviewPlanImageEligibilityAndShortageAllocation(t *testing.T) {
	reviews, memos := reviewFixture(t, 4)
	store := reviews.Store
	store.Settings.Notifications.Times = []string{"09:00", "12:00", "18:00"}
	store.Settings.Review.Count = 2
	for index, mime := range []string{"application/pdf", "image/png"} {
		name := "memos/review-0" + string(rune('0'+index))
		memos[name]["content"] = ""
		memos[name]["attachments"] = []any{map[string]any{
			"name": "attachments/review-image", "type": mime, "filename": "fixture",
		}}
		memo, err := normalizeMemo(memos[name])
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Refresh(name, &memo); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := reviews.Daily(t.Context(), time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC))
	if err != nil || len(batch.Items) != 3 {
		t.Fatalf("content eligibility: %+v %v", batch, err)
	}
	if slices.ContainsFunc(batch.Items, func(item ReviewItem) bool { return item.Name == "memos/review-00" }) ||
		!slices.ContainsFunc(batch.Items, func(item ReviewItem) bool { return item.Name == "memos/review-01" }) {
		t.Fatal("file-only or image-only eligibility is wrong")
	}
	slots, err := reviews.slots(batch.ID)
	if err != nil || len(slots) != 3 || slots[0].Count != 2 || slots[1].Count != 1 || slots[2].Count != 0 {
		t.Fatalf("shortage should fill earlier slots first: %+v %v", slots, err)
	}
}

func TestReviewRecoveryOnlyLatestDueAndKeepsOldQueue(t *testing.T) {
	reviews, _ := reviewFixture(t, 12)
	store := reviews.Store
	store.Settings.Notifications.Times = []string{"09:00", "12:00", "18:00"}
	store.Settings.Notifications.Enabled = true
	service := NewNotificationService(store, reviews, &notificationPlatform{})
	ctx := context.Background()
	late := time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
	if count, err := service.Schedule(ctx, late); err != nil || count != 1 {
		t.Fatalf("late recovery: %d %v", count, err)
	}
	items, err := service.List("")
	if err != nil || len(items) != 1 {
		t.Fatalf("late queue: %+v %v", items, err)
	}
	var clock string
	if err := store.state.QueryRow(`SELECT clock FROM review_plan_slots WHERE id = ?`, *items[0].SlotID).Scan(&clock); err != nil || clock != "18:00" {
		t.Fatalf("recovered an earlier slot: %s %v", clock, err)
	}
	if _, err := store.state.Exec(`UPDATE notifications SET status = 'unknown' WHERE id = ?`, items[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{late.Add(time.Hour), late.AddDate(0, 0, 1)} {
		if _, err := service.Schedule(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := service.Detail(items[0].ID)
	if err != nil || detail.Notification.Status != "unknown" || detail.Notification.SlotID == nil {
		t.Fatalf("recovery changed an existing unknown task: %+v %v", detail, err)
	}
}

func TestLegacyQueuedReviewRetainsWholeBatchWithoutNewDuplicate(t *testing.T) {
	reviews, _ := reviewFixture(t, 4)
	store := reviews.Store
	store.Settings.Notifications.Times = []string{"09:00", "18:00"}
	store.Settings.Notifications.Enabled = true
	at := time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC)
	legacy := seedLegacyReview(t, reviews, at, "old-key", "random-v1", 1)
	memo, _ := reviews.Source.Get(t.Context(), "memos/review-00")
	if _, err := store.state.Exec(`INSERT INTO review_items
		(batch_id, position, memo_name, source_hash, selected_at) VALUES (?, 0, ?, ?, ?)`,
		legacy.ID, memo.Name, memo.SourceHash, nowText()); err != nil {
		t.Fatal(err)
	}
	service := NewNotificationService(store, reviews, &notificationPlatform{})
	id, err := service.enqueue("review", "2026-10-28:09:00", "2026-10-28", &legacy.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := service.Schedule(t.Context(), at); err != nil || count != 0 {
		t.Fatalf("legacy batch was repeated: %d %v", count, err)
	}
	detail, _ := service.Detail(id)
	batch, err := reviews.forNotification(t.Context(), detail.Notification)
	if err != nil || batch.ID != legacy.ID || len(batch.Items) != 1 {
		t.Fatalf("legacy queue lost its original items: %+v %v", batch, err)
	}
	if retry, err := reviews.Replace(t.Context(), "old-key", at); err != nil || retry.ID != legacy.ID {
		t.Fatalf("historical idempotency replay: %+v %v", retry, err)
	}
}

func TestReviewSourceFailureDoesNotFreezeEmptyPlan(t *testing.T) {
	reviews, _ := reviewFixture(t, 4)
	client := reviews.Source.(*MemosClient)
	original := client.client.Transport
	client.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})
	at := time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
	if _, err := reviews.Daily(t.Context(), at); err == nil {
		t.Fatal("source failure produced a plan")
	}
	var count int
	if err := reviews.Store.state.QueryRow(`SELECT COUNT(*) FROM review_plans`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("source error persisted an empty plan: %d %v", count, err)
	}
	client.client.Transport = original
	if batch, err := reviews.Daily(t.Context(), at); err != nil || len(batch.Items) != 3 {
		t.Fatalf("source recovery: %+v %v", batch, err)
	}
}

func TestReviewTextOnlySlotsSkipImagesWithoutReplacement(t *testing.T) {
	reviews, memos := reviewFixture(t, 8)
	store := reviews.Store
	store.Settings.Notifications.Enabled = true
	store.Settings.Notifications.Times = []string{"09:00", "18:00"}
	store.Settings.Review.Count = 3
	at := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	batch, err := reviews.Daily(t.Context(), at)
	if err != nil || len(batch.Items) != 6 {
		t.Fatalf("six-item plan: %+v %v", batch, err)
	}
	raw := memos[batch.Items[0].Name]
	raw["content"] = ""
	raw["attachments"] = []any{map[string]any{
		"name": "attachments/image-only", "type": "image/png", "filename": "image.png",
	}}
	memo, err := normalizeMemo(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Refresh(memo.Name, &memo); err != nil {
		t.Fatal(err)
	}
	memos[batch.Items[1].Name]["attachments"] = raw["attachments"]
	var texts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message struct {
			Type    string `json:"msg_type"`
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.NewDecoder(r.Body).Decode(&message) != nil || message.Type != "text" {
			t.Error("text-only channel received an image")
		}
		texts = append(texts, message.Content.Text)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()
	store.Settings.Channel.Kind = "feishu-webhook"
	store.Settings.Channel.WebhookURL = stringPointer(server.URL)
	queue := NewNotificationService(store, reviews, NewFeishuDelivery(store, reviews, nil))
	for ordinal, offset := range []time.Duration{time.Hour, 10 * time.Hour} {
		start := len(texts)
		if count, err := queue.Schedule(t.Context(), at.Add(offset)); err != nil || count != 1 {
			t.Fatalf("text-only slot schedule: %d %v", count, err)
		}
		for range 20 {
			sent, err := queue.Deliver(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !sent {
				break
			}
		}
		sent := texts[start:]
		expected := 7
		if ordinal == 0 {
			expected = 5
		}
		if len(sent) != expected || sent[0] != "「今日回顾」" {
			t.Fatalf("slot %d repeated or substituted an item: %q", ordinal, sent)
		}
		for position := ordinal * 3; position < ordinal*3+3; position++ {
			if position == 0 {
				continue
			}
			content := memos[batch.Items[position].Name]["content"].(string)
			if !slices.Contains(sent, content) {
				t.Fatalf("slot %d did not send its assigned note %s", ordinal, batch.Items[position].Name)
			}
		}
	}
	fresh, err := reviews.ReadDaily(t.Context(), at)
	if err != nil || fresh.ID != batch.ID || len(fresh.Items) != 6 {
		t.Fatalf("delivery changed the Web plan: %+v %v", fresh, err)
	}
	var selections int
	if err := store.state.QueryRow(`SELECT COUNT(*) FROM review_items`).Scan(&selections); err != nil || selections != 6 {
		t.Fatalf("channel capability caused another draw: %d %v", selections, err)
	}
	var missing int
	if err := store.state.QueryRow(`SELECT COUNT(*) FROM notifications WHERE slot_id IS NULL`).Scan(&missing); err != nil || missing != 0 {
		t.Fatalf("delivery parts lost their slot reference: %d %v", missing, err)
	}
}

func TestReviewPlanTimezoneTransitionAndRestart(t *testing.T) {
	for _, scenario := range []struct {
		name, oldZone, newZone, instant string
		sameDate                        bool
	}{
		{"west", "Asia/Hong_Kong", "America/Los_Angeles", "2026-10-28T02:00:00Z", true},
		{"east", "America/Los_Angeles", "Asia/Hong_Kong", "2026-10-28T18:00:00Z", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			reviews, _ := reviewFixture(t, 20)
			store := reviews.Store
			store.Settings.Timezone = scenario.oldZone
			store.Settings.Notifications.Enabled = true
			store.Settings.Notifications.Times = []string{"09:00", "18:00"}
			store.Settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
			if err := store.initializeProductSettings(); err != nil {
				t.Fatal(err)
			}
			at, _ := time.Parse(time.RFC3339, scenario.instant)
			first, err := reviews.Daily(t.Context(), at)
			if err != nil {
				t.Fatal(err)
			}
			queue := NewNotificationService(store, reviews, &notificationPlatform{})
			if count, err := queue.Schedule(t.Context(), at); err != nil || count != 1 {
				t.Fatalf("old-zone first slot: %d %v", count, err)
			}
			old := store.Settings
			zone, _ := time.LoadLocation(old.Timezone)
			day := at.In(zone)
			until := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, zone)
			next := old
			next.Timezone, next.Review.Count = scenario.newZone, 2
			next.Notifications.Times = []string{"10:00", "20:00"}
			next.daily = &dailySettings{old.Timezone, old.Review, old.Notifications, until, store.productVersion}
			if err := store.persistSettings(next); err != nil {
				t.Fatal(err)
			}
			store.Settings = next
			before := until.Add(-time.Minute)
			if count, err := queue.Schedule(t.Context(), before); err != nil || count != 1 {
				t.Fatalf("frozen old-zone last slot: %d %v", count, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := testStore(t, next)
			recovered := NewReviewService(reopened, reviews.Source)
			queue = NewNotificationService(reopened, recovered, &notificationPlatform{})
			after := until.Add(time.Second)
			batch, err := recovered.Daily(t.Context(), after)
			if err != nil {
				t.Fatal(err)
			}
			wantNewSlots := 1
			if scenario.sameDate {
				wantNewSlots = 0
				if batch.ID != first.ID || batch.Timezone != old.Timezone ||
					batch.SettingsVersion != 1 || len(batch.Items) != 6 {
					t.Fatalf("westward date overlap changed a frozen plan: %+v", batch)
				}
			} else if batch.ID == first.ID || batch.Timezone != next.Timezone ||
				batch.SettingsVersion != 2 || len(batch.Items) != 4 {
				t.Fatalf("eastward next date did not use saved rules: %+v", batch)
			}
			read, err := recovered.ReadDaily(t.Context(), after)
			if err != nil || read.ID != batch.ID {
				t.Fatalf("read and create disagree after timezone restart: %+v %v", read, err)
			}
			if count, err := queue.Schedule(t.Context(), after); err != nil || count != wantNewSlots {
				t.Fatalf("timezone recovery scheduled duplicate or missing slots: %d %v", count, err)
			}
			if count, err := queue.Schedule(t.Context(), after.Add(time.Minute)); err != nil || count != 0 {
				t.Fatalf("timezone restart duplicated a slot: %d %v", count, err)
			}
		})
	}
}

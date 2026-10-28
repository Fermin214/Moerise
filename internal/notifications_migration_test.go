package moerise

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func legacyNotificationFixture(t *testing.T, version string) (*NotificationService, ReviewBatch) {
	t.Helper()
	settings := testSettings(t)
	settings.StateDir = t.TempDir()
	settings.Notifications.Enabled = true
	settings.Notifications.Times = []string{"09:00", "12:00"}
	db, err := openDB(filepath.Join(settings.StateDir, "state.sqlite3"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE schema_migrations
		(version TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations/state")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		number := strings.TrimSuffix(entry.Name(), ".sql")
		if number > version {
			continue
		}
		body, err := migrationSQL("state", number)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO schema_migrations VALUES (?, ?, ?)", number, hashBytes(body), nowText()); err != nil {
			t.Fatal(err)
		}
	}
	batch := ReviewBatch{ID: newID(), Date: "2026-10-28", Timezone: settings.Timezone}
	if _, err := db.Exec("INSERT INTO bindings VALUES (?, ?, ?, ?)",
		settings.Memos.InstanceID, settings.Memos.BaseURL, settings.Memos.User, nowText()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO review_batches
		(id, instance_id, user_name, local_date, timezone, rule_version, sequence, created_at)
		VALUES (?, ?, ?, ?, ?, 'legacy-rules', 1, ?)`, batch.ID, settings.Memos.InstanceID,
		settings.Memos.User, batch.Date, batch.Timezone, nowText()); err != nil {
		t.Fatal(err)
	}
	memo := testMemo(t, "legacy-review", "legacy review content")
	if _, err := db.Exec(`INSERT INTO review_items (batch_id, position, memo_name, source_hash, selected_at)
		VALUES (?, 0, ?, ?, ?)`, batch.ID, memo.Name, memo.SourceHash, nowText()); err != nil {
		t.Fatal(err)
	}
	source := fakeClient(settings, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		if request.URL.Path == "/api/v1/"+memo.Name {
			return 200, rawMemo("legacy-review", "legacy review content"), nil
		}
		return 404, nil, nil
	})
	store := &Store{Settings: settings, state: db}
	return NewNotificationService(store, NewReviewService(store, source), &notificationPlatform{}), batch
}

func upgradeNotificationFixture(t *testing.T, service *NotificationService) {
	t.Helper()
	rows, err := service.Store.state.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	invalid := rows.Next()
	err = rows.Err()
	rows.Close()
	if invalid || err != nil {
		t.Fatalf("legacy fixture has invalid references: %v", err)
	}
	if err := service.Store.state.Close(); err != nil {
		t.Fatal(err)
	}
	service.Store = testStore(t, service.Store.Settings)
	service.Reviews = NewReviewService(service.Store, service.Reviews.Source)
}

func TestNotificationMigrationRecoversLegacyEnqueueBeforeScheduleAdvance(t *testing.T) {
	for _, test := range []struct {
		name, version, lastDate, status string
		prepared                        bool
	}{
		{"first-schedule", "008", "", "pending", false},
		{"previous-date", "008", "2026-10-27", "pending", false},
		{"running", "008", "2026-10-27", "running", false},
		{"unknown", "008", "2026-10-27", "unknown", false},
		{"sent", "008", "2026-10-27", "sent", false},
		{"schedule-recorded", "008", "2026-10-28", "sent", false},
		{"prepared-legacy-root", "011", "", "unknown", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, batch := legacyNotificationFixture(t, test.version)
			root, err := service.enqueue("review", batch.Date+":"+batch.ID, batch.Date, &batch.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			platform := service.Transport.(*notificationPlatform)
			switch test.status {
			case "running":
				platform.mode = "crash"
				func() {
					defer func() { _ = recover() }()
					_, _ = service.Deliver(context.Background())
				}()
			case "unknown", "sent":
				if test.status == "unknown" {
					platform.mode = "unknown"
				}
				if progress, err := service.Deliver(context.Background()); err != nil || !progress {
					t.Fatalf("legacy delivery: %v %v", progress, err)
				}
			}
			platform.mode = ""
			var before string
			if err := service.Store.state.QueryRow("SELECT status FROM notifications WHERE id = ?", root).Scan(&before); err != nil || before != test.status {
				t.Fatalf("legacy send state: %s %v", before, err)
			}
			if test.lastDate != "" {
				if _, err := service.Store.state.Exec("INSERT INTO notification_schedules VALUES (?, ?, ?, ?, ?)",
					service.Store.Settings.Memos.InstanceID, service.Store.Settings.Memos.User,
					service.channelKey(), batch.Timezone, test.lastDate); err != nil {
					t.Fatal(err)
				}
			}
			if test.prepared {
				if _, err := service.Store.state.Exec(`INSERT INTO feishu_review_parts
					(notification_id, root_id, position, kind, memo_name, source_version)
					VALUES (?, ?, 0, 'text', 'memos/legacy-review', 'legacy-source')`, root, root); err != nil {
					t.Fatal(err)
				}
			}
			upgradeNotificationFixture(t, service)
			var slots int
			if err := service.Store.state.QueryRow(`SELECT COUNT(*) FROM review_schedule_slots
				WHERE local_date = ? AND clock = ''`, batch.Date).Scan(&slots); err != nil || slots != 1 {
				t.Fatalf("queued legacy root did not consume the first slot: %d %v", slots, err)
			}
			ctx := context.Background()
			at := time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
			if count, err := service.Schedule(ctx, at); err != nil || count != 0 {
				t.Fatalf("upgrade duplicated the legacy review: %d %v", count, err)
			}
			if count, err := service.Schedule(ctx, at.Add(3*time.Hour)); err != nil || count != 1 {
				t.Fatalf("legacy root consumed a later slot: %d %v", count, err)
			}
			if count, err := service.Schedule(ctx, at.Add(4*time.Hour)); err != nil || count != 0 {
				t.Fatalf("rescheduling duplicated a processed slot: %d %v", count, err)
			}
			items, err := service.List("")
			if err != nil || len(items) != 2 {
				t.Fatalf("wrong queue after upgrade: %+v %v", items, err)
			}
			var status string
			if err := service.Store.state.QueryRow("SELECT status FROM notifications WHERE id = ?", root).Scan(&status); err != nil || status != test.status {
				t.Fatalf("migration changed the legacy send outcome: %s %v", status, err)
			}
		})
	}
}

func TestNotificationMigrationDoesNotConsumeSlotForModernRootOrParts(t *testing.T) {
	service, batch := legacyNotificationFixture(t, "011")
	root, err := service.enqueue("review", batch.Date+":12:00", batch.Date, &batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.enqueue("review", "review-part:"+root+":1", batch.Date, &batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.state.Exec(`INSERT INTO feishu_review_parts
		(notification_id, root_id, position, kind, memo_name, source_version) VALUES
		(?, ?, 0, 'text', 'memos/legacy-review', 'legacy-source'),
		(?, ?, 1, 'text', 'memos/legacy-review', 'legacy-source')`, root, root, child, root); err != nil {
		t.Fatal(err)
	}
	upgradeNotificationFixture(t, service)
	var slots int
	if err := service.Store.state.QueryRow("SELECT COUNT(*) FROM review_schedule_slots WHERE clock = ''").Scan(&slots); err != nil || slots != 0 {
		t.Fatalf("modern root or part was mistaken for a legacy root: %d %v", slots, err)
	}
	if count, err := service.Schedule(context.Background(), time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)); err != nil || count != 1 {
		t.Fatalf("an unscheduled first slot was skipped: %d %v", count, err)
	}
}

func TestReviewPartsMigrationPreservesUploadedImageAndContinuation(t *testing.T) {
	service, batch := legacyNotificationFixture(t, "014")
	root, err := service.enqueue("review", batch.Date+":09:00", batch.Date, &batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.enqueue("review", "review-part:"+root+":1", batch.Date, &batch.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.state.Exec("UPDATE notifications SET available_at = 1.0e30 WHERE id = ?", child); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.state.Exec(`INSERT INTO feishu_review_parts
		(notification_id, root_id, position, kind, memo_name, source_version,
		byte_start, byte_end, attachment_name, image_key) VALUES
		(?, ?, 0, 'text', 'memos/legacy-review', 'saved-version', 2, 12, '', NULL),
		(?, ?, 1, 'image', 'memos/legacy-review', 'saved-version', 0, 0,
		'attachments/saved-image', 'saved-image-key')`, root, root, child, root); err != nil {
		t.Fatal(err)
	}
	upgradeNotificationFixture(t, service)
	part, err := (&FeishuDelivery{Store: service.Store}).reviewPart(child)
	if err != nil || part.Kind != "image" || part.Root != root || part.Position != 1 ||
		part.Attachment != "attachments/saved-image" || part.ImageKey != "saved-image-key" ||
		part.Version != "saved-version" {
		t.Fatalf("migration lost the prepared image: %+v %v", part, err)
	}
	part, err = (&FeishuDelivery{Store: service.Store}).reviewPart(root)
	if err != nil || part.Kind != "text" || part.Start != 2 || part.End != 12 {
		t.Fatalf("migration changed the saved body range: %+v %v", part, err)
	}
	ctx := context.Background()
	if progress, err := service.Deliver(ctx); err != nil || !progress {
		t.Fatalf("first saved part: %v %v", progress, err)
	}
	var available float64
	if err := service.Store.state.QueryRow("SELECT available_at FROM notifications WHERE id = ?", child).
		Scan(&available); err != nil || available != 0 {
		t.Fatalf("migration lost the continuation trigger: %v %v", available, err)
	}
	if progress, err := service.Deliver(ctx); err != nil || !progress {
		t.Fatalf("saved next part: %v %v", progress, err)
	}
	if progress, err := service.Deliver(ctx); err != nil || progress {
		t.Fatalf("completed parts were sent again: %v %v", progress, err)
	}
}

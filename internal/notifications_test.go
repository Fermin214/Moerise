package moerise

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

type notificationPlatform struct {
	calls int
	uuids []string
	mode  string
}

func (p *notificationPlatform) Send(_ context.Context, item Notification) (string, error) {
	p.calls++
	p.uuids = append(p.uuids, item.PlatformUUID)
	switch p.mode {
	case "rejected":
		return "", upstreamError("CHANNEL_REJECTED", "rate limited")
	case "unknown":
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "response lost")
	case "crash":
		panic("interrupted after durable send attempt")
	}
	return "platform-receipt:" + item.ID, nil
}

func notificationFixture(t *testing.T) (*NotificationService, *notificationPlatform) {
	t.Helper()
	reviews, _ := reviewFixture(t, 12)
	reviews.Store.Settings.Notifications.Enabled = true
	reviews.Store.Settings.Notifications.Times = []string{"09:00"}
	platform := &notificationPlatform{}
	return NewNotificationService(reviews.Store, reviews, platform), platform
}

func TestNotificationDatesAndIndependentOpenState(t *testing.T) {
	service, _ := notificationFixture(t)
	ctx := context.Background()
	before := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	if count, err := service.Schedule(ctx, before); err != nil || count != 0 {
		t.Fatalf("before push time: count=%d err=%v", count, err)
	}
	at := before.Add(2 * time.Hour)
	if count, err := service.Schedule(ctx, at); err != nil || count != 1 {
		t.Fatalf("due push: count=%d err=%v", count, err)
	}
	if count, err := service.Schedule(ctx, at.Add(time.Hour)); err != nil || count != 0 {
		t.Fatal("same date created another notification")
	}
	if count, err := service.Schedule(ctx, at.AddDate(0, 0, 2)); err != nil || count != 1 {
		t.Fatalf("restart should schedule today without old daily pushes: count=%d err=%v", count, err)
	}
	items, err := service.List("")
	if err != nil || len(items) != 2 {
		t.Fatalf("wrong persisted queue: %v %v", items, err)
	}
	dates := map[string]bool{}
	for _, item := range items {
		if dates[item.Date] {
			t.Fatal("duplicate local date in queue")
		}
		dates[item.Date] = true
	}
	if progress, err := service.Deliver(ctx); err != nil || !progress {
		t.Fatal("pending notification was not delivered")
	}
	sent, _ := service.List("sent")
	if len(sent) != 1 || sent[0].Receipt == nil {
		t.Fatal("send result was not persisted")
	}
	_, err = service.Reviews.Get(ctx, *sent[0].BatchID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestNotificationDSTGapAndRepeatedHour(t *testing.T) {
	for _, test := range []struct {
		name, clock, before, due, after string
	}{
		{"spring-gap", "02:30", "2026-03-08T06:59:00Z", "2026-03-08T07:00:00Z", "2026-03-08T08:00:00Z"},
		{"fall-repeat", "01:30", "2026-11-01T05:29:00Z", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := notificationFixture(t)
			service.Store.Settings.Timezone = "America/New_York"
			service.Store.Settings.Notifications.Times = []string{test.clock}
			parse := func(value string) time.Time {
				at, err := time.Parse(time.RFC3339, value)
				if err != nil {
					t.Fatal(err)
				}
				return at
			}
			ctx := context.Background()
			if count, err := service.Schedule(ctx, parse(test.before)); err != nil || count != 0 {
				t.Fatalf("early DST schedule: %d %v", count, err)
			}
			if count, err := service.Schedule(ctx, parse(test.due)); err != nil || count != 1 {
				t.Fatalf("DST date was missed: %d %v", count, err)
			}
			if count, err := service.Schedule(ctx, parse(test.after)); err != nil || count != 0 {
				t.Fatalf("repeated local hour duplicated a task: %d %v", count, err)
			}
		})
	}
}

func TestDailyReviewWithoutPushAndMultipleSlots(t *testing.T) {
	service, platform := notificationFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
	service.Store.Settings.Notifications.Enabled = false
	service.Store.Settings.Notifications.Times = []string{"09:00", "12:00"}
	if count, err := service.Schedule(ctx, at); err != nil || count != 0 {
		t.Fatalf("daily generation without push: %d %v", count, err)
	}
	first, err := service.Reviews.ReadDaily(ctx, at)
	if err != nil || len(first.Items) != 6 || platform.calls != 0 {
		t.Fatalf("disabled push should still expose today's review: %+v %v", first, err)
	}
	service.Store.Settings.Notifications.Enabled = true
	service.Store.Settings.Notifications.Times = []string{"09:00", "12:00"}
	if count, err := service.Schedule(ctx, at); err != nil || count != 1 {
		t.Fatalf("first due slot: %d %v", count, err)
	}
	service.Store.Settings.Review.Count = 1
	service.Store.Settings.Review.IncludeArchived = true
	service.Store.Settings.Notifications.Times = []string{"09:00", "10:00", "12:00"}
	if count, err := service.Schedule(ctx, at.Add(3*time.Hour)); err != nil || count != 1 {
		t.Fatalf("added and later slots: %d %v", count, err)
	}
	notifications, err := service.List("")
	if err != nil || len(notifications) != 2 {
		t.Fatalf("slot queue: %+v %v", notifications, err)
	}
	for _, item := range notifications {
		if item.BatchID == nil || *item.BatchID != first.ID {
			t.Fatal("a different time or changed rule resampled today's batch")
		}
	}
	restarted := NewNotificationService(service.Store, service.Reviews, platform)
	if count, err := restarted.Schedule(ctx, at.Add(4*time.Hour)); err != nil || count != 0 {
		t.Fatalf("restart duplicated a processed slot: %d %v", count, err)
	}
}

func TestEmptyReviewSlotsAndLocalScheduling(t *testing.T) {
	reviews, _ := reviewFixture(t, 0)
	reviews.Store.Settings.Notifications.Enabled = true
	service := NewNotificationService(reviews.Store, reviews, &notificationPlatform{})
	at := time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
	if count, err := service.Schedule(t.Context(), at); err != nil || count != 0 {
		t.Fatalf("empty: %d %v", count, err)
	}
	var ordinal int
	if err := reviews.Store.state.QueryRow(`SELECT scheduled_ordinal FROM review_batches`).Scan(&ordinal); err != nil || ordinal != 0 {
		t.Fatalf("empty slot: %d %v", ordinal, err)
	}
	reviews.Source = fakeClient(reviews.Store.Settings, func(*http.Request) (int, any, error) {
		t.Fatal("local scheduling contacted source")
		return 500, nil, nil
	})
	if count, err := service.Schedule(t.Context(), at.AddDate(0, 0, 1)); err != nil || count != 0 {
		t.Fatalf("local empty: %d %v", count, err)
	}
}

func TestNotificationFailureRetryAndRestart(t *testing.T) {
	service, platform := notificationFixture(t)
	at := time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC)
	if _, err := service.Schedule(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	items, _ := service.List("")
	id := items[0].ID
	for attempt, mode := range []string{"rejected", "unknown"} {
		platform.mode = mode
		if worked, err := service.Deliver(t.Context()); err != nil || !worked {
			t.Fatalf("send: %v", err)
		}
		detail, err := service.Detail(id)
		if err != nil || detail.Notification.Status != "failed" || detail.Notification.Attempts != attempt+1 {
			t.Fatalf("failed record: %+v %v", detail, err)
		}
		if worked, err := service.Deliver(t.Context()); err != nil || worked {
			t.Fatal("failed send automatically retried")
		}
		if _, err := service.Retry(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	platform.mode = ""
	if worked, err := service.Deliver(t.Context()); err != nil || !worked || platform.calls != 3 {
		t.Fatalf("retry: %v", err)
	}
	if _, err := service.Retry(t.Context(), id); errorCode(err) != "NOTIFICATION_CONFLICT" {
		t.Fatalf("sent retry: %v", err)
	}
	if _, err := service.Schedule(t.Context(), at.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	platform.mode = "crash"
	func() { defer func() { _ = recover() }(); _, _ = service.Deliver(t.Context()) }()
	if err := service.recover(); err != nil {
		t.Fatal(err)
	}
	failed, _ := service.List("failed")
	if len(failed) != 1 || configured(failed[0].LastError) != "PROCESS_INTERRUPTED" {
		t.Fatalf("restart: %+v", failed)
	}
}

func TestRestoredNotificationsRequireOutcomeReview(t *testing.T) {
	service, _ := notificationFixture(t)
	ctx := context.Background()
	if _, err := service.Schedule(ctx, time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	settings := service.Store.Settings
	snapshot := filepath.Join(filepath.Dir(settings.StateDir), "notification-snapshot")
	if _, err := Backup(settings, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	service.Store.Close()
	if err := Restore(settings, snapshot, filepath.Join(filepath.Dir(settings.StateDir), "before-notification-restore")); err != nil {
		t.Fatal(err)
	}
	restored := testStore(t, settings)
	recovered := NewNotificationService(restored, NewReviewService(restored, service.Reviews.Source), service.Transport)
	unknown, err := recovered.List("failed")
	if err != nil || len(unknown) != 1 || unknown[0].LastError == nil || *unknown[0].LastError != "STATE_RESTORED" {
		t.Fatalf("restored queue can repeat a later send: %v %v", unknown, err)
	}
	if progress, err := recovered.Deliver(ctx); err != nil || progress {
		t.Fatal("restoring an older queue snapshot resent a notification")
	}
}

func TestNotificationUpgradePreservesUncertainFragmentsAndAudit(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "state.sqlite3"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	body, err := migrationSQL("state", "001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY,checksum TEXT,applied_at TEXT);
		INSERT INTO bindings VALUES ('instance','base','user','stamp');
		INSERT INTO notifications (id, instance_id, user_name, channel_key, logical_key, kind, local_date, status,
			platform_uuid, available_at, created_at, updated_at)
		VALUES ('first','instance','user','destination','part-0','review','date','unknown','uuid',0,'stamp','stamp'),
			('next','instance','user','destination','part-1','review','date','pending','second',1e30,'stamp','stamp');
		INSERT INTO review_delivery_parts(notification_id,root_id,position,kind,memo_name,source_version)
		VALUES ('first','first',0,'header','',''),('next','first',1,'text','memos/note','v1');
		INSERT INTO notification_attempts VALUES ('attempt','first',1,'uuid','start','end','unknown',NULL,'lost');
		INSERT INTO notification_resolutions VALUES ('resolve','first','not_sent',NULL,'owner','time');
        INSERT INTO review_batches VALUES('batch','instance','user','2026-11-21','Asia/Shanghai','stamp');
        INSERT INTO review_plans VALUES('batch','instance','user','2026-11-21',7,'{"review":{"tags":["普通文本","中文","123",null]}}');
        INSERT INTO review_plan_slots VALUES('slot','batch',0,'09:00',0,3);
        INSERT INTO review_schedule_slots VALUES('instance','user','destination','Asia/Shanghai','2026-11-21','09:00');
        INSERT INTO channel_events(message_id,event_id,event_hash,app_id,sender_open_id,payload,status,reply_uuid,available_at,created_at)
        VALUES('legacy','event','digest','app','peer',x'00112233','processing','reply',0,'2026-11-21T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO schema_migrations VALUES ('001',?,'stamp')", hashBytes(body)); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, "state"); err != nil {
		t.Fatal(err)
	}
	var clock, sealedBody string
	var ordinal int
	if err := db.QueryRow(`SELECT json_extract(slots_json,'$[0].clock'),scheduled_ordinal FROM review_batches WHERE id='batch'`).Scan(&clock, &ordinal); err != nil || clock != "09:00" || ordinal != 0 {
		t.Fatalf("frozen plan migration: %v", err)
	}
	if err := db.QueryRow(`SELECT hex(body) FROM bot_inbox WHERE message_id='legacy' AND phase='pending' AND operation='feishu-legacy'`).Scan(&sealedBody); err != nil || sealedBody != "00112233" {
		t.Fatalf("legacy payload: %q %v", sealedBody, err)
	}
	var intact bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM delivery_logs WHERE id='first' AND status='failed')
        AND EXISTS(SELECT 1 FROM delivery_logs WHERE id='next' AND status='pending' AND root_id='first' AND part_ordinal=1 AND memo_name='memos/note' AND available_at=0)
        AND NOT EXISTS(SELECT 1 FROM pragma_foreign_key_check)
        AND NOT EXISTS(SELECT 1 FROM sqlite_master WHERE type='trigger')
        AND NOT EXISTS(SELECT 1 FROM sqlite_master WHERE name IN ('notifications','notification_attempts','notification_resolutions','channel_events','review_plans','review_plan_slots'))`).Scan(&intact); err != nil || !intact {
		t.Fatalf("migration: %v", err)
	}
}

func TestRestoreRequiresExplicitRetryForEveryPendingFragment(t *testing.T) {
	queue, platform := notificationFixture(t)
	var ids []string
	for position := range 3 {
		id, err := queue.enqueue("review", fmt.Sprint(position), "2026-11-18", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if _, err := queue.Store.state.Exec(`UPDATE delivery_logs SET root_id=?,part_ordinal=? WHERE id=?`, ids[0], position, id); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	settings := queue.Store.Settings
	if _, err := Backup(settings, snapshot); err != nil {
		t.Fatal(err)
	}
	queue.Store.Close()
	if err := Restore(settings, snapshot, filepath.Join(t.TempDir(), "safety")); err != nil {
		t.Fatal(err)
	}
	restored := testStore(t, settings)
	queue = NewNotificationService(restored, nil, platform)
	if worked, err := queue.Deliver(t.Context()); err != nil || worked {
		t.Fatal("restored send repeated")
	}
	if _, err := queue.Retry(t.Context(), ids[1]); err != nil {
		t.Fatal(err)
	}
	if worked, err := queue.Deliver(t.Context()); err != nil || worked {
		t.Fatal("fragment overtook predecessor")
	}
	for _, id := range []string{ids[0], ids[2]} {
		if _, err := queue.Retry(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		worked, err := queue.Deliver(t.Context())
		detail, e := queue.Detail(id)
		if err != nil || e != nil || !worked || detail.Notification.Status != "sent" {
			t.Fatalf("fragment: %+v %v %v", detail, err, e)
		}
	}
	if platform.calls != 3 {
		t.Fatal("wrong resumed sends")
	}
}

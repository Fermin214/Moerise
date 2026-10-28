package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	return "platform-receipt", nil
}

func (p *notificationPlatform) Check(_ context.Context, _ Notification, receipt string) (bool, error) {
	return receipt == "platform-receipt", nil
}

func notificationFixture(t *testing.T) (*NotificationService, *notificationPlatform) {
	t.Helper()
	reviews, _ := reviewFixture(t, 12)
	reviews.Store.Settings.Notifications.Enabled = true
	reviews.Store.Settings.Notifications.Time = "09:00"
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
	batch, err := service.Reviews.Get(ctx, *sent[0].BatchID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range batch.Items {
		if item.OpenedAt != nil || item.SkippedAt != nil {
			t.Fatal("notification success changed reading history")
		}
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
			service.Store.Settings.Notifications.Time = test.clock
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
	if count, err := service.Schedule(ctx, at); err != nil || count != 0 {
		t.Fatalf("daily generation without push: %d %v", count, err)
	}
	first, err := service.Reviews.ReadDaily(ctx, at)
	if err != nil || len(first.Items) != 3 || platform.calls != 0 {
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
	if count, err := service.Schedule(ctx, at.Add(3*time.Hour)); err != nil || count != 2 {
		t.Fatalf("added and later slots: %d %v", count, err)
	}
	notifications, err := service.List("")
	if err != nil || len(notifications) != 3 {
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

func TestEmptyReviewSlotsAreSilentAndSourceFailureRetries(t *testing.T) {
	reviews, _ := reviewFixture(t, 0)
	reviews.Store.Settings.Notifications.Enabled = true
	service := NewNotificationService(reviews.Store, reviews, &notificationPlatform{})
	ctx := context.Background()
	at := time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
	if count, err := service.Schedule(ctx, at); err != nil || count != 0 {
		t.Fatalf("empty pool: %d %v", count, err)
	}
	items, err := service.List("")
	if err != nil || len(items) != 0 {
		t.Fatal("empty review created a platform notification")
	}
	var slots int
	if err := reviews.Store.state.QueryRow("SELECT COUNT(*) FROM review_schedule_slots").Scan(&slots); err != nil || slots != 1 {
		t.Fatalf("empty slot was not marked processed: %d %v", slots, err)
	}
	client := reviews.Source.(*MemosClient)
	client.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("offline")
	})
	if _, err := service.Schedule(ctx, at.AddDate(0, 0, 1)); err == nil {
		t.Fatal("source failure was treated as an empty pool")
	}
	if err := reviews.Store.state.QueryRow("SELECT COUNT(*) FROM review_schedule_slots").Scan(&slots); err != nil || slots != 1 {
		t.Fatal("failed source lookup consumed the next slot")
	}
}

func TestNotificationAttemptsUnknownResolutionAndRestart(t *testing.T) {
	service, platform := notificationFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC)
	if _, err := service.Schedule(ctx, at); err != nil {
		t.Fatal(err)
	}
	items, _ := service.List("")
	id := items[0].ID
	platform.mode = "rejected"
	if _, err := service.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.state.Exec("UPDATE notifications SET available_at = 0"); err != nil {
		t.Fatal(err)
	}
	platform.mode = "unknown"
	if _, err := service.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	detail, err := service.Detail(id)
	if err != nil || detail.Notification.Status != "unknown" || len(detail.Attempts) != 2 ||
		detail.Attempts[0].Outcome != "rejected" || detail.Attempts[1].Outcome != "unknown" {
		t.Fatalf("attempt history lost: %+v %v", detail, err)
	}
	if progress, err := service.Deliver(ctx); err != nil || progress || platform.calls != 2 {
		t.Fatal("unknown outcome was automatically retried")
	}
	if _, err := service.Resolve(ctx, id, "sent", "wrong-receipt", "operator"); errorCode(err) != "NOTIFICATION_CONFLICT" {
		t.Fatal("unrelated receipt marked a notification as sent")
	}
	platform.mode = ""
	if _, err := service.Resolve(ctx, id, "not_sent", "", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deliver(ctx); err != nil || platform.calls != 3 ||
		platform.uuids[0] != platform.uuids[1] || platform.uuids[2] == platform.uuids[1] {
		t.Fatal("resolution did not preserve known retries or create a new confirmed attempt")
	}
	if _, err := service.Schedule(ctx, at.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	platform.mode = "crash"
	func() {
		defer func() { _ = recover() }()
		_, _ = service.Deliver(ctx)
	}()
	if err := service.recover(); err != nil {
		t.Fatal(err)
	}
	unknown, _ := service.List("unknown")
	if len(unknown) != 1 || unknown[0].LastError == nil || *unknown[0].LastError != "PROCESS_INTERRUPTED" {
		t.Fatal("interrupted attempt was blindly returned to pending")
	}
	platform.mode = ""
	if _, err := service.Resolve(ctx, unknown[0].ID, "sent", "platform-receipt", "operator"); err != nil {
		t.Fatal(err)
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
	unknown, err := recovered.List("unknown")
	if err != nil || len(unknown) != 1 || unknown[0].LastError == nil || *unknown[0].LastError != "STATE_RESTORED" {
		t.Fatalf("restored queue can repeat a later send: %v %v", unknown, err)
	}
	if progress, err := recovered.Deliver(ctx); err != nil || progress {
		t.Fatal("restoring an older queue snapshot resent a notification")
	}
}

func TestNotificationHTTPScopesAndReceiptResolution(t *testing.T) {
	service, platform := notificationFixture(t)
	ctx := context.Background()
	if _, err := service.Schedule(ctx, time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	platform.mode = "unknown"
	if _, err := service.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	items, _ := service.List("unknown")
	id := items[0].ID
	settings := service.Store.Settings
	settings.AccessTokens[0].Scopes = append(settings.AccessTokens[0].Scopes, "reviews:read")
	settings.AccessTokens[1].Scopes = append(settings.AccessTokens[1].Scopes, "reviews:read", "reviews:write")
	source := service.Reviews.Source
	api, err := NewAPI(settings, service.Store, source, NewSyncService(service.Store, source))
	if err != nil {
		t.Fatal(err)
	}
	api.Notifications = service
	call := func(method, path, body string, token int, expected int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+settings.AccessTokens[token].Token)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != expected {
			t.Fatalf("notification HTTP: %d %s", response.Code, response.Body.String())
		}
		return response
	}
	call("GET", "/api/ext/v1/notifications?status=unknown", "", 0, 200)
	call("GET", "/api/ext/v1/notifications?status=pending%7Crunning", "", 0, 422)
	path := "/api/ext/v1/notifications/" + id + "/resolve"
	call("POST", path, `{"decision":"sent","receipt":"platform-receipt"}`, 0, 403)
	call("POST", path, `{"decision":"sent","receipt":"unrelated"}`, 1, 409)
	call("POST", path, `{"decision":"not_sent","receipt":"unrelated"}`, 1, 422)
	response := call("POST", path, `{"decision":"sent","receipt":"platform-receipt"}`, 1, 200)
	var detail NotificationDetail
	if json.Unmarshal(response.Body.Bytes(), &detail) != nil || detail.Notification.Status != "sent" ||
		len(detail.Resolutions) != 1 || detail.Resolutions[0].ResolvedBy != "operator" {
		t.Fatal("receipt check and operator audit were not exposed in the notification result")
	}
}

package moerise

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

type Notification struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Date         string  `json:"date"`
	BatchID      *string `json:"batch_id"`
	SourceEvent  *string `json:"source_event"`
	Status       string  `json:"status"`
	Attempts     int     `json:"attempts"`
	AvailableAt  float64 `json:"available_at"`
	Receipt      *string `json:"receipt"`
	LastError    *string `json:"last_error"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	PlatformUUID string  `json:"-"`
	channelKey   string
}

type NotificationAttempt struct {
	Attempt     int     `json:"attempt"`
	StartedAt   string  `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
	Outcome     string  `json:"outcome"`
	Receipt     *string `json:"receipt"`
	ErrorCode   *string `json:"error_code"`
}

type NotificationDetail struct {
	Notification Notification             `json:"notification"`
	Attempts     []NotificationAttempt    `json:"attempts"`
	Resolutions  []NotificationResolution `json:"resolutions"`
}

type NotificationResolution struct {
	Decision   string  `json:"decision"`
	Receipt    *string `json:"receipt"`
	ResolvedBy string  `json:"resolved_by"`
	CreatedAt  string  `json:"created_at"`
}

type NotificationTransport interface {
	Send(context.Context, Notification) (string, error)
	Check(context.Context, Notification, string) (bool, error)
}

type NotificationService struct {
	Store            *Store
	Reviews          *ReviewService
	Transport        NotificationTransport
	channelTransport func(ChannelConfig) (NotificationTransport, func())
	mu               sync.Mutex
}

func NewNotificationService(store *Store, reviews *ReviewService, transport NotificationTransport) *NotificationService {
	return &NotificationService{Store: store, Reviews: reviews, Transport: transport}
}

func (s *NotificationService) channelKey() string {
	return notificationChannelKey(s.Store.Settings.Channel)
}

func notificationChannelKey(channel ChannelConfig) string {
	destination := configured(channel.Destination)
	if channel.Kind == "feishu-app" && destination == "" {
		destination = configured(channel.UserOpenID)
	}
	return hashBytes(canonicalJSON([]string{channel.Kind, configured(channel.AppID),
		configured(channel.UserOpenID), destination, configured(channel.WebhookURL)}))
}

// Schedule fixes today's batch independently of delivery and processes each due slot once.
func (s *NotificationService) Schedule(ctx context.Context, at time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := s.Store.reviewSettings(at)
	location, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return 0, err
	}
	instance, user, channel := settings.Memos.InstanceID, settings.Memos.User, s.channelKey()
	today := at.In(location).Format(time.DateOnly)
	var batch ReviewBatch
	var batchID string
	err = s.Store.state.QueryRow(`SELECT id FROM review_batches
		WHERE instance_id = ? AND user_name = ? AND timezone = ? AND local_date = ?`+reviewBatchOrder,
		instance, user, settings.Timezone, today).Scan(&batchID)
	loaded := false
	if errors.Is(err, sql.ErrNoRows) {
		batch, err = s.Reviews.Daily(ctx, at)
		batchID, loaded = batch.ID, true
	}
	if err != nil {
		return 0, err
	}
	if !settings.Notifications.Enabled {
		return 0, nil
	}
	created := 0
	for ordinal, clock := range settings.Notifications.Clocks() {
		moment, err := scheduledMoment(today, clock, location)
		if err != nil {
			return created, err
		}
		if moment.After(at) {
			continue
		}
		var done bool
		if err := s.Store.state.QueryRow(`SELECT EXISTS (SELECT 1 FROM review_schedule_slots
			WHERE instance_id = ? AND user_name = ? AND timezone = ?
			AND local_date = ? AND (clock = ? OR (clock = '' AND ? = 0))
			UNION ALL SELECT 1 FROM notifications
			WHERE instance_id = ? AND user_name = ? AND kind = 'review' AND logical_key = ?)`,
			instance, user, settings.Timezone, today, clock, ordinal,
			instance, user, today+":"+clock).Scan(&done); err != nil {
			return created, err
		}
		if done {
			continue
		}
		if !loaded {
			batch, err = s.Reviews.Get(ctx, batchID)
			if err != nil {
				return created, err
			}
			loaded = true
		}
		available := slices.ContainsFunc(batch.Items, func(item ReviewItem) bool {
			return !item.Unavailable && item.Note != nil
		})
		if available {
			if _, err := s.enqueue("review", today+":"+clock, today, &batch.ID, nil); err != nil {
				return created, err
			}
			created++
		}
		if _, err := s.Store.state.Exec(`INSERT OR IGNORE INTO review_schedule_slots
			(instance_id, user_name, channel_key, timezone, local_date, clock) VALUES (?, ?, ?, ?, ?, ?)`,
			instance, user, channel, settings.Timezone, today, clock); err != nil {
			return created, err
		}
	}
	return created, nil
}

func scheduledMoment(date, clock string, location *time.Location) (time.Time, error) {
	day, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return time.Time{}, err
	}
	wanted, err := time.Parse("15:04", clock)
	if err != nil {
		return time.Time{}, err
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location).Add(-3 * time.Hour)
	for offset := 0; offset < 30*60; offset++ {
		moment := start.Add(time.Duration(offset) * time.Minute)
		local := moment.In(location)
		if local.Format(time.DateOnly) == date && local.Hour()*60+local.Minute() >= wanted.Hour()*60+wanted.Minute() {
			return moment, nil
		}
	}
	return time.Time{}, fmt.Errorf("local date has no scheduled instant: %s", date)
}

func (s *NotificationService) enqueue(kind, logical, date string, batch, event *string) (string, error) {
	settings := s.Store.Settings
	channel := s.channelKey()
	id := derivedID(settings.CursorSecret, "notification", settings.Memos.InstanceID, settings.Memos.User, channel, logical)
	stamp := nowText()
	_, err := s.Store.state.Exec(`INSERT OR IGNORE INTO notifications
		(id, instance_id, user_name, channel_key, logical_key, kind, local_date, batch_id, source_event,
		 status, platform_uuid, available_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?)`, id,
		settings.Memos.InstanceID, settings.Memos.User, channel, logical, kind, date, batch, event,
		derivedID(settings.CursorSecret, "notification-send", id), nowSeconds(), stamp, stamp)
	if err != nil {
		return "", err
	}
	var saved string
	err = s.Store.state.QueryRow(`SELECT id FROM notifications WHERE instance_id = ? AND user_name = ?
		AND channel_key = ? AND logical_key = ?`, settings.Memos.InstanceID, settings.Memos.User, channel, logical).Scan(&saved)
	return saved, err
}

func (s *NotificationService) EnqueueReply(messageID string) (string, error) {
	settings := s.Store.Settings
	channel := s.channelKey()
	logical := "reply:" + messageID
	id := derivedID(settings.CursorSecret, "notification", settings.Memos.InstanceID, settings.Memos.User, channel, logical)
	err := transaction(s.Store.state, func(tx *sql.Tx) error {
		var replyStatus, uuid, created string
		var lastError *string
		if err := tx.QueryRow(`SELECT reply_status, reply_uuid, created_at, last_error FROM channel_events
			WHERE message_id = ? AND app_id = ? AND sender_open_id = ? AND status = 'processed'`,
			messageID, configured(settings.Channel.AppID), configured(settings.Channel.UserOpenID)).
			Scan(&replyStatus, &uuid, &created, &lastError); err != nil {
			return err
		}
		location, err := time.LoadLocation(settings.Timezone)
		if err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return err
		}
		status := "pending"
		if replyStatus == "unknown" {
			status = "unknown"
		}
		stamp := nowText()
		if _, err := tx.Exec(`INSERT OR IGNORE INTO notifications
			(id, instance_id, user_name, channel_key, logical_key, kind, local_date, source_event,
			 status, platform_uuid, available_at, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'reply', ?, ?, ?, ?, ?, ?, ?, ?)`, id, settings.Memos.InstanceID,
			settings.Memos.User, channel, logical, at.In(location).Format(time.DateOnly), messageID,
			status, uuid, nowSeconds(), lastError, stamp, stamp); err != nil {
			return err
		}
		item, err := scanNotification(tx.QueryRow("SELECT "+notificationColumns+` FROM notifications
			WHERE instance_id = ? AND user_name = ? AND channel_key = ? AND logical_key = ?`,
			settings.Memos.InstanceID, settings.Memos.User, channel, logical))
		if err != nil {
			return err
		}
		id = item.ID
		return syncEventNotification(tx, item, item.Status, item.Receipt, item.LastError)
	})
	return id, err
}

func syncEventNotification(tx *sql.Tx, item Notification, status string, receipt, code any) error {
	if item.SourceEvent == nil {
		return nil
	}
	eventStatus, replyStatus := "processed", "queued"
	if status == "sent" {
		eventStatus, replyStatus = "succeeded", "sent"
	} else if status == "unknown" {
		replyStatus = "unknown"
	}
	_, err := tx.Exec(`UPDATE channel_events SET status = ?, reply_status = ?, receipt = ?, last_error = ?
		WHERE message_id = ?`, eventStatus, replyStatus, receipt, code, *item.SourceEvent)
	return err
}

const notificationColumns = `id, kind, local_date, batch_id, source_event, status, attempts, available_at,
	receipt, last_error, created_at, updated_at, platform_uuid, channel_key`

func scanNotification(row interface{ Scan(...any) error }) (Notification, error) {
	var notification Notification
	err := row.Scan(&notification.ID, &notification.Kind, &notification.Date, &notification.BatchID,
		&notification.SourceEvent, &notification.Status, &notification.Attempts, &notification.AvailableAt,
		&notification.Receipt, &notification.LastError, &notification.CreatedAt, &notification.UpdatedAt,
		&notification.PlatformUUID, &notification.channelKey)
	return notification, err
}

func (s *NotificationService) Detail(id string) (NotificationDetail, error) {
	detail := NotificationDetail{Attempts: []NotificationAttempt{}, Resolutions: []NotificationResolution{}}
	if !uuidPattern.MatchString(id) {
		return detail, failure("INVALID_ARGUMENT", "Invalid notification identifier", 422, false)
	}
	settings := s.Store.Settings
	var err error
	detail.Notification, err = scanNotification(s.Store.state.QueryRow("SELECT "+notificationColumns+` FROM notifications
		WHERE id = ? AND instance_id = ? AND user_name = ?`, id,
		settings.Memos.InstanceID, settings.Memos.User))
	if errors.Is(err, sql.ErrNoRows) {
		return detail, failure("NOTIFICATION_NOT_FOUND", "Notification is unavailable", 404, false)
	}
	if err != nil {
		return detail, err
	}
	rows, err := s.Store.state.Query(`SELECT attempt, started_at, completed_at, outcome, receipt, error_code
		FROM notification_attempts WHERE notification_id = ? ORDER BY attempt`, id)
	if err != nil {
		return detail, err
	}
	for rows.Next() {
		var attempt NotificationAttempt
		if err := rows.Scan(&attempt.Attempt, &attempt.StartedAt, &attempt.CompletedAt,
			&attempt.Outcome, &attempt.Receipt, &attempt.ErrorCode); err != nil {
			rows.Close()
			return detail, err
		}
		detail.Attempts = append(detail.Attempts, attempt)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return detail, err
	}
	rows, err = s.Store.state.Query(`SELECT decision, receipt, resolved_by, created_at FROM notification_resolutions
		WHERE notification_id = ? ORDER BY created_at, id`, id)
	if err != nil {
		return detail, err
	}
	defer rows.Close()
	for rows.Next() {
		var resolution NotificationResolution
		if err := rows.Scan(&resolution.Decision, &resolution.Receipt, &resolution.ResolvedBy, &resolution.CreatedAt); err != nil {
			return detail, err
		}
		detail.Resolutions = append(detail.Resolutions, resolution)
	}
	return detail, rows.Err()
}

func (s *NotificationService) List(status string) ([]Notification, error) {
	if status != "" && !slices.Contains([]string{"pending", "running", "unknown", "sent"}, status) {
		return nil, failure("INVALID_ARGUMENT", "Invalid notification status", 422, false)
	}
	settings := s.Store.Settings
	query := "SELECT " + notificationColumns + ` FROM notifications WHERE instance_id = ? AND user_name = ?`
	args := []any{settings.Memos.InstanceID, settings.Memos.User}
	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	rows, err := s.Store.state.Query(query+" ORDER BY created_at DESC, id DESC LIMIT 100", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Notification{}
	for rows.Next() {
		item, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *NotificationService) recover() error {
	return transaction(s.Store.state, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE notification_attempts SET outcome = 'unknown', completed_at = ?
			WHERE outcome = 'running'`, nowText()); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE notifications SET status = 'unknown', last_error = 'PROCESS_INTERRUPTED',
			updated_at = ? WHERE status = 'running'`, nowText()); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE channel_events SET reply_status = 'unknown', last_error = 'PROCESS_INTERRUPTED'
			WHERE message_id IN (SELECT source_event FROM notifications
				WHERE status = 'unknown' AND last_error = 'PROCESS_INTERRUPTED')`)
		return err
	})
}

func (s *NotificationService) Deliver(ctx context.Context) (bool, error) {
	if s.Transport == nil && s.channelTransport == nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := s.Store.Settings
	var item Notification
	var attemptID string
	err := transaction(s.Store.state, func(tx *sql.Tx) error {
		var err error
		item, err = scanNotification(tx.QueryRow("SELECT "+notificationColumns+` FROM notifications
			WHERE status = 'pending' AND (kind = 'reply' OR ?) AND available_at <= ? AND instance_id = ? AND user_name = ?
			ORDER BY available_at, created_at, id LIMIT 1`, settings.Notifications.Enabled, nowSeconds(), settings.Memos.InstanceID,
			settings.Memos.User))
		if err != nil {
			return err
		}
		item.Attempts++
		attemptID = newID()
		stamp := nowText()
		if _, err := tx.Exec(`INSERT INTO notification_attempts
			(id, notification_id, attempt, platform_uuid, started_at, outcome) VALUES (?, ?, ?, ?, ?, 'running')`,
			attemptID, item.ID, item.Attempts, item.PlatformUUID, stamp); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE notifications SET status = 'running', attempts = ?, updated_at = ? WHERE id = ?`,
			item.Attempts, stamp, item.ID)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	transport, closeTransport, sendErr := s.transportFor(item)
	defer closeTransport()
	receipt := ""
	if sendErr == nil {
		receipt, sendErr = transport.Send(ctx, item)
	}
	status, outcome, code := "sent", "sent", ""
	var savedReceipt any
	if sendErr == nil && receipt == "" {
		sendErr = upstreamError("SEND_OUTCOME_UNKNOWN", "Channel returned no receipt")
	}
	if sendErr != nil {
		code, status, outcome = errorCode(sendErr), "pending", "rejected"
		if code == "SEND_OUTCOME_UNKNOWN" || code == "SYNC_INTERNAL" {
			status, outcome = "unknown", "unknown"
		}
		slog.Warn("notification deferred", "notification_id", item.ID, "code", code)
	} else {
		savedReceipt = receipt
	}
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
		stamp := nowText()
		if _, err := tx.Exec(`UPDATE notification_attempts SET completed_at = ?, outcome = ?, receipt = ?, error_code = ?
			WHERE id = ?`, stamp, outcome, savedReceipt, nullable(code), attemptID); err != nil {
			return err
		}
		if err := syncEventNotification(tx, item, status, savedReceipt, nullable(code)); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE notifications SET status = ?, receipt = ?, last_error = ?,
			available_at = ?, updated_at = ? WHERE id = ?`, status, savedReceipt, nullable(code),
			nowSeconds()+float64(int64(1)<<min(item.Attempts, 8)), stamp, item.ID)
		return err
	})
	return true, err
}

func nullable(text string) any {
	if text == "" {
		return nil
	}
	return text
}

func (s *NotificationService) Resolve(ctx context.Context, id, decision, receipt, actor string) (NotificationDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	detail, err := s.Detail(id)
	if err != nil {
		return detail, err
	}
	if decision != "sent" && decision != "not_sent" || len(receipt) > 256 || decision == "not_sent" && receipt != "" {
		return detail, failure("INVALID_ARGUMENT", "Invalid notification decision or receipt", 422, false)
	}
	if detail.Notification.Status != "unknown" {
		// Replaying a completed resolution does not create another attempt.
		if decision == "sent" && detail.Notification.Status == "sent" ||
			decision == "not_sent" && detail.Notification.Status == "pending" {
			return detail, nil
		}
		return detail, failure("NOTIFICATION_CONFLICT", "Only an unknown notification can be resolved", 409, false)
	}
	if decision == "sent" && receipt != "" {
		transport, closeTransport, err := s.transportFor(detail.Notification)
		defer closeTransport()
		if err != nil {
			return detail, err
		}
		found, err := transport.Check(ctx, detail.Notification, receipt)
		if err != nil {
			return detail, err
		}
		if !found {
			return detail, failure("NOTIFICATION_CONFLICT", "Receipt does not match this notification", 409, false)
		}
	}
	status, uuid := "sent", detail.Notification.PlatformUUID
	if decision == "not_sent" {
		status, uuid = "pending", newID()
		receipt = ""
	}
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
		if receipt != "" {
			var used bool
			if err := tx.QueryRow(`SELECT EXISTS (
				SELECT 1 FROM notifications WHERE receipt = ? AND id <> ?
				UNION ALL SELECT 1 FROM notification_attempts WHERE receipt = ? AND notification_id <> ?)`,
				receipt, id, receipt, id).Scan(&used); err != nil {
				return err
			}
			if used {
				return failure("NOTIFICATION_CONFLICT", "Receipt already belongs to another notification", 409, false)
			}
		}
		if _, err := tx.Exec(`INSERT INTO notification_resolutions
			(id, notification_id, decision, receipt, resolved_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			newID(), id, decision, nullable(receipt), actor, nowText()); err != nil {
			return err
		}
		if err := syncEventNotification(tx, detail.Notification, status, nullable(receipt), nil); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE notifications SET status = ?, platform_uuid = ?, receipt = ?,
			available_at = ?, updated_at = ?, last_error = NULL WHERE id = ?`,
			status, uuid, nullable(receipt), nowSeconds(), nowText(), id)
		return err
	})
	if err != nil {
		return detail, err
	}
	return s.Detail(id)
}

func (s *NotificationService) Run(ctx context.Context) error {
	unlock := s.Store.lockRuntime()
	err := s.recover()
	unlock()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		unlock := s.Store.lockRuntime()
		if _, scheduleErr := s.Schedule(ctx, time.Now()); scheduleErr != nil {
			slog.Warn("notification schedule deferred", "code", errorCode(scheduleErr))
		}
		_, err := s.Deliver(ctx)
		unlock()
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

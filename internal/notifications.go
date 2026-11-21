package moerise

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

type Notification struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Date         string  `json:"date"`
	BatchID      *string `json:"batch_id"`
	SlotID       *string `json:"slot_id,omitempty"`
	ChannelKind  string  `json:"channel_kind,omitempty"`
	Clock        string  `json:"clock,omitempty"`
	SourceEvent  *string `json:"source_event"`
	Status       string  `json:"status"`
	Attempts     int     `json:"attempts"`
	AvailableAt  float64 `json:"available_at"`
	Receipt      *string `json:"receipt"`
	LastError    *string `json:"last_error"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	RootID       string  `json:"delivery_id"`
	Part         int     `json:"part"`
	PlatformUUID string  `json:"-"`
	channelKey   string
}

type NotificationDetail struct {
	Notification Notification `json:"notification"`
}

type NotificationTransport interface {
	Send(context.Context, Notification) (string, error)
}

type NotificationService struct {
	Store            *Store
	Reviews          *ReviewService
	Transport        NotificationTransport
	transportKey     string
	resolved         map[string]resolvedTransport
	channelTransport func(ChannelConfig) (NotificationTransport, func())
	mu               sync.Mutex
}

func NewNotificationService(store *Store, reviews *ReviewService, transport NotificationTransport) *NotificationService {
	return &NotificationService{Store: store, Reviews: reviews, Transport: transport}
}

func (s *NotificationService) channelKey() string {
	return notificationChannelKey(s.Store.deliveryChannel())
}

func notificationChannelKey(channel ChannelConfig) string {
	if channel.Kind == "qq" {
		return hashBytes(canonicalJSON([]string{"qq", configured(channel.QQAppID), configured(channel.Destination)}))
	}
	if channel.Kind == "telegram" {
		bot, _, _ := strings.Cut(configured(channel.BotToken), ":")
		return hashBytes(canonicalJSON([]string{channel.Kind, bot, configured(channel.Destination)}))
	}
	destination := configured(channel.Destination)
	if channel.Kind == "feishu-app" && destination == "" {
		destination = configured(channel.UserOpenID)
	}
	return hashBytes(canonicalJSON([]string{channel.Kind, configured(channel.AppID), configured(channel.UserOpenID), destination, configured(channel.WebhookURL)}))
}

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
	var batchID, timezone string
	err = s.Store.state.QueryRow(`SELECT id,timezone FROM review_batches WHERE instance_id=? AND user_name=? AND local_date=?`, instance, user, today).Scan(&batchID, &timezone)
	if errors.Is(err, sql.ErrNoRows) {
		var batch ReviewBatch
		batch, err = s.Reviews.DailyLocal(ctx, at)
		batchID, timezone, today = batch.ID, batch.Timezone, batch.Date
	}
	if err != nil || !settings.Notifications.Enabled {
		return 0, err
	}
	location, err = time.LoadLocation(timezone)
	if err != nil {
		return 0, err
	}
	slots, err := s.Reviews.slots(batchID)
	if err != nil {
		return 0, err
	}
	latest := -1
	var moment time.Time
	for i, slot := range slots {
		due, e := scheduledMoment(today, slot.Clock, location)
		if e != nil {
			return 0, e
		}
		if !due.After(at) && (latest < 0 || !due.Before(moment)) {
			latest, moment = i, due
		}
	}
	if latest < 0 {
		return 0, nil
	}
	var previous int
	if err = s.Store.state.QueryRow(`SELECT scheduled_ordinal FROM review_batches WHERE id=?`, batchID).Scan(&previous); err != nil || previous >= latest {
		return 0, err
	}
	eligible, err := s.slotAfterBinding(moment)
	if err != nil {
		return 0, err
	}
	available := false
	if eligible && slots[latest].Count > 0 {
		batch, e := s.Reviews.forNotification(ctx, Notification{BatchID: &batchID, SlotID: &slots[latest].ID})
		if e != nil {
			return 0, e
		}
		available = slices.ContainsFunc(batch.Items, func(item ReviewItem) bool { return !item.Unavailable && item.Note != nil })
	}
	created := 0
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
		result, e := tx.Exec(`UPDATE review_batches SET scheduled_ordinal=? WHERE id=? AND scheduled_ordinal<?`, latest, batchID, latest)
		if e != nil {
			return e
		}
		changed, e := result.RowsAffected()
		if e != nil || changed == 0 || !available {
			return e
		}
		slot := slots[latest]
		logical := today + ":" + slot.Clock
		id := derivedID(settings.CursorSecret, "notification", instance, user, channel, logical)
		stamp := nowText()
		_, e = tx.Exec(`INSERT INTO delivery_logs (id,instance_id,user_name,channel_key,channel_kind,logical_key,kind,local_date,batch_id,slot_id,clock,status,platform_uuid,available_at,created_at,updated_at,root_id)
   VALUES(?,?,?,?,?,?,'review',?,?,?,?,'pending',?,?,?,?,?)`, id, instance, user, channel, settings.Channel.Kind, logical, today, batchID, slot.ID, slot.Clock, derivedID(settings.CursorSecret, "notification-send", id), nowSeconds(), stamp, stamp, id)
		if e == nil {
			created = 1
		}
		return e
	})
	return created, err
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

func syncEventNotification(tx *sql.Tx, item Notification, status string, receipt, code any) error {
	if item.SourceEvent == nil {
		return nil
	}
	_, err := tx.Exec(`UPDATE bot_inbox SET phase=?,receipt=?,error_code=? WHERE kind='feishu-app' AND message_id=?`, status, receipt, code, *item.SourceEvent)
	return err
}

const notificationColumns = `id,kind,local_date,batch_id,source_event,status,attempts,available_at,receipt,last_error,created_at,updated_at,platform_uuid,channel_key,slot_id,channel_kind,clock,root_id,part_ordinal`

func scanNotification(row interface{ Scan(...any) error }) (Notification, error) {
	var item Notification
	err := row.Scan(&item.ID, &item.Kind, &item.Date, &item.BatchID, &item.SourceEvent, &item.Status, &item.Attempts, &item.AvailableAt, &item.Receipt, &item.LastError, &item.CreatedAt, &item.UpdatedAt, &item.PlatformUUID, &item.channelKey, &item.SlotID, &item.ChannelKind, &item.Clock, &item.RootID, &item.Part)
	return item, err
}

func (s *NotificationService) Detail(id string) (NotificationDetail, error) {
	if !uuidPattern.MatchString(id) {
		return NotificationDetail{}, failure("INVALID_ARGUMENT", "Invalid notification identifier", 422, false)
	}
	item, err := scanNotification(s.Store.state.QueryRow("SELECT "+notificationColumns+` FROM delivery_logs WHERE id=? AND instance_id=? AND user_name=?`, id, s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User))
	if errors.Is(err, sql.ErrNoRows) {
		err = failure("NOTIFICATION_NOT_FOUND", "Notification is unavailable", 404, false)
	}
	return NotificationDetail{item}, err
}

func (s *NotificationService) List(status string, kinds ...string) ([]Notification, error) {
	if status != "" && !slices.Contains([]string{"pending", "running", "sent", "failed"}, status) {
		return nil, failure("INVALID_ARGUMENT", "Invalid notification status", 422, false)
	}
	if len(kinds) > 1 || len(kinds) == 1 && !slices.Contains([]string{"", "review", "reply"}, kinds[0]) {
		return nil, failure("INVALID_ARGUMENT", "Invalid notification kind", 422, false)
	}
	query := "SELECT " + notificationColumns + ` FROM delivery_logs WHERE instance_id=? AND user_name=?`
	args := []any{s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	if len(kinds) == 1 && kinds[0] != "" {
		query += " AND kind=?"
		args = append(args, kinds[0])
	}
	rows, err := s.Store.state.Query(query+` ORDER BY (status='failed') DESC,created_at DESC,root_id,part_ordinal LIMIT 100`, args...)
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
	_, err := s.Store.state.Exec(`UPDATE delivery_logs SET status='failed',last_error='PROCESS_INTERRUPTED',updated_at=? WHERE status='running'`, nowText())
	return err
}

func (s *NotificationService) Deliver(ctx context.Context) (bool, error) {
	if s.Transport == nil && s.channelTransport == nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := s.Store.Settings
	item, err := scanNotification(s.Store.state.QueryRow("SELECT "+notificationColumns+` FROM delivery_logs d
 WHERE status='pending' AND (kind='reply' OR ?) AND available_at<=? AND instance_id=? AND user_name=?
 AND NOT EXISTS(SELECT 1 FROM delivery_logs prior WHERE prior.root_id=d.root_id AND prior.part_ordinal<d.part_ordinal AND prior.status!='sent')
 ORDER BY available_at,created_at,root_id,part_ordinal LIMIT 1`, settings.Notifications.Enabled, nowSeconds(), settings.Memos.InstanceID, settings.Memos.User))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	item.Attempts++
	if _, err = s.Store.state.Exec(`UPDATE delivery_logs SET status='running',attempts=?,updated_at=? WHERE id=?`, item.Attempts, nowText(), item.ID); err != nil {
		return false, err
	}
	transport, closeTransport, sendErr := s.transportFor(item)
	defer closeTransport()
	receipt := ""
	if sendErr == nil {
		receipt, sendErr = transport.Send(ctx, item)
	}
	if sendErr == nil && receipt == "" {
		sendErr = upstreamError("SEND_OUTCOME_UNKNOWN", "Channel returned no receipt")
	}
	status, code := "sent", ""
	available := nowSeconds()
	if sendErr != nil {
		status, code = "failed", errorCode(sendErr)
		var delayed *notificationRetryError
		if errors.As(sendErr, &delayed) {
			status = "pending"
			available += max(delayed.after, time.Second).Seconds()
		}
		slog.Warn("notification failed", "notification_id", item.ID, "code", code)
	}
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
		if e := syncEventNotification(tx, item, status, nullable(receipt), nullable(code)); e != nil {
			return e
		}
		_, e := tx.Exec(`UPDATE delivery_logs SET status=?,receipt=?,last_error=?,available_at=?,updated_at=? WHERE id=?`, status, nullable(receipt), nullable(code), available, nowText(), item.ID)
		return e
	})
	return true, err
}

func nullable(text string) any {
	if text == "" {
		return nil
	}
	return text
}

func (s *NotificationService) Retry(ctx context.Context, id string) (NotificationDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	detail, err := s.Detail(id)
	if err != nil {
		return detail, err
	}
	if detail.Notification.Status != "failed" {
		return detail, failure("NOTIFICATION_CONFLICT", "Only a failed delivery can be retried", 409, false)
	}
	_, err = s.Store.state.ExecContext(ctx, `UPDATE delivery_logs SET status='pending',last_error=NULL,available_at=0,updated_at=? WHERE id=? AND status='failed'`, nowText(), id)
	if err != nil {
		return detail, err
	}
	s.Store.wakeChannels()
	return s.Detail(id)
}

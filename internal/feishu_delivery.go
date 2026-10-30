package moerise

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

type FeishuDelivery struct {
	Store   *Store
	Reviews *ReviewService
	Events  *FeishuService
	http    *http.Client
	images  feishuImagePlatform
}

func NewFeishuDelivery(store *Store, reviews *ReviewService, events *FeishuService) NotificationTransport {
	return newFeishuDelivery(store.Settings.Channel, store, reviews, events)
}

func newFeishuDelivery(config ChannelConfig, store *Store, reviews *ReviewService, events *FeishuService) NotificationTransport {
	if events == nil && (config.Kind != "feishu-webhook" || configured(config.WebhookURL) == "") {
		return nil
	}
	return &FeishuDelivery{Store: store, Reviews: reviews, Events: events, images: NewFeishuClient(config),
		http: &http.Client{Timeout: 15 * time.Second, Transport: http.DefaultTransport.(*http.Transport).Clone(),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *FeishuDelivery) Send(ctx context.Context, item Notification) (string, error) {
	if item.Kind == "reply" {
		if s.Events == nil || item.SourceEvent == nil {
			return "", upstreamError("CHANNEL_UNAVAILABLE", "Feishu application replies are unavailable")
		}
		settings := s.Store.Settings
		var kind, resource string
		if err := s.Store.state.QueryRow(`SELECT result_kind, resource_name FROM channel_events
			WHERE message_id = ? AND app_id = ? AND sender_open_id = ?`, *item.SourceEvent,
			configured(settings.Channel.AppID), configured(settings.Channel.UserOpenID)).Scan(&kind, &resource); err != nil {
			return "", err
		}
		if kind == "ai" {
			return s.Events.sendAIPart(ctx, item)
		}
		text, err := s.Events.render(ctx, kind, resource)
		if err != nil {
			return "", err
		}
		return s.Events.Client.Reply(ctx, *item.SourceEvent, item.PlatformUUID, text)
	}
	if item.BatchID == nil {
		return "", failure("INVALID_ARGUMENT", "Review notification has no batch", 422, false)
	}
	return s.sendReviewPart(ctx, item)
}

func (s *FeishuDelivery) Check(ctx context.Context, item Notification, receipt string) (bool, error) {
	if s.Store.Settings.Channel.Kind == "feishu-webhook" || s.Events == nil {
		return false, failure("CHANNEL_RECEIPT_UNSUPPORTED", "Webhook outcomes require operator confirmation without a receipt", 422, false)
	}
	var used bool
	if err := s.Store.state.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM notifications WHERE receipt = ? AND id <> ?
		UNION ALL SELECT 1 FROM notification_attempts WHERE receipt = ? AND notification_id <> ?)`,
		receipt, item.ID, receipt, item.ID).Scan(&used); err != nil {
		return false, err
	}
	if used {
		return false, nil
	}
	message, err := s.Events.Client.GetMessage(ctx, receipt)
	if err != nil {
		return false, err
	}
	if message.ID != receipt || message.Deleted || message.SenderType != "app" ||
		message.SenderIDType != "app_id" || message.SenderID != configured(s.Store.Settings.Channel.AppID) {
		return false, nil
	}
	// Platform timestamps have millisecond precision; compare with the first send attempt.
	var started *string
	if err := s.Store.state.QueryRow(`SELECT MIN(started_at) FROM notification_attempts WHERE notification_id = ?`,
		item.ID).Scan(&started); err != nil {
		return false, err
	}
	if started != nil && !message.CreateTime.IsZero() {
		at, err := time.Parse(time.RFC3339Nano, *started)
		if err != nil {
			return false, err
		}
		if message.CreateTime.Before(at.Add(-5*time.Second).Truncate(time.Millisecond)) || message.CreateTime.After(time.Now().Add(time.Minute)) {
			return false, nil
		}
	}
	if message.ChatID != "" {
		matches, err := s.receiptChatMatches(ctx, item, message.ChatID)
		if err != nil || !matches {
			return false, err
		}
	}
	if item.Kind == "reply" {
		if item.SourceEvent == nil || message.ParentID != *item.SourceEvent {
			return false, nil
		}
		var ai bool
		if err := s.Store.state.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM feishu_ai_parts WHERE notification_id = ?)`, item.ID).Scan(&ai); err != nil {
			return false, err
		}
		if ai {
			return s.Events.checkAIPart(item, message)
		}
		return true, nil
	}
	part, err := s.reviewPart(item.ID)
	if errors.Is(err, sql.ErrNoRows) || item.BatchID == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if part.Kind == "header" {
		return message.Text == "「今日回顾」", nil
	}
	if part.Kind == "image" {
		return part.ImageKey != "" && part.ImageKey == message.ImageKey, nil
	}
	if part.Kind != "text" && part.Kind != "footer" {
		return false, nil
	}
	note, err := s.reviewNote(ctx, part)
	if err != nil {
		return false, err
	}
	var text string
	if part.Kind == "footer" {
		text, err = s.reviewFooter(note)
	} else {
		text, err = reviewPartText(part, note)
	}
	return err == nil && text == message.Text, err
}

func (s *FeishuDelivery) receiptChatMatches(ctx context.Context, item Notification, chat string) (bool, error) {
	settings := s.Store.Settings
	var reference string
	err := s.Store.state.QueryRow(`SELECT message_id FROM channel_events
		WHERE app_id = ? AND sender_open_id = ? LIMIT 1`,
		configured(settings.Channel.AppID), configured(settings.Channel.UserOpenID)).Scan(&reference)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.Store.state.QueryRow(`SELECT a.receipt FROM notification_attempts a
			JOIN notifications n ON n.id = a.notification_id
			WHERE n.instance_id = ? AND n.user_name = ? AND n.channel_key = ?
			AND n.id <> ? AND a.outcome = 'sent' AND a.receipt IS NOT NULL
			AND a.receipt NOT LIKE 'empty:%' ORDER BY a.started_at DESC LIMIT 1`,
			settings.Memos.InstanceID, settings.Memos.User,
			NewNotificationService(s.Store, s.Reviews, nil).channelKey(), item.ID).Scan(&reference)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	message, err := s.Events.Client.GetMessage(ctx, reference)
	if err != nil {
		return false, err
	}
	return message.ID == reference && message.ChatID != "" && message.ChatID == chat, nil
}

func (s *FeishuDelivery) webhook(ctx context.Context, id, text string) (string, error) {
	return s.webhookMessage(ctx, id, "text", map[string]string{"text": text})
}

func (s *FeishuDelivery) webhookMessage(ctx context.Context, id, kind string, content map[string]string) (string, error) {
	config := s.Store.Settings.Channel
	body := map[string]any{"msg_type": kind, "content": content}
	if secret := configured(config.WebhookSecret); secret != "" {
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(stamp+"\n"+secret))
		body["timestamp"], body["sign"] = stamp, base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	data, _ := json.Marshal(body)
	if len(data) > 20*1024 {
		return "", failure("CHANNEL_MESSAGE_SIZE", "Webhook message exceeds 20 KiB", 422, false)
	}
	request, err := http.NewRequestWithContext(ctx, "POST", configured(config.WebhookURL), bytes.NewReader(data))
	if err != nil {
		return "", failure("CHANNEL_PROTOCOL", "Invalid Webhook URL", 422, false)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.http.Do(request)
	if err != nil {
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "Webhook send outcome is unknown")
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 {
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "Webhook send outcome is unknown")
	}
	if response.StatusCode != 200 {
		return "", upstreamError("CHANNEL_REJECTED", "Webhook rejected the notification")
	}
	var result struct {
		Code   *int `json:"code"`
		Legacy *int `json:"StatusCode"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result) != nil ||
		result.Code == nil && result.Legacy == nil {
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "Webhook returned an untrusted response")
	}
	if result.Code != nil && *result.Code != 0 || result.Legacy != nil && *result.Legacy != 0 {
		return "", upstreamError("CHANNEL_REJECTED", "Webhook rejected the notification")
	}
	// Custom robots acknowledge delivery but do not return a queryable message resource.
	return "webhook:" + id, nil
}

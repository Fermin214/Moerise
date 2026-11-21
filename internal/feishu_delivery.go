package moerise

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

type FeishuDelivery struct {
	config  ChannelConfig
	Store   *Store
	Reviews *ReviewService
	Events  *FeishuService
	http    *http.Client
	images  feishuImagePlatform
}

func newFeishuDelivery(config ChannelConfig, store *Store, reviews *ReviewService, events *FeishuService) NotificationTransport {
	if config.Kind == "feishu-webhook" {
		events = nil
		if configured(config.WebhookURL) == "" {
			return nil
		}
	} else if config.Kind != "feishu-app" || events == nil {
		return nil
	}
	return &FeishuDelivery{config: config, Store: store, Reviews: reviews, Events: events, images: NewFeishuClient(config),
		http: &http.Client{Timeout: 15 * time.Second, Transport: http.DefaultTransport.(*http.Transport).Clone(),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *FeishuDelivery) Send(ctx context.Context, item Notification) (string, error) {
	if item.Kind == "reply" {
		if s.Events == nil || item.SourceEvent == nil {
			return "", upstreamError("CHANNEL_UNAVAILABLE", "Feishu application replies are unavailable")
		}
		owner, err := s.Store.feishuOwner()
		if err != nil {
			return "", err
		}
		if owner == "" || owner != configured(s.config.UserOpenID) || configured(s.config.AppID) != configured(s.Store.Settings.Channel.AppID) {
			return "", failure("CHANNEL_BINDING_CHANGED", "Feishu private chat changed", 409, false)
		}
		var kind, resource string
		if err := s.Store.state.QueryRow(`SELECT result_kind,resource_name FROM bot_inbox WHERE kind='feishu-app' AND message_id=? AND bot_id=? AND peer=?`, *item.SourceEvent,
			configured(s.config.AppID), owner).Scan(&kind, &resource); err != nil {
			return "", err
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
	return s.reviewDelivery().sendReviewPart(ctx, item)
}

func (s *FeishuDelivery) webhook(ctx context.Context, id, text string) (string, error) {
	return s.webhookMessage(ctx, id, "text", map[string]string{"text": text})
}

func (s *FeishuDelivery) webhookMessage(ctx context.Context, id, kind string, content map[string]string) (string, error) {
	config := s.config
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
	if response.StatusCode == http.StatusTooManyRequests {
		return "", feishuRateLimit(response.Header)
	}
	if response.StatusCode >= 500 {
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "Webhook send outcome is unknown")
	}
	if response.StatusCode != 200 {
		return "", upstreamError("CHANNEL_REJECTED", "Webhook rejected the notification")
	}
	var result struct {
		Code       *int `json:"code"`
		StatusCode *int `json:"StatusCode"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result) != nil ||
		result.Code == nil && result.StatusCode == nil {
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "Webhook returned an untrusted response")
	}
	if result.Code != nil && *result.Code != 0 || result.StatusCode != nil && *result.StatusCode != 0 {
		if result.Code != nil && *result.Code == 9499 {
			return "", feishuRateLimit(response.Header)
		}
		return "", upstreamError("CHANNEL_REJECTED", "Webhook rejected the notification")
	}
	// Custom robots acknowledge delivery but do not return a queryable message resource.
	return "webhook:" + id, nil
}

func feishuRateLimit(header http.Header) error {
	delay := time.Minute
	for _, field := range []string{"Retry-After", "X-Ogw-Ratelimit-Reset"} {
		if seconds, err := strconv.ParseInt(header.Get(field), 10, 32); err == nil && seconds > 0 {
			delay = time.Duration(seconds) * time.Second
			break
		}
	}
	return &notificationRetryError{error: upstreamError("CHANNEL_RATE_LIMITED", "Feishu rate limit reached"), after: delay}
}

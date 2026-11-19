package moerise

import "time"

type notificationRetryError struct {
	error
	after time.Duration
}

func (e *notificationRetryError) Unwrap() error { return e.error }

func newNotificationTransport(channel ChannelConfig, store *Store, reviews *ReviewService, events *FeishuService) NotificationTransport {
	switch channel.Kind {
	case "qq":
		return newQQDelivery(channel, store, reviews)
	case "telegram":
		return newTelegramDelivery(channel, store, reviews)
	case "feishu-app", "feishu-webhook":
		return newFeishuDelivery(channel, store, reviews, events)
	}
	return nil
}

func (s *Store) feishuReplyChannel() ChannelConfig {
	config := s.Settings.Channel
	config.Kind = "feishu-app"
	return s.channelForDelivery(config)
}

func (product *ProductSettings) normalizeDeliveryChannels() {
	compact := func(channel ChannelConfig) ChannelConfig {
		result := ChannelConfig{Kind: channel.Kind}
		switch channel.Kind {
		case "telegram":
			result.BotToken, result.Destination = channel.BotToken, channel.Destination
		case "qq":
			result.QQAppID, result.QQAppSecret, result.Destination = channel.QQAppID, channel.QQAppSecret, channel.Destination
		case "feishu-app":
			result.AppID, result.AppSecret, result.UserOpenID = channel.AppID, channel.AppSecret, channel.UserOpenID
			result.Destination = channel.UserOpenID
		case "feishu-webhook":
			result.WebhookURL, result.WebhookSecret = channel.WebhookURL, channel.WebhookSecret
			result.AppID, result.AppSecret = channel.AppID, channel.AppSecret
		}
		return result
	}
	for key, channel := range product.NotificationChannels {
		product.NotificationChannels[key] = compact(channel)
	}
	if product.Channel.FeishuMode == "" {
		old := product.Channel
		frozen := compact(old)
		if key := notificationChannelKey(old); key != notificationChannelKey(frozen) {
			if product.NotificationChannels == nil {
				product.NotificationChannels = make(map[string]ChannelConfig)
			}
			product.NotificationChannels[key] = frozen
		}
	}
	product.Channel.migrateLegacy()
}

func (s *NotificationService) slotAfterBinding(moment time.Time) (bool, error) {
	settings := s.Store.Settings
	var restored int64
	if err := s.Store.state.QueryRow(`SELECT COALESCE((SELECT restored_at FROM bot_restore WHERE instance_id = ?), 0)`,
		settings.Memos.InstanceID).Scan(&restored); err != nil || moment.UnixMilli() <= restored {
		return false, err
	}
	config := settings.Channel
	switch config.Kind {
	case "qq":
	case "feishu-app":
		if configured(config.UserOpenID) != "" {
			return true, nil
		}
	case "telegram":
		if configured(config.Destination) != "" {
			return true, nil
		}
	default:
		return true, nil
	}
	peer, at, err := s.Store.botPeer(config.Kind, config.botID(config.Kind))
	return peer != "" && moment.After(at), err
}

// 只保存尚需发送或确认的渠道；凭据随产品设置加密持久化。
func (s *Store) retainNotificationChannels(settings *Settings) error {
	oldChannel, nextChannel := s.deliveryChannel(), s.channelForDelivery(settings.Channel)
	oldReply := s.feishuReplyChannel()
	nextReplyConfig := settings.Channel
	nextReplyConfig.Kind = "feishu-app"
	nextReply := s.channelForDelivery(nextReplyConfig)
	rows, err := s.state.Query(`SELECT DISTINCT kind,channel_key FROM notifications
		WHERE instance_id = ? AND user_name = ? AND (status IN ('pending','running','unknown')
			OR status = 'skipped' AND last_error = 'PART_INTERRUPTED')`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User)
	if err != nil {
		return err
	}
	defer rows.Close()
	channels := map[string]ChannelConfig{}
	for rows.Next() {
		var kind, key string
		if err := rows.Scan(&kind, &key); err != nil {
			return err
		}
		old, next := oldChannel, nextChannel
		if kind == "reply" {
			old, next = oldReply, nextReply
		}
		if key == notificationChannelKey(next) {
			continue
		} else if key == notificationChannelKey(old) {
			channels[key] = old
		} else if channel, exists := s.Settings.notificationChannels[key]; exists {
			channels[key] = channel
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	settings.notificationChannels = channels
	return nil
}

type resolvedTransport struct {
	key       string
	transport NotificationTransport
	close     func()
}

func (s *NotificationService) closeResolvedTransports() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cached := range s.resolved {
		cached.close()
	}
	clear(s.resolved)
}

func (s *NotificationService) transportFor(item Notification) (NotificationTransport, func(), error) {
	closeTransport := func() {}
	channel := s.Store.deliveryChannel()
	if item.Kind == "reply" {
		channel = s.Store.feishuReplyChannel()
		key := item.channelKey
		if previous, ok := s.Store.Settings.notificationChannels[key]; ok {
			key = notificationChannelKey(previous)
		}
		if notificationChannelKey(channel) != key || !channel.botEnabled("feishu-app") {
			return nil, closeTransport, failure("CHANNEL_BINDING_CHANGED", "Feishu private chat changed", 409, false)
		}
		item.channelKey = key
	}
	resolvedKey := notificationChannelKey(channel)
	current := item.channelKey == "" || item.channelKey == resolvedKey
	if current {
		if s.Transport != nil && (s.transportKey == resolvedKey || s.transportKey == "") {
			return s.Transport, closeTransport, nil
		}
		if cached, ok := s.resolved[channel.Kind]; ok {
			if cached.key == resolvedKey {
				return cached.transport, closeTransport, nil
			}
			cached.close()
			delete(s.resolved, channel.Kind)
		}
	} else {
		var exists bool
		channel, exists = s.Store.Settings.notificationChannels[item.channelKey]
		if !exists {
			return nil, closeTransport, upstreamError("CHANNEL_UNAVAILABLE", "Original notification channel is unavailable")
		}
	}
	if s.channelTransport != nil {
		transport, closeChannel := s.channelTransport(channel)
		if transport != nil {
			if current {
				if s.resolved == nil {
					s.resolved = map[string]resolvedTransport{}
				}
				s.resolved[channel.Kind] = resolvedTransport{resolvedKey, transport, closeChannel}
				return transport, closeTransport, nil
			}
			return transport, closeChannel, nil
		}
		closeChannel()
	}
	return nil, closeTransport, upstreamError("CHANNEL_UNAVAILABLE", "Original notification channel is unavailable")
}

func (a *App) notificationTransport(channel ChannelConfig) (NotificationTransport, func()) {
	var events *FeishuService
	if channel.Kind == "feishu-app" {
		current := a.Store.Settings.Channel
		if a.Feishu != nil && configured(channel.AppID) == configured(current.AppID) &&
			configured(channel.AppSecret) == configured(current.AppSecret) {
			transport := newFeishuDelivery(channel, a.Store, a.Reviews, a.Feishu)
			return transport, func() { closeChannel(nil, transport) }
		}
		events = newFeishuService(channel, a.Store, a.Source, a.Records, a.Notifications)
	}
	transport := newNotificationTransport(channel, a.Store, a.Reviews, events)
	return transport, func() { closeChannel(events, transport) }
}

func (s *NotificationService) resetTransport(config ChannelConfig, records *FeishuService) {
	s.transportKey = notificationChannelKey(config)
	s.Transport = newNotificationTransport(config, s.Store, s.Reviews, records)
}

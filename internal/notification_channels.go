package moerise

// 只保存尚需发送或确认的渠道；凭据随产品设置加密持久化。
func (s *Store) retainNotificationChannels(settings *Settings) error {
	rows, err := s.state.Query(`SELECT DISTINCT channel_key FROM notifications
		WHERE instance_id = ? AND user_name = ? AND status <> 'sent'`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User)
	if err != nil {
		return err
	}
	defer rows.Close()
	channels := map[string]ChannelConfig{}
	oldKey, nextKey := notificationChannelKey(s.Settings.Channel), notificationChannelKey(settings.Channel)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		if key == nextKey {
			continue
		}
		if key == oldKey {
			channels[key] = s.Settings.Channel
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

func (s *NotificationService) transportFor(item Notification) (NotificationTransport, func(), error) {
	closeTransport := func() {}
	if item.channelKey == "" || item.channelKey == s.channelKey() {
		if s.Transport != nil {
			return s.Transport, closeTransport, nil
		}
	} else if channel, exists := s.Store.Settings.notificationChannels[item.channelKey]; exists && s.channelTransport != nil {
		transport, closeChannel := s.channelTransport(channel)
		if transport != nil {
			return transport, closeChannel, nil
		}
		closeChannel()
	}
	return nil, closeTransport, upstreamError("CHANNEL_UNAVAILABLE", "Original notification channel is unavailable")
}

func (a *App) notificationTransport(channel ChannelConfig) (NotificationTransport, func()) {
	// 队列和业务服务共享，渠道配置独立，旧任务始终向原接收方发送。
	store := &Store{Settings: a.Store.Settings, state: a.Store.state, index: a.Store.index}
	store.Settings.Channel = channel
	notifications := NewNotificationService(store, a.Reviews, nil)
	events := newFeishuService(channel, store, a.Source, a.Records, a.Reviews, notifications)
	if events != nil {
		events.Search, events.AI = a.Search, a.AI
	}
	transport := newFeishuDelivery(channel, store, a.Reviews, events)
	return transport, func() { closeChannel(events, transport) }
}

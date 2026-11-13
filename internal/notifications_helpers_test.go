package moerise

func (s *NotificationService) enqueue(kind, logical, date string, batch, event *string) (string, error) {
	settings := s.Store.Settings
	channel := s.channelKey()
	id := derivedID(settings.CursorSecret, "notification", settings.Memos.InstanceID, settings.Memos.User, channel, logical)
	stamp := nowText()
	_, err := s.Store.state.Exec(`INSERT OR IGNORE INTO notifications
		(id, instance_id, user_name, channel_key, logical_key, kind, local_date, batch_id, source_event, slot_id,
		 status, platform_uuid, available_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT id FROM review_plan_slots WHERE batch_id = ? ORDER BY ordinal LIMIT 1), 'pending', ?, ?, ?, ?)`, id,
		settings.Memos.InstanceID, settings.Memos.User, channel, logical, kind, date, batch, event, batch,
		derivedID(settings.CursorSecret, "notification-send", id), nowSeconds(), stamp, stamp)
	if err != nil {
		return "", err
	}
	var saved string
	err = s.Store.state.QueryRow(`SELECT id FROM notifications WHERE instance_id = ? AND user_name = ?
		AND channel_key = ? AND logical_key = ?`, settings.Memos.InstanceID, settings.Memos.User, channel, logical).Scan(&saved)
	return saved, err
}

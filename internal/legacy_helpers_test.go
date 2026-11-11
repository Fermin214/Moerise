package moerise

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

func (s *SearchService) recentReads() (map[string]bool, error) {
	settings := s.Store.Settings
	return namesQuery(s.Store.state, `SELECT memo_name FROM discovery_reads WHERE instance_id = ? AND user_name = ?
		AND julianday(opened_at) >= julianday('now', '-7 days') UNION SELECT i.memo_name FROM review_items i
		JOIN review_batches b ON b.id = i.batch_id WHERE b.instance_id = ? AND b.user_name = ?
		AND julianday(i.opened_at) >= julianday('now', '-7 days')`,
		settings.Memos.InstanceID, settings.Memos.User, settings.Memos.InstanceID, settings.Memos.User)
}

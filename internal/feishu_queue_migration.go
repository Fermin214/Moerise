package moerise

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
)

// migrateFeishuQueue 使用旧设置执行，调用前须排空使用队列的工作单元。
func (s *Store) migrateFeishuQueue() error {
	if s.Settings.Web.StateEncryptionKey == "" {
		return nil
	}
	if _, err := newStateCipher(s.Settings, "feishu-queue"); err != nil {
		return err
	}
	return transaction(s.state, func(tx *sql.Tx) error {
		if err := s.migrateFeishuEvents(tx); err != nil {
			return err
		}
		for _, field := range []struct {
			query  string
			update string
		}{
			{`SELECT g.message_id, e.app_id, e.sender_open_id, 'ai-request:' || g.message_id, g.request
				FROM feishu_ai_generations g JOIN channel_events e ON e.message_id = g.message_id`,
				"UPDATE feishu_ai_generations SET request = ? WHERE message_id = ?"},
			{`SELECT g.message_id, e.app_id, e.sender_open_id, 'ai-result:' || g.message_id, g.result
				FROM feishu_ai_generations g JOIN channel_events e ON e.message_id = g.message_id
				WHERE g.result IS NOT NULL`,
				"UPDATE feishu_ai_generations SET result = ? WHERE message_id = ?"},
			{`SELECT p.notification_id, e.app_id, e.sender_open_id,
				'ai-part:' || p.message_id || ':' || p.ordinal, p.payload
				FROM feishu_ai_parts p JOIN channel_events e ON e.message_id = p.message_id`,
				"UPDATE feishu_ai_parts SET payload = ? WHERE notification_id = ?"},
		} {
			if err := s.migrateFeishuField(tx, field.query, field.update); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) feishuQueueBinding(appID, sender string) *FeishuService {
	settings := s.Settings
	settings.Channel.AppID, settings.Channel.UserOpenID = &appID, &sender
	return &FeishuService{Store: &Store{Settings: settings}}
}

func (s *FeishuService) migrateQueueCipher(message string, payload []byte) ([]byte, error) {
	body, stable, err := s.openQueue(message, payload)
	if err != nil {
		return nil, err
	}
	if stable {
		return payload, nil
	}
	return s.seal(message, body)
}

func (s *Store) migrateFeishuEvents(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT message_id, app_id, sender_open_id, payload,
		COALESCE(result_kind, ''), resource_name FROM channel_events ORDER BY message_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type event struct {
		id       string
		payload  []byte
		resource sql.NullString
	}
	var updates []event
	for rows.Next() {
		var item event
		var appID, sender, kind string
		if err := rows.Scan(&item.id, &appID, &sender, &item.payload, &kind, &item.resource); err != nil {
			return err
		}
		service := s.feishuQueueBinding(appID, sender)
		changed := false
		if item.payload != nil {
			payload, err := service.migrateQueueCipher(item.id, item.payload)
			if err != nil {
				return fmt.Errorf("migrate Feishu event payload: %w", err)
			}
			changed, item.payload = !bytes.Equal(payload, item.payload), payload
		}
		if kind == "search" || kind == "walk" {
			id, encoded, ok := strings.Cut(item.resource.String, ".")
			if !ok || id != item.id {
				return upstreamError("CHANNEL_QUEUE_KEY", "Invalid encrypted Feishu result reference")
			}
			payload, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
			if err != nil {
				return upstreamError("CHANNEL_QUEUE_KEY", "Invalid encrypted Feishu result")
			}
			migrated, err := service.migrateQueueCipher(kind+":"+id, payload)
			if err != nil {
				return fmt.Errorf("migrate Feishu event result: %w", err)
			}
			if !bytes.Equal(payload, migrated) {
				changed = true
				item.resource.String = id + "." + base64.RawURLEncoding.EncodeToString(migrated)
			}
		}
		if changed {
			updates = append(updates, item)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := tx.Exec("UPDATE channel_events SET payload = ?, resource_name = ? WHERE message_id = ?",
			item.payload, item.resource, item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateFeishuField(tx *sql.Tx, query, update string) error {
	rows, err := tx.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	type field struct {
		id      string
		payload []byte
	}
	var updates []field
	for rows.Next() {
		var item field
		var appID, sender, message string
		if err := rows.Scan(&item.id, &appID, &sender, &message, &item.payload); err != nil {
			return err
		}
		payload, err := s.feishuQueueBinding(appID, sender).migrateQueueCipher(message, item.payload)
		if err != nil {
			return fmt.Errorf("migrate Feishu AI queue: %w", err)
		}
		if !bytes.Equal(payload, item.payload) {
			item.payload = payload
			updates = append(updates, item)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := tx.Exec(update, item.payload, item.id); err != nil {
			return err
		}
	}
	return nil
}

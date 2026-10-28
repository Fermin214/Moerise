package moerise

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// The encrypted queue format fixes text semantics at acceptance; event hashes stay unchanged.
type queuedFeishuMessage struct {
	feishuMessage
	VerbatimText bool `json:"verbatim_text,omitempty"`
}

func (s *FeishuService) executeQueued(ctx context.Context, message queuedFeishuMessage) (string, string, error) {
	if message.VerbatimText || message.Type != "text" {
		return s.execute(ctx, message.feishuMessage)
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(message.Content), &content); err != nil {
		return s.execute(ctx, message.feishuMessage)
	}
	text := strings.TrimSpace(content.Text)
	fields := strings.Fields(text)
	if len(fields) == 0 || fields[0] != "记录" {
		return s.execute(ctx, message.feishuMessage)
	}
	key := "feishu:" + hashBytes([]byte(message.ID))
	// Unmarked events may already have a write accepted by the verbatim entrypoint.
	if _, hash, err := prepareNote(CreateNote{Content: content.Text}); err == nil {
		var savedHash string
		err := s.Store.state.QueryRow(`SELECT request_hash FROM note_writes
			WHERE instance_id = ? AND user_name = ? AND idempotency_key = ?`,
			s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, key).Scan(&savedHash)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
		if err == nil && savedHash == hash {
			return s.execute(ctx, message.feishuMessage)
		}
	}
	note, _, err := s.Records.Create(ctx, key, CreateNote{Content: strings.TrimSpace(strings.TrimPrefix(text, "记录"))})
	return "saved", note.Name, err
}

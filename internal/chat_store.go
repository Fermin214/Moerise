package moerise

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

func chatNotFound() error {
	return failure("CHAT_NOT_FOUND", "Chat is unavailable", 404, false)
}

func chatConflict() error {
	return failure("CHAT_CONFLICT", "Chat version has changed; reload before retrying", 409, false)
}

func chatBusy() error {
	return failure("CHAT_BUSY", "Chat generation is still running", 409, true)
}

func validateChat(title string, materials []ChatMaterial) error {
	if !utf8.ValidString(title) || len(title) > 800 || utf8.RuneCountInString(title) > 200 {
		return failure("INVALID_ARGUMENT", "Chat requires a title of at most 200 characters", 422, false)
	}
	seen := map[string]bool{}
	for _, material := range materials {
		if !memoPattern.MatchString(material.Name) || seen[material.Name] || len(material.SourceHash) != 64 ||
			strings.Trim(material.SourceHash, "0123456789abcdef") != "" {
			return failure("INVALID_ARGUMENT", "Chat materials require distinct memo names and source hashes", 422, false)
		}
		seen[material.Name] = true
	}
	return nil
}

func putChatMaterials(tx *sql.Tx, id string, materials []ChatMaterial) error {
	if _, err := tx.Exec("DELETE FROM chat_materials WHERE chat_id = ?", id); err != nil {
		return err
	}
	for i, material := range materials {
		if _, err := tx.Exec("INSERT INTO chat_materials VALUES (?, ?, ?, ?)", id, i, material.Name, material.SourceHash); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) createChat(key, title string, materials []ChatMaterial) (ChatSession, bool, error) {
	if len(materials) == 0 {
		return ChatSession{}, false, failure("INVALID_ARGUMENT", "Select at least one memo", 422, false)
	}
	if !idempotencyPattern.MatchString(key) {
		return ChatSession{}, false, failure("INVALID_ARGUMENT", "Chat requires a stable request_id", 422, false)
	}
	if err := validateChat(title, materials); err != nil {
		return ChatSession{}, false, err
	}
	names := make([]string, len(materials))
	for i, material := range materials {
		names[i] = material.Name
	}
	hash := hashBytes(canonicalJSON([]any{title, names}))
	id, created := newID(), false
	err := transaction(s.state, func(tx *sql.Tx) error {
		var savedHash string
		err := tx.QueryRow(`SELECT id, request_hash FROM chat_sessions
			WHERE instance_id = ? AND user_name = ? AND creation_key = ?`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, key).Scan(&id, &savedHash)
		if err == nil {
			if savedHash != hash {
				return failure("IDEMPOTENCY_CONFLICT", "This request_id belongs to a different chat request", 409, false)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		stamp := nowText()
		if _, err := tx.Exec(`INSERT INTO chat_sessions
			(id, instance_id, user_name, creation_key, request_hash, title, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, s.Settings.Memos.InstanceID, s.Settings.Memos.User, key, hash, title, stamp, stamp); err != nil {
			return err
		}
		created = true
		return putChatMaterials(tx, id, materials)
	})
	if err != nil {
		return ChatSession{}, false, err
	}
	chat, err := s.chat(id)
	return chat, created, err
}

// 通过同一事务读取摘要、材料和消息，避免混合不同的会话版本。
func (s *Store) chat(id string) (ChatSession, error) {
	result := ChatSession{Materials: []ChatMaterial{}, Turns: []ChatTurn{}}
	err := transaction(s.state, func(tx *sql.Tx) error {
		if err := s.readChatSummary(tx, id, &result.ChatSummary); err != nil {
			return err
		}
		rows, err := tx.Query("SELECT memo_name, source_hash FROM chat_materials WHERE chat_id = ? ORDER BY position", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ref ChatMaterial
			if err := rows.Scan(&ref.Name, &ref.SourceHash); err != nil {
				rows.Close()
				return err
			}
			result.Materials = append(result.Materials, ref)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.Query(chatTurnQuery+" WHERE t.chat_id = ? ORDER BY t.position", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			turn, err := scanChatTurn(rows)
			if err != nil {
				return err
			}
			result.Turns = append(result.Turns, turn)
		}
		return rows.Err()
	})
	return result, err
}

const chatSummaryQuery = `SELECT id, title,
		COALESCE((SELECT CASE WHEN kind = 'insight' THEN '一键洞察' ELSE substr(user_content, 1, 80) END
			FROM chat_turns WHERE chat_id = c.id ORDER BY position LIMIT 1), title),
		COALESCE((SELECT kind FROM chat_turns WHERE chat_id = c.id ORDER BY position LIMIT 1), ''), version,
		(SELECT COUNT(*) FROM chat_materials WHERE chat_id = c.id), created_at, updated_at
		FROM chat_sessions c`

func (s *Store) readChatSummary(tx *sql.Tx, id string, result *ChatSummary) error {
	err := tx.QueryRow(chatSummaryQuery+` WHERE id = ? AND instance_id = ? AND user_name = ?`,
		id, s.Settings.Memos.InstanceID, s.Settings.Memos.User).Scan(&result.ID, &result.Title, &result.Preview, &result.PreviewKind, &result.Version,
		&result.MaterialCount, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return chatNotFound()
	}
	return err
}

func (s *Store) listChats(beforeTime, beforeID string, limit int) ([]ChatSummary, error) {
	result := []ChatSummary{}
	if limit < 1 || limit > 100 {
		return result, failure("INVALID_ARGUMENT", "Invalid chat page size", 422, false)
	}
	rows, err := s.state.Query(chatSummaryQuery+` WHERE instance_id = ? AND user_name = ?
		AND (? = '' OR updated_at < ? OR (updated_at = ? AND id < ?))
		ORDER BY updated_at DESC, id DESC LIMIT ?`, s.Settings.Memos.InstanceID, s.Settings.Memos.User,
		beforeTime, beforeTime, beforeTime, beforeID, limit)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var chat ChatSummary
		if err := rows.Scan(&chat.ID, &chat.Title, &chat.Preview, &chat.PreviewKind, &chat.Version, &chat.MaterialCount, &chat.CreatedAt, &chat.UpdatedAt); err != nil {
			return result, err
		}
		result = append(result, chat)
	}
	return result, rows.Err()
}

func (s *Store) updateChat(id string, version int64, title string, materials []ChatMaterial) (ChatSession, error) {
	if err := validateChat(title, materials); err != nil {
		return ChatSession{}, err
	}
	err := transaction(s.state, func(tx *sql.Tx) error {
		var chat ChatSummary
		if err := s.readChatSummary(tx, id, &chat); err != nil {
			return err
		}
		if chat.Version != version {
			return chatConflict()
		}
		var running int
		if err := tx.QueryRow("SELECT COUNT(*) FROM chat_turns WHERE chat_id = ? AND status = 'running'", id).Scan(&running); err != nil {
			return err
		}
		if running != 0 {
			return chatBusy()
		}
		if _, err := tx.Exec("UPDATE chat_sessions SET title = ?, version = version + 1, updated_at = ? WHERE id = ?", title, nowText(), id); err != nil {
			return err
		}
		return putChatMaterials(tx, id, materials)
	})
	if err != nil {
		return ChatSession{}, err
	}
	return s.chat(id)
}

func (s *Store) deleteChat(id string, version int64) error {
	return transaction(s.state, func(tx *sql.Tx) error {
		var chat ChatSummary
		if err := s.readChatSummary(tx, id, &chat); err != nil {
			return err
		}
		if chat.Version != version {
			return chatConflict()
		}
		var running int
		if err := tx.QueryRow("SELECT COUNT(*) FROM chat_turns WHERE chat_id = ? AND status = 'running'", id).Scan(&running); err != nil {
			return err
		}
		if running != 0 {
			return chatBusy()
		}
		_, err := tx.Exec("DELETE FROM chat_sessions WHERE id = ?", id)
		return err
	})
}

const chatTurnQuery = `SELECT t.id, t.request_id, t.kind, t.user_content, t.assistant_content,
	t.message_version, t.status, t.error_code, t.settings_version, t.prompt, t.model, t.materials_json,
	t.usage_json, t.created_at, t.updated_at, COALESCE(s.memo_name, '')
	FROM chat_turns t LEFT JOIN chat_saves s ON s.turn_id = t.id`

func scanChatTurn(row interface{ Scan(...any) error }) (ChatTurn, error) {
	var turn ChatTurn
	var materials, usage string
	err := row.Scan(&turn.ID, &turn.RequestID, &turn.Kind, &turn.User.Content, &turn.Assistant.Content,
		&turn.Assistant.Version, &turn.Assistant.Status, &turn.Assistant.ErrorCode, &turn.SettingsVersion,
		&turn.Prompt, &turn.Model, &materials, &usage, &turn.User.CreatedAt, &turn.Assistant.UpdatedAt, &turn.Assistant.SavedName)
	if err != nil {
		return turn, err
	}
	turn.User.ID, turn.Assistant.ID = turn.ID+":user", turn.ID+":assistant"
	turn.User.Role, turn.Assistant.Role = "user", "assistant"
	turn.User.Version, turn.User.Status, turn.User.UpdatedAt = 1, "completed", turn.User.CreatedAt
	turn.Assistant.CreatedAt = turn.User.CreatedAt
	if err := json.Unmarshal([]byte(materials), &turn.Materials); err != nil {
		return turn, err
	}
	err = json.Unmarshal([]byte(usage), &turn.Usage)
	return turn, err
}

func (s *Store) chatTurn(chatID, requestID string) (ChatTurn, error) {
	turn, err := scanChatTurn(s.state.QueryRow(chatTurnQuery+` JOIN chat_sessions c ON c.id = t.chat_id
		WHERE t.chat_id = ? AND t.request_id = ? AND c.instance_id = ? AND c.user_name = ?`,
		chatID, requestID, s.Settings.Memos.InstanceID, s.Settings.Memos.User))
	if errors.Is(err, sql.ErrNoRows) {
		return turn, failure("CHAT_MESSAGE_NOT_FOUND", "Chat message is unavailable", 404, false)
	}
	return turn, err
}

func chatGenerateHash(input ChatGenerate) string { return hashBytes(canonicalJSON(input)) }

func validateChatGenerate(input ChatGenerate) error {
	if !idempotencyPattern.MatchString(input.RequestID) || input.Version < 1 ||
		(input.Kind != "insight" && input.Kind != "question") || !utf8.ValidString(input.Content) ||
		(input.Kind == "question" && strings.TrimSpace(input.Content) == "") ||
		(input.Kind == "insight" && input.Content != "") ||
		(input.Language != "" && input.Language != "en" && input.Language != "zh-CN") {
		return failure("INVALID_ARGUMENT", "Invalid chat generation request", 422, false)
	}
	return nil
}

func (s *Store) replayChatTurn(chatID string, input ChatGenerate) (ChatTurn, bool, error) {
	var hash string
	err := s.state.QueryRow(`SELECT t.request_hash FROM chat_turns t JOIN chat_sessions c ON c.id = t.chat_id
		WHERE t.chat_id = ? AND t.request_id = ? AND c.instance_id = ? AND c.user_name = ?`,
		chatID, input.RequestID, s.Settings.Memos.InstanceID, s.Settings.Memos.User).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatTurn{}, false, nil
	}
	if err != nil {
		return ChatTurn{}, false, err
	}
	if hash != chatGenerateHash(input) {
		return ChatTurn{}, false, failure("IDEMPOTENCY_CONFLICT", "This request_id belongs to a different message", 409, false)
	}
	turn, err := s.chatTurn(chatID, input.RequestID)
	return turn, true, err
}

func (s *Store) reserveChatTurn(chatID string, input ChatGenerate, settingsVersion int64, prompt, model string) (ChatTurn, bool, error) {
	if err := validateChatGenerate(input); err != nil {
		return ChatTurn{}, false, err
	}
	created, hash := false, chatGenerateHash(input)
	err := transaction(s.state, func(tx *sql.Tx) error {
		var chat ChatSummary
		if err := s.readChatSummary(tx, chatID, &chat); err != nil {
			return err
		}
		var savedHash string
		err := tx.QueryRow("SELECT request_hash FROM chat_turns WHERE chat_id = ? AND request_id = ?", chatID, input.RequestID).Scan(&savedHash)
		if err == nil {
			if savedHash != hash {
				return failure("IDEMPOTENCY_CONFLICT", "This request_id belongs to a different message", 409, false)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if chat.Version != input.Version {
			return chatConflict()
		}
		var running, position int
		if err := tx.QueryRow("SELECT COUNT(*) FROM chat_turns WHERE chat_id = ? AND status = 'running'", chatID).Scan(&running); err != nil {
			return err
		}
		if running != 0 {
			return chatBusy()
		}
		if err := tx.QueryRow("SELECT COALESCE(MAX(position), -1) + 1 FROM chat_turns WHERE chat_id = ?", chatID).Scan(&position); err != nil {
			return err
		}
		rows, err := tx.Query("SELECT memo_name, source_hash FROM chat_materials WHERE chat_id = ? ORDER BY position", chatID)
		if err != nil {
			return err
		}
		refs := []ChatMaterial{}
		for rows.Next() {
			var ref ChatMaterial
			if err := rows.Scan(&ref.Name, &ref.SourceHash); err != nil {
				rows.Close()
				return err
			}
			refs = append(refs, ref)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		content := input.Content
		if input.Kind == "insight" {
			content = prompt
		}
		stamp := nowText()
		if _, err := tx.Exec(`INSERT INTO chat_turns (id, chat_id, position, request_id, request_hash, kind,
			user_content, status, settings_version, prompt, model, materials_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'running', ?, ?, ?, ?, ?, ?)`, newID(), chatID, position, input.RequestID,
			hash, input.Kind, content, settingsVersion, prompt, model, string(canonicalJSON(refs)), stamp, stamp); err != nil {
			return err
		}
		created = true
		_, err = tx.Exec("UPDATE chat_sessions SET updated_at = ? WHERE id = ?", stamp, chatID)
		return err
	})
	if err != nil {
		return ChatTurn{}, false, err
	}
	turn, err := s.chatTurn(chatID, input.RequestID)
	return turn, created, err
}

func (s *Store) appendChatText(chatID, turnID, text string) error {
	if text == "" || !utf8.ValidString(text) {
		return failure("LLM_PROTOCOL", "Chat delta must contain valid text", 502, false)
	}
	if len(text) > llmOutputBytes {
		return failure("LLM_OUTPUT_LIMIT", "Chat output exceeds the text limit", 422, false)
	}
	result, err := s.state.Exec(`UPDATE chat_turns SET assistant_content = assistant_content || ?,
		message_version = message_version + 1, updated_at = ? WHERE id = ? AND chat_id = ? AND status = 'running'
		AND EXISTS (SELECT 1 FROM chat_sessions WHERE id = ? AND instance_id = ? AND user_name = ?)
		AND length(CAST(assistant_content AS BLOB)) + ? <= ?`, text, nowText(), turnID, chatID, chatID,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User, len(text), llmOutputBytes)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		var status string
		err := s.state.QueryRow(`SELECT t.status FROM chat_turns t JOIN chat_sessions c ON c.id = t.chat_id
			WHERE t.id = ? AND t.chat_id = ? AND c.instance_id = ? AND c.user_name = ?`,
			turnID, chatID, s.Settings.Memos.InstanceID, s.Settings.Memos.User).Scan(&status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if status == "running" {
			return failure("LLM_OUTPUT_LIMIT", "Chat output exceeds the text limit", 422, false)
		}
		return failure("CHAT_STREAM_CLOSED", "Chat generation has ended", 409, false)
	}
	return err
}

func (s *Store) finishChatTurn(chatID, turnID, status, code string, usage LLMUsage) error {
	if status != "completed" && status != "failed" && status != "canceled" && status != "interrupted" ||
		(status == "completed" && code != "") {
		return failure("INVALID_ARGUMENT", "Invalid final chat status", 422, false)
	}
	return transaction(s.state, func(tx *sql.Tx) error {
		stamp := nowText()
		result, err := tx.Exec(`UPDATE chat_turns SET status = ?, error_code = ?, usage_json = ?,
			message_version = message_version + 1, updated_at = ? WHERE id = ? AND chat_id = ? AND status = 'running'
			AND EXISTS (SELECT 1 FROM chat_sessions WHERE id = ? AND instance_id = ? AND user_name = ?)`,
			status, code, string(canonicalJSON(usage)), stamp, turnID, chatID, chatID, s.Settings.Memos.InstanceID, s.Settings.Memos.User)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return failure("CHAT_STREAM_CLOSED", "Chat generation has already ended", 409, false)
		}
		_, err = tx.Exec("UPDATE chat_sessions SET updated_at = ? WHERE id = ?", stamp, chatID)
		return err
	})
}

// 仅启动时恢复 running，不重跑供应商；失败和明确取消保持原状态。
func (s *Store) recoverChats() error {
	return transaction(s.state, func(tx *sql.Tx) error {
		stamp := nowText()
		if _, err := tx.Exec(`UPDATE chat_sessions SET updated_at = ? WHERE instance_id = ? AND user_name = ?
			AND id IN (SELECT chat_id FROM chat_turns WHERE status = 'running')`, stamp,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE chat_turns SET status = 'interrupted', error_code = 'CHAT_INTERRUPTED',
			message_version = message_version + 1, updated_at = ? WHERE status = 'running'
			AND chat_id IN (SELECT id FROM chat_sessions WHERE instance_id = ? AND user_name = ?)`,
			stamp, s.Settings.Memos.InstanceID, s.Settings.Memos.User)
		return err
	})
}

func (s *Store) reserveChatSave(chatID, requestID string, version int64) (chatSave, string, error) {
	var save chatSave
	var content string
	err := transaction(s.state, func(tx *sql.Tx) error {
		turn, err := scanChatTurn(tx.QueryRow(chatTurnQuery+` JOIN chat_sessions c ON c.id = t.chat_id
			WHERE t.chat_id = ? AND t.request_id = ? AND c.instance_id = ? AND c.user_name = ?`,
			chatID, requestID, s.Settings.Memos.InstanceID, s.Settings.Memos.User))
		if errors.Is(err, sql.ErrNoRows) {
			return failure("CHAT_MESSAGE_NOT_FOUND", "Chat message is unavailable", 404, false)
		}
		if err != nil {
			return err
		}
		if turn.Assistant.Version != version {
			return chatConflict()
		}
		if turn.Assistant.Status != "completed" || strings.TrimSpace(turn.Assistant.Content) == "" {
			return failure("CHAT_MESSAGE_INCOMPLETE", "Only a completed assistant message can be saved", 409, false)
		}
		content = turn.Assistant.Content
		save.Key = "chat:" + turn.ID + ":" + hashBytes([]byte(content))[:16]
		save.Version = version
		stamp := nowText()
		if _, err := tx.Exec(`INSERT OR IGNORE INTO chat_saves VALUES (?, ?, ?, ?, '', ?, ?)`,
			turn.ID, version, hashBytes([]byte(content)), save.Key, stamp, stamp); err != nil {
			return err
		}
		return tx.QueryRow("SELECT idempotency_key, memo_name, message_version FROM chat_saves WHERE turn_id = ?", turn.ID).
			Scan(&save.Key, &save.Name, &save.Version)
	})
	return save, content, err
}

func (s *Store) completeChatSave(chatID, requestID, key, name string) error {
	if !memoPattern.MatchString(name) {
		return failure("INVALID_RESOURCE", "Invalid saved memo resource", 422, false)
	}
	result, err := s.state.Exec(`UPDATE chat_saves SET memo_name = ?, updated_at = ? WHERE idempotency_key = ?
		AND turn_id IN (SELECT t.id FROM chat_turns t JOIN chat_sessions c ON c.id = t.chat_id
		WHERE t.chat_id = ? AND t.request_id = ? AND c.instance_id = ? AND c.user_name = ?)`,
		name, nowText(), key, chatID, requestID, s.Settings.Memos.InstanceID, s.Settings.Memos.User)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return chatNotFound()
	}
	return err
}

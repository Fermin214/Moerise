package moerise

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
)

func (s *Store) selectionReceipt(chatID, key string) (ChatSaveReceipt, error) {
	var receipt ChatSaveReceipt
	if !idempotencyPattern.MatchString(key) {
		return receipt, failure("INVALID_ARGUMENT", "Invalid save operation key", 422, false)
	}
	err := s.state.QueryRow(`SELECT ss.memo_name FROM chat_selection_saves ss JOIN chat_sessions c ON c.id = ss.chat_id
		WHERE c.id = ? AND c.instance_id = ? AND c.user_name = ? AND ss.operation_key = ?`,
		chatID, s.Settings.Memos.InstanceID, s.Settings.Memos.User, key).Scan(&receipt.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return receipt, failure("CHAT_SAVE_NOT_FOUND", "Save operation is unavailable", 404, false)
	}
	receipt.Status = "pending"
	if receipt.Name != "" {
		receipt.Status = "saved"
	}
	return receipt, err
}

func validateSaveSelection(input ChatSaveSelection) error {
	if !idempotencyPattern.MatchString(input.OperationKey) || (input.Language != "en" && input.Language != "zh-CN") ||
		len(input.Messages) == 0 {
		return failure("INVALID_ARGUMENT", "Select messages and supply a language and operation key", 422, false)
	}
	seen := map[[2]string]bool{}
	for _, ref := range input.Messages {
		key := [2]string{ref.RequestID, ref.Role}
		if !idempotencyPattern.MatchString(ref.RequestID) || ref.Version < 1 ||
			(ref.Role != "user" && ref.Role != "assistant") || seen[key] {
			return failure("INVALID_ARGUMENT", "Invalid or repeated message reference", 422, false)
		}
		seen[key] = true
	}
	return nil
}

func selectionText(tx *sql.Tx, chatID string, input ChatSaveSelection) (string, string, error) {
	rows, err := tx.Query(`WITH refs AS (
			SELECT value->>'request_id' AS request_id, value->>'role' AS role, value->>'version' AS version
			FROM json_each(?)
		) SELECT t.id, t.kind, refs.role, refs.version,
			CASE refs.role WHEN 'user' THEN 1 ELSE t.message_version END,
			CASE refs.role WHEN 'user' THEN 'completed' ELSE t.status END,
			CASE refs.role WHEN 'user' THEN t.user_content ELSE t.assistant_content END
		FROM refs JOIN chat_turns t ON t.chat_id = ? AND t.request_id = refs.request_id
		ORDER BY t.position, refs.role DESC`, string(canonicalJSON(input.Messages)), chatID)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	var body strings.Builder
	var singleAnswerID string
	count := 0
	for rows.Next() {
		var turnID, kind, role, status, content string
		var wanted, version int64
		if err := rows.Scan(&turnID, &kind, &role, &wanted, &version, &status, &content); err != nil {
			return "", "", err
		}
		if role == "user" && kind != "question" {
			return "", "", failure("INVALID_ARGUMENT", "Only user-authored questions can be selected", 422, false)
		}
		if version != wanted {
			return "", "", chatConflict()
		}
		if strings.TrimSpace(content) == "" || (status != "completed" && !((status == "canceled" || status == "truncated") && input.ConfirmIncomplete)) {
			return "", "", failure("CHAT_MESSAGE_INCOMPLETE", "A stopped reply requires confirmation; other unfinished replies cannot be saved", 409, false)
		}
		if len(input.Messages) > 1 {
			if count != 0 {
				body.WriteString("\n\n")
			}
			label := "Moerise"
			if role == "user" {
				label = "我"
				if input.Language == "en" {
					label = "Me"
				}
			}
			body.WriteString("**" + label + "**\n\n")
		} else if role == "assistant" {
			singleAnswerID = turnID
		}
		if len(content) > 65536-body.Len() {
			return "", "", failure("CHAT_SAVE_LIMIT", "Reduce the selection to 65536 UTF-8 bytes", 422, false)
		}
		body.WriteString(content)
		count++
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if count != len(input.Messages) {
		return "", "", failure("CHAT_MESSAGE_NOT_FOUND", "Selected message is unavailable", 404, false)
	}
	return body.String(), singleAnswerID, nil
}

func (s *Store) reserveSelection(chatID string, input ChatSaveSelection) (string, string, error) {
	if err := validateSaveSelection(input); err != nil {
		return "", "", err
	}
	input.Messages = slices.Clone(input.Messages)
	slices.SortFunc(input.Messages, func(left, right ChatMessageRef) int {
		if left.RequestID != right.RequestID {
			return strings.Compare(left.RequestID, right.RequestID)
		}
		return strings.Compare(left.Role, right.Role)
	})
	hash := hashBytes(canonicalJSON(input))
	key := "chat-selection:" + hashBytes(canonicalJSON([]string{chatID, input.OperationKey}))
	var content string
	err := transaction(s.state, func(tx *sql.Tx) error {
		var summary ChatSummary
		if err := s.readChatSummary(tx, chatID, &summary); err != nil {
			return err
		}
		var previous string
		err := tx.QueryRow("SELECT request_hash FROM chat_selection_saves WHERE chat_id = ? AND operation_key = ?",
			chatID, input.OperationKey).Scan(&previous)
		if err == nil && previous != hash {
			return failure("IDEMPOTENCY_CONFLICT", "This operation key belongs to a different selection", 409, false)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var turnID string
		content, turnID, err = selectionText(tx, chatID, input)
		if err != nil {
			return err
		}
		if turnID != "" {
			single, err := reserveSingleAnswer(tx, turnID, input.Messages[0].Version, content)
			if err != nil {
				return err
			}
			key = single.Key
		}
		_, err = tx.Exec(`INSERT INTO chat_selection_saves(chat_id, operation_key, request_hash, write_key)
			VALUES (?, ?, ?, ?) ON CONFLICT(chat_id, operation_key) DO NOTHING`, chatID, input.OperationKey, hash, key)
		return err
	})
	return key, content, err
}

func (s *ChatService) SaveSelection(ctx context.Context, id string, input ChatSaveSelection) (Note, int, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return Note{}, 0, err
	}
	defer unlock()
	key, content, err := s.Store.reserveSelection(id, input)
	if err != nil {
		return Note{}, 0, err
	}
	note, status, err := s.Records.Create(ctx, key, CreateNote{Content: content})
	if err != nil {
		return Note{}, 0, err
	}
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE chat_selection_saves SET memo_name = ? WHERE chat_id = ? AND write_key = ?", note.Name, id, key); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE chat_saves SET memo_name = ?, updated_at = ? WHERE idempotency_key = ?", note.Name, nowText(), key)
		return err
	})
	return note, status, err
}

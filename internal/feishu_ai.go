package moerise

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	feishuAIPartBytes    = 8 * 1024
	feishuAIFailed       = "AI 服务暂时无法完成请求。请稍后重新发送请求。"
	feishuAIInterrupted  = "AI 生成已中断，结果不明，已终止此任务。需要继续时请重新发送请求。"
	feishuAISourceFailed = "笔记来源已变化、删除或无法核实，AI 回复已终止。请重新发送请求。"
)

type feishuAIGeneration struct {
	Operation string
	Status    string
	Code      string
	Result    []byte
}

func parseFeishuAI(text string) (string, AIRequest, error) {
	fields := strings.Fields(text)
	request := AIRequest{}
	if len(fields) == 0 {
		return "", request, failure("INVALID_ARGUMENT", "Missing AI command", 422, false)
	}
	operation := map[string]string{"洞察": "insight", "问答": "answer", "历程": "timeline"}[fields[0]]
	if operation == "" {
		return "", request, failure("INVALID_ARGUMENT", "Invalid AI command", 422, false)
	}
	if operation == "insight" {
		for _, name := range strings.Split(strings.TrimSpace(strings.TrimPrefix(text, fields[0])), ",") {
			request.Names = append(request.Names, strings.TrimSpace(name))
		}
	} else {
		words := []string{}
		for _, word := range fields[1:] {
			if strings.HasPrefix(word, "#") {
				request.Tags = append(request.Tags, strings.TrimPrefix(word, "#"))
			} else {
				words = append(words, word)
			}
		}
		request.Question = strings.Join(words, " ")
		request.Timeline = operation == "timeline"
	}
	_, err := normalizeEvidence(request)
	return operation, request, err
}

func (s *FeishuService) aiGeneration(messageID string) (feishuAIGeneration, error) {
	var item feishuAIGeneration
	config := s.Store.Settings.Channel
	err := s.Store.state.QueryRow(`SELECT g.operation, g.status, COALESCE(g.error_code, ''), g.result
		FROM feishu_ai_generations g JOIN channel_events e ON e.message_id = g.message_id
		WHERE g.message_id = ? AND e.app_id = ? AND e.sender_open_id = ?`,
		messageID, configured(config.AppID), configured(config.UserOpenID)).
		Scan(&item.Operation, &item.Status, &item.Code, &item.Result)
	return item, err
}

func (s *FeishuService) recoverAI() error {
	config := s.Store.Settings.Channel
	_, err := s.Store.state.Exec(`UPDATE feishu_ai_generations SET status = 'interrupted',
		error_code = 'GENERATION_INTERRUPTED', updated_at = ? WHERE status = 'running'
		AND message_id IN (SELECT message_id FROM channel_events WHERE app_id = ? AND sender_open_id = ?)`,
		nowText(), configured(config.AppID), configured(config.UserOpenID))
	return err
}

func (s *FeishuService) executeAI(ctx context.Context, messageID, text string) (string, string, error) {
	operation, request, err := parseFeishuAI(text)
	if err != nil {
		return "", "", err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return "", "", err
	}
	sealed, err := s.seal("ai-request:"+messageID, body)
	if err != nil {
		return "", "", err
	}
	// The insert commits before entering the shared service. An existing running
	// generation is an uncertain outcome, never permission to call the model again.
	status, code := "running", ""
	var restored bool
	config := s.Store.Settings.Channel
	if err := s.Store.state.QueryRow(`SELECT COALESCE(last_error = 'STATE_RESTORED', 0)
		FROM channel_events WHERE message_id = ? AND app_id = ? AND sender_open_id = ?`,
		messageID, configured(config.AppID), configured(config.UserOpenID)).Scan(&restored); err != nil {
		return "", "", err
	}
	if restored {
		status, code = "interrupted", "STATE_RESTORED"
	}
	stamp := nowText()
	inserted, err := s.Store.state.Exec(`INSERT OR IGNORE INTO feishu_ai_generations
		(message_id, operation, request, status, error_code, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, messageID, operation, sealed, status, nullable(code), stamp, stamp)
	if err != nil {
		return "", "", err
	}
	count, err := inserted.RowsAffected()
	if err != nil {
		return "", "", err
	}
	if count == 0 {
		if _, err := s.Store.state.Exec(`UPDATE feishu_ai_generations SET status = 'interrupted',
			error_code = 'GENERATION_INTERRUPTED', updated_at = ? WHERE message_id = ? AND status = 'running'`,
			nowText(), messageID); err != nil {
			return "", "", err
		}
		return "ai", messageID, nil
	}
	if restored {
		return "ai", messageID, nil
	}
	result := AIResult{}
	if s.AI == nil {
		err = failure("LLM_DISABLED", "AI is unavailable", 503, false)
	} else if operation == "insight" {
		result, err = s.AI.Insight(ctx, request, nil)
	} else {
		result, err = s.AI.Answer(ctx, request, nil)
	}
	var saved []byte
	status, code = "completed", ""
	if err != nil || !result.Completed {
		status, code = "failed", "AI_GENERATION_FAILED"
		// Keep only a fixed classification, never provider/source error text.
		if err != nil && strings.HasPrefix(errorCode(err), "SOURCE_") {
			code = "AI_SOURCE_FAILED"
		}
	} else {
		body, err = json.Marshal(result)
		if err == nil {
			saved, err = s.seal("ai-result:"+messageID, body)
		}
		if err != nil {
			return "", "", err
		}
	}
	_, err = s.Store.state.Exec(`UPDATE feishu_ai_generations SET status = ?, result = ?,
		error_code = ?, updated_at = ? WHERE message_id = ? AND status = 'running'`,
		status, saved, nullable(code), nowText(), messageID)
	return "ai", messageID, err
}

func (s *FeishuService) aiResult(messageID string, item feishuAIGeneration) (AIResult, error) {
	body, err := s.unseal("ai-result:"+messageID, item.Result)
	if err != nil {
		return AIResult{}, err
	}
	var result AIResult
	if json.Unmarshal(body, &result) != nil || !result.Completed {
		return AIResult{}, upstreamError("CHANNEL_QUEUE_KEY", "Invalid saved AI result")
	}
	return result, nil
}

func feishuAIError(item feishuAIGeneration) string {
	if item.Status == "interrupted" {
		return feishuAIInterrupted
	}
	if item.Code == "AI_SOURCE_FAILED" {
		return feishuAISourceFailed
	}
	return feishuAIFailed
}

func renderFeishuAI(operation string, result AIResult) string {
	var text strings.Builder
	fmt.Fprintln(&text, map[string]string{"insight": "笔记洞察", "answer": "笔记问答", "timeline": "主题历程"}[operation])
	if result.Status == "insufficient" {
		fmt.Fprintln(&text, "证据不足。")
	}
	for _, statement := range result.Statements {
		label := map[string]string{"fact": "事实", "interpretation": "解释", "question": "追问"}[statement.Kind]
		fmt.Fprintf(&text, "\n[%s] %s", label, statement.Text)
		if len(statement.Citations) > 0 {
			fmt.Fprintf(&text, " [%s]", strings.Join(statement.Citations, ", "))
		}
		fmt.Fprintln(&text)
	}
	if len(result.Timeline) > 0 {
		fmt.Fprintln(&text, "\n时间分段（按笔记创建时间，结束时间不含）：")
		for _, period := range result.Timeline {
			fmt.Fprintf(&text, "\n[%s] %s 至 %s\n", period.ID, period.From, period.Until)
			if period.Empty {
				reason := "没有符合条件且可用的笔记证据。"
				if period.MissingReason == "CONTEXT_BUDGET" {
					reason = "上下文预算不足，未纳入该时间段的证据。"
				}
				fmt.Fprintln(&text, "缺少证据："+reason)
			} else if len(period.RepresentativeNames) > 0 {
				fmt.Fprintln(&text, "代表笔记："+strings.Join(period.RepresentativeNames, ", "))
			}
		}
	}
	for _, reference := range result.References {
		fmt.Fprintf(&text, "\n引用 %s：%s 第 %d-%d 行\n%s\n",
			reference.ID, reference.Name, reference.LineStart, reference.LineEnd, reference.URL)
	}
	if len(result.UsedSources) > 0 {
		fmt.Fprintf(&text, "\n实际引用：%d 条笔记。\n", len(result.UsedSources))
	}
	if len(result.Missing) > 0 {
		fmt.Fprintln(&text, "\n缺失项：")
		for _, missing := range result.Missing {
			fmt.Fprintln(&text, "- "+missing)
		}
	}
	if len(result.Omitted) > 0 {
		fmt.Fprintln(&text, "\n未纳入或未完整纳入：")
		for _, omitted := range result.Omitted {
			fmt.Fprintln(&text, "- "+omitted)
		}
	}
	return text.String()
}

func splitFeishuAI(text string) []string {
	const budget = feishuAIPartBytes - 128
	parts := []string{}
	for len(text) > budget {
		end := budget
		for !utf8.RuneStart(text[end]) {
			end--
		}
		if newline := strings.LastIndexByte(text[:end], '\n'); newline >= end/2 {
			end = newline + 1
		}
		parts = append(parts, text[:end])
		text = text[end:]
	}
	parts = append(parts, text)
	for i := range parts {
		parts[i] = fmt.Sprintf("AI 回复（%d/%d）\n", i+1, len(parts)) + parts[i]
	}
	return parts
}

func (s *FeishuService) enqueueReply(messageID string) error {
	var kind string
	config := s.Store.Settings.Channel
	if err := s.Store.state.QueryRow(`SELECT result_kind FROM channel_events
		WHERE message_id = ? AND app_id = ? AND sender_open_id = ?`, messageID,
		configured(config.AppID), configured(config.UserOpenID)).Scan(&kind); err != nil {
		return err
	}
	if kind != "ai" {
		_, err := s.Notifications.EnqueueReply(messageID)
		return err
	}
	item, err := s.aiGeneration(messageID)
	if err != nil {
		return err
	}
	text := feishuAIError(item)
	if item.Status == "completed" {
		result, err := s.aiResult(messageID, item)
		if err != nil {
			text = feishuAIFailed
		} else {
			text = renderFeishuAI(item.Operation, result)
		}
	}
	parts := splitFeishuAI(text)
	sealed := make([][]byte, len(parts))
	for i, part := range parts {
		sealed[i], err = s.seal(fmt.Sprintf("ai-part:%s:%d", messageID, i), []byte(part))
		if err != nil {
			return err
		}
	}
	settings := s.Store.Settings
	return transaction(s.Store.state, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM feishu_ai_parts WHERE message_id = ?", messageID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return nil
		}
		var created, replyStatus string
		var lastError *string
		if err := tx.QueryRow("SELECT created_at, reply_status, last_error FROM channel_events WHERE message_id = ?", messageID).
			Scan(&created, &replyStatus, &lastError); err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return err
		}
		location, err := time.LoadLocation(settings.Timezone)
		if err != nil {
			return err
		}
		status := "pending"
		if replyStatus == "unknown" {
			status = "unknown"
		}
		stamp, channel := nowText(), s.Notifications.channelKey()
		for i := range parts {
			logical := fmt.Sprintf("reply:%s:ai:%d", messageID, i)
			id := derivedID(settings.CursorSecret, "notification", settings.Memos.InstanceID, settings.Memos.User, channel, logical)
			available := 1.0e30
			if i == 0 {
				available = nowSeconds()
			}
			if _, err := tx.Exec(`INSERT INTO notifications
				(id, instance_id, user_name, channel_key, logical_key, kind, local_date, source_event,
				 status, platform_uuid, available_at, last_error, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, 'reply', ?, ?, ?, ?, ?, ?, ?, ?)`, id,
				settings.Memos.InstanceID, settings.Memos.User, channel, logical, at.In(location).Format(time.DateOnly),
				messageID, status, derivedID(settings.CursorSecret, "notification-send", id),
				available, lastError, stamp, stamp); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO feishu_ai_parts (notification_id, message_id, ordinal, payload)
				VALUES (?, ?, ?, ?)`, id, messageID, i, sealed[i]); err != nil {
				return err
			}
		}
		if status == "pending" {
			_, err = tx.Exec("UPDATE channel_events SET reply_status = 'queued' WHERE message_id = ?", messageID)
		}
		return err
	})
}

func (s *FeishuService) aiPart(item Notification) (string, int, error) {
	if item.SourceEvent == nil {
		return "", 0, upstreamError("CHANNEL_QUEUE_KEY", "AI reply has no source event")
	}
	var payload []byte
	var ordinal int
	if err := s.Store.state.QueryRow(`SELECT payload, ordinal FROM feishu_ai_parts
		WHERE notification_id = ? AND message_id = ?`, item.ID, *item.SourceEvent).Scan(&payload, &ordinal); err != nil {
		return "", 0, err
	}
	body, err := s.unseal(fmt.Sprintf("ai-part:%s:%d", *item.SourceEvent, ordinal), payload)
	return string(body), ordinal, err
}

func (s *FeishuService) sendAIPart(ctx context.Context, item Notification) (string, error) {
	if ctx.Err() != nil {
		return "", upstreamError("CHANNEL_UNAVAILABLE", "AI reply verification was cancelled")
	}
	text, ordinal, err := s.aiPart(item)
	if err != nil {
		// No network operation has occurred; hold corrupt ciphertext for inspection.
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "Saved AI reply is unavailable")
	}
	generation, err := s.aiGeneration(*item.SourceEvent)
	if err != nil {
		return "", err
	}
	invalid := generation.Code == "AI_SOURCE_FAILED"
	if generation.Status == "completed" {
		result, verifyErr := s.aiResult(*item.SourceEvent, generation)
		if verifyErr == nil {
			if s.Search == nil {
				verifyErr = failure("SOURCE_UNAVAILABLE", "Evidence verification unavailable", 503, false)
			} else {
				verifyErr = NewEvidenceService(s.Search, 0).Verify(ctx, result.References)
			}
		}
		if ctx.Err() != nil {
			return "", upstreamError("CHANNEL_UNAVAILABLE", "AI reply verification was cancelled")
		}
		invalid = verifyErr != nil
	}
	if invalid {
		text = fmt.Sprintf("AI 回复（第 %d 片）\n%s", ordinal+1, feishuAISourceFailed)
		if err := s.invalidateAIParts(item, ordinal, text); err != nil {
			return "", err
		}
	}
	return s.Client.Reply(ctx, *item.SourceEvent, item.PlatformUUID, text)
}

func (s *FeishuService) invalidateAIParts(item Notification, ordinal int, text string) error {
	sealed, err := s.seal(fmt.Sprintf("ai-part:%s:%d", *item.SourceEvent, ordinal), []byte(text))
	if err != nil {
		return err
	}
	return transaction(s.Store.state, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE feishu_ai_generations SET status = 'failed',
			error_code = 'AI_SOURCE_FAILED', updated_at = ? WHERE message_id = ?`,
			nowText(), *item.SourceEvent); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE feishu_ai_parts SET payload = ? WHERE notification_id = ?", sealed, item.ID); err != nil {
			return err
		}
		// Preserve any send or resolution history. Untouched later parts can be
		// removed rather than mark unsent text as sent.
		_, err := tx.Exec(`DELETE FROM notifications WHERE id IN (
			SELECT notification_id FROM feishu_ai_parts WHERE message_id = ? AND ordinal > ?
		) AND status = 'pending' AND attempts = 0 AND NOT EXISTS (
			SELECT 1 FROM notification_resolutions r WHERE r.notification_id = notifications.id
		)`, *item.SourceEvent, ordinal)
		return err
	})
}

func (s *FeishuService) checkAIPart(item Notification, message FeishuSentMessage) (bool, error) {
	text, _, err := s.aiPart(item)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && text == message.Text, err
}

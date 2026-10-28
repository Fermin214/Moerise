package moerise

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Keep the encoded request below the Webhook's 20 KB limit, including signing
// fields. Application text messages allow larger bodies but share this layout.
const feishuReviewBytes = 18 * 1024

type feishuReviewPart struct {
	Root, Kind, Name, Version string
	Position, Start, End      int
}

func (s *FeishuDelivery) reviewPart(id string) (feishuReviewPart, error) {
	var part feishuReviewPart
	err := s.Store.state.QueryRow(`SELECT root_id, position, kind, memo_name, source_version,
		byte_start, byte_end
		FROM feishu_review_parts WHERE notification_id = ?`, id).
		Scan(&part.Root, &part.Position, &part.Kind, &part.Name, &part.Version,
			&part.Start, &part.End)
	return part, err
}

func reviewTextFits(text string) bool {
	content, _ := json.Marshal(map[string]string{"text": text})
	// Double encoding for the application API is larger than the Webhook form.
	body, _ := json.Marshal(map[string]string{"receive_id": strings.Repeat("x", 128),
		"msg_type": "text", "uuid": strings.Repeat("x", 50), "content": string(content)})
	return len(body) <= feishuReviewBytes
}

func reviewTextEnd(content string) (int, error) {
	if len(content) <= feishuReviewBytes && reviewTextFits(content) {
		return len(content), nil
	}
	low, high := 0, min(len(content), feishuReviewBytes)
	for low < high {
		mid := (low + high + 1) / 2
		end := mid
		for end > 0 && end < len(content) && !utf8.RuneStart(content[end]) {
			end--
		}
		if reviewTextFits(content[:end]) {
			low = mid
		} else {
			high = mid - 1
		}
	}
	end := low
	for end > 0 && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	if newline := strings.LastIndexByte(content[:end], '\n'); newline >= end/2 {
		end = newline + 1
	}
	if end == 0 {
		return 0, failure("CHANNEL_MESSAGE_SIZE", "Review text cannot fit in a message", 422, false)
	}
	return end, nil
}

func (s *FeishuDelivery) prepareReview(ctx context.Context, item Notification) (feishuReviewPart, error) {
	part, err := s.reviewPart(item.ID)
	if !errors.Is(err, sql.ErrNoRows) {
		return part, err
	}
	batch, err := s.Reviews.Get(ctx, *item.BatchID)
	if err != nil {
		return part, err
	}
	parts := []feishuReviewPart{}
	for _, entry := range batch.Items {
		if entry.Unavailable || entry.Note == nil || strings.TrimSpace(entry.Note.Content) == "" {
			continue
		}
		note := *entry.Note
		start := 0
		for {
			length, err := reviewTextEnd(note.Content[start:])
			if err != nil {
				return part, err
			}
			parts = append(parts, feishuReviewPart{Root: item.ID, Kind: "text", Name: note.Name,
				Version: note.SourceHash, Position: len(parts), Start: start, End: start + length})
			start += length
			if start == len(note.Content) {
				break
			}
		}
	}
	if len(parts) == 0 {
		parts = append(parts, feishuReviewPart{Root: item.ID, Kind: "empty"})
	}
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM feishu_review_parts WHERE root_id = ?)`, item.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		for i, candidate := range parts {
			id := item.ID
			if i != 0 {
				id = derivedID(s.Store.Settings.CursorSecret, "review-part", item.ID, strconv.Itoa(i))
				_, err := tx.Exec(`INSERT INTO notifications
					(id, instance_id, user_name, channel_key, logical_key, kind, local_date, batch_id,
					status, platform_uuid, available_at, created_at, updated_at)
					SELECT ?, instance_id, user_name, channel_key, ?, kind, local_date, batch_id,
					'pending', ?, 1.0e30, ?, ? FROM notifications WHERE id = ?`,
					id, "review-part:"+item.ID+":"+strconv.Itoa(i),
					derivedID(s.Store.Settings.CursorSecret, "notification-send", id), nowText(), nowText(), item.ID)
				if err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`INSERT INTO feishu_review_parts
				(notification_id, root_id, position, kind, memo_name, source_version,
				byte_start, byte_end) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				id, item.ID, i, candidate.Kind, candidate.Name, candidate.Version,
				candidate.Start, candidate.End); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return part, err
	}
	return s.reviewPart(item.ID)
}

func (s *FeishuDelivery) reviewNote(ctx context.Context, part feishuReviewPart) (Note, error) {
	if err := s.Reviews.verify(ctx); err != nil {
		return Note{}, err
	}
	memo, err := s.Reviews.Source.Get(ctx, part.Name)
	if err != nil {
		return Note{}, err
	}
	if memo == nil {
		return Note{}, failure("SOURCE_NOT_FOUND", "Review note is unavailable", 404, false)
	}
	// Reuse Memos' existing normalized source version rather than storing a body copy.
	if memo.SourceHash != part.Version {
		return Note{}, failure("SOURCE_CHANGED", "Review note changed during delivery", 409, false)
	}
	return memo.Note(s.Store.Settings), nil
}

func reviewPartText(part feishuReviewPart, note Note) (string, error) {
	if part.Start < 0 || part.End < part.Start || part.End > len(note.Content) {
		return "", upstreamError("CHANNEL_PROTOCOL", "Invalid saved review text range")
	}
	return note.Content[part.Start:part.End], nil
}

func (s *FeishuDelivery) skipReviewNote(part feishuReviewPart) error {
	_, err := s.Store.state.Exec(`UPDATE feishu_review_parts SET kind = 'empty'
		WHERE root_id = ? AND memo_name = ? AND position >= ? AND notification_id IN
		(SELECT id FROM notifications WHERE status IN ('pending', 'running'))`,
		part.Root, part.Name, part.Position)
	return err
}

func (s *FeishuDelivery) sendReviewText(ctx context.Context, item Notification, text string) (string, error) {
	if s.Store.Settings.Channel.Kind == "feishu-webhook" {
		return s.webhook(ctx, item.ID, text)
	}
	if s.Events == nil {
		return "", upstreamError("CHANNEL_UNAVAILABLE", "Feishu application is unavailable")
	}
	return s.Events.Client.SendText(ctx, configured(s.Store.Settings.Channel.UserOpenID), item.PlatformUUID, text)
}

func (s *FeishuDelivery) sendReviewPart(ctx context.Context, item Notification) (string, error) {
	part, err := s.prepareReview(ctx, item)
	if err != nil {
		return "", err
	}
	if part.Kind != "text" {
		return "empty:" + item.ID, nil
	}
	if part.Position != 0 {
		var ready bool
		if err := s.Store.state.QueryRow(`SELECT n.status = 'sent'
			FROM feishu_review_parts p JOIN notifications n ON n.id = p.notification_id
			WHERE p.root_id = ? AND p.position = ?`, part.Root, part.Position-1).Scan(&ready); err != nil {
			return "", err
		}
		if !ready {
			return "", upstreamError("CHANNEL_PART_PENDING", "Previous review part is not confirmed")
		}
	}
	note, err := s.reviewNote(ctx, part)
	if err != nil {
		if errorCode(err) == "SOURCE_NOT_FOUND" || errorCode(err) == "SOURCE_CHANGED" {
			if err := s.skipReviewNote(part); err != nil {
				return "", err
			}
			return "empty:" + item.ID, nil
		}
		return "", err
	}
	text, err := reviewPartText(part, note)
	if err != nil {
		return "", err
	}
	return s.sendReviewText(ctx, item, text)
}

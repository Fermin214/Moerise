package moerise

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Keep the encoded request below the Webhook's 20 KB limit, including signing
// fields. Application text messages allow larger bodies but share this layout.
const feishuReviewBytes = 18 * 1024

type feishuReviewPart struct {
	Root, Kind, Name, Version string
	Attachment, ImageKey      string
	Position, Start, End      int
}

func (s *FeishuDelivery) reviewPart(id string) (feishuReviewPart, error) {
	var part feishuReviewPart
	err := s.Store.state.QueryRow(`SELECT root_id, position, kind, memo_name, source_version,
		byte_start, byte_end, attachment_name, COALESCE(image_key, '')
		FROM feishu_review_parts WHERE notification_id = ?`, id).
		Scan(&part.Root, &part.Position, &part.Kind, &part.Name, &part.Version,
			&part.Start, &part.End, &part.Attachment, &part.ImageKey)
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
	batch, err := s.Reviews.forNotification(ctx, item)
	if err != nil {
		return part, err
	}
	parts := []feishuReviewPart{}
	for _, entry := range batch.Items {
		if entry.Unavailable || entry.Note == nil {
			continue
		}
		note := *entry.Note
		before := len(parts)
		start := 0
		for start < len(note.Content) && strings.TrimSpace(note.Content) != "" {
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
		for _, attachment := range note.Attachments {
			kind, _ := attachment["type"].(string)
			name, _ := attachment["name"].(string)
			if strings.HasPrefix(kind, "image/") && attachmentPattern.MatchString(name) &&
				configured(s.Store.Settings.Channel.AppID) != "" && configured(s.Store.Settings.Channel.AppSecret) != "" {
				parts = append(parts, feishuReviewPart{Root: item.ID, Kind: "image", Name: note.Name,
					Version: note.SourceHash, Position: len(parts), Attachment: name})
			}
		}
		if len(parts) != before {
			parts = append(parts, feishuReviewPart{Root: item.ID, Kind: "footer", Name: note.Name,
				Version: note.SourceHash})
		}
	}
	if len(parts) == 0 {
		parts = append(parts, feishuReviewPart{Root: item.ID, Kind: "empty"})
	} else {
		parts = append([]feishuReviewPart{{Root: item.ID, Kind: "header"}}, parts...)
	}
	for i := range parts {
		parts[i].Position = i
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
					(id, instance_id, user_name, channel_key, logical_key, kind, local_date, batch_id, slot_id,
					status, platform_uuid, available_at, created_at, updated_at)
					SELECT ?, instance_id, user_name, channel_key, ?, kind, local_date, batch_id, slot_id,
					'pending', ?, 1.0e30, ?, ? FROM notifications WHERE id = ?`,
					id, "review-part:"+item.ID+":"+strconv.Itoa(i),
					derivedID(s.Store.Settings.CursorSecret, "notification-send", id), nowText(), nowText(), item.ID)
				if err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`INSERT INTO feishu_review_parts
				(notification_id, root_id, position, kind, memo_name, source_version,
				byte_start, byte_end, attachment_name) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, item.ID, i, candidate.Kind, candidate.Name, candidate.Version,
				candidate.Start, candidate.End, candidate.Attachment); err != nil {
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

func (s *FeishuDelivery) reviewFooter(note Note) (string, error) {
	created, err := time.Parse(time.RFC3339Nano, note.CreateTime)
	if err != nil {
		return "", upstreamError("UPSTREAM_PROTOCOL", "Invalid source creation time")
	}
	location, err := time.LoadLocation(s.Store.Settings.Timezone)
	if err != nil {
		return "", err
	}
	tags := make([]string, 0, len(note.Tags))
	for _, tag := range note.Tags {
		tags = append(tags, "#"+tag)
	}
	footer := "创建于" + created.In(location).Format(time.DateOnly)
	if len(tags) != 0 {
		footer = strings.Join(tags, " ") + "  " + footer
	}
	return footer, nil
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
	if part.Kind == "empty" {
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
	if part.Kind == "header" {
		ready, err := s.reviewHeaderReady(ctx, part.Root)
		if err != nil {
			return "", err
		}
		if !ready {
			return "empty:" + item.ID, nil
		}
		return s.sendReviewText(ctx, item, "「今日回顾」")
	}
	if part.Kind != "text" && part.Kind != "image" && part.Kind != "footer" {
		return "empty:" + item.ID, nil
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
	if part.Kind == "image" {
		receipt, err := s.sendReviewImage(ctx, item, part, note)
		if errorCode(err) == "SOURCE_NOT_FOUND" || errorCode(err) == "CHANNEL_IMAGE_SIZE" || errorCode(err) == "CHANNEL_IMAGE_UNSUPPORTED" {
			return "empty:" + item.ID, nil
		}
		return receipt, err
	}
	if part.Kind == "footer" {
		var delivered bool
		if err := s.Store.state.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM feishu_review_parts p JOIN notifications n ON n.id = p.notification_id
			WHERE p.root_id = ? AND p.memo_name = ? AND p.kind IN ('text', 'image')
			AND n.status = 'sent' AND n.receipt NOT LIKE 'empty:%')`, part.Root, part.Name).Scan(&delivered); err != nil {
			return "", err
		}
		if !delivered {
			return "empty:" + item.ID, nil
		}
		footer, err := s.reviewFooter(note)
		if err != nil {
			return "", err
		}
		return s.sendReviewText(ctx, item, footer)
	}
	text, err := reviewPartText(part, note)
	if err != nil {
		return "", err
	}
	return s.sendReviewText(ctx, item, text)
}

// Check for actual content before emitting the standalone title.
func (s *FeishuDelivery) reviewHeaderReady(ctx context.Context, root string) (bool, error) {
	rows, err := s.Store.state.Query(`SELECT notification_id FROM feishu_review_parts
		WHERE root_id = ? AND kind IN ('text', 'image') ORDER BY position`, root)
	if err != nil {
		return false, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		part, err := s.reviewPart(id)
		if err != nil {
			return false, err
		}
		note, err := s.reviewNote(ctx, part)
		if errorCode(err) == "SOURCE_NOT_FOUND" || errorCode(err) == "SOURCE_CHANGED" {
			if err := s.skipReviewNote(part); err != nil {
				return false, err
			}
			continue
		}
		if err != nil {
			return false, err
		}
		if part.Kind == "text" {
			return true, nil
		}
		key, err := s.reviewImageKey(ctx, id, part, note)
		if err != nil {
			switch errorCode(err) {
			case "SOURCE_NOT_FOUND", "CHANNEL_IMAGE_SIZE", "CHANNEL_IMAGE_UNSUPPORTED":
			default:
				return false, err
			}
		}
		if key != "" {
			return true, nil
		}
		if _, err := s.Store.state.Exec(`UPDATE feishu_review_parts SET kind = 'empty' WHERE notification_id = ?`, id); err != nil {
			return false, err
		}
	}
	return false, nil
}

type feishuImagePlatform interface {
	UploadImage(context.Context, CreateAttachment) (string, error)
	SendImage(context.Context, string, string, string) (string, error)
}

func (s *FeishuDelivery) imageClient() feishuImagePlatform {
	if s.Events != nil {
		if client, ok := s.Events.Client.(feishuImagePlatform); ok {
			return client
		}
	}
	return s.images
}

func (s *FeishuDelivery) sendReviewImage(ctx context.Context, item Notification, part feishuReviewPart, note Note) (string, error) {
	key, err := s.reviewImageKey(ctx, item.ID, part, note)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "empty:" + item.ID, nil
	}
	if s.Store.Settings.Channel.Kind == "feishu-webhook" {
		return s.webhookMessage(ctx, item.ID, "image", map[string]string{"image_key": key})
	}
	return s.imageClient().SendImage(ctx, configured(s.Store.Settings.Channel.UserOpenID), item.PlatformUUID, key)
}

func (s *FeishuDelivery) reviewImageKey(ctx context.Context, id string, part feishuReviewPart, note Note) (string, error) {
	if configured(s.Store.Settings.Channel.AppID) == "" {
		return "", nil
	}
	client := s.imageClient()
	key := part.ImageKey
	if key == "" {
		var attachment map[string]any
		for _, candidate := range note.Attachments {
			if candidate["name"] == part.Attachment {
				attachment = candidate
				break
			}
		}
		if attachment == nil {
			return "", failure("SOURCE_NOT_FOUND", "Review image is unavailable", 404, false)
		}
		source, ok := s.Reviews.Source.(interface {
			DownloadAttachment(context.Context, map[string]any) (CreateAttachment, error)
		})
		if !ok {
			return "", upstreamError("UPSTREAM_PROTOCOL", "Source cannot download images")
		}
		image, err := source.DownloadAttachment(ctx, attachment)
		if err != nil {
			return "", err
		}
		key, err = client.UploadImage(ctx, image)
		if err != nil {
			return "", err
		}
		if _, err := s.Store.state.Exec(`UPDATE feishu_review_parts SET image_key = ? WHERE notification_id = ?`,
			key, id); err != nil {
			return "", err
		}
	}
	return key, nil
}

package moerise

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

type reviewPlatform interface {
	reviewTextEnd(string) (int, error)
	reviewImages() bool
	reviewTitle() string
	reviewDatePrefix() string
	sendReviewText(context.Context, Notification, string) (string, error)
	reviewImageKey(context.Context, string, reviewDeliveryPart, Note) (string, error)
	sendReviewImage(context.Context, Notification, reviewDeliveryPart, Note) (string, error)
}

type reviewDelivery struct {
	Store    *Store
	Reviews  *ReviewService
	platform reviewPlatform
}

// The existing table and trigger also store Telegram parts to preserve queued deliveries.
type reviewDeliveryPart struct {
	Root, Kind, Name, Version string
	Attachment, ImageKey      string
	Position, Start, End      int
}

func (s *reviewDelivery) reviewPart(id string) (reviewDeliveryPart, error) {
	var part reviewDeliveryPart
	err := s.Store.state.QueryRow(`SELECT root_id, position, kind, memo_name, source_version,
		byte_start, byte_end, attachment_name, COALESCE(image_key, '')
		FROM feishu_review_parts WHERE notification_id = ?`, id).
		Scan(&part.Root, &part.Position, &part.Kind, &part.Name, &part.Version,
			&part.Start, &part.End, &part.Attachment, &part.ImageKey)
	return part, err
}

func (s *reviewDelivery) prepareReview(ctx context.Context, item Notification) (reviewDeliveryPart, error) {
	part, err := s.reviewPart(item.ID)
	if !errors.Is(err, sql.ErrNoRows) {
		return part, err
	}
	batch, err := s.Reviews.forNotification(ctx, item)
	if err != nil {
		return part, err
	}
	parts := []reviewDeliveryPart{}
	for _, entry := range batch.Items {
		if entry.Unavailable || entry.Note == nil {
			continue
		}
		note := *entry.Note
		before := len(parts)
		start := 0
		for start < len(note.Content) && strings.TrimSpace(note.Content) != "" {
			length, err := s.platform.reviewTextEnd(note.Content[start:])
			if err != nil {
				return part, err
			}
			parts = append(parts, reviewDeliveryPart{Root: item.ID, Kind: "text", Name: note.Name,
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
				s.platform.reviewImages() {
				parts = append(parts, reviewDeliveryPart{Root: item.ID, Kind: "image", Name: note.Name,
					Version: note.SourceHash, Position: len(parts), Attachment: name})
			}
		}
		if len(parts) != before {
			footer, err := s.reviewFooter(note)
			if err != nil {
				return part, err
			}
			for start := 0; start < len(footer); {
				length, err := s.platform.reviewTextEnd(footer[start:])
				if err != nil {
					return part, err
				}
				parts = append(parts, reviewDeliveryPart{Root: item.ID, Kind: "footer", Name: note.Name,
					Version: note.SourceHash, Start: start, End: start + length})
				start += length
			}
		}
	}
	if len(parts) == 0 {
		parts = append(parts, reviewDeliveryPart{Root: item.ID, Kind: "empty"})
	} else {
		parts = append([]reviewDeliveryPart{{Root: item.ID, Kind: "header"}}, parts...)
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

func (s *reviewDelivery) reviewNote(ctx context.Context, part reviewDeliveryPart) (Note, error) {
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

func reviewPartText(part reviewDeliveryPart, note Note) (string, error) {
	if part.Start < 0 || part.End < part.Start || part.End > len(note.Content) {
		return "", upstreamError("CHANNEL_PROTOCOL", "Invalid saved review text range")
	}
	return note.Content[part.Start:part.End], nil
}

func (s *reviewDelivery) reviewFooter(note Note) (string, error) {
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
	footer := s.platform.reviewDatePrefix() + created.In(location).Format(time.DateOnly)
	if len(tags) != 0 {
		footer = strings.Join(tags, " ") + "  " + footer
	}
	return footer, nil
}

func (s *reviewDelivery) reviewFooterText(part reviewDeliveryPart, note Note) (string, error) {
	footer, err := s.reviewFooter(note)
	if err != nil || part.End == 0 {
		// Existing footer parts store no range and continue to send the whole footer.
		return footer, err
	}
	if part.Start < 0 || part.End <= part.Start || part.End > len(footer) {
		return "", upstreamError("CHANNEL_PROTOCOL", "Invalid saved review footer range")
	}
	return footer[part.Start:part.End], nil
}

func (s *reviewDelivery) splitLegacyFooter(item Notification, part reviewDeliveryPart, note Note) (reviewDeliveryPart, error) {
	if part.End != 0 {
		return part, nil
	}
	footer, err := s.reviewFooter(note)
	if err != nil {
		return part, err
	}
	type textRange struct{ start, end int }
	ranges := []textRange{}
	for start := 0; start < len(footer); {
		length, err := s.platform.reviewTextEnd(footer[start:])
		if err != nil {
			return part, err
		}
		ranges = append(ranges, textRange{start, start + length})
		start += length
	}
	if len(ranges) <= 1 {
		return part, nil
	}
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
		var end int
		if err := tx.QueryRow(`SELECT byte_end FROM feishu_review_parts WHERE notification_id = ?`, item.ID).Scan(&end); err != nil || end != 0 {
			return err
		}
		// Move later positions out of the unique key's range before making room for new parts.
		if _, err := tx.Exec(`UPDATE feishu_review_parts SET position = -position-1
			WHERE root_id = ? AND position > ?`, part.Root, part.Position); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE feishu_review_parts SET position = -position-1+?
			WHERE root_id = ? AND position < 0`, len(ranges)-1, part.Root); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE feishu_review_parts SET byte_start = 0, byte_end = ? WHERE notification_id = ?`, ranges[0].end, item.ID); err != nil {
			return err
		}
		for ordinal, interval := range ranges[1:] {
			id := derivedID(s.Store.Settings.CursorSecret, "review-footer-part", item.ID, strconv.Itoa(ordinal+1))
			if _, err := tx.Exec(`INSERT INTO notifications
				(id, instance_id, user_name, channel_key, logical_key, kind, local_date, batch_id, slot_id,
				status, platform_uuid, available_at, created_at, updated_at)
				SELECT ?, instance_id, user_name, channel_key, ?, kind, local_date, batch_id, slot_id,
				'pending', ?, 1.0e30, ?, ? FROM notifications WHERE id = ?`,
				id, "review-footer-part:"+item.ID+":"+strconv.Itoa(ordinal+1),
				derivedID(s.Store.Settings.CursorSecret, "notification-send", id), nowText(), nowText(), item.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO feishu_review_parts
				(notification_id, root_id, position, kind, memo_name, source_version, byte_start, byte_end)
				VALUES (?, ?, ?, 'footer', ?, ?, ?, ?)`, id, part.Root, part.Position+ordinal+1,
				part.Name, part.Version, interval.start, interval.end); err != nil {
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

func (s *reviewDelivery) skipReviewNote(part reviewDeliveryPart) error {
	_, err := s.Store.state.Exec(`UPDATE feishu_review_parts SET kind = 'empty'
		WHERE root_id = ? AND memo_name = ? AND position >= ? AND notification_id IN
		(SELECT id FROM notifications WHERE status IN ('pending', 'running'))`,
		part.Root, part.Name, part.Position)
	return err
}

func (s *reviewDelivery) sendReviewPart(ctx context.Context, item Notification) (string, error) {
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
		return s.platform.sendReviewText(ctx, item, s.platform.reviewTitle())
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
		receipt, err := s.platform.sendReviewImage(ctx, item, part, note)
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
			AND n.status = 'sent' AND (n.receipt IS NULL OR n.receipt NOT LIKE 'empty:%'))`, part.Root, part.Name).Scan(&delivered); err != nil {
			return "", err
		}
		if !delivered {
			return "empty:" + item.ID, nil
		}
		part, err = s.splitLegacyFooter(item, part, note)
		if err != nil {
			return "", err
		}
		footer, err := s.reviewFooterText(part, note)
		if err != nil {
			return "", err
		}
		return s.platform.sendReviewText(ctx, item, footer)
	}
	text, err := reviewPartText(part, note)
	if err != nil {
		return "", err
	}
	return s.platform.sendReviewText(ctx, item, text)
}

// Check for actual content before emitting the standalone title.
func (s *reviewDelivery) reviewHeaderReady(ctx context.Context, root string) (bool, error) {
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
		key, err := s.platform.reviewImageKey(ctx, id, part, note)
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

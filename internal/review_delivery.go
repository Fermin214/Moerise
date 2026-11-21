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

// Review parts share ordering and outcome tracking across delivery channels.
type reviewDeliveryPart struct {
	Root, Kind, Name, Version string
	Attachment, ImageKey      string
	Position, Start, End      int
}

func (s *reviewDelivery) reviewPart(id string) (reviewDeliveryPart, error) {
	var part reviewDeliveryPart
	err := s.Store.state.QueryRow(`SELECT root_id, part_ordinal, part_kind, memo_name, source_version,
		byte_start, byte_end, attachment_name, COALESCE(image_key, '')
		FROM delivery_logs WHERE id = ? AND part_kind != ''`, id).
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
		if err := tx.QueryRow(`SELECT part_kind!='' FROM delivery_logs WHERE id=?`, item.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		for i, candidate := range parts {
			id := item.ID
			if i > 0 {
				id = derivedID(s.Store.Settings.CursorSecret, "review-part", item.ID, strconv.Itoa(i))
				_, err := tx.Exec(`INSERT INTO delivery_logs (id,instance_id,user_name,channel_key,channel_kind,logical_key,kind,local_date,batch_id,slot_id,clock,status,platform_uuid,available_at,created_at,updated_at,root_id,part_ordinal)
     VALUES(?,?,?,?,?,?,'review',?,?,?,?,'pending',?,0,?,?,?,?)`, id, s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, item.channelKey, item.ChannelKind, "review-part:"+item.ID+":"+strconv.Itoa(i), item.Date, item.BatchID, item.SlotID, item.Clock, derivedID(s.Store.Settings.CursorSecret, "notification-send", id), nowText(), nowText(), item.ID, i)
				if err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`UPDATE delivery_logs SET root_id=?,part_ordinal=?,part_kind=?,memo_name=?,source_version=?,byte_start=?,byte_end=?,attachment_name=? WHERE id=?`, candidate.Root, i, candidate.Kind, candidate.Name, candidate.Version, candidate.Start, candidate.End, candidate.Attachment, id); err != nil {
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
	memo, err := s.Store.GetMemoLocal(ctx, part.Name)
	if err != nil {
		return Note{}, err
	}
	if memo == nil {
		return Note{}, failure("SOURCE_NOT_FOUND", "Review note is unavailable", 404, false)
	}
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
	if err != nil {
		return footer, err
	}
	if part.Start < 0 || part.End <= part.Start || part.End > len(footer) {
		return "", upstreamError("CHANNEL_PROTOCOL", "Invalid saved review footer range")
	}
	return footer[part.Start:part.End], nil
}

func (s *reviewDelivery) skipReviewNote(part reviewDeliveryPart) error {
	_, err := s.Store.state.Exec(`UPDATE delivery_logs SET part_kind='empty' WHERE root_id=? AND memo_name=? AND part_ordinal>=? AND status IN ('pending','running')`, part.Root, part.Name, part.Position)
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
			SELECT 1 FROM delivery_logs WHERE root_id = ? AND memo_name = ? AND part_kind IN ('text','image') AND status='sent' AND (receipt IS NULL OR receipt NOT LIKE 'empty:%'))`, part.Root, part.Name).Scan(&delivered); err != nil {
			return "", err
		}
		if !delivered {
			return "empty:" + item.ID, nil
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
	rows, err := s.Store.state.Query(`SELECT id FROM delivery_logs WHERE root_id=? AND part_kind IN ('text','image') ORDER BY part_ordinal`, root)
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
		if _, err := s.Store.state.Exec(`UPDATE delivery_logs SET part_kind='empty' WHERE id=?`, id); err != nil {
			return false, err
		}
	}
	return false, nil
}

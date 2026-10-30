package moerise

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type reviewPlanRules struct {
	Review ReviewConfig `json:"review"`
	Clocks []string     `json:"clocks"`
}

type reviewSlot struct {
	ID, Clock    string
	First, Count int
}

func (s *ReviewService) freezePlan(tx *sql.Tx, id, date string, settings Settings) error {
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM review_plans WHERE batch_id = ?)`, id).Scan(&exists); err != nil || exists {
		return err
	}
	instance, user := s.binding()
	version := s.Store.productVersion
	if previous := settings.daily; previous != nil {
		version = previous.Version
	}
	clocks := settings.Notifications.Clocks()
	rules, err := json.Marshal(reviewPlanRules{settings.Review, clocks})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO review_plans
		(batch_id, instance_id, user_name, local_date, settings_version, rules_json) VALUES (?, ?, ?, ?, ?, ?)`,
		id, instance, user, date, version, string(rules)); err != nil {
		return err
	}
	var count int
	var queued bool
	if err := tx.QueryRow(`SELECT COUNT(*) FROM review_items WHERE batch_id = ?`, id).Scan(&count); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM notifications
		WHERE batch_id = ? AND kind = 'review' AND slot_id IS NULL)`, id).Scan(&queued); err != nil {
		return err
	}
	for ordinal, clock := range clocks {
		first := min(count, ordinal*settings.Review.Count)
		allocated := min(settings.Review.Count, count-first)
		if ordinal == len(clocks)-1 {
			allocated = count - first
		}
		// Legacy queued notifications already own their entire batch.
		if queued {
			first, allocated = count, 0
		}
		if _, err := tx.Exec(`INSERT INTO review_plan_slots
			(id, batch_id, ordinal, clock, first_position, item_count) VALUES (?, ?, ?, ?, ?, ?)`,
			newID(), id, ordinal, clock, first, allocated); err != nil {
			return err
		}
	}
	return nil
}

func (s *ReviewService) slots(id string) ([]reviewSlot, error) {
	rows, err := s.Store.state.Query(`SELECT id, clock, first_position, item_count
		FROM review_plan_slots WHERE batch_id = ? ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	slots := []reviewSlot{}
	for rows.Next() {
		var slot reviewSlot
		if err := rows.Scan(&slot.ID, &slot.Clock, &slot.First, &slot.Count); err != nil {
			return nil, err
		}
		slots = append(slots, slot)
	}
	return slots, rows.Err()
}

func (s *ReviewService) forNotification(ctx context.Context, item Notification) (ReviewBatch, error) {
	if item.BatchID == nil {
		return ReviewBatch{}, failure("INVALID_ARGUMENT", "Review notification has no plan", 422, false)
	}
	if err := s.verify(ctx); err != nil {
		return ReviewBatch{}, err
	}
	if item.SlotID == nil {
		return s.load(ctx, *item.BatchID)
	}
	var slot reviewSlot
	err := s.Store.state.QueryRow(`SELECT first_position, item_count FROM review_plan_slots
		WHERE id = ? AND batch_id = ?`, *item.SlotID, *item.BatchID).Scan(&slot.First, &slot.Count)
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewBatch{}, failure("REVIEW_NOT_FOUND", "Notification allocation is unavailable", 404, false)
	}
	if err != nil {
		return ReviewBatch{}, err
	}
	return s.loadRange(ctx, *item.BatchID, slot.First, slot.Count)
}

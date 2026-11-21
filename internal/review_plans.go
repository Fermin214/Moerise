package moerise

import (
	"context"
	"database/sql"
	"encoding/json"
)

type reviewPlanRules struct {
	Review ReviewConfig `json:"review"`
	Clocks []string     `json:"clocks"`
}

type reviewSlot struct {
	ID    string `json:"id"`
	Clock string `json:"clock"`
	First int    `json:"first"`
	Count int    `json:"count"`
}

func (s *ReviewService) freezePlan(tx *sql.Tx, id, date string, settings Settings) error {
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
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM review_items WHERE batch_id = ?`, id).Scan(&count); err != nil {
		return err
	}
	slots := []reviewSlot{}
	for ordinal, clock := range clocks {
		first := min(count, ordinal*settings.Review.Count)
		allocated := min(settings.Review.Count, count-first)
		if ordinal == len(clocks)-1 {
			allocated = count - first
		}
		slots = append(slots, reviewSlot{newID(), clock, first, allocated})
	}
	encoded, err := json.Marshal(slots)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE review_batches SET settings_version = ?, rules_json = ?, slots_json = ?
		WHERE id = ? AND instance_id = ? AND user_name = ?`, version, string(rules), string(encoded), id, instance, user)
	return err
}

func (s *ReviewService) slots(id string) ([]reviewSlot, error) {
	var encoded string
	instance, user := s.binding()
	err := s.Store.state.QueryRow(`SELECT slots_json FROM review_batches WHERE id = ? AND instance_id = ? AND user_name = ?`,
		id, instance, user).Scan(&encoded)
	if err != nil {
		return nil, err
	}
	slots := []reviewSlot{}
	err = json.Unmarshal([]byte(encoded), &slots)
	return slots, err
}

func (s *ReviewService) forNotification(ctx context.Context, item Notification) (ReviewBatch, error) {
	if item.BatchID == nil || item.SlotID == nil {
		return ReviewBatch{}, failure("INVALID_ARGUMENT", "Review notification has no plan", 422, false)
	}
	slots, err := s.slots(*item.BatchID)
	if err != nil {
		return ReviewBatch{}, err
	}
	for _, slot := range slots {
		if slot.ID == *item.SlotID {
			return s.loadRangeWith(ctx, *item.BatchID, slot.First, slot.Count, true)
		}
	}
	return ReviewBatch{}, failure("REVIEW_NOT_FOUND", "Notification allocation is unavailable", 404, false)
}

package moerise

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
)

type ReviewItem struct {
	Name        string  `json:"name"`
	SourceHash  string  `json:"selected_source_hash"`
	SelectedAt  string  `json:"selected_at"`
	OpenedAt    *string `json:"opened_at"`
	SkippedAt   *string `json:"skipped_at"`
	Unavailable bool    `json:"unavailable"`
	Note        *Note   `json:"note,omitempty"`
}

type ReviewBatch struct {
	ID          string       `json:"id"`
	Date        string       `json:"date"`
	Timezone    string       `json:"timezone"`
	RuleVersion string       `json:"rule_version"`
	Sequence    int          `json:"sequence"`
	CreatedAt   string       `json:"created_at"`
	Items       []ReviewItem `json:"items"`
}

type ReviewService struct {
	Store  *Store
	Source Source
}

func NewReviewService(store *Store, source Source) *ReviewService {
	return &ReviewService{Store: store, Source: source}
}

func (s *ReviewService) ruleVersion() string {
	return "rules-v1-" + hashBytes(canonicalJSON(s.Store.Settings.Review))[:16]
}

func (s *ReviewService) binding() (string, string) {
	return s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User
}

func (s *ReviewService) verify(ctx context.Context) error {
	return NewRecordService(s.Store, s.Source).verify(ctx)
}

// Daily creates only the first batch; Replace is the sole way to advance its sequence.
func (s *ReviewService) Daily(ctx context.Context, at time.Time) (ReviewBatch, error) {
	return s.batch(ctx, at, "")
}

func (s *ReviewService) Replace(ctx context.Context, key string, at time.Time) (ReviewBatch, error) {
	if !idempotencyPattern.MatchString(key) {
		return ReviewBatch{}, failure("INVALID_ARGUMENT", "A valid Idempotency-Key is required", 422, false)
	}
	return s.batch(ctx, at, key)
}

func (s *ReviewService) batch(ctx context.Context, at time.Time, key string) (ReviewBatch, error) {
	s.Store.reviewMu.Lock()
	defer s.Store.reviewMu.Unlock()
	if err := s.verify(ctx); err != nil {
		return ReviewBatch{}, err
	}
	settings := s.Store.Settings
	location, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return ReviewBatch{}, err
	}
	date, rule := at.In(location).Format(time.DateOnly), s.ruleVersion()
	instance, user := s.binding()
	query := `SELECT id FROM review_batches WHERE instance_id = ? AND user_name = ?
		AND local_date = ? AND timezone = ? AND rule_version = ?`
	args := []any{instance, user, date, settings.Timezone, rule}
	if key != "" {
		query += " AND request_key = ?"
		args = append(args, key)
	}
	query += " ORDER BY sequence DESC LIMIT 1"
	var id string
	err = s.Store.state.QueryRow(query, args...).Scan(&id)
	if err == nil {
		return s.load(ctx, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ReviewBatch{}, err
	}
	candidates, err := s.selectMemos(ctx, at, location, date, rule)
	if err != nil {
		return ReviewBatch{}, err
	}
	id, stamp := newID(), at.UTC().Format(time.RFC3339Nano)
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
		// Recheck after acquiring the database write lock as well as the in-process gate.
		var existing string
		err := tx.QueryRow(query, args...).Scan(&existing)
		if err == nil {
			id = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var sequence int
		if err := tx.QueryRow(`SELECT COALESCE(MAX(sequence), 0) FROM review_batches
			WHERE instance_id = ? AND user_name = ? AND local_date = ? AND timezone = ? AND rule_version = ?`,
			instance, user, date, settings.Timezone, rule).Scan(&sequence); err != nil {
			return err
		}
		var requestKey any
		if key != "" {
			requestKey = key
		}
		if _, err := tx.Exec(`INSERT INTO review_batches
			(id, instance_id, user_name, local_date, timezone, rule_version, sequence, request_key, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, instance, user, date, settings.Timezone, rule, sequence+1, requestKey, stamp); err != nil {
			return err
		}
		for i, memo := range candidates {
			if _, err := tx.Exec(`INSERT INTO review_items (batch_id, position, memo_name, source_hash, selected_at)
				VALUES (?, ?, ?, ?, ?)`, id, i, memo.Name, memo.SourceHash, stamp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ReviewBatch{}, err
	}
	return s.load(ctx, id)
}

type reviewCandidate struct {
	memo        Memo
	last        string
	anniversary bool
	order       string
}

func (s *ReviewService) selectMemos(ctx context.Context, at time.Time, location *time.Location, date, rule string) ([]Memo, error) {
	instance, user := s.binding()
	rows, err := s.Store.state.Query(`SELECT i.memo_name, MAX(b.local_date) FROM review_items i
		JOIN review_batches b ON b.id = i.batch_id WHERE b.instance_id = ? AND b.user_name = ?
		GROUP BY i.memo_name`, instance, user)
	if err != nil {
		return nil, err
	}
	history := map[string]string{}
	for rows.Next() {
		var name, last string
		if err := rows.Scan(&name, &last); err != nil {
			rows.Close()
			return nil, err
		}
		history[name] = last
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.Store.index.Query(`SELECT payload_json FROM memos WHERE instance_id = ? AND user_name = ?`, instance, user)
	if err != nil {
		return nil, err
	}
	rules := s.Store.Settings.Review
	recent := at.In(location).AddDate(0, 0, 1-rules.RecentDays).Format(time.DateOnly)
	candidates := []reviewCandidate{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			return nil, err
		}
		memo, err := decodeMemo(data)
		if err != nil {
			rows.Close()
			return nil, err
		}
		last := history[memo.Name]
		created, err := time.Parse(time.RFC3339Nano, memo.CreateTime)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if !s.eligible(memo, at) || (last != "" && last >= recent) {
			continue
		}
		local := created.In(location)
		anniversary := local.Year() < at.In(location).Year() && local.Month() == at.In(location).Month() && local.Day() == at.In(location).Day()
		candidates = append(candidates, reviewCandidate{memo, last, anniversary, hashBytes([]byte(date + rule + memo.Name))})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(candidates, func(a, b reviewCandidate) int {
		if a.last != b.last {
			return strings.Compare(a.last, b.last)
		}
		if a.anniversary != b.anniversary {
			if a.anniversary {
				return -1
			}
			return 1
		}
		return strings.Compare(a.order, b.order)
	})
	selected, topics := []Memo{}, map[string]int{}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		memo, err := s.Source.Get(ctx, candidate.memo.Name)
		if err != nil {
			return nil, err
		}
		if err := s.Store.Refresh(candidate.memo.Name, memo); err != nil {
			return nil, err
		}
		if memo == nil || !s.eligible(*memo, at) {
			continue
		}
		roots := map[string]bool{}
		for _, tag := range memo.Tags {
			roots[strings.SplitN(tag, "/", 2)[0]] = true
		}
		allowed := true
		for root := range roots {
			if topics[root] >= rules.TopicLimit {
				allowed = false
			}
		}
		if !allowed {
			continue
		}
		selected = append(selected, *memo)
		for root := range roots {
			topics[root]++
		}
		if len(selected) == rules.Count {
			break
		}
	}
	if err := s.verify(ctx); err != nil {
		return nil, err
	}
	return selected, nil
}

func (s *ReviewService) eligible(memo Memo, at time.Time) bool {
	created, err := time.Parse(time.RFC3339Nano, memo.CreateTime)
	return err == nil && memo.Creator == s.Store.Settings.Memos.User &&
		(memo.State == "NORMAL" || s.Store.Settings.Review.IncludeArchived && memo.State == "ARCHIVED") &&
		!created.After(at.Add(-time.Duration(s.Store.Settings.Review.MinAgeHours)*time.Hour))
}

func (s *ReviewService) load(ctx context.Context, id string) (ReviewBatch, error) {
	instance, user := s.binding()
	batch := ReviewBatch{Items: []ReviewItem{}}
	err := s.Store.state.QueryRow(`SELECT id, local_date, timezone, rule_version, sequence, created_at
		FROM review_batches WHERE id = ? AND instance_id = ? AND user_name = ?`, id, instance, user).
		Scan(&batch.ID, &batch.Date, &batch.Timezone, &batch.RuleVersion, &batch.Sequence, &batch.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewBatch{}, failure("REVIEW_NOT_FOUND", "Review batch is unavailable", 404, false)
	}
	if err != nil {
		return ReviewBatch{}, err
	}
	rows, err := s.Store.state.Query(`SELECT memo_name, source_hash, selected_at, opened_at, skipped_at
		FROM review_items WHERE batch_id = ? ORDER BY position`, id)
	if err != nil {
		return ReviewBatch{}, err
	}
	for rows.Next() {
		var item ReviewItem
		if err := rows.Scan(&item.Name, &item.SourceHash, &item.SelectedAt, &item.OpenedAt, &item.SkippedAt); err != nil {
			rows.Close()
			return ReviewBatch{}, err
		}
		batch.Items = append(batch.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ReviewBatch{}, err
	}
	for i := range batch.Items {
		item := &batch.Items[i]
		memo, err := s.Source.Get(ctx, item.Name)
		if err != nil {
			return ReviewBatch{}, err
		}
		if err := s.Store.Refresh(item.Name, memo); err != nil {
			return ReviewBatch{}, err
		}
		if memo == nil {
			item.Unavailable = true
		} else {
			note := memo.Note(s.Store.Settings)
			item.Note = &note
		}
	}
	if err := s.verify(ctx); err != nil {
		return ReviewBatch{}, err
	}
	return batch, nil
}

func (s *ReviewService) Get(ctx context.Context, id string) (ReviewBatch, error) {
	if !uuidPattern.MatchString(id) {
		return ReviewBatch{}, failure("INVALID_ARGUMENT", "Invalid review batch", 422, false)
	}
	if err := s.verify(ctx); err != nil {
		return ReviewBatch{}, err
	}
	return s.load(ctx, id)
}

func (s *ReviewService) Mark(ctx context.Context, id, name, action string, at time.Time) (ReviewBatch, error) {
	if action != "open" && action != "skip" || !memoPattern.MatchString(name) {
		return ReviewBatch{}, failure("INVALID_ARGUMENT", "Invalid review action or resource", 422, false)
	}
	s.Store.reviewMu.Lock()
	defer s.Store.reviewMu.Unlock()
	batch, err := s.Get(ctx, id)
	if err != nil {
		return ReviewBatch{}, err
	}
	found := false
	for _, item := range batch.Items {
		if item.Name == name {
			if item.Unavailable {
				return ReviewBatch{}, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
			}
			found = true
		}
	}
	if !found {
		return ReviewBatch{}, failure("REVIEW_ITEM_NOT_FOUND", "Memo is not in this review batch", 404, false)
	}
	column := "opened_at"
	if action == "skip" {
		column = "skipped_at"
	}
	_, err = s.Store.state.Exec("UPDATE review_items SET "+column+" = COALESCE("+column+", ?) WHERE batch_id = ? AND memo_name = ?",
		at.UTC().Format(time.RFC3339Nano), id, name)
	if err != nil {
		return ReviewBatch{}, err
	}
	return s.load(ctx, id)
}

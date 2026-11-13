package moerise

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"strings"
	"time"
)

type ReviewItem struct {
	Name        string `json:"name"`
	SourceHash  string `json:"selected_source_hash"`
	SelectedAt  string `json:"selected_at"`
	Unavailable bool   `json:"unavailable"`
	Note        *Note  `json:"note,omitempty"`
}

type ReviewBatch struct {
	ID              string       `json:"id"`
	Date            string       `json:"date"`
	Timezone        string       `json:"timezone"`
	CreatedAt       string       `json:"created_at"`
	Items           []ReviewItem `json:"items"`
	SettingsVersion int64        `json:"settings_version"`
}

type ReviewService struct {
	Store  *Store
	Source Source
}

func NewReviewService(store *Store, source Source) *ReviewService {
	return &ReviewService{Store: store, Source: source}
}

func (s *ReviewService) binding() (string, string) {
	return s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User
}

func (s *ReviewService) verify(ctx context.Context) error {
	return NewRecordService(s.Store, s.Source).verify(ctx)
}

// Daily freezes the day's selection and disjoint slot allocations together.
func (s *ReviewService) Daily(ctx context.Context, at time.Time) (ReviewBatch, error) {
	return s.batch(ctx, at)
}

func (s *ReviewService) ReadDaily(ctx context.Context, at time.Time) (ReviewBatch, error) {
	if err := s.verify(ctx); err != nil {
		return ReviewBatch{}, err
	}
	settings := s.Store.reviewSettings(at)
	location, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return ReviewBatch{}, err
	}
	instance, user := s.binding()
	var id string
	err = s.dailyID(s.Store.state, instance, user, at.In(location).Format(time.DateOnly)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewBatch{}, failure("REVIEW_NOT_FOUND", "No daily review has been created", 404, false)
	}
	if err != nil {
		return ReviewBatch{}, err
	}
	return s.load(ctx, id)
}

type reviewQuery interface {
	QueryRow(string, ...any) *sql.Row
}

func (s *ReviewService) dailyID(db reviewQuery, instance, user, date string) *sql.Row {
	return db.QueryRow(`SELECT batch_id FROM review_plans
        WHERE instance_id = ? AND user_name = ? AND local_date = ?`, instance, user, date)
}

func (s *ReviewService) batch(ctx context.Context, at time.Time) (ReviewBatch, error) {
	s.Store.reviewMu.Lock()
	defer s.Store.reviewMu.Unlock()
	if err := s.verify(ctx); err != nil {
		return ReviewBatch{}, err
	}
	settings := s.Store.reviewSettings(at)
	location, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return ReviewBatch{}, err
	}
	date := at.In(location).Format(time.DateOnly)
	instance, user := s.binding()
	var id string
	err = s.dailyID(s.Store.state, instance, user, date).Scan(&id)
	if err == nil {
		return s.load(ctx, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ReviewBatch{}, err
	}
	state, err := s.Store.Status()
	if err != nil {
		return ReviewBatch{}, err
	}
	if state.LastFull == nil {
		return ReviewBatch{}, upstreamError("INDEX_NOT_READY", "Daily review requires the first complete synchronization")
	}
	candidates, err := s.selectMemos(ctx, at, location, settings.Review.Count*len(settings.Notifications.Clocks()))
	if err != nil {
		return ReviewBatch{}, err
	}
	id, stamp := newID(), at.UTC().Format(time.RFC3339Nano)
	err = transaction(s.Store.state, func(tx *sql.Tx) error {
		// Recheck after acquiring the database write lock as well as the in-process gate.
		var existing string
		err := s.dailyID(tx, instance, user, date).Scan(&existing)
		if err == nil {
			id = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO review_batches
            (id, instance_id, user_name, local_date, timezone, created_at)
            VALUES (?, ?, ?, ?, ?, ?)`, id, instance, user, date, settings.Timezone, stamp); err != nil {
			return err
		}
		for i, memo := range candidates {
			if _, err := tx.Exec(`INSERT INTO review_items (batch_id, position, memo_name, source_hash, selected_at)
				VALUES (?, ?, ?, ?, ?)`, id, i, memo.Name, memo.SourceHash, stamp); err != nil {
				return err
			}
		}
		return s.freezePlan(tx, id, date, settings)
	})
	if err != nil {
		return ReviewBatch{}, err
	}
	return s.load(ctx, id)
}

func (s *ReviewService) selectMemos(ctx context.Context, at time.Time, location *time.Location, count int) ([]Memo, error) {
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
	rules := s.Store.reviewSettings(at).Review
	recent := at.In(location).AddDate(0, 0, 1-rules.RecentDays).Format(time.DateOnly)
	candidates := []Memo{}
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
		if !s.eligible(memo, at) || (rules.RecentDays > 0 && last != "" && last >= recent) {
			continue
		}
		candidates = append(candidates, memo)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	selected := []Memo{}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		memo, err := s.Source.Get(ctx, candidate.Name)
		if err != nil {
			return nil, err
		}
		if err := s.Store.Refresh(candidate.Name, memo); err != nil {
			return nil, err
		}
		if memo == nil || !s.eligible(*memo, at) {
			continue
		}
		selected = append(selected, *memo)
		if len(selected) == count {
			break
		}
	}
	if err := s.verify(ctx); err != nil {
		return nil, err
	}
	return selected, nil
}

func (s *ReviewService) eligible(memo Memo, at time.Time) bool {
	settings := s.Store.reviewSettings(at)
	created, err := time.Parse(time.RFC3339Nano, memo.CreateTime)
	return err == nil && memo.Creator == s.Store.Settings.Memos.User &&
		(memo.State == "NORMAL" || settings.Review.IncludeArchived && memo.State == "ARCHIVED") &&
		!created.After(at.Add(-time.Duration(settings.Review.MinAgeHours)*time.Hour)) &&
		reviewContent(memo.Content, memo.Attachments)
}

func reviewContent(content string, attachments []map[string]any) bool {
	if strings.TrimSpace(content) != "" {
		return true
	}
	for _, attachment := range attachments {
		kind, _ := attachment["type"].(string)
		name, _ := attachment["name"].(string)
		if strings.HasPrefix(kind, "image/") && attachmentPattern.MatchString(name) {
			return true
		}
	}
	return false
}

func (s *ReviewService) load(ctx context.Context, id string) (ReviewBatch, error) {
	return s.loadRange(ctx, id, 0, -1)
}

func (s *ReviewService) loadRange(ctx context.Context, id string, first, count int) (ReviewBatch, error) {
	instance, user := s.binding()
	batch := ReviewBatch{Items: []ReviewItem{}}
	err := s.Store.state.QueryRow(`SELECT b.id, b.local_date, b.timezone, b.created_at, p.settings_version
		FROM review_batches b JOIN review_plans p ON p.batch_id = b.id
		WHERE b.id = ? AND b.instance_id = ? AND b.user_name = ?`, id, instance, user).
		Scan(&batch.ID, &batch.Date, &batch.Timezone, &batch.CreatedAt, &batch.SettingsVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewBatch{}, failure("REVIEW_NOT_FOUND", "Review batch is unavailable", 404, false)
	}
	if err != nil {
		return ReviewBatch{}, err
	}
	rows, err := s.Store.state.Query(`SELECT memo_name, source_hash, selected_at
		FROM review_items WHERE batch_id = ? ORDER BY position LIMIT ? OFFSET ?`, id, count, first)
	if err != nil {
		return ReviewBatch{}, err
	}
	for rows.Next() {
		var item ReviewItem
		if err := rows.Scan(&item.Name, &item.SourceHash, &item.SelectedAt); err != nil {
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

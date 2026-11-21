package moerise

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Store) GetMemoLocal(ctx context.Context, name string) (*Memo, error) {
	if !memoPattern.MatchString(name) {
		return nil, failure("INVALID_RESOURCE", "Invalid memo resource name", 422, false)
	}
	var data []byte
	err := s.indexReader().QueryRowContext(ctx,
		`SELECT payload_json FROM memos WHERE instance_id = ? AND user_name = ? AND name = ?`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User, name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	memo, err := decodeMemo(data)
	if err != nil {
		return nil, err
	}
	if memo.Creator != s.Settings.Memos.User || memo.Name != name {
		return nil, nil
	}
	return &memo, nil
}

func isASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func (s *Store) ListLocalMemos(ctx context.Context, states []string, filter LiveFilter, after *[2]string, limit int) ([]Memo, error) {
	where := "m.instance_id = ? AND m.user_name = ?"
	args := []any{s.Settings.Memos.InstanceID, s.Settings.Memos.User}
	if len(states) == 1 {
		where += " AND m.state = ?"
		args = append(args, states[0])
	} else if len(states) > 1 {
		placeholders := strings.Repeat("?,", len(states))
		where += " AND m.state IN (" + placeholders[:len(placeholders)-1] + ")"
		for _, state := range states {
			args = append(args, state)
		}
	}
	if filter.AnyTag && len(filter.Tags) > 0 {
		placeholders := strings.Repeat("?,", len(filter.Tags))
		where += " AND EXISTS (SELECT 1 FROM memo_tags mt WHERE mt.instance_id = m.instance_id AND mt.user_name = m.user_name AND mt.memo_name = m.name AND mt.tag IN (" + placeholders[:len(placeholders)-1] + "))"
		for _, tag := range filter.Tags {
			args = append(args, tag)
		}
	} else {
		for _, tag := range filter.Tags {
			where += " AND EXISTS (SELECT 1 FROM memo_tags mt WHERE mt.instance_id = m.instance_id AND mt.user_name = m.user_name AND mt.memo_name = m.name AND mt.tag = ?)"
			args = append(args, tag)
		}
	}
	for _, bound := range []struct {
		value *time.Time
		op    string
	}{{timePtr(filter.CreatedFrom), ">="}, {timePtr(filter.CreatedUntil), "<"}} {
		if bound.value == nil {
			continue
		}
		where += " AND (julianday(substr(m.create_time, 1, 19)), CAST(substr(m.create_time, 21, 6) AS INTEGER)) " + bound.op + " (julianday(?), ?)"
		args = append(args, bound.value.UTC().Truncate(time.Second).Format(time.RFC3339),
			(bound.value.Nanosecond()+999)/1000)
	}
	if after != nil {
		where += " AND (m.create_time < ? OR (m.create_time = ? AND m.name < ?))"
		args = append(args, after[0], after[0], after[1])
	}
	sqlQuery := "SELECT m.payload_json FROM memos m WHERE " + where
	useGoQueryFilter := false
	if filter.Query != "" {
		if isASCII(filter.Query) {
			sqlQuery += " AND instr(lower(m.content), ?) > 0"
			args = append(args, strings.ToLower(filter.Query))
		} else {
			useGoQueryFilter = true
		}
	}
	sqlQuery += " ORDER BY m.create_time DESC, m.name DESC"
	if !useGoQueryFilter && limit > 0 {
		sqlQuery += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.indexReader().QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Memo{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		memo, err := decodeMemo(data)
		if err != nil {
			return nil, err
		}
		if memo.Creator != s.Settings.Memos.User {
			continue
		}
		if useGoQueryFilter && !filter.matches(&memo) {
			continue
		}
		result = append(result, memo)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, rows.Err()
}

func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func (s *Store) TagsLocal(ctx context.Context, state string) ([]TagCount, error) {
	rows, err := s.indexReader().QueryContext(ctx, `SELECT mt.tag, COUNT(*)
		FROM memo_tags mt
		JOIN memos m ON m.instance_id = mt.instance_id AND m.user_name = mt.user_name AND m.name = mt.memo_name
		WHERE mt.instance_id = ? AND mt.user_name = ? AND m.state = ?
		GROUP BY mt.tag ORDER BY mt.tag ASC`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TagCount{}
	for rows.Next() {
		var item TagCount
		if err := rows.Scan(&item.Tag, &item.Count); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CalendarLocal(ctx context.Context, state string, from, until time.Time, location *time.Location) ([]DayCount, error) {
	rows, err := s.indexReader().QueryContext(ctx, `SELECT create_time FROM memos
		WHERE instance_id = ? AND user_name = ? AND state = ?
		AND (julianday(substr(create_time, 1, 19)), CAST(substr(create_time, 21, 6) AS INTEGER)) >= (julianday(?), ?)
		AND (julianday(substr(create_time, 1, 19)), CAST(substr(create_time, 21, 6) AS INTEGER)) < (julianday(?), ?)`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User, state,
		from.UTC().Truncate(time.Second).Format(time.RFC3339), (from.Nanosecond()+999)/1000,
		until.UTC().Truncate(time.Second).Format(time.RFC3339), (until.Nanosecond()+999)/1000)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var createdText string
		if err := rows.Scan(&createdText); err != nil {
			return nil, err
		}
		created, err := time.Parse(time.RFC3339Nano, createdText)
		if err != nil {
			continue
		}
		counts[created.In(location).Format(time.DateOnly)]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]DayCount, 0, len(counts))
	for date, count := range counts {
		result = append(result, DayCount{Date: date, Count: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Date < result[j].Date })
	return result, nil
}

type syncNotesCursor struct {
	Version    int    `json:"v"`
	InstanceID string `json:"i"`
	UserName   string `json:"u"`
	Generation string `json:"g"`
	Phase      string `json:"p"`
	BaseSeq    int64  `json:"b"`
	MaxSeq     int64  `json:"m"`
	AfterSeq   int64  `json:"s"`
	LastCreate string `json:"c,omitempty"`
	LastName   string `json:"n,omitempty"`
}

func (s *Store) SyncNotes(ctx context.Context, signer *NoteService, rawCursor string, limit int) (SyncNotesResponse, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 || len(rawCursor) > 4096 {
		return SyncNotesResponse{}, failure("INVALID_ARGUMENT", "Invalid sync parameters", 422, false)
	}
	tx, err := s.indexReader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SyncNotesResponse{}, err
	}
	defer tx.Rollback()

	instance, user := s.Settings.Memos.InstanceID, s.Settings.Memos.User
	var syncGeneration string
	var minSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT sync_generation, min_change_seq FROM projection_meta WHERE instance_id = ?`,
		instance).Scan(&syncGeneration, &minSeq); err != nil {
		return SyncNotesResponse{}, err
	}
	var latestSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM memo_changes WHERE instance_id = ? AND user_name = ?`,
		instance, user).Scan(&latestSeq); err != nil {
		return SyncNotesResponse{}, err
	}

	phase, baseSeq, maxSeq, afterSeq := "full", int64(0), latestSeq, int64(0)
	lastCreate, lastName := "", ""
	if rawCursor != "" {
		var cursor syncNotesCursor
		if err := signer.readCursorLimit(rawCursor, &cursor, 4096); err != nil {
			return SyncNotesResponse{}, err
		}
		if cursor.Version != 1 || (cursor.Phase != "full" && cursor.Phase != "reset" && cursor.Phase != "delta") ||
			cursor.BaseSeq < 0 || cursor.MaxSeq < 0 || cursor.AfterSeq < 0 ||
			cursor.AfterSeq > cursor.MaxSeq || cursor.BaseSeq > cursor.MaxSeq ||
			(cursor.LastCreate != "" && cursor.LastName == "") || (cursor.LastCreate == "" && cursor.LastName != "") {
			return SyncNotesResponse{}, failure("INVALID_PAGE_TOKEN", "Page token is invalid or expired", 422, false)
		}
		if cursor.InstanceID != instance || cursor.UserName != user ||
			cursor.Generation != syncGeneration || syncGeneration == "" ||
			(cursor.Phase == "delta" && cursor.BaseSeq < minSeq) || cursor.MaxSeq > latestSeq {
			phase, baseSeq, maxSeq, afterSeq = "reset", 0, latestSeq, 0
			lastCreate, lastName = "", ""
		} else {
			phase = cursor.Phase
			if phase == "delta" {
				if cursor.BaseSeq == cursor.MaxSeq {
					baseSeq, maxSeq, afterSeq = cursor.MaxSeq, latestSeq, cursor.MaxSeq
				} else {
					baseSeq, maxSeq, afterSeq = cursor.BaseSeq, cursor.MaxSeq, cursor.AfterSeq
				}
			} else {
				maxSeq, lastCreate, lastName = cursor.MaxSeq, cursor.LastCreate, cursor.LastName
			}
		}
	}

	result := SyncNotesResponse{
		Mode:           phase,
		Items:          []Note{},
		Deleted:        []string{},
		SyncGeneration: syncGeneration,
	}

	if phase == "full" || phase == "reset" {
		where := "instance_id = ? AND user_name = ?"
		args := []any{instance, user}
		if lastCreate != "" {
			where += " AND (create_time < ? OR (create_time = ? AND name < ?))"
			args = append(args, lastCreate, lastCreate, lastName)
		}
		args = append(args, limit+1)
		rows, err := tx.QueryContext(ctx,
			`SELECT payload_json FROM memos WHERE `+where+` ORDER BY create_time DESC, name DESC LIMIT ?`,
			args...)
		if err != nil {
			return SyncNotesResponse{}, err
		}
		memos := make([]Memo, 0, min(limit+1, 256))
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				rows.Close()
				return SyncNotesResponse{}, err
			}
			memo, err := decodeMemo(data)
			if err != nil {
				rows.Close()
				return SyncNotesResponse{}, err
			}
			if memo.Creator != user {
				continue
			}
			memos = append(memos, memo)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return SyncNotesResponse{}, err
		}
		if len(memos) > limit {
			memos = memos[:limit]
			result.HasMore = true
		}
		for _, memo := range memos {
			result.Items = append(result.Items, memo.Note(s.Settings))
		}
		if result.HasMore {
			last := memos[len(memos)-1]
			result.NextCursor = signer.signCursor(syncNotesCursor{
				Version: 1, InstanceID: instance, UserName: user, Generation: syncGeneration,
				Phase: phase, BaseSeq: 0, MaxSeq: maxSeq, AfterSeq: 0,
				LastCreate: last.CreateTime, LastName: last.Name,
			})
		} else {
			result.NextCursor = signer.signCursor(syncNotesCursor{
				Version: 1, InstanceID: instance, UserName: user, Generation: syncGeneration,
				Phase: "delta", BaseSeq: maxSeq, MaxSeq: maxSeq, AfterSeq: maxSeq,
			})
		}
		return result, nil
	}

	if afterSeq < maxSeq {
		rows, err := tx.QueryContext(ctx, `WITH latest AS (
				SELECT name, MAX(seq) AS seq
				FROM memo_changes
				WHERE instance_id = ? AND user_name = ? AND seq > ? AND seq <= ?
				GROUP BY name
			)
			SELECT l.seq, l.name, mc.deleted, COALESCE(m.payload_json, '')
			FROM latest l
			JOIN memo_changes mc ON mc.seq = l.seq
			LEFT JOIN memos m ON m.instance_id = mc.instance_id AND m.user_name = mc.user_name AND m.name = l.name
			WHERE l.seq > ?
			ORDER BY l.seq ASC
			LIMIT ?`,
			instance, user, baseSeq, maxSeq, afterSeq, limit+1)
		if err != nil {
			return SyncNotesResponse{}, err
		}
		type deltaRow struct {
			seq     int64
			name    string
			deleted int
			payload []byte
		}
		entries := make([]deltaRow, 0, min(limit+1, 256))
		for rows.Next() {
			var entry deltaRow
			if err := rows.Scan(&entry.seq, &entry.name, &entry.deleted, &entry.payload); err != nil {
				rows.Close()
				return SyncNotesResponse{}, err
			}
			entries = append(entries, entry)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return SyncNotesResponse{}, err
		}
		if len(entries) > limit {
			entries = entries[:limit]
			result.HasMore = true
		}
		for _, entry := range entries {
			if entry.deleted != 0 || len(entry.payload) == 0 {
				result.Deleted = append(result.Deleted, entry.name)
				continue
			}
			memo, err := decodeMemo(entry.payload)
			if err != nil {
				return SyncNotesResponse{}, err
			}
			if memo.Creator != user {
				result.Deleted = append(result.Deleted, entry.name)
				continue
			}
			result.Items = append(result.Items, memo.Note(s.Settings))
		}
		if result.HasMore {
			last := entries[len(entries)-1]
			result.NextCursor = signer.signCursor(syncNotesCursor{
				Version: 1, InstanceID: instance, UserName: user, Generation: syncGeneration,
				Phase: "delta", BaseSeq: baseSeq, MaxSeq: maxSeq, AfterSeq: last.seq,
			})
			return result, nil
		}
	}

	result.NextCursor = signer.signCursor(syncNotesCursor{
		Version: 1, InstanceID: instance, UserName: user, Generation: syncGeneration,
		Phase: "delta", BaseSeq: maxSeq, MaxSeq: maxSeq, AfterSeq: maxSeq,
	})
	return result, nil
}

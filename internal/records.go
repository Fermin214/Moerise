package moerise

import (
	"context"
	"database/sql"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

var idempotencyPattern = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,128}$`)
var tagPattern = regexp.MustCompile(`^[\p{L}\p{Nd}_-]+(/[\p{L}\p{Nd}_-]+)*$`)

type noteWrite struct {
	Name   string
	Status string
	New    bool
}

func prepareNote(input CreateNote) (string, string, error) {
	if strings.TrimSpace(input.Content) == "" || len(input.Tags) > 20 {
		return "", "", failure("INVALID_ARGUMENT", "Content or tags are invalid", 422, false)
	}
	tags := slices.Clone(input.Tags)
	for _, tag := range tags {
		if utf8.RuneCountInString(tag) > 100 || !tagPattern.MatchString(tag) {
			return "", "", failure("INVALID_ARGUMENT", "Tags must use letters, decimal digits, underscores, hyphens or slash-separated segments", 422, false)
		}
	}
	slices.Sort(tags)
	tags = slices.Compact(tags)
	if tags == nil {
		tags = []string{}
	}
	content := input.Content
	if len(tags) > 0 {
		content = "#" + strings.Join(tags, " #") + "\n\n" + content
	}
	if len(content) > 65536 {
		return "", "", failure("INVALID_ARGUMENT", "Content including tags exceeds 65536 UTF-8 bytes", 422, false)
	}
	return content, hashBytes(canonicalJSON(CreateNote{Content: input.Content, Tags: tags})), nil
}

func (s *Store) reserveNote(key, hash string) (noteWrite, error) {
	var result noteWrite
	err := transaction(s.state, func(tx *sql.Tx) error {
		stamp := nowText()
		inserted, err := tx.Exec(`INSERT OR IGNORE INTO note_writes
			(instance_id, user_name, idempotency_key, request_hash, memo_name, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`, s.Settings.Memos.InstanceID, s.Settings.Memos.User,
			key, hash, "memos/"+newID(), stamp, stamp)
		if err != nil {
			return err
		}
		changed, err := inserted.RowsAffected()
		if err != nil {
			return err
		}
		result.New = changed == 1
		var savedHash string
		if err := tx.QueryRow(`SELECT memo_name, status, request_hash FROM note_writes
			WHERE instance_id = ? AND user_name = ? AND idempotency_key = ?`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, key).Scan(&result.Name, &result.Status, &savedHash); err != nil {
			return err
		}
		if savedHash != hash {
			return failure("IDEMPOTENCY_CONFLICT", "This idempotency key belongs to a different request", 409, false)
		}
		return nil
	})
	return result, err
}

func (s *Store) updateNoteWrite(key, status, code string) error {
	var lastError any
	if code != "" {
		lastError = code
	}
	_, err := s.state.Exec(`UPDATE note_writes SET status = ?, last_error = ?, updated_at = ?
		WHERE instance_id = ? AND user_name = ? AND idempotency_key = ?`,
		status, lastError, nowText(), s.Settings.Memos.InstanceID, s.Settings.Memos.User, key)
	return err
}

type RecordService struct {
	Store  *Store
	Source Source
	gate   chan struct{}
}

func NewRecordService(store *Store, source Source) *RecordService {
	return &RecordService{Store: store, Source: source, gate: make(chan struct{}, 1)}
}

func (s *RecordService) verify(ctx context.Context) error {
	version, err := s.Source.Identity(ctx)
	if err != nil {
		if healthErr := s.Store.SourceHealth(false, errorCode(err), ""); healthErr != nil {
			return healthErr
		}
		return err
	}
	return s.Store.SourceHealth(true, "", version)
}

func (s *RecordService) Create(ctx context.Context, key string, input CreateNote) (Note, int, error) {
	if !idempotencyPattern.MatchString(key) {
		return Note{}, 0, failure("INVALID_ARGUMENT", "A valid Idempotency-Key header is required", 422, false)
	}
	content, hash, err := prepareNote(input)
	if err != nil {
		return Note{}, 0, err
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return Note{}, 0, upstreamError("UPSTREAM_UNAVAILABLE", "The request was canceled before writing")
	}
	if err := ctx.Err(); err != nil {
		return Note{}, 0, upstreamError("UPSTREAM_UNAVAILABLE", "The request was canceled before writing")
	}
	write, err := s.Store.reserveNote(key, hash)
	if err != nil {
		return Note{}, 0, err
	}
	if err := s.verify(ctx); err != nil {
		return Note{}, 0, err
	}
	memo, err := s.Source.Get(ctx, write.Name)
	if err != nil {
		return Note{}, 0, err
	}
	if write.Status == "succeeded" {
		if memo == nil {
			if err := s.Store.Refresh(write.Name, nil); err != nil {
				return Note{}, 0, err
			}
			return Note{}, 0, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
		}
		return s.finish(ctx, key, memo, 200)
	}
	if memo != nil {
		return s.reconcile(ctx, key, memo, content, input.Tags, 200)
	}
	// Persist uncertainty before the network call so a crash cannot lose a sent write.
	if err := s.Store.updateNoteWrite(key, "unknown", ""); err != nil {
		return Note{}, 0, err
	}
	_, err = s.Source.Create(ctx, write.Name, content)
	status := 200
	if write.New {
		status = 201
	}
	conflict := errorCode(err) == "UPSTREAM_RESOURCE_EXISTS"
	if err != nil {
		switch errorCode(err) {
		case "UPSTREAM_AUTH", "UPSTREAM_REQUEST_REJECTED", "SOURCE_NOT_FOUND":
			if saveErr := s.Store.updateNoteWrite(key, "pending", errorCode(err)); saveErr != nil {
				return Note{}, 0, saveErr
			}
			return Note{}, 0, err
		case "UPSTREAM_RESOURCE_EXISTS":
			status = 200
		default:
			return s.unknown(key, errorCode(err))
		}
	}
	memo, err = s.Source.Get(ctx, write.Name)
	if err != nil {
		return s.unknown(key, errorCode(err))
	}
	if memo == nil {
		if conflict {
			return Note{}, 0, failure("WRITE_CONFLICT", "The reserved Memos resource is unavailable or belongs to another author", 409, false)
		}
		return s.unknown(key, "SOURCE_NOT_FOUND")
	}
	return s.reconcile(ctx, key, memo, content, input.Tags, status)
}

func (s *RecordService) unknown(key, code string) (Note, int, error) {
	if err := s.Store.updateNoteWrite(key, "unknown", code); err != nil {
		return Note{}, 0, err
	}
	return Note{}, 0, upstreamError("WRITE_OUTCOME_UNKNOWN", "Write outcome is unknown; retry the same request with the same Idempotency-Key")
}

func (s *RecordService) reconcile(ctx context.Context, key string, memo *Memo, content string, tags []string, status int) (Note, int, error) {
	if memo.Content != content || memo.Visibility != "PRIVATE" {
		return Note{}, 0, failure("WRITE_CONFLICT", "The reserved Memos resource differs from this request", 409, false)
	}
	for _, tag := range tags {
		if !slices.Contains(memo.Tags, tag) {
			return Note{}, 0, failure("WRITE_CONFLICT", "Memos did not preserve the requested tags; inspect the source memo", 409, false)
		}
	}
	return s.finish(ctx, key, memo, status)
}

func (s *RecordService) finish(ctx context.Context, key string, memo *Memo, status int) (Note, int, error) {
	if err := s.verify(ctx); err != nil {
		return Note{}, 0, err
	}
	if err := s.Store.updateNoteWrite(key, "succeeded", ""); err != nil {
		return Note{}, 0, err
	}
	if err := s.Store.Refresh(memo.Name, memo); err != nil {
		return Note{}, 0, err
	}
	return memo.Note(s.Store.Settings), status, nil
}

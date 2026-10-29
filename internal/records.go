package moerise

import (
	"context"
	"database/sql"
	"errors"
	"mime"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var idempotencyPattern = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,128}$`)
var tagPattern = regexp.MustCompile(`^[\p{L}\p{Nd}_-]+(/[\p{L}\p{Nd}_-]+)*$`)

type noteWrite struct {
	Name          string
	Status        string
	New           bool
	SourceCreated bool
}

func prepareNote(input CreateNote) (string, string, error) {
	if len(input.Tags) != 0 {
		return "", "", failure("INVALID_ARGUMENT", "Put native #tags in content instead of tags", 422, false)
	}
	if (strings.TrimSpace(input.Content) == "" && len(input.Attachments) == 0) || len(input.Content) > 65536 {
		return "", "", failure("INVALID_ARGUMENT", "A note requires content or attachments; content cannot exceed 65536 UTF-8 bytes", 422, false)
	}
	if err := validateAttachments(input.Attachments); err != nil {
		return "", "", err
	}
	return input.Content, noteRequestHash(input, []string{}), nil
}

// Historical digests and injected content are only accepted for an existing write.
func prepareLegacyNote(input CreateNote) (string, string, error) {
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
	if err := validateAttachments(input.Attachments); err != nil {
		return "", "", err
	}
	return content, noteRequestHash(input, tags), nil
}

func validateAttachments(files []CreateAttachment) error {
	if len(files) > 10 {
		return failure("INVALID_ARGUMENT", "At most 10 attachments are allowed", 422, false)
	}
	total := 0
	for _, attachment := range files {
		mediaType, parameters, err := mime.ParseMediaType(attachment.Type)
		if attachment.Filename == "" || len(attachment.Filename) > 255 ||
			strings.ContainsAny(attachment.Filename, "/\\\x00\r\n") ||
			strings.ContainsFunc(attachment.Filename, unicode.IsControl) ||
			strings.TrimSpace(attachment.Filename) != attachment.Filename ||
			attachment.Filename == "." || attachment.Filename == ".." ||
			err != nil || len(parameters) != 0 || mediaType != attachment.Type ||
			!strings.Contains(mediaType, "/") || len(attachment.Content) == 0 {
			return failure("INVALID_ARGUMENT", "Invalid attachment filename, MIME type or content", 422, false)
		}
		total += len(attachment.Content)
	}
	if total > 8*1024*1024 {
		return failure("INVALID_ARGUMENT", "Attachments exceed 8 MiB in total", 422, false)
	}
	return nil
}

func noteRequestHash(input CreateNote, tags []string) string {
	// Hash file digests rather than retaining their bytes in product state.
	payload := map[string]any{"content": input.Content, "tags": tags}
	if len(input.Attachments) > 0 {
		files := []map[string]string{}
		for _, attachment := range input.Attachments {
			files = append(files, map[string]string{"filename": attachment.Filename, "type": attachment.Type,
				"sha256": hashBytes(attachment.Content)})
		}
		payload["attachments"] = files
	}
	return hashBytes(canonicalJSON(payload))
}

func (s *Store) savedNoteHash(key string) (string, error) {
	var hash string
	err := s.state.QueryRow(`SELECT request_hash FROM note_writes
		WHERE instance_id = ? AND user_name = ? AND idempotency_key = ?`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User, key).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return hash, err
}

func (s *RecordService) prepare(key string, input CreateNote) (string, string, error) {
	if len(input.Tags) == 0 {
		return prepareNote(input)
	}
	content, hash, err := prepareLegacyNote(input)
	if err != nil {
		return "", "", err
	}
	saved, err := s.Store.savedNoteHash(key)
	if err != nil {
		return "", "", err
	}
	if saved == "" {
		return "", "", failure("INVALID_ARGUMENT", "Put native #tags in content instead of tags", 422, false)
	}
	if saved != hash {
		return "", "", failure("IDEMPOTENCY_CONFLICT", "This idempotency key belongs to a different request", 409, false)
	}
	return content, hash, nil
}

func (s *Store) reserveNote(key, hash string) (noteWrite, error) {
	var result noteWrite
	err := transaction(s.state, func(tx *sql.Tx) error {
		stamp := nowText()
		inserted, err := tx.Exec(`INSERT OR IGNORE INTO note_writes
			(instance_id, user_name, idempotency_key, request_hash, memo_name, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`, s.Settings.Memos.InstanceID, s.Settings.Memos.User,
			key, hash, "memos/"+derivedID(s.Settings.CursorSecret, "memo", s.Settings.Memos.InstanceID, s.Settings.Memos.User, key), stamp, stamp)
		if err != nil {
			return err
		}
		changed, err := inserted.RowsAffected()
		if err != nil {
			return err
		}
		result.New = changed == 1
		var savedHash string
		if err := tx.QueryRow(`SELECT memo_name, status, request_hash, source_created FROM note_writes
			WHERE instance_id = ? AND user_name = ? AND idempotency_key = ?`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, key).Scan(&result.Name, &result.Status, &savedHash, &result.SourceCreated); err != nil {
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
	content, hash, err := s.prepare(key, input)
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
	if memo == nil && write.SourceCreated {
		return Note{}, 0, failure("SOURCE_NOT_FOUND", "The partially recorded memo was deleted", 404, false)
	}
	if memo != nil {
		return s.reconcile(ctx, key, memo, content, input, 200)
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
	return s.reconcile(ctx, key, memo, content, input, status)
}

func (s *RecordService) unknown(key, code string) (Note, int, error) {
	if err := s.Store.updateNoteWrite(key, "unknown", code); err != nil {
		return Note{}, 0, err
	}
	return Note{}, 0, upstreamError("WRITE_OUTCOME_UNKNOWN", "Write outcome is unknown; retry the same request with the same Idempotency-Key")
}

func (s *RecordService) reconcile(ctx context.Context, key string, memo *Memo, content string, input CreateNote, status int) (Note, int, error) {
	if memo.Content != content || memo.Visibility != "PRIVATE" {
		return Note{}, 0, failure("WRITE_CONFLICT", "The reserved Memos resource differs from this request", 409, false)
	}
	for _, tag := range input.Tags {
		if !slices.Contains(memo.Tags, tag) {
			return Note{}, 0, failure("WRITE_CONFLICT", "Memos did not preserve the requested tags; inspect the source memo", 409, false)
		}
	}
	if len(input.Attachments) > 0 {
		if _, err := s.Store.state.Exec(`UPDATE note_writes SET source_created = 1 WHERE
			instance_id = ? AND user_name = ? AND idempotency_key = ?`,
			s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, key); err != nil {
			return Note{}, 0, err
		}
		var err error
		memo, err = s.attach(ctx, key, memo, input.Attachments)
		if err != nil {
			if errorCode(err) == "WRITE_CONFLICT" || errorCode(err) == "IDENTITY_MISMATCH" ||
				errorCode(err) == "SOURCE_NOT_FOUND" {
				return Note{}, 0, err
			}
			return s.unknown(key, errorCode(err))
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

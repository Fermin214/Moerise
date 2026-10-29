package moerise

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"sync"
)

type UpdateNote struct {
	Content           *string             `json:"content,omitempty"`
	State             string              `json:"state,omitempty"`
	SourceHash        string              `json:"source_hash"`
	Attachments       *[]CreateAttachment `json:"attachments,omitempty"`
	RemoveAttachments []string            `json:"remove_attachments,omitempty"`
}

type memoMutationSource interface {
	UpdateMemo(context.Context, string, *string, string) (*Memo, error)
	DeleteMemo(context.Context, string) error
}

type attachmentMutationSource interface {
	attachmentSource
	DeleteAttachment(context.Context, string) error
}

// The single-user service serializes mutations across NoteService instances.
var noteMutationMu sync.Mutex

type noteMutation struct {
	Step       int    `json:"step"`
	Checkpoint string `json:"checkpoint"`
	Pending    string `json:"pending,omitempty"`
}

type noteMutationStep struct {
	Kind string
	Name string
	File CreateAttachment
}

func sourceChanged() error {
	return failure("SOURCE_CHANGED", "Source memo changed; reload the note before writing", 409, true)
}

func validSourceHash(hash string) bool {
	return len(hash) == 64 && strings.IndexFunc(hash, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	}) == -1
}

func (s *NoteService) mutationMemo(ctx context.Context, name string) (*Memo, error) {
	if err := s.Verify(ctx); err != nil {
		return nil, err
	}
	memo, err := s.fresh(ctx, name)
	if err != nil {
		return nil, err
	}
	if memo == nil {
		return nil, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
	}
	return memo, nil
}

func validateUpdate(input UpdateNote) ([]CreateAttachment, error) {
	files := []CreateAttachment{}
	if input.Attachments != nil {
		files = *input.Attachments
	}
	if !validSourceHash(input.SourceHash) ||
		(input.State != "" && input.State != "NORMAL" && input.State != "ARCHIVED") ||
		(input.Content != nil && len(*input.Content) > 65536) ||
		len(input.RemoveAttachments) > 10 {
		return nil, failure("INVALID_ARGUMENT", "Invalid content, state, source_hash or attachments", 422, false)
	}
	if err := validateAttachments(files); err != nil {
		return nil, err
	}
	if input.Content == nil && input.State == "" && len(files) == 0 && len(input.RemoveAttachments) == 0 {
		return nil, failure("INVALID_ARGUMENT", "No note changes supplied", 422, false)
	}
	seen := map[string]bool{}
	for _, name := range input.RemoveAttachments {
		if !attachmentPattern.MatchString(name) || seen[name] {
			return nil, failure("INVALID_ARGUMENT", "Invalid or duplicate attachment to remove", 422, false)
		}
		seen[name] = true
	}
	return files, nil
}

func validateUpdateParent(memo *Memo, input UpdateNote, files []CreateAttachment) error {
	for _, name := range input.RemoveAttachments {
		if !hasAttachment(memo, name) {
			return failure("INVALID_ARGUMENT", "The attachment to remove must belong to this memo", 422, false)
		}
	}
	count := len(memo.Attachments) - len(input.RemoveAttachments) + len(files)
	content := memo.Content
	if input.Content != nil {
		content = *input.Content
	}
	if count > 10 || (strings.TrimSpace(content) == "" && count == 0) {
		return failure("INVALID_ARGUMENT", "A note requires content or attachments, with at most 10 attachments", 422, false)
	}
	return nil
}

func hasAttachment(memo *Memo, name string) bool {
	return slices.ContainsFunc(memo.Attachments, func(raw map[string]any) bool { return raw["name"] == name })
}

// Each intent contains only fingerprints. It shares the durable write journal and
// attachment-name ledger without retaining private content or attachment bytes.
func (s *NoteService) saveMutation(key string, journal noteMutation, status string) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	_, err = s.Store.state.Exec(`UPDATE note_writes SET status = ?, last_error = ?, updated_at = ?
		WHERE instance_id = ? AND user_name = ? AND idempotency_key = ?`,
		status, string(data), nowText(), s.Settings.Memos.InstanceID, s.Settings.Memos.User, key)
	return err
}

func mutationFingerprint(memo *Memo, step noteMutationStep, input UpdateNote, expected bool) string {
	copy := *memo
	copy.SourceHash, copy.ContentHash, copy.UpdateTime = "", "", ""
	copy.Attachments = slices.Clone(memo.Attachments)
	switch step.Kind {
	case "add":
		if !expected {
			copy.Attachments = slices.DeleteFunc(copy.Attachments, func(raw map[string]any) bool { return raw["name"] == step.Name })
		}
	case "remove":
		if expected {
			copy.Attachments = slices.DeleteFunc(copy.Attachments, func(raw map[string]any) bool { return raw["name"] == step.Name })
		}
	case "memo":
		if input.Content != nil {
			if expected {
				copy.Content = *input.Content
			}
			// Native tags are parsed by Memos from the new content.
			copy.Tags = nil
		}
		if expected && input.State != "" {
			copy.State = input.State
		}
	}
	return hashBytes(canonicalJSON(copy))
}

func mutationApplied(memo *Memo, step noteMutationStep, input UpdateNote, pending string) bool {
	if step.Kind == "add" {
		found := false
		for _, raw := range memo.Attachments {
			if raw["name"] == step.Name {
				found = raw["memo"] == memo.Name && raw["filename"] == step.File.Filename && raw["type"] == step.File.Type
			}
		}
		if !found {
			return false
		}
	}
	if step.Kind == "remove" && hasAttachment(memo, step.Name) {
		return false
	}
	return mutationFingerprint(memo, step, input, false) == pending
}

func (s *NoteService) confirmMutationAttachment(ctx context.Context, memo *Memo, step noteMutationStep) error {
	if step.Kind != "add" && step.Kind != "remove" {
		return nil
	}
	raw, err := s.Source.(attachmentMutationSource).GetAttachment(ctx, step.Name)
	if err != nil {
		return err
	}
	if step.Kind == "remove" {
		if raw != nil {
			return failure("WRITE_CONFLICT", "The removed attachment still exists", 409, false)
		}
		return nil
	}
	if raw == nil || raw["name"] != step.Name || raw["memo"] != memo.Name ||
		raw["filename"] != step.File.Filename || raw["type"] != step.File.Type {
		return failure("WRITE_CONFLICT", "The reserved attachment differs from this request", 409, false)
	}
	return nil
}

func (s *NoteService) applyMutation(ctx context.Context, memo *Memo, step noteMutationStep, input UpdateNote) error {
	switch step.Kind {
	case "memo":
		source := s.Source.(memoMutationSource)
		_, err := source.UpdateMemo(ctx, memo.Name, input.Content, input.State)
		return err
	case "add", "remove":
		source := s.Source.(attachmentMutationSource)
		raw, err := source.GetAttachment(ctx, step.Name)
		if err != nil {
			return err
		}
		if raw != nil && (raw["name"] != step.Name || raw["memo"] != memo.Name) {
			return failure("WRITE_CONFLICT", "Attachment belongs to a different resource", 409, false)
		}
		fresh, err := s.mutationMemo(ctx, memo.Name)
		if err != nil {
			return err
		}
		if fresh.SourceHash != memo.SourceHash {
			return sourceChanged()
		}
		if step.Kind == "remove" {
			if raw == nil {
				return failure("WRITE_CONFLICT", "The linked attachment is unavailable", 409, false)
			}
			return source.DeleteAttachment(ctx, step.Name)
		}
		if raw != nil {
			return failure("WRITE_CONFLICT", "The reserved attachment is not linked as expected", 409, false)
		}
		// Replays always use this persisted resource name, even after a lost response.
		return source.UploadAttachment(ctx, step.Name, memo.Name, step.File)
	}
	return nil
}

func (s *NoteService) Update(ctx context.Context, name, key string, input UpdateNote) (Note, error) {
	if !memoPattern.MatchString(name) {
		return Note{}, failure("INVALID_RESOURCE", "Invalid memo resource name", 422, false)
	}
	if !idempotencyPattern.MatchString(key) {
		return Note{}, failure("INVALID_ARGUMENT", "A valid Idempotency-Key header is required", 422, false)
	}
	files, err := validateUpdate(input)
	if err != nil {
		return Note{}, err
	}
	if _, ok := s.Source.(memoMutationSource); !ok {
		return Note{}, upstreamError("UPSTREAM_PROTOCOL", "Source does not support memo mutations")
	}
	if len(files)+len(input.RemoveAttachments) > 0 {
		if _, ok := s.Source.(attachmentMutationSource); !ok {
			return Note{}, upstreamError("UPSTREAM_PROTOCOL", "Source does not support attachment mutations")
		}
	}
	noteMutationMu.Lock()
	defer noteMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Note{}, err
	}
	// '~' cannot occur in a public creation key, isolating both write namespaces.
	writeKey := "~update:" + hashBytes([]byte(key))
	hash := hashBytes(canonicalJSON(map[string]any{"name": name, "input": input}))
	saved, err := s.Store.savedNoteHash(writeKey)
	if err != nil {
		return Note{}, err
	}
	if saved != "" && saved != hash {
		return Note{}, failure("IDEMPOTENCY_CONFLICT", "This idempotency key belongs to a different request", 409, false)
	}
	memo, err := s.mutationMemo(ctx, name)
	if err != nil {
		return Note{}, err
	}
	if saved == "" {
		if memo.SourceHash != input.SourceHash {
			return Note{}, sourceChanged()
		}
		if err := validateUpdateParent(memo, input, files); err != nil {
			return Note{}, err
		}
	}
	write, err := s.Store.reserveNote(writeKey, hash)
	if err != nil {
		return Note{}, err
	}
	var data sql.NullString
	if err := s.Store.state.QueryRow(`SELECT last_error FROM note_writes WHERE instance_id = ? AND user_name = ? AND idempotency_key = ?`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User, writeKey).Scan(&data); err != nil {
		return Note{}, err
	}
	journal := noteMutation{}
	if data.Valid {
		if json.Unmarshal([]byte(data.String), &journal) != nil || !validSourceHash(journal.Checkpoint) {
			return Note{}, upstreamError("WRITE_OUTCOME_UNKNOWN", "Invalid persisted mutation checkpoint")
		}
	} else {
		if memo.SourceHash != input.SourceHash {
			return Note{}, sourceChanged()
		}
		if err := validateUpdateParent(memo, input, files); err != nil {
			return Note{}, err
		}
		journal.Checkpoint = memo.SourceHash
		if err := s.saveMutation(writeKey, journal, "pending"); err != nil {
			return Note{}, err
		}
	}
	if write.Status == "succeeded" {
		if err := s.Verify(ctx); err != nil {
			return Note{}, err
		}
		return memo.Note(s.Settings), nil
	}
	steps := []noteMutationStep{}
	records := NewRecordService(s.Store, s.Source)
	for position, file := range files {
		resource, err := records.attachmentName(writeKey, position)
		if err != nil {
			return Note{}, err
		}
		steps = append(steps, noteMutationStep{Kind: "add", Name: resource, File: file})
	}
	if input.Content != nil || input.State != "" {
		steps = append(steps, noteMutationStep{Kind: "memo"})
	}
	for _, resource := range input.RemoveAttachments {
		steps = append(steps, noteMutationStep{Kind: "remove", Name: resource})
	}
	if journal.Step < 0 || journal.Step > len(steps) || (journal.Step == len(steps) && journal.Pending != "") {
		return Note{}, upstreamError("WRITE_OUTCOME_UNKNOWN", "Invalid persisted mutation step")
	}
	for journal.Step < len(steps) {
		step := steps[journal.Step]
		memo, err = s.mutationMemo(ctx, name)
		if err != nil {
			return Note{}, err
		}
		applied := journal.Pending != "" && mutationApplied(memo, step, input, journal.Pending)
		if !applied {
			if memo.SourceHash != journal.Checkpoint {
				return Note{}, sourceChanged()
			}
			journal.Pending = mutationFingerprint(memo, step, input, true)
			if err := s.saveMutation(writeKey, journal, "unknown"); err != nil {
				return Note{}, err
			}
			// Memos 0.31 has no CAS. Identity and source rechecks bound each write.
			if err := s.applyMutation(ctx, memo, step, input); err != nil {
				return Note{}, err
			}
			memo, err = s.mutationMemo(ctx, name)
			if err != nil {
				return Note{}, err
			}
			if !mutationApplied(memo, step, input, journal.Pending) {
				return Note{}, sourceChanged()
			}
		}
		if err := s.confirmMutationAttachment(ctx, memo, step); err != nil {
			return Note{}, err
		}
		journal.Step++
		journal.Checkpoint, journal.Pending = memo.SourceHash, ""
		if err := s.saveMutation(writeKey, journal, "pending"); err != nil {
			return Note{}, err
		}
	}
	memo, err = s.mutationMemo(ctx, name)
	if err != nil {
		return Note{}, err
	}
	if memo.SourceHash != journal.Checkpoint {
		return Note{}, sourceChanged()
	}
	if err := s.Verify(ctx); err != nil {
		return Note{}, err
	}
	if err := s.saveMutation(writeKey, journal, "succeeded"); err != nil {
		return Note{}, err
	}
	return memo.Note(s.Settings), nil
}

func (s *NoteService) Delete(ctx context.Context, name, sourceHash string) error {
	if !memoPattern.MatchString(name) {
		return failure("INVALID_RESOURCE", "Invalid memo resource name", 422, false)
	}
	if !validSourceHash(sourceHash) {
		return failure("INVALID_ARGUMENT", "A valid source_hash is required", 422, false)
	}
	source, ok := s.Source.(memoMutationSource)
	if !ok {
		return upstreamError("UPSTREAM_PROTOCOL", "Source does not support memo mutations")
	}
	noteMutationMu.Lock()
	defer noteMutationMu.Unlock()
	if err := s.Verify(ctx); err != nil {
		return err
	}
	memo, err := s.fresh(ctx, name)
	if err != nil {
		return err
	}
	if memo == nil {
		return s.Verify(ctx)
	}
	if memo.SourceHash != sourceHash {
		return sourceChanged()
	}
	if err := source.DeleteMemo(ctx, name); err != nil && errorCode(err) != "SOURCE_NOT_FOUND" {
		return err
	}
	if err := s.Verify(ctx); err != nil {
		return err
	}
	fresh, err := s.fresh(ctx, name)
	if err != nil {
		return err
	}
	if fresh != nil {
		return upstreamError("WRITE_OUTCOME_UNKNOWN", "Memos did not confirm deletion; reload the note")
	}
	return s.Verify(ctx)
}

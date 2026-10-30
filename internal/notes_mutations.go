package moerise

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
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
	CheckAttachmentRemoval(context.Context, string, []string) error
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
		(input.Content != nil && len(*input.Content) > 65536) {
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
	if strings.TrimSpace(content) == "" && count == 0 {
		return failure("INVALID_ARGUMENT", "A note requires content or attachments", 422, false)
	}
	return nil
}

func hasAttachment(memo *Memo, name string) bool {
	return slices.ContainsFunc(memo.Attachments, func(raw map[string]any) bool { return raw["name"] == name })
}

func attachmentReferenced() error {
	return failure("ATTACHMENT_REFERENCED",
		"Remove the native attachment image reference from the memo content before deleting the attachment", 422, false)
}

func attachmentDeleteRejected() error {
	return failure("ATTACHMENT_DELETE_REJECTED",
		"Memos rejected attachment deletion; correct managed image references in the memo content or check attachment group constraints", 422, false)
}

// 与 Memos 0.31 使用同版 Goldmark，只检查图片节点；普通链接与代码不构成附件引用。
func attachmentImageDestinations(content string) ([]string, error) {
	const domain = `(?:[\p{L}\p{N}_-]+\.)*[\p{L}\p{N}-]+\.[\p{L}\p{N}-]+`
	linkify := extension.NewLinkifyParser(
		extension.WithLinkifyAllowedProtocols([]string{"http:", "https:"}),
		extension.WithLinkifyURLRegexp(regexp.MustCompile(`^https?://`+domain+`[^<\x09-\x0d\p{Z}]*`)),
		extension.WithLinkifyWWWRegexp(regexp.MustCompile(`^www\.`+domain+`[^<\x09-\x0d\p{Z}]*`)),
		extension.WithLinkifyEmailRegexp(regexp.MustCompile(`a^`)),
	)
	md := goldmark.New(
		goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList),
		goldmark.WithParserOptions(parser.WithInlineParsers(util.Prioritized(linkify, 999))),
	)
	source := []byte(content)
	root := md.Parser().Parse(text.NewReader(source))
	// Memos 将裸目标中括号未闭合的引用定义当作普通文本。
	var masked []byte
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		definition, ok := node.(*ast.LinkReferenceDefinition)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		depth := 0
		for i := 0; i < len(definition.Destination); i++ {
			switch definition.Destination[i] {
			case '\\':
				if i+1 < len(definition.Destination) && util.IsPunct(definition.Destination[i+1]) {
					i++
				}
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth <= 0 {
			return ast.WalkContinue, nil
		}
		reader := text.NewBlockReader(source, definition.Lines())
		reader.SkipSpaces()
		if reader.Peek() != '[' {
			return ast.WalkContinue, nil
		}
		reader.Advance(1)
		if _, found := reader.FindClosure('[', ']', text.FindClosureOptions{Nesting: false, Newline: true, Advance: true}); !found || reader.Peek() != ':' {
			return ast.WalkContinue, nil
		}
		_, position := reader.Position()
		colon := position.Start
		reader.Advance(1)
		reader.SkipSpaces()
		if colon >= 0 && colon < len(source) && source[colon] == ':' && reader.Peek() != '<' {
			if masked == nil {
				masked = slices.Clone(source)
			}
			masked[colon] = ';'
		}
		return ast.WalkContinue, nil
	})
	if masked != nil {
		root = md.Parser().Parse(text.NewReader(masked))
	}
	destinations := []string{}
	err := ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := node.(type) {
		case *ast.Image:
			destinations = append(destinations, string(node.Destination))
		case *ast.RawHTML:
			if strings.Contains(string(node.Segments.Value(source)), "/file/attachments/") {
				return ast.WalkStop, attachmentDeleteRejected()
			}
		case *ast.HTMLBlock:
			raw := string(node.Lines().Value(source))
			if node.HasClosure() {
				raw += string(node.ClosureLine.Value(source))
			}
			if strings.Contains(raw, "/file/attachments/") {
				return ast.WalkStop, attachmentDeleteRejected()
			}
		}
		return ast.WalkContinue, nil
	})
	return destinations, err
}

func (m *MemosClient) CheckAttachmentRemoval(ctx context.Context, content string, names []string) error {
	destinations, err := attachmentImageDestinations(content)
	if err != nil {
		return err
	}
	var instance *url.URL
	loadedOrigin := false
	for _, destination := range destinations {
		parsed, err := url.Parse(destination)
		if err != nil || !strings.HasPrefix(parsed.Path, "/file/attachments/") {
			continue
		}
		if parsed.IsAbs() || parsed.Host != "" {
			if !loadedOrigin {
				var profile struct {
					InstanceURL string `json:"instanceUrl"`
				}
				if err := m.request(ctx, "/api/v1/instance/profile", nil, &profile); err != nil {
					return err
				}
				if origin := strings.TrimSpace(profile.InstanceURL); origin != "" {
					instance, err = url.Parse(origin)
					if err != nil || !instance.IsAbs() || instance.Host == "" {
						return upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos instance URL")
					}
				}
				loadedOrigin = true
			}
			// instanceUrl 是 Memos 的来源配置，不能用 Moerise PublicURL 代替。
			if parsed.Scheme == "" {
				withScheme := *parsed
				if instance != nil {
					withScheme.Scheme = instance.Scheme
				}
				if instance == nil || sameAttachmentOrigin(&withScheme, instance) {
					return attachmentDeleteRejected()
				}
				continue
			}
			if instance == nil {
				return attachmentDeleteRejected()
			}
			if !sameAttachmentOrigin(parsed, instance) {
				continue
			}
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
			strings.Contains(parsed.EscapedPath(), "%") {
			return attachmentDeleteRejected()
		}
		parts := strings.Split(strings.TrimPrefix(parsed.Path, "/file/attachments/"), "/")
		if (len(parts) != 1 && len(parts) != 2) || !attachmentPattern.MatchString("attachments/"+parts[0]) ||
			(len(parts) == 2 && parts[1] == "") {
			return attachmentDeleteRejected()
		}
		if slices.Contains(names, "attachments/"+parts[0]) {
			return attachmentReferenced()
		}
	}
	return nil
}

func sameAttachmentOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if value := u.Port(); value != "" {
			return value
		}
		switch strings.ToLower(u.Scheme) {
		case "http":
			return "80"
		case "https":
			return "443"
		}
		return ""
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func (s *NoteService) checkAttachmentRemoval(ctx context.Context, memo *Memo, input UpdateNote) error {
	if len(input.RemoveAttachments) == 0 {
		return nil
	}
	content := memo.Content
	if input.Content != nil {
		content = *input.Content
	}
	return s.Source.(attachmentMutationSource).CheckAttachmentRemoval(ctx, content, input.RemoveAttachments)
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
		if err := s.checkAttachmentRemoval(ctx, memo, input); err != nil {
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
	if saved != "" {
		if err := s.checkAttachmentRemoval(ctx, memo, input); err != nil {
			return Note{}, err
		}
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

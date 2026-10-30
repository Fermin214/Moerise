package moerise

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type NoteService struct {
	Settings Settings
	Store    *Store
	Source   Source
}

func NewNoteService(settings Settings, store *Store, source Source) *NoteService {
	return &NoteService{settings, store, source}
}

func (s *NoteService) Verify(ctx context.Context) error {
	version, err := s.Source.Identity(ctx)
	if err != nil {
		if healthErr := s.Store.SourceHealth(false, errorCode(err), ""); healthErr != nil {
			return healthErr
		}
		return err
	}
	return s.Store.SourceHealth(true, "", version)
}

func (s *NoteService) fresh(ctx context.Context, name string) (*Memo, error) {
	memo, err := s.Source.Get(ctx, name)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		if healthErr := s.Store.SourceHealth(false, errorCode(err), ""); healthErr != nil {
			return nil, healthErr
		}
		return nil, err
	}
	if memo != nil && memo.Creator != s.Settings.Memos.User {
		memo = nil
	}
	if err := s.Store.Refresh(name, memo); err != nil {
		return nil, err
	}
	return memo, nil
}

func (s *NoteService) Get(ctx context.Context, name string) (Note, error) {
	if !memoPattern.MatchString(name) {
		return Note{}, failure("INVALID_RESOURCE", "Invalid memo resource name", 422, false)
	}
	if err := s.Verify(ctx); err != nil {
		return Note{}, err
	}
	memo, err := s.fresh(ctx, name)
	if err != nil {
		return Note{}, err
	}
	if memo == nil {
		return Note{}, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
	}
	if err := s.Verify(ctx); err != nil {
		return Note{}, err
	}
	return memo.Note(s.Settings), nil
}

func parseNoteList(query url.Values, live bool) (NoteList, error) {
	args := NoteList{State: "NORMAL", PageSize: 20}
	if live {
		args.Order = "created_desc"
	}
	for key, values := range query {
		if live && key == "tag" && len(values) > 0 {
			continue
		}
		if len(values) != 1 || (key != "state" && key != "page_size" && key != "page_token" &&
			!(live && (key == "order" || key == "query" || key == "date"))) {
			return args, failure("INVALID_ARGUMENT", "Invalid list parameters", 422, false)
		}
	}
	if query.Get("state") != "" {
		args.State = query.Get("state")
	}
	if query.Has("page_size") {
		var err error
		args.PageSize, err = strconv.Atoi(query.Get("page_size"))
		if err != nil {
			return args, failure("INVALID_ARGUMENT", "Invalid page size", 422, false)
		}
	}
	args.PageToken = query.Get("page_token")
	if query.Has("order") {
		args.Order = query.Get("order")
	}
	if args.PageSize < 1 || args.PageSize > 100 || len(args.PageToken) > 4096 ||
		(args.State != "NORMAL" && args.State != "ARCHIVED") || (live && args.Order != "created_desc") {
		return args, failure("INVALID_ARGUMENT", "Invalid list parameters", 422, false)
	}
	if live {
		args.Query, args.Tags, args.Date = query.Get("query"), query["tag"], query.Get("date")
		return normalizeLiveList(args)
	}
	return args, nil
}

func normalizeLiveList(args NoteList) (NoteList, error) {
	bad := failure("INVALID_ARGUMENT", "Invalid list parameters", 422, false)
	if args.State == "" {
		args.State = "NORMAL"
	}
	if args.Order == "" {
		args.Order = "created_desc"
	}
	if args.PageSize == 0 {
		args.PageSize = 20
	}
	if args.PageSize < 1 || args.PageSize > 100 || args.Order != "created_desc" ||
		(args.State != "NORMAL" && args.State != "ARCHIVED") || len(args.PageToken) > 4096 ||
		!utf8.ValidString(args.Query) || len(args.Query) > 1024 || utf8.RuneCountInString(args.Query) > 256 ||
		len(args.Tags) > 20 {
		return args, bad
	}
	args.Query = strings.TrimSpace(args.Query)
	args.Tags = slices.Clone(args.Tags)
	for _, tag := range args.Tags {
		if tag == "" || !utf8.ValidString(tag) || len(tag) > 400 || utf8.RuneCountInString(tag) > 100 {
			return args, bad
		}
	}
	slices.Sort(args.Tags)
	args.Tags = slices.Compact(args.Tags)
	if len(args.Tags) == 0 {
		args.Tags = nil
	}
	if args.Date != "" {
		date, err := time.Parse(time.DateOnly, args.Date)
		if err != nil || date.Year() < 1 || date.Format(time.DateOnly) != args.Date {
			return args, bad
		}
	}
	return args, nil
}

func (s *NoteService) cursorContext() []string {
	generation, _ := s.Store.Generation()
	return []string{s.Settings.Memos.InstanceID, s.Settings.Memos.User, generation}
}

func (s *NoteService) encodeCursor(state string, after []string) string {
	return s.signCursor([]any{1, s.cursorContext(), state, after})
}

func (s *NoteService) signCursor(value any) string {
	raw, _ := json.Marshal(value)
	sign := hmac.New(sha256.New, []byte(s.Settings.CursorSecret))
	sign.Write(raw)
	return base64.RawURLEncoding.EncodeToString(append(sign.Sum(nil), raw...))
}

func (s *NoteService) readCursor(token string, target any) error {
	invalid := failure("INVALID_PAGE_TOKEN", "Page token is invalid or expired", 422, false)
	data, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(data) < sha256.Size || len(token) > 4096 {
		return invalid
	}
	sign := hmac.New(sha256.New, []byte(s.Settings.CursorSecret))
	sign.Write(data[sha256.Size:])
	if !hmac.Equal(data[:sha256.Size], sign.Sum(nil)) || json.Unmarshal(data[sha256.Size:], target) != nil {
		return invalid
	}
	return nil
}

func (s *NoteService) decodeCursor(token, state string) ([]string, error) {
	invalid := failure("INVALID_PAGE_TOKEN", "Page token is invalid or expired", 422, false)
	var parts []json.RawMessage
	if s.readCursor(token, &parts) != nil || len(parts) != 4 {
		return nil, invalid
	}
	var version int
	var context, after []string
	var boundState string
	if json.Unmarshal(parts[0], &version) != nil || json.Unmarshal(parts[1], &context) != nil ||
		json.Unmarshal(parts[2], &boundState) != nil || json.Unmarshal(parts[3], &after) != nil ||
		version != 1 || !slices.Equal(context, s.cursorContext()) || boundState != state || len(after) != 2 {
		return nil, invalid
	}
	if _, err := time.Parse(time.RFC3339Nano, after[0]); err != nil {
		return nil, invalid
	}
	return after, nil
}

func (s *NoteService) ListIndexed(ctx context.Context, args NoteList) (NotePage, error) {
	result := NotePage{Notes: []Note{}}
	if args.State == "" {
		args.State = "NORMAL"
	}
	if args.PageSize == 0 {
		args.PageSize = 20
	}
	if args.PageSize < 1 || args.PageSize > 100 || (args.State != "NORMAL" && args.State != "ARCHIVED") {
		return result, failure("INVALID_ARGUMENT", "Invalid list parameters", 422, false)
	}
	var cursor []string
	var err error
	if args.PageToken != "" {
		cursor, err = s.decodeCursor(args.PageToken, args.State)
		if err != nil {
			return result, err
		}
	}
	if err := s.Verify(ctx); err != nil {
		return result, err
	}
	rows, err := s.Store.Page(args.State, args.PageSize+1, cursor)
	if err != nil {
		return result, err
	}
	var consumed []string
	for _, memo := range rows[:min(args.PageSize, len(rows))] {
		consumed = []string{memo.UpdateTime, memo.Name}
		fresh, err := s.fresh(ctx, memo.Name)
		if err != nil {
			return NotePage{}, err
		}
		if fresh != nil && fresh.State == args.State {
			result.Notes = append(result.Notes, fresh.Note(s.Settings))
		}
	}
	if len(rows) > args.PageSize && consumed != nil {
		result.NextPageToken = s.encodeCursor(args.State, consumed)
	}
	if err := s.Verify(ctx); err != nil {
		return NotePage{}, err
	}
	return result, nil
}

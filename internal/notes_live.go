package moerise

import (
	"context"
	"slices"
	"strings"
)

type liveListCursor struct {
	Kind    string   `json:"kind"`
	Context []string `json:"context"`
	State   string   `json:"state"`
	Order   string   `json:"order"`
	Token   string   `json:"token"`
}

func (s *NoteService) liveContext() []string {
	return []string{s.Settings.Memos.InstanceID, strings.TrimRight(s.Settings.Memos.BaseURL, "/"), s.Settings.Memos.User}
}

func (s *NoteService) ListLive(ctx context.Context, args NoteList) (NotePage, error) {
	result := NotePage{Notes: []Note{}}
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
		(args.State != "NORMAL" && args.State != "ARCHIVED") {
		return result, failure("INVALID_ARGUMENT", "Invalid list parameters", 422, false)
	}
	source, ok := s.Source.(LiveSource)
	if !ok {
		return result, upstreamError("UPSTREAM_PROTOCOL", "The source does not support live lists")
	}
	token := ""
	if args.PageToken != "" {
		var cursor liveListCursor
		if s.readCursor(args.PageToken, &cursor) != nil || cursor.Kind != "live-v1" ||
			!slices.Equal(cursor.Context, s.liveContext()) || cursor.State != args.State ||
			cursor.Order != args.Order || cursor.Token == "" {
			return result, failure("INVALID_PAGE_TOKEN", "Page token is invalid or expired", 422, false)
		}
		token = cursor.Token
	}
	if err := s.Verify(ctx); err != nil {
		return result, err
	}
	memos, next, err := source.ListLivePage(ctx, args.State, args.PageSize, token)
	if err != nil {
		return result, err
	}
	for _, candidate := range memos {
		memo, err := s.fresh(ctx, candidate.Name)
		if err != nil {
			return NotePage{}, err
		}
		if memo == nil || memo.State != args.State {
			continue
		}
		if memo.CreateTime != candidate.CreateTime {
			return NotePage{}, failure("SOURCE_CHANGED", "Memo creation time changed during pagination", 409, true)
		}
		result.Notes = append(result.Notes, memo.Note(s.Settings))
	}
	if err := s.Verify(ctx); err != nil {
		return NotePage{}, err
	}
	if next != "" {
		result.NextPageToken = s.signCursor(liveListCursor{"live-v1", s.liveContext(), args.State, args.Order, next})
	}
	return result, nil
}

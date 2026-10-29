package moerise

import (
	"context"
	"slices"
	"strings"
	"sync"
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
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	checked := make([]*Memo, len(memos))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(4, len(memos)) {
		workers.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				candidate := memos[i]
				memo, err := s.fresh(ctx, candidate.Name)
				if err != nil {
					cancel(err)
					return
				}
				if memo == nil || memo.State != args.State {
					continue
				}
				if memo.CreateTime != candidate.CreateTime {
					cancel(failure("SOURCE_CHANGED", "Memo creation time changed during pagination", 409, true))
					return
				}
				// 按候选位置保存，来源响应顺序不影响创建时间和名称排序。
				checked[i] = memo
			}
		})
	}
dispatch:
	for i := range memos {
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	workers.Wait()
	if err := context.Cause(ctx); err != nil {
		return NotePage{}, err
	}
	for _, memo := range checked {
		if memo != nil {
			result.Notes = append(result.Notes, memo.Note(s.Settings))
		}
	}
	if err := s.Verify(ctx); err != nil {
		return NotePage{}, err
	}
	if next != "" {
		result.NextPageToken = s.signCursor(liveListCursor{"live-v1", s.liveContext(), args.State, args.Order, next})
	}
	return result, nil
}

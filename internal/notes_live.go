package moerise

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type liveListCursor struct {
	Kind    string   `json:"kind"`
	Context []string `json:"context"`
	State   string   `json:"state"`
	Order   string   `json:"order"`
	Token   string   `json:"token"`
	Filter  string   `json:"filter"`
}

func (s *NoteService) liveContext() []string {
	return []string{s.Settings.Memos.InstanceID, strings.TrimRight(s.Settings.Memos.BaseURL, "/"), s.Settings.Memos.User}
}

func (s *NoteService) ListLive(ctx context.Context, args NoteList) (NotePage, error) {
	result := NotePage{Notes: []Note{}}
	args, err := normalizeLiveList(args)
	if err != nil {
		return result, err
	}
	filter, err := s.liveFilter(args)
	if err != nil {
		return result, err
	}
	boundFilters := []any{args.Query, args.Tags, args.Date}
	if args.Date != "" {
		boundFilters = append(boundFilters, s.Settings.Timezone)
	}
	binding := hashBytes(canonicalJSON(boundFilters))
	token := ""
	if args.PageToken != "" {
		var cursor liveListCursor
		if s.readCursor(args.PageToken, &cursor) != nil || cursor.Kind != "live-v1" ||
			!slices.Equal(cursor.Context, s.liveContext()) || cursor.State != args.State ||
			cursor.Order != args.Order || cursor.Token == "" ||
			(cursor.Filter != binding && !(cursor.Filter == "" && filter.empty())) {
			return result, failure("INVALID_PAGE_TOKEN", "Page token is invalid or expired", 422, false)
		}
		token = cursor.Token
	}
	if err := s.Verify(ctx); err != nil {
		return result, err
	}
	memos, next, err := s.livePage(ctx, args.State, args.PageSize, token, filter)
	if err != nil {
		return result, err
	}
	checked, _, err := s.checkLiveMemos(ctx, memos, args.State, filter, true, memosLiveMaxRetainedBytes)
	if err != nil {
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
		result.NextPageToken = s.signCursor(liveListCursor{
			Kind: "live-v1", Context: s.liveContext(), State: args.State, Order: args.Order, Token: next, Filter: binding,
		})
	}
	return result, nil
}

func (s *NoteService) liveFilter(args NoteList) (LiveFilter, error) {
	filter := LiveFilter{Query: args.Query, Tags: args.Tags}
	if args.Date != "" {
		location, err := time.LoadLocation(s.Settings.Timezone)
		if err != nil {
			return filter, failure("INVALID_CONFIGURATION", "Invalid product timezone", 500, false)
		}
		date, err := time.Parse(time.DateOnly, args.Date)
		if err != nil {
			return filter, failure("INVALID_ARGUMENT", "Invalid creation date", 422, false)
		}
		filter.CreatedFrom = productDayStart(date, location)
		filter.CreatedUntil = productDayStart(date.AddDate(0, 0, 1), location)
	}
	return filter, nil
}

func productDayStart(date time.Time, location *time.Location) time.Time {
	// 午夜可能被夏令时跳过；查找当地日期的起点，也能表示整日被跳过的空区间。
	target := date.Format(time.DateOnly)
	low, high := date.Unix()-48*60*60, date.Unix()+48*60*60
	for low < high {
		middle := low + (high-low)/2
		if time.Unix(middle, 0).In(location).Format(time.DateOnly) < target {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return time.Unix(low, 0).UTC()
}

func (s *NoteService) checkLiveMemos(ctx context.Context, memos []Memo, state string, filter LiveFilter, refresh bool, maxBytes int) ([]*Memo, int, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	checked := make([]*Memo, len(memos))
	checkedBytes := 0
	var budgetMu sync.Mutex
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(4, len(memos)) {
		workers.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				candidate := memos[i]
				var memo *Memo
				var err error
				if refresh {
					memo, err = s.fresh(ctx, candidate.Name)
				} else {
					memo, err = s.Source.Get(ctx, candidate.Name)
				}
				if err != nil {
					cancel(err)
					return
				}
				if memo != nil {
					raw, err := json.Marshal(memo)
					if err != nil {
						cancel(upstreamError("UPSTREAM_PROTOCOL", "Invalid source memo"))
						return
					}
					budgetMu.Lock()
					overBudget := len(raw) > maxBytes-checkedBytes
					if !overBudget {
						checkedBytes += len(raw)
					}
					budgetMu.Unlock()
					if overBudget {
						cancel(upstreamError("LIVE_LIST_LIMIT", "Source checks exceeded the byte budget"))
						return
					}
				}
				if memo == nil || memo.Creator != s.Settings.Memos.User || memo.State != state {
					continue
				}
				if memo.CreateTime != candidate.CreateTime {
					cancel(failure("SOURCE_CHANGED", "Memo creation time changed during pagination", 409, true))
					return
				}
				if !filter.matches(memo) {
					continue
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
		return nil, 0, err
	}
	return checked, checkedBytes, nil
}

func (s *NoteService) livePage(ctx context.Context, state string, size int, token string, filter LiveFilter) ([]Memo, string, error) {
	if source, ok := s.Source.(FilteredLiveSource); ok {
		return source.ListFilteredLivePage(ctx, state, size, token, filter)
	}
	source, ok := s.Source.(LiveSource)
	if !ok {
		return nil, "", upstreamError("UPSTREAM_PROTOCOL", "The source does not support live lists")
	}
	if filter.empty() {
		return source.ListLivePage(ctx, state, size, token)
	}
	var after memosLiveCursor
	if token != "" {
		data, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || json.Unmarshal(data, &after) != nil || after.Kind != "filtered-fallback-v1" ||
			!memoPattern.MatchString(after.Name) {
			return nil, "", failure("INVALID_PAGE_TOKEN", "Invalid live list page token", 422, false)
		}
		if _, err := time.Parse(time.RFC3339Nano, after.Created); err != nil {
			return nil, "", failure("INVALID_PAGE_TOKEN", "Invalid live list page token", 422, false)
		}
	}
	items := []Memo{}
	err := s.walkLive(ctx, state, filter, memosLiveLimits{memosLiveMaxRetainedMemos, memosLiveMaxRetainedBytes}, func(memos []Memo, _ *int) error {
		for _, memo := range memos {
			if after.Created == "" || memo.CreateTime < after.Created || (memo.CreateTime == after.Created && memo.Name < after.Name) {
				items = append(items, memo)
				items = finishLivePage(items, size)
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > size {
		last := items[size-1]
		data, _ := json.Marshal(memosLiveCursor{Kind: "filtered-fallback-v1", Created: last.CreateTime, Name: last.Name})
		next = base64.RawURLEncoding.EncodeToString(data)
		items = items[:size]
	}
	return items, next, nil
}

type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

type DayCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

func (s *NoteService) Tags(ctx context.Context, state string) ([]TagCount, error) {
	counts := map[string]int{}
	err := s.aggregateLive(ctx, state, LiveFilter{}, func(memo *Memo) {
		seen := map[string]bool{}
		for _, tag := range memo.Tags {
			if !seen[tag] {
				counts[tag]++
				seen[tag] = true
			}
		}
	})
	if err != nil {
		return nil, err
	}
	result := make([]TagCount, 0, len(counts))
	for tag, count := range counts {
		result = append(result, TagCount{Tag: tag, Count: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Tag < result[j].Tag })
	return result, nil
}

func (s *NoteService) Calendar(ctx context.Context, state, month string) ([]DayCount, error) {
	date, err := time.Parse("2006-01", month)
	if err != nil || date.Year() < 1 || date.Format("2006-01") != month {
		return nil, failure("INVALID_ARGUMENT", "Invalid calendar month", 422, false)
	}
	filter, err := s.liveFilter(NoteList{Date: month + "-01"})
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(s.Settings.Timezone)
	if err != nil {
		return nil, failure("INVALID_CONFIGURATION", "Invalid product timezone", 500, false)
	}
	filter.CreatedUntil = productDayStart(date.AddDate(0, 1, 0), location)
	counts := map[string]int{}
	err = s.aggregateLive(ctx, state, filter, func(memo *Memo) {
		created, _ := time.Parse(time.RFC3339Nano, memo.CreateTime)
		counts[created.In(location).Format(time.DateOnly)]++
	})
	if err != nil {
		return nil, err
	}
	result := make([]DayCount, 0, len(counts))
	for date, count := range counts {
		result = append(result, DayCount{Date: date, Count: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Date < result[j].Date })
	return result, nil
}

func (s *NoteService) aggregateLive(ctx context.Context, state string, filter LiveFilter, count func(*Memo)) error {
	args, err := normalizeLiveList(NoteList{State: state})
	if err != nil {
		return err
	}
	if err := s.Verify(ctx); err != nil {
		return err
	}
	limits := memosLiveLimits{memosLiveMaxRetainedMemos, memosLiveMaxRetainedBytes}
	if err := s.walkLive(ctx, args.State, filter, limits, func(memos []Memo, bytes *int) error {
		checked, used, err := s.checkLiveMemos(ctx, memos, args.State, filter, false, limits.maxBytes-*bytes)
		if err != nil {
			return err
		}
		*bytes += used
		for _, memo := range checked {
			if memo == nil {
				continue
			}
			count(memo)
		}
		return nil
	}); err != nil {
		return err
	}
	return s.Verify(ctx)
}

// walkLive 仅保留一页正文，按全量扫描的数量、字节和请求预算约束去重集合。
func (s *NoteService) walkLive(ctx context.Context, state string, filter LiveFilter, limits memosLiveLimits, visit func([]Memo, *int) error) error {
	source, ok := s.Source.(LiveSource)
	if !ok {
		return upstreamError("UPSTREAM_PROTOCOL", "The source does not support live lists")
	}
	seenNames, seenTokens := map[string]bool{}, map[string]bool{}
	token, previous := "", ""
	count, bytes := 0, 0
	for range min(s.Settings.Memos.MaxPages, limits.maxMemos+1) {
		if err := ctx.Err(); err != nil {
			return err
		}
		var memos []Memo
		var next string
		var err error
		if filtered, ok := s.Source.(FilteredLiveSource); ok {
			memos, next, err = filtered.ListFilteredLivePage(ctx, state, 100, token, filter)
		} else {
			memos, next, err = source.ListLivePage(ctx, state, 100, token)
		}
		if err != nil {
			return err
		}
		matching := []Memo{}
		for _, memo := range memos {
			if err := ctx.Err(); err != nil {
				return err
			}
			if seenNames[memo.Name] || (previous != "" && memo.CreateTime > previous) {
				return failure("SOURCE_CHANGED", "Memos changed during source aggregation", 409, true)
			}
			if memo.Creator != s.Settings.Memos.User || memo.State != state || !memoPattern.MatchString(memo.Name) {
				return upstreamError("UPSTREAM_PROTOCOL", "Invalid source aggregation scope")
			}
			if _, err := time.Parse(time.RFC3339Nano, memo.CreateTime); err != nil {
				return upstreamError("UPSTREAM_PROTOCOL", "Invalid source creation time")
			}
			raw, err := json.Marshal(memo)
			if err != nil {
				return upstreamError("UPSTREAM_PROTOCOL", "Invalid aggregate memo")
			}
			if count >= limits.maxMemos || len(raw) > limits.maxBytes-bytes {
				return upstreamError("LIVE_LIST_LIMIT", "Source scan exceeded the result budget")
			}
			count++
			bytes += len(raw)
			seenNames[memo.Name], previous = true, memo.CreateTime
			if filter.matches(&memo) {
				matching = append(matching, memo)
			}
		}
		if err := visit(matching, &bytes); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		if seenTokens[next] || next == token {
			return upstreamError("UPSTREAM_PROTOCOL", "Source repeated a page token")
		}
		if len(next) > limits.maxBytes-bytes {
			return upstreamError("LIVE_LIST_LIMIT", "Source scan exceeded the byte budget")
		}
		bytes += len(next)
		seenTokens[next], token = true, next
	}
	return upstreamError("UPSTREAM_PAGINATION_LIMIT", "Source scan exceeded the page budget")
}

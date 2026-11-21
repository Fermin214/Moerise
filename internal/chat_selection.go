package moerise

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type ChatSelection struct {
	State, Query, FromDate, UntilDate, PageToken, TagMatch, Format string
	Tags                                                           []string
	PageSize                                                       int
}

type chatMaterialCursor struct {
	Kind, Binding, Phase, Token string
	Context                     []string
}

func parseChatSelection(query url.Values) (ChatSelection, error) {
	input := ChatSelection{State: "ALL", PageSize: 100}
	for key, values := range query {
		if !slices.Contains([]string{"state", "query", "tag", "tag_match", "format", "from", "until", "page_size", "page_token"}, key) ||
			(key != "tag" && len(values) != 1) {
			return input, failure("INVALID_ARGUMENT", "Invalid material selection parameters", 422, false)
		}
	}
	input.Query, input.Tags = query.Get("query"), query["tag"]
	input.TagMatch, input.Format = query.Get("tag_match"), query.Get("format")
	if (query.Has("tag_match") && !slices.Contains([]string{"any", "all"}, input.TagMatch)) ||
		(query.Has("format") && !slices.Contains([]string{"compact", "full"}, input.Format)) {
		return input, failure("INVALID_ARGUMENT", "Invalid material filter or format", 422, false)
	}
	input.FromDate, input.UntilDate, input.PageToken = query.Get("from"), query.Get("until"), query.Get("page_token")
	if query.Has("state") {
		input.State = query.Get("state")
	}
	if query.Has("page_size") {
		size, err := strconv.Atoi(query.Get("page_size"))
		if err != nil {
			return input, failure("INVALID_ARGUMENT", "Invalid material page size", 422, false)
		}
		input.PageSize = size
	}
	return input, nil
}

func (s *ChatService) Select(ctx context.Context, input ChatSelection) (NotePage, error) {
	result := NotePage{Notes: []Note{}}
	if input.State == "" {
		input.State = "ALL"
	}
	if !slices.Contains([]string{"ALL", "NORMAL", "ARCHIVED"}, input.State) {
		return result, failure("INVALID_ARGUMENT", "Invalid material state", 422, false)
	}
	args, err := normalizeLiveList(NoteList{State: "NORMAL", Query: input.Query, Tags: input.Tags,
		PageSize: input.PageSize, PageToken: input.PageToken})
	if err != nil {
		return result, err
	}
	filter := LiveFilter{Query: args.Query, Tags: args.Tags, AnyTag: input.TagMatch == "any"}
	location, err := time.LoadLocation(s.Store.Settings.Timezone)
	if err != nil {
		return result, err
	}
	for _, bound := range []struct {
		value  string
		target *time.Time
	}{{input.FromDate, &filter.CreatedFrom}, {input.UntilDate, &filter.CreatedUntil}} {
		if bound.value != "" {
			date, err := time.Parse(time.DateOnly, bound.value)
			if err != nil || date.Year() < 1 || date.Format(time.DateOnly) != bound.value {
				return result, failure("INVALID_ARGUMENT", "Invalid material date", 422, false)
			}
			*bound.target = productDayStart(date, location)
		}
	}
	if !filter.CreatedFrom.IsZero() && !filter.CreatedUntil.IsZero() && !filter.CreatedFrom.Before(filter.CreatedUntil) {
		return result, failure("INVALID_ARGUMENT", "Material dates must form a nonempty half-open interval", 422, false)
	}
	binding := hashBytes(canonicalJSON([]any{input.State, filter, s.Store.Settings.Timezone}))
	phase, token := input.State, ""
	if phase == "ALL" {
		phase = "NORMAL"
	}
	var after *[2]string
	if args.PageToken != "" {
		var cursor chatMaterialCursor
		if s.Notes.readCursor(args.PageToken, &cursor) != nil || cursor.Kind != "chat-materials-v1" ||
			!slices.Equal(cursor.Context, s.Notes.liveContext()) || cursor.Binding != binding ||
			!slices.Contains([]string{"NORMAL", "ARCHIVED"}, cursor.Phase) ||
			(input.State != "ALL" && cursor.Phase != input.State) {
			return result, failure("INVALID_PAGE_TOKEN", "Invalid material page token", 422, false)
		}
		phase, token = cursor.Phase, cursor.Token
		if token != "" {
			parts := strings.SplitN(token, "\x00", 2)
			if len(parts) != 2 || !memoPattern.MatchString(parts[1]) {
				return result, failure("INVALID_PAGE_TOKEN", "Invalid material page token", 422, false)
			}
			if _, err := time.Parse(time.RFC3339Nano, parts[0]); err != nil {
				return result, failure("INVALID_PAGE_TOKEN", "Invalid material page token", 422, false)
			}
			key := [2]string{parts[0], parts[1]}
			after = &key
		}
	}
	memos, err := s.Notes.Store.ListLocalMemos(ctx, []string{phase}, filter, after, args.PageSize+1)
	if err != nil {
		return result, err
	}
	next := ""
	if len(memos) > args.PageSize {
		memos = memos[:args.PageSize]
		last := memos[len(memos)-1]
		next = last.CreateTime + "\x00" + last.Name
	}
	for _, memo := range memos {
		result.Notes = append(result.Notes, memo.Note(s.Notes.Settings))
	}
	if next == "" && input.State == "ALL" && phase == "NORMAL" {
		archived, err := s.Notes.Store.ListLocalMemos(ctx, []string{"ARCHIVED"}, filter, nil, 1)
		if err != nil {
			return result, err
		}
		if len(archived) > 0 {
			phase = "ARCHIVED"
		} else {
			return result, nil
		}
	} else if next == "" {
		return result, nil
	}
	result.NextPageToken = s.Notes.signCursor(chatMaterialCursor{
		Kind: "chat-materials-v1", Binding: binding, Phase: phase, Token: next, Context: s.Notes.liveContext(),
	})
	return result, nil
}

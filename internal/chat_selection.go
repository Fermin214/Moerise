package moerise

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"time"
)

type ChatSelection struct {
	State, Query, FromDate, UntilDate, PageToken string
	Tags                                         []string
	PageSize                                     int
}

type chatMaterialCursor struct {
	Kind, Binding, Phase, Token string
	Context                     []string
}

func parseChatSelection(query url.Values) (ChatSelection, error) {
	input := ChatSelection{State: "ALL", PageSize: 100}
	for key, values := range query {
		if !slices.Contains([]string{"state", "query", "tag", "from", "until", "page_size", "page_token"}, key) ||
			(key != "tag" && len(values) != 1) {
			return input, failure("INVALID_ARGUMENT", "Invalid material selection parameters", 422, false)
		}
	}
	input.Query, input.Tags = query.Get("query"), query["tag"]
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
	filter := LiveFilter{Query: args.Query, Tags: args.Tags}
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
	if args.PageToken != "" {
		var cursor chatMaterialCursor
		if s.Notes.readCursor(args.PageToken, &cursor) != nil || cursor.Kind != "chat-materials-v1" ||
			!slices.Equal(cursor.Context, s.Notes.liveContext()) || cursor.Binding != binding ||
			!slices.Contains([]string{"NORMAL", "ARCHIVED"}, cursor.Phase) ||
			(input.State != "ALL" && cursor.Phase != input.State) {
			return result, failure("INVALID_PAGE_TOKEN", "Invalid material page token", 422, false)
		}
		phase, token = cursor.Phase, cursor.Token
	}
	if err := s.Notes.Verify(ctx); err != nil {
		return result, err
	}
	memos, next, err := s.Notes.livePage(ctx, phase, args.PageSize, token, filter)
	if err != nil {
		return result, err
	}
	checked, _, err := s.Notes.checkLiveMemos(ctx, memos, phase, filter, true, memosLiveMaxRetainedBytes)
	if err != nil {
		return result, err
	}
	for _, memo := range checked {
		if memo != nil {
			result.Notes = append(result.Notes, memo.Note(s.Notes.Settings))
		}
	}
	if err := s.Notes.Verify(ctx); err != nil {
		return NotePage{}, err
	}
	if next == "" && input.State == "ALL" && phase == "NORMAL" {
		phase = "ARCHIVED"
	} else if next == "" {
		return result, nil
	}
	result.NextPageToken = s.Notes.signCursor(chatMaterialCursor{
		Kind: "chat-materials-v1", Binding: binding, Phase: phase, Token: next, Context: s.Notes.liveContext(),
	})
	return result, nil
}

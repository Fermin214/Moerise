package moerise

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type EvidenceService struct{ Search *SearchService }

func NewEvidenceService(search *SearchService) *EvidenceService {
	return &EvidenceService{Search: search}
}

// Verify checks persisted legacy references before channel delivery.
func (s *EvidenceService) Verify(ctx context.Context, items []Evidence) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.Search.verify(ctx); err != nil {
		return err
	}
	for i, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		invalid := failure("INVALID_EVIDENCE", "Invalid evidence source or fragment", 422, false)
		if item.ID != "E"+strconv.Itoa(i+1) || !memoPattern.MatchString(item.Name) || item.SourceHash == "" {
			return invalid
		}
		fresh, err := s.Search.liveSearchMemo(ctx, item.Name)
		if err != nil {
			return err
		}
		if fresh == nil {
			return failure("SOURCE_NOT_FOUND", "Evidence source is unavailable", 404, false)
		}
		if fresh.SourceHash != item.SourceHash {
			return failure("SOURCE_CHANGED", "Evidence source changed; retry the request", 409, true)
		}
		if fresh.Name != item.Name || fresh.Creator != s.Search.Store.Settings.Memos.User ||
			!slices.Contains([]string{"NORMAL", "ARCHIVED"}, fresh.State) ||
			item.URL != fresh.Note(s.Search.Store.Settings).URL ||
			item.CreateTime != fresh.CreateTime || item.UpdateTime != fresh.UpdateTime {
			return invalid
		}
		content := fresh.Content
		if !utf8.ValidString(content) || item.ByteStart < 0 || item.ByteEnd <= item.ByteStart || item.ByteEnd > len(content) ||
			item.ByteStart > 0 && !utf8.RuneStart(content[item.ByteStart]) ||
			item.ByteEnd < len(content) && !utf8.RuneStart(content[item.ByteEnd]) {
			return invalid
		}
		text := content[item.ByteStart:item.ByteEnd]
		lineStart := 1 + strings.Count(content[:item.ByteStart], "\n")
		lineEnd := lineStart + strings.Count(strings.TrimSuffix(text, "\n"), "\n")
		if text != item.Content || item.LineStart != lineStart || item.LineEnd != lineEnd {
			return invalid
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Search.verify(ctx)
}

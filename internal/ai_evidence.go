package moerise

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const defaultEvidenceBudget = 32 * 1024

type EvidenceService struct {
	Search *SearchService
	Budget int
}

func NewEvidenceService(search *SearchService, budget int) *EvidenceService {
	if budget <= 0 {
		budget = defaultEvidenceBudget
	}
	return &EvidenceService{Search: search, Budget: budget}
}

func normalizeEvidence(request AIRequest) (SearchRequest, error) {
	bad := failure("INVALID_ARGUMENT", "Invalid evidence parameters", 422, false)
	if len(request.Names) > 20 {
		return SearchRequest{}, bad
	}
	seen := map[string]bool{}
	for _, name := range request.Names {
		if !memoPattern.MatchString(name) || seen[name] {
			return SearchRequest{}, bad
		}
		seen[name] = true
	}
	query := strings.TrimSpace(request.Question)
	if len(request.Names) == 0 || request.Question != "" {
		if query == "" || !utf8.ValidString(query) || len(query) > 1024 || utf8.RuneCountInString(query) > 256 {
			return SearchRequest{}, bad
		}
	}
	options, err := normalizeSearchOptions(SearchRequest{
		Query: query, Mode: "hybrid", Tags: request.Tags, State: request.State, Limit: 20,
	})
	if err != nil {
		return SearchRequest{}, err
	}
	for _, bound := range []struct {
		value  string
		target **time.Time
	}{{request.From, &options.createdFrom}, {request.Until, &options.createdUntil}} {
		if bound.value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, bound.value)
		if err != nil {
			return SearchRequest{}, bad
		}
		*bound.target = &parsed
	}
	if options.createdFrom != nil && options.createdUntil != nil &&
		!options.createdFrom.Before(*options.createdUntil) {
		return SearchRequest{}, bad
	}
	return options, nil
}

func evidenceFromChunk(note Note, chunk TextChunk) Evidence {
	return Evidence{Name: note.Name, SourceHash: note.SourceHash,
		ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd,
		LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, Content: chunk.Content,
		URL: note.URL, CreateTime: note.CreateTime, UpdateTime: note.UpdateTime}
}

func evidenceFromHits(hits []SearchHit) []Evidence {
	items := []Evidence{}
	for _, hit := range hits {
		for _, match := range hit.Matches {
			items = append(items, evidenceFromChunk(hit.Note, TextChunk{
				ByteStart: match.ByteStart, ByteEnd: match.ByteEnd,
				LineStart: match.LineStart, LineEnd: match.LineEnd, Content: match.Content,
			}))
		}
	}
	return items
}

func (s *EvidenceService) related(ctx context.Context, seed Memo, request SearchRequest) ([]SearchHit, SearchSemantic, error) {
	request.Limit = 3
	keywords, err := s.Search.Store.relatedLexicalCandidates(ctx, seed, request, false)
	if err != nil {
		return nil, SearchSemantic{}, err
	}
	tags, err := s.Search.Store.relatedLexicalCandidates(ctx, seed, request, true)
	if err != nil {
		return nil, SearchSemantic{}, err
	}
	semantic, status, err := s.Search.relatedSemanticCandidates(ctx, seed, request)
	if err != nil {
		return nil, SearchSemantic{}, err
	}
	// Identity and positions, rather than body equality, define duplicate evidence.
	hits, err := s.Search.loadSearchHits(ctx, request, fuseSearch(keywords, tags, semantic), seed.Name, false)
	return hits, status, err
}

func (s *EvidenceService) Collect(ctx context.Context, request AIRequest) (EvidenceSet, error) {
	options, err := normalizeEvidence(request)
	if err != nil {
		return EvidenceSet{}, err
	}
	result := EvidenceSet{Items: []Evidence{}, Omitted: []string{}, Semantic: SearchSemantic{Reason: "SELECTED_NOTES"}}
	candidates := []Evidence{}
	if len(request.Names) == 0 {
		response, err := s.Search.Search(ctx, options)
		if err != nil {
			return EvidenceSet{}, err
		}
		result.Semantic = response.Semantic
		candidates = evidenceFromHits(response.Results)
	} else {
		if err := s.Search.verify(ctx); err != nil {
			return EvidenceSet{}, err
		}
		selected := []Memo{}
		for _, name := range request.Names {
			memo, err := s.Search.liveSearchMemo(ctx, name)
			if err != nil {
				return EvidenceSet{}, err
			}
			if memo == nil {
				return EvidenceSet{}, failure("SOURCE_NOT_FOUND", "Evidence source is unavailable", 404, false)
			}
			if !searchAccepts(options, memo.State, memo.Tags) || !searchDateAccepts(options, memo.CreateTime) {
				result.Omitted = append(result.Omitted, name)
				continue
			}
			selected = append(selected, *memo)
			chunks := ChunkText(memo.Content)
			if len(chunks) == 0 {
				if len(request.Names) == 1 {
					return EvidenceSet{}, failure("INSUFFICIENT_EVIDENCE", "Source has no evidence text", 422, false)
				}
				result.Omitted = append(result.Omitted, name)
			}
			for _, chunk := range chunks {
				candidates = append(candidates, evidenceFromChunk(memo.Note(s.Search.Store.Settings), chunk))
			}
		}
		if len(request.Names) == 1 && len(selected) == 1 {
			hits, status, err := s.related(ctx, selected[0], options)
			if err != nil {
				return EvidenceSet{}, err
			}
			result.Semantic = status
			candidates = append(candidates, evidenceFromHits(hits)...)
		}
	}
	if err := s.fitBudget(&result, candidates, len(request.Names) == 1); err != nil {
		return EvidenceSet{}, err
	}
	if err := s.Verify(ctx, result.Items); err != nil {
		return EvidenceSet{}, err
	}
	return result, nil
}

func evidenceSize(item Evidence) int {
	data, _ := json.Marshal(item)
	return len(data)
}

// Only the first required seed may be shortened. Serialized metadata and JSON
// escaping participate in the byte budget, including array commas and brackets.
func fitSeed(item Evidence, available int) (Evidence, bool) {
	ends := []int{}
	for offset, r := range item.Content {
		ends = append(ends, offset+utf8.RuneLen(r))
	}
	prefix := func(end int) Evidence {
		copy := item
		copy.Content = item.Content[:end]
		copy.ByteEnd = item.ByteStart + end
		copy.LineEnd = item.LineStart + strings.Count(strings.TrimSuffix(copy.Content, "\n"), "\n")
		return copy
	}
	low, high := 0, len(ends)
	for low < high {
		middle := low + (high-low)/2
		if evidenceSize(prefix(ends[middle])) <= available {
			low = middle + 1
		} else {
			high = middle
		}
	}
	if low == 0 {
		return Evidence{}, false
	}
	item = prefix(ends[low-1])
	return item, strings.TrimSpace(item.Content) != ""
}

func (s *EvidenceService) fitBudget(result *EvidenceSet, candidates []Evidence, requiredSeed bool) error {
	budget := s.Budget
	if budget <= 0 {
		budget = defaultEvidenceBudget
	}
	if budget < 2 {
		return failure("EVIDENCE_BUDGET_EXCEEDED", "Evidence budget cannot hold the evidence array", 422, false)
	}
	used := 2
	type key struct {
		name, hash string
		start, end int
	}
	seen := map[key]bool{}
	omit := func(name string) {
		if !slices.Contains(result.Omitted, name) {
			result.Omitted = append(result.Omitted, name)
		}
	}
	for _, item := range candidates {
		id := key{item.Name, item.SourceHash, item.ByteStart, item.ByteEnd}
		if seen[id] {
			continue
		}
		seen[id] = true
		item.ID = "E" + strconv.Itoa(len(result.Items)+1)
		separator := 0
		if len(result.Items) > 0 {
			separator = 1
		}
		if used+separator+evidenceSize(item) > budget {
			omit(item.Name)
			if requiredSeed && len(result.Items) == 0 {
				cropped, ok := fitSeed(item, budget-used)
				if !ok {
					return failure("EVIDENCE_BUDGET_EXCEEDED", "Evidence budget cannot hold the required seed", 422, false)
				}
				item = cropped
			} else {
				continue
			}
		}
		result.Items = append(result.Items, item)
		used += separator + evidenceSize(item)
	}
	return nil
}

// Verify must also be called immediately before model invocation and before
// publishing generated output. Evidence is server-owned; models return IDs only.
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

func ValidateCitations(ids []string, items []Evidence) error {
	bad := failure("INVALID_CITATION", "Citation does not identify supplied evidence", 422, false)
	available := map[string]bool{}
	for i, item := range items {
		if item.ID != "E"+strconv.Itoa(i+1) {
			return bad
		}
		available[item.ID] = true
	}
	for _, id := range ids {
		if !available[id] {
			return bad
		}
	}
	return nil
}

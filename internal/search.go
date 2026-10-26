package moerise

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const searchCandidateLimit = 200

type searchCandidate struct {
	Name        string
	ContentHash string
	Start       int
	End         int
	Score       float64
	Similarity  *float64
	Signals     []string
}

func searchArgumentError() error {
	return failure("INVALID_ARGUMENT", "Search or discovery parameters are invalid", 422, false)
}

func (s *SearchService) acquire(ctx context.Context) (func(), error) {
	select {
	case s.queries <- struct{}{}:
		return func() { <-s.queries }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, failure("SEARCH_BUSY", "Search concurrency limit reached; retry shortly", 429, true)
	}
}

// Trigram FTS handles long terms; literal substring matching supplies 1–2 rune terms.
func keywordTerms(query string) (long, short []string) {
	seen := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	}) {
		runes := []rune(word)
		terms := []string{word}
		if slices.ContainsFunc(runes, func(r rune) bool { return unicode.Is(unicode.Han, r) }) && len(runes) > 3 {
			terms = nil
			for i := 0; i+3 <= len(runes); i++ {
				terms = append(terms, string(runes[i:i+3]))
			}
		}
		for _, term := range terms {
			if seen[term] || len(seen) >= 32 {
				continue
			}
			seen[term] = true
			if utf8.RuneCountInString(term) < 3 {
				short = append(short, term)
			} else {
				long = append(long, term)
			}
		}
	}
	return long, short
}

func (s *SearchService) lexical(ctx context.Context, query string, archived bool, tags []string) ([]searchCandidate, error) {
	long, short := keywordTerms(query)
	where := "m.instance_id = ? AND m.user_name = ?"
	args := []any{s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User}
	if !archived {
		where += " AND m.state = 'NORMAL'"
	}
	for _, tag := range tags {
		where += ` AND EXISTS (SELECT 1 FROM json_each(m.payload_json, '$.tags') t WHERE t.value = ? OR instr(t.value, ?) = 1)`
		args = append(args, tag, tag+"/")
	}
	matches := []string{}
	if len(long) > 0 {
		quoted := make([]string, len(long))
		for i, term := range long {
			quoted[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
		}
		matches = append(matches, `m.name IN (SELECT name FROM memo_fts WHERE memo_fts MATCH ? AND instance_id = ? AND user_name = ?)`)
		args = append(args, strings.Join(quoted, " OR "), s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User)
	}
	for _, term := range short {
		matches = append(matches, `instr(lower(json_extract(m.payload_json, '$.content')), ?) > 0`)
		args = append(args, term)
	}
	// Tags participate as a signal even when they aren't written inline in the body.
	for _, term := range append(slices.Clone(long), short...) {
		matches = append(matches, `EXISTS (SELECT 1 FROM json_each(m.payload_json, '$.tags') t WHERE instr(lower(t.value), ?) > 0)`)
		args = append(args, term)
	}
	if len(matches) > 0 {
		where += " AND (" + strings.Join(matches, " OR ") + ")"
	} else if len(tags) == 0 {
		return []searchCandidate{}, nil
	}
	// Rank in SQLite before limiting so older high-coverage matches are retained.
	terms := append(slices.Clone(long), short...)
	rank, rankArgs := []string{}, []any{}
	for _, term := range terms {
		rank = append(rank, `(instr(lower(json_extract(m.payload_json, '$.content')), ?) > 0)`)
		rankArgs = append(rankArgs, term)
	}
	rankSQL := "0"
	if len(rank) > 0 {
		rankSQL = strings.Join(rank, " + ")
	}
	args = append(rankArgs, args...)
	rows, err := s.Store.index.QueryContext(ctx, "SELECT m.name, m.content_hash, ("+rankSQL+") AS coverage FROM memos m WHERE "+where+
		" ORDER BY coverage DESC, m.update_time DESC, m.name LIMIT ?", append(args, searchCandidateLimit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []searchCandidate{}
	for rows.Next() {
		var candidate searchCandidate
		var coverage int
		if err := rows.Scan(&candidate.Name, &candidate.ContentHash, &coverage); err != nil {
			return nil, err
		}
		candidate.Signals = []string{"keyword"}
		if len(tags) > 0 {
			candidate.Signals = append(candidate.Signals, "tag")
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

func matchesTags(memo Memo, tags []string) bool {
	for _, required := range tags {
		if !slices.ContainsFunc(memo.Tags, func(tag string) bool { return tag == required || strings.HasPrefix(tag, required+"/") }) {
			return false
		}
	}
	return true
}

func keywordMatch(memo Memo, query string, tags []string) bool {
	long, short := keywordTerms(query)
	if len(long)+len(short) == 0 {
		return len(tags) > 0
	}
	text := strings.ToLower(memo.Content + " " + strings.Join(memo.Tags, " "))
	return slices.ContainsFunc(append(long, short...), func(term string) bool { return strings.Contains(text, term) })
}

func (s *SearchService) semantic(ctx context.Context, query string, archived bool, tags []string) ([]searchCandidate, error) {
	if !s.Store.Settings.Embedding.Enabled() || s.Embedder == nil {
		return nil, failure("EMBEDDING_DISABLED", "Configure embedding to enable semantic discovery", 503, false)
	}
	matrix, err := s.loadMatrix()
	if err != nil {
		return nil, err
	}
	if matrix.size() == 0 {
		return []searchCandidate{}, nil
	}
	vectors, err := s.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, upstreamError("EMBEDDING_PROTOCOL", "Invalid query embedding response")
	}
	if err := normalizeVector(vectors[0], matrix.dimension()); err != nil {
		return nil, err
	}
	return s.semanticVector(ctx, matrix, vectors[0], archived, tags)
}

func (s *SearchService) relatedCandidates(ctx context.Context, memo Memo, archived bool) ([]searchCandidate, error) {
	if !s.Store.Settings.Embedding.Enabled() || s.Embedder == nil {
		return nil, failure("EMBEDDING_DISABLED", "Configure embedding to enable semantic discovery", 503, false)
	}
	matrix, err := s.loadMatrix()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(memo.Content) == "" {
		return []searchCandidate{}, nil
	}
	vector := matrix.memoVector(memo.Name, memo.ContentHash)
	if vector == nil {
		return nil, upstreamError("SEARCH_NOT_READY", "The current memo version is still being indexed")
	}
	return s.semanticVector(ctx, matrix, vector, archived, nil)
}

func (s *SearchService) semanticVector(ctx context.Context, matrix vectorIndex, vector []float32, archived bool, tags []string) ([]searchCandidate, error) {
	// Filter before the top-k cut so archived notes and tag mismatches can't crowd out eligible results.
	allowed, err := namesQuery(s.Store.index, `SELECT m.name FROM memos m JOIN embedding_memos e ON e.name = m.name
		AND e.content_hash = m.content_hash WHERE e.space_id = ? AND m.instance_id = ? AND m.user_name = ?
		AND (? OR m.state = 'NORMAL')`, matrix.spaceID(), s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, archived)
	if err != nil {
		return nil, err
	}
	if len(tags) > 0 {
		for _, tag := range tags {
			names, err := namesQuery(s.Store.index, `SELECT m.name FROM memos m WHERE m.instance_id = ? AND m.user_name = ?
				AND EXISTS (SELECT 1 FROM json_each(m.payload_json, '$.tags') t WHERE t.value = ? OR instr(t.value, ?) = 1)`,
				s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, tag, tag+"/")
			if err != nil {
				return nil, err
			}
			for name := range allowed {
				if !names[name] {
					delete(allowed, name)
				}
			}
		}
	}
	return matrix.nearest(ctx, vector, allowed, searchCandidateLimit)
}

func validateSearch(request *SearchRequest) error {
	request.Query = strings.TrimSpace(request.Query)
	if request.Mode == "" {
		request.Mode = "hybrid"
	}
	if request.Limit == 0 {
		request.Limit = 10
	}
	if !utf8.ValidString(request.Query) || utf8.RuneCountInString(request.Query) > 500 ||
		(request.Query == "" && (len(request.Tags) == 0 || request.Mode == "semantic")) || request.Limit < 1 || request.Limit > 20 ||
		!slices.Contains([]string{"hybrid", "keyword", "semantic"}, request.Mode) || len(request.Tags) > 10 {
		return searchArgumentError()
	}
	for _, tag := range request.Tags {
		if utf8.RuneCountInString(tag) > 100 || !tagPattern.MatchString(tag) {
			return searchArgumentError()
		}
	}
	return nil
}

func (s *SearchService) Search(ctx context.Context, request SearchRequest) (SearchResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result := SearchResponse{Results: []SearchHit{}, Warnings: []string{}}
	if err := validateSearch(&request); err != nil {
		return result, err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	if err := s.verify(ctx); err != nil {
		return result, err
	}
	result.Mode = request.Mode
	if request.Query == "" {
		result.Mode = "keyword"
	}
	groups := [][]searchCandidate{}
	if request.Mode != "semantic" {
		candidates, err := s.lexical(ctx, request.Query, request.IncludeArchived, request.Tags)
		if err != nil {
			return result, err
		}
		groups = append(groups, candidates)
	}
	if request.Mode != "keyword" && request.Query != "" {
		candidates, err := s.semantic(ctx, request.Query, request.IncludeArchived, request.Tags)
		if err != nil {
			if request.Mode == "semantic" {
				return result, err
			}
			result.Mode, result.Warnings = "keyword", append(result.Warnings, errorCode(err))
		} else {
			groups = append(groups, candidates)
		}
	}
	merged := map[string]searchCandidate{}
	for _, group := range groups {
		for rank, candidate := range group {
			previous, ok := merged[candidate.Name]
			if !ok {
				previous = candidate
			} else if candidate.Similarity != nil {
				previous.Start, previous.End, previous.ContentHash, previous.Similarity = candidate.Start, candidate.End, candidate.ContentHash, candidate.Similarity
				previous.Signals = append(previous.Signals, "semantic")
			}
			previous.Score += 1 / float64(60+rank+1)
			merged[candidate.Name] = previous
		}
	}
	candidates := make([]searchCandidate, 0, len(merged))
	for _, candidate := range merged {
		candidates = append(candidates, candidate)
	}
	sortCandidates(candidates)
	for _, candidate := range candidates[:min(len(candidates), searchCandidateLimit)] {
		memo, err := s.live(ctx, candidate.Name)
		if err != nil {
			return SearchResponse{}, err
		}
		if memo == nil || (!request.IncludeArchived && memo.State != "NORMAL") || !matchesTags(*memo, request.Tags) {
			continue
		}
		if memo.ContentHash != candidate.ContentHash {
			if request.Mode == "semantic" || !keywordMatch(*memo, request.Query, request.Tags) {
				continue
			}
			candidate.Start, candidate.End, candidate.Similarity, candidate.Signals = 0, 0, nil, []string{"keyword"}
		}
		result.Results = append(result.Results, s.hit(*memo, candidate, request.Query))
		if len(result.Results) == request.Limit {
			break
		}
	}
	if err := s.verify(ctx); err != nil {
		return SearchResponse{}, err
	}
	return result, nil
}

func sortCandidates(candidates []searchCandidate) {
	slices.SortFunc(candidates, func(a, b searchCandidate) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

func (s *SearchService) hit(memo Memo, candidate searchCandidate, query string) SearchHit {
	runes := []rune(memo.Content)
	start, end := candidate.Start, candidate.End
	if start < 0 || end <= start || end > len(runes) {
		start, end = 0, min(240, len(runes))
		long, short := keywordTerms(query)
		for _, term := range append(long, short...) {
			if byteOffset := strings.Index(strings.ToLower(memo.Content), term); byteOffset >= 0 {
				// Match on runes because Unicode lowercase can change byte widths.
				lower := []rune(strings.ToLower(memo.Content))
				needle := []rune(term)
				for i := 0; i+len(needle) <= len(lower); i++ {
					if slices.Equal(lower[i:i+len(needle)], needle) {
						start, end = max(0, i-60), min(len(runes), i+180)
						break
					}
				}
				break
			}
		}
	}
	end = min(end, start+240)
	return SearchHit{memo.Note(s.Store.Settings), SearchSnippet{string(runes[start:end]), start, end, memo.SourceHash},
		candidate.Score, candidate.Signals, candidate.Similarity}
}

package moerise

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

func (r *RelatedRequest) UnmarshalJSON(data []byte) error {
	type wire RelatedRequest
	body := wire{Limit: 10}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return err
	}
	if body.Limit < 1 || body.Limit > 20 {
		return failure("INVALID_ARGUMENT", "Related note limit must be between 1 and 20", 422, false)
	}
	*r = RelatedRequest(body)
	return nil
}

func normalizeRelated(request RelatedRequest) (SearchRequest, error) {
	if !memoPattern.MatchString(request.Name) {
		return SearchRequest{}, failure("INVALID_ARGUMENT", "Invalid source memo name", 422, false)
	}
	return normalizeSearchOptions(SearchRequest{Tags: request.Tags, State: request.State, Limit: request.Limit})
}

func relatedTerms(content string) []string {
	counts := map[string]int{}
	for _, term := range searchTerms(content, false) {
		if len(term) <= 1+2*128 {
			counts[term]++
		}
	}
	terms := make([]string, 0, len(counts))
	for term := range counts {
		terms = append(terms, term)
	}
	slices.SortFunc(terms, func(a, b string) int {
		if counts[a] != counts[b] {
			return counts[b] - counts[a]
		}
		return strings.Compare(a, b)
	})
	return terms[:min(len(terms), 64)]
}

func (s *Store) relatedLexicalCandidates(ctx context.Context, seed Memo, request SearchRequest, tags bool) ([]searchCandidate, error) {
	where, args := searchFilter(s.Settings, request)
	where += " AND m.name != ?"
	args = append(args, seed.Name)
	from, order := "", ""
	if tags {
		if len(seed.Tags) == 0 {
			return nil, nil
		}
		data, err := json.Marshal(seed.Tags)
		if err != nil {
			return nil, err
		}
		overlap := "(SELECT COUNT(DISTINCT value) FROM json_each(m.payload_json, '$.tags') WHERE value IN (SELECT value FROM json_each(?)))"
		where += " AND " + overlap + " > 0"
		order = overlap + " DESC, m.name, c.ordinal"
		args = append(args, string(data), string(data))
	} else {
		terms := relatedTerms(seed.Content)
		if len(terms) == 0 {
			return nil, nil
		}
		from = " JOIN search_fts ON search_fts.rowid = c.id"
		where += " AND search_fts MATCH ?"
		args = append(args, `"`+strings.Join(terms, `" OR "`)+`"`)
		order = "bm25(search_fts), m.name, c.ordinal"
	}
	return s.rankedSearchCandidates(ctx, from, where, order, args)
}

func (s *SearchService) relatedSemanticCandidates(ctx context.Context, seed Memo, request SearchRequest) ([]searchCandidate, SearchSemantic, error) {
	candidates, semantic, err := s.allRelatedSemanticCandidates(ctx, seed, request)
	if err != nil {
		return nil, semantic, err
	}
	sortSearchCandidates(candidates)
	return candidates[:min(len(candidates), searchCandidates)], semantic, nil
}

func (s *SearchService) allRelatedSemanticCandidates(ctx context.Context, seed Memo, request SearchRequest) ([]searchCandidate, SearchSemantic, error) {
	unused := func(reason string) ([]searchCandidate, SearchSemantic, error) {
		return nil, SearchSemantic{Reason: reason}, nil
	}
	if !s.Store.Settings.Embedding.Enabled {
		return unused("EMBEDDING_DISABLED")
	}
	matrix, err := s.vectors(ctx)
	if err == sql.ErrNoRows {
		return unused("INDEX_NOT_READY")
	}
	if err != nil {
		return nil, SearchSemantic{}, err
	}
	// Equal weights for distinct normalized source chunks keep repeated passages from dominating.
	query := make([]float64, s.Store.Settings.Embedding.Dimensions)
	seen := map[string]bool{}
	for i, entry := range matrix.entries {
		if i%256 == 0 && ctx.Err() != nil {
			return nil, SearchSemantic{}, ctx.Err()
		}
		if entry.candidate.name != seed.Name || entry.candidate.sourceHash != seed.SourceHash {
			continue
		}
		hash := hashBytes([]byte(entry.candidate.chunk.Content))
		if seen[hash] {
			continue
		}
		seen[hash] = true
		for j, value := range entry.vector {
			query[j] += float64(value) / entry.norm
		}
	}
	if len(seen) == 0 {
		return unused("NO_SOURCE_VECTORS")
	}
	norm := float64(0)
	for i := range query {
		query[i] /= float64(len(seen))
		norm += query[i] * query[i]
	}
	if norm == 0 {
		return unused("NO_SOURCE_VECTORS")
	}
	norm = math.Sqrt(norm)
	for i := range query {
		query[i] /= norm
	}
	candidates := []searchCandidate{}
	for i, entry := range matrix.entries {
		if i%256 == 0 && ctx.Err() != nil {
			return nil, SearchSemantic{}, ctx.Err()
		}
		if entry.candidate.name == seed.Name || !searchAccepts(request, entry.state, entry.tags) ||
			!searchDateAccepts(request, entry.createTime) {
			continue
		}
		dot := float64(0)
		for j, value := range entry.vector {
			dot += float64(value) * query[j]
		}
		if similarity := dot / entry.norm; similarity > 0 {
			item := entry.candidate
			item.score = similarity
			candidates = append(candidates, item)
		}
	}
	return candidates, SearchSemantic{Used: true}, ctx.Err()
}

func (s *SearchService) Related(ctx context.Context, request RelatedRequest) (RelatedResponse, error) {
	options, err := normalizeRelated(request)
	if err != nil {
		return RelatedResponse{}, err
	}
	if err := s.verify(ctx); err != nil {
		return RelatedResponse{}, err
	}
	seed, err := s.liveSearchMemo(ctx, request.Name)
	if err != nil {
		return RelatedResponse{}, err
	}
	if seed == nil {
		return RelatedResponse{}, failure("SOURCE_NOT_FOUND", "Source memo is unavailable", 404, false)
	}
	keywords, err := s.Store.relatedLexicalCandidates(ctx, *seed, options, false)
	if err != nil {
		return RelatedResponse{}, err
	}
	tags, err := s.Store.relatedLexicalCandidates(ctx, *seed, options, true)
	if err != nil {
		return RelatedResponse{}, err
	}
	semantic, status, err := s.relatedSemanticCandidates(ctx, *seed, options)
	if err != nil {
		return RelatedResponse{}, err
	}
	results, err := s.loadSearchHits(ctx, options, fuseSearch(keywords, tags, semantic), seed.Name, true)
	if err != nil {
		return RelatedResponse{}, err
	}
	fresh, err := s.liveSearchMemo(ctx, seed.Name)
	if err != nil {
		return RelatedResponse{}, err
	}
	if err := s.verify(ctx); err != nil {
		return RelatedResponse{}, err
	}
	if fresh == nil {
		return RelatedResponse{}, failure("SOURCE_NOT_FOUND", "Source memo is unavailable", 404, false)
	}
	if fresh.SourceHash != seed.SourceHash {
		return RelatedResponse{}, failure("SOURCE_CHANGED", "Source memo changed during related retrieval; retry the request", 409, true)
	}
	return RelatedResponse{Source: fresh.Note(s.Store.Settings), Results: results, Semantic: status}, nil
}

func (a *API) related(request *http.Request) (any, int, error) {
	invalid := failure("INVALID_ARGUMENT", "Invalid related note parameters", 422, false)
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return nil, 0, invalid
	}
	for key, values := range query {
		if !slices.Contains([]string{"limit", "state", "tags"}, key) || key != "tags" && len(values) != 1 {
			return nil, 0, invalid
		}
	}
	body := RelatedRequest{Name: "memos/" + strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/api/ext/v1/notes/"), "/related"),
		Tags: query["tags"], State: query.Get("state"), Limit: 10}
	if query.Has("limit") {
		body.Limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || body.Limit < 1 || body.Limit > 20 {
			return nil, 0, invalid
		}
	}
	if query.Has("state") && body.State == "" {
		return nil, 0, invalid
	}
	result, err := a.Search.Related(request.Context(), body)
	return result, 200, err
}

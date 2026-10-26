package moerise

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

func (r *SearchRequest) UnmarshalJSON(data []byte) error {
	type wire SearchRequest
	body := wire{Limit: 10}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return err
	}
	if body.Limit < 1 || body.Limit > 20 {
		return failure("INVALID_ARGUMENT", "Search limit must be between 1 and 20", 422, false)
	}
	*r = SearchRequest(body)
	return nil
}

type searchVector struct {
	candidate searchCandidate
	state     string
	tags      []string
	vector    []float32
	norm      float64
}

type searchMatrix struct {
	id      string
	entries []searchVector
}

type SearchService struct {
	Store    *Store
	Source   Source
	Provider EmbeddingProvider
	mu       sync.Mutex
	matrix   *searchMatrix
}

func NewSearchService(store *Store, source Source, provider EmbeddingProvider) *SearchService {
	return &SearchService{Store: store, Source: source, Provider: provider}
}

func normalizeSearch(request SearchRequest) (SearchRequest, error) {
	bad := failure("INVALID_ARGUMENT", "Invalid search parameters", 422, false)
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" || !utf8.ValidString(request.Query) || len(request.Query) > 1024 ||
		utf8.RuneCountInString(request.Query) > 256 || len(request.Tags) > 20 {
		return request, bad
	}
	if request.Limit == 0 {
		request.Limit = 10
	}
	if request.State == "" {
		request.State = "NORMAL"
	}
	if request.Limit < 1 || request.Limit > 20 ||
		!slices.Contains([]string{"NORMAL", "ARCHIVED", "ALL"}, request.State) {
		return request, bad
	}
	request.Tags = slices.Clone(request.Tags)
	for _, tag := range request.Tags {
		if !utf8.ValidString(tag) || utf8.RuneCountInString(tag) > 100 || !tagPattern.MatchString(tag) {
			return request, bad
		}
	}
	slices.Sort(request.Tags)
	request.Tags = slices.Compact(request.Tags)
	return request, nil
}

func vectorNorm(vector []float32) float64 {
	norm := float64(0)
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return 0
		}
		norm += float64(value) * float64(value)
	}
	return math.Sqrt(norm)
}

// Cache one published matrix. Configuration and projection checks also run on cache hits.
func (s *SearchService) vectors(ctx context.Context) (*searchMatrix, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var matrix *searchMatrix
	err := transaction(s.Store.index, func(tx *sql.Tx) error {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT g.id FROM vector_generations g JOIN projection_meta p USING (instance_id)
			WHERE g.instance_id = ? AND g.user_name = ? AND g.status = 'active'
			AND g.space_hash = ? AND g.projection_revision = p.revision`,
			s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User,
			embeddingSpace(s.Store.Settings.Embedding)).Scan(&id)
		if err != nil {
			return err
		}
		if s.matrix != nil && s.matrix.id == id {
			matrix = s.matrix
			return nil
		}
		matrix = &searchMatrix{id: id}
		rows, err := tx.QueryContext(ctx, `SELECT c.name, n.source_hash, c.ordinal,
			c.byte_start, c.byte_end, c.line_start, c.line_end, c.content, c.vector, m.payload_json
			FROM vector_chunks c JOIN vector_notes n USING (generation_id, name)
			JOIN vector_generations g ON g.id = c.generation_id
			JOIN memos m ON m.instance_id = g.instance_id AND m.user_name = g.user_name AND m.name = c.name
			WHERE c.generation_id = ? ORDER BY c.name, c.ordinal`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entry searchVector
			var data, payload []byte
			item := &entry.candidate
			if err := rows.Scan(&item.name, &item.sourceHash, &item.chunk.Ordinal, &item.chunk.ByteStart,
				&item.chunk.ByteEnd, &item.chunk.LineStart, &item.chunk.LineEnd, &item.chunk.Content, &data, &payload); err != nil {
				return err
			}
			memo, err := decodeMemo(payload)
			if err != nil {
				return err
			}
			if memo.Creator != s.Store.Settings.Memos.User {
				continue
			}
			if len(data) != 4*s.Store.Settings.Embedding.Dimensions {
				return failure("EMBEDDING_PROTOCOL", "Invalid stored search vector", 503, false)
			}
			entry.vector = make([]float32, len(data)/4)
			for i := range entry.vector {
				entry.vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
			}
			entry.norm = vectorNorm(entry.vector)
			if entry.norm == 0 {
				return failure("EMBEDDING_PROTOCOL", "Invalid stored search vector", 503, false)
			}
			entry.state, entry.tags = memo.State, memo.Tags
			matrix.entries = append(matrix.entries, entry)
		}
		return rows.Err()
	})
	if err != nil {
		s.matrix = nil
		return nil, err
	}
	s.matrix = matrix
	return matrix, nil
}

func searchAccepts(request SearchRequest, state string, tags []string) bool {
	if request.State != "ALL" && request.State != state {
		return false
	}
	for _, tag := range request.Tags {
		if !slices.Contains(tags, tag) {
			return false
		}
	}
	return true
}

func (s *SearchService) semanticCandidates(ctx context.Context, request SearchRequest) ([]searchCandidate, SearchSemantic, error) {
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
		if ctx.Err() != nil {
			return nil, SearchSemantic{}, ctx.Err()
		}
		return nil, SearchSemantic{}, err
	}
	if s.Provider == nil {
		return unused("EMBEDDING_UNAVAILABLE")
	}
	result, err := s.Provider.Embed(ctx, []string{request.Query}, EmbeddingQuery)
	if ctx.Err() != nil {
		return nil, SearchSemantic{}, ctx.Err()
	}
	if err != nil {
		return unused(errorCode(err))
	}
	if len(result.Vectors) != 1 || len(result.Vectors[0]) != s.Store.Settings.Embedding.Dimensions ||
		vectorNorm(result.Vectors[0]) == 0 {
		return unused("EMBEDDING_PROTOCOL")
	}
	// The projection may change while the query model is running.
	matrix, err = s.vectors(ctx)
	if err == sql.ErrNoRows {
		return unused("INDEX_NOT_READY")
	}
	if err != nil {
		return nil, SearchSemantic{}, err
	}
	query, norm := result.Vectors[0], vectorNorm(result.Vectors[0])
	candidates := []searchCandidate{}
	for i, entry := range matrix.entries {
		if i%256 == 0 && ctx.Err() != nil {
			return nil, SearchSemantic{}, ctx.Err()
		}
		if !searchAccepts(request, entry.state, entry.tags) {
			continue
		}
		dot := float64(0)
		for j, value := range entry.vector {
			dot += float64(value) * float64(query[j])
		}
		similarity := dot / (norm * entry.norm)
		if similarity > 0 {
			item := entry.candidate
			item.score = similarity
			candidates = append(candidates, item)
		}
	}
	sortSearchCandidates(candidates)
	return candidates[:min(len(candidates), searchCandidates)], SearchSemantic{Used: true}, nil
}

func sortSearchCandidates(items []searchCandidate) {
	slices.SortFunc(items, func(a, b searchCandidate) int {
		if a.score != b.score {
			if a.score > b.score {
				return -1
			}
			return 1
		}
		if order := strings.Compare(a.name, b.name); order != 0 {
			return order
		}
		return a.chunk.Ordinal - b.chunk.Ordinal
	})
}

func fuseSearch(lists ...[]searchCandidate) []searchCandidate {
	type key struct {
		name    string
		ordinal int
	}
	merged := map[key]*searchCandidate{}
	for channel, list := range lists {
		for rank, item := range list {
			id := key{item.name, item.chunk.Ordinal}
			if merged[id] == nil {
				copy := item
				copy.score, copy.signals = 0, []string{}
				merged[id] = &copy
			}
			merged[id].score += 1 / float64(60+rank+1)
			merged[id].signals = append(merged[id].signals, []string{"keyword", "tag", "semantic"}[channel])
		}
	}
	result := make([]searchCandidate, 0, len(merged))
	for _, item := range merged {
		result = append(result, *item)
	}
	sortSearchCandidates(result)
	return result
}

func (s *SearchService) verify(ctx context.Context) error {
	version, err := s.Source.Identity(ctx)
	if err != nil {
		if health := s.Store.SourceHealth(false, errorCode(err), ""); health != nil {
			return health
		}
		return err
	}
	return s.Store.SourceHealth(true, "", version)
}

func (s *SearchService) Search(ctx context.Context, request SearchRequest) (SearchResponse, error) {
	request, err := normalizeSearch(request)
	response := SearchResponse{Query: request.Query, Results: []SearchHit{}}
	if err != nil {
		return response, err
	}
	if err := s.verify(ctx); err != nil {
		return response, err
	}
	keywords, err := s.Store.lexicalCandidates(ctx, request, false)
	if err != nil {
		return response, err
	}
	tags, err := s.Store.lexicalCandidates(ctx, request, true)
	if err != nil {
		return response, err
	}
	semantic, status, err := s.semanticCandidates(ctx, request)
	if err != nil {
		return response, err
	}
	response.Semantic = status
	checked, positions := map[string]bool{}, map[string]int{}
	for _, item := range fuseSearch(keywords, tags, semantic) {
		if ctx.Err() != nil {
			return SearchResponse{}, ctx.Err()
		}
		if !checked[item.name] {
			checked[item.name] = true
			if len(response.Results) >= request.Limit {
				continue
			}
			fresh, err := s.Source.Get(ctx, item.name)
			if err != nil {
				_ = s.Store.SourceHealth(false, errorCode(err), "")
				return SearchResponse{}, err
			}
			if fresh != nil && (fresh.Creator != s.Store.Settings.Memos.User || fresh.Name != item.name) {
				fresh = nil
			}
			if err := s.Store.Refresh(item.name, fresh); err != nil {
				return SearchResponse{}, err
			}
			if fresh == nil || fresh.SourceHash != item.sourceHash ||
				!searchAccepts(request, fresh.State, fresh.Tags) {
				continue
			}
			positions[item.name] = len(response.Results)
			response.Results = append(response.Results, SearchHit{Note: fresh.Note(s.Store.Settings),
				Score: item.score, Matches: []SearchMatch{}})
		}
		position, ok := positions[item.name]
		if !ok {
			continue
		}
		hit := &response.Results[position]
		if len(hit.Matches) >= 3 || hit.Note.SourceHash != item.sourceHash {
			continue
		}
		chunk := item.chunk
		if chunk.ByteStart < 0 || chunk.ByteEnd > len(hit.Note.Content) || chunk.ByteEnd <= chunk.ByteStart ||
			hit.Note.Content[chunk.ByteStart:chunk.ByteEnd] != chunk.Content {
			continue
		}
		hit.Matches = append(hit.Matches, SearchMatch{chunk.Ordinal, chunk.ByteStart, chunk.ByteEnd,
			chunk.LineStart, chunk.LineEnd, chunk.Content, item.signals})
	}
	if err := s.verify(ctx); err != nil {
		return SearchResponse{}, err
	}
	return response, nil
}

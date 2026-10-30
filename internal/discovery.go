package moerise

import (
	"context"
	"database/sql"
	"math/rand/v2"
	"slices"
	"time"
)

func (s *SearchService) recentReads() (map[string]bool, error) {
	settings := s.Store.Settings
	return namesQuery(s.Store.state, `SELECT memo_name FROM discovery_reads WHERE instance_id = ? AND user_name = ?
		AND julianday(opened_at) >= julianday('now', '-7 days') UNION SELECT i.memo_name FROM review_items i
		JOIN review_batches b ON b.id = i.batch_id WHERE b.instance_id = ? AND b.user_name = ?
		AND julianday(i.opened_at) >= julianday('now', '-7 days')`,
		settings.Memos.InstanceID, settings.Memos.User, settings.Memos.InstanceID, settings.Memos.User)
}

func (s *SearchService) randomWalkCandidates(ctx context.Context, request WalkRequest, path []string) ([]searchCandidate, error) {
	visited := make(map[string]bool, len(path))
	for _, name := range path {
		visited[name] = true
	}
	rows, err := s.Store.index.QueryContext(ctx, `SELECT name, source_hash FROM memos
		WHERE instance_id = ? AND user_name = ? AND (? OR state = 'NORMAL') ORDER BY name`,
		s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, request.IncludeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []searchCandidate{}
	for rows.Next() {
		var candidate searchCandidate
		if err := rows.Scan(&candidate.name, &candidate.sourceHash); err != nil {
			return nil, err
		}
		if visited[candidate.name] {
			continue
		}
		candidate.score = 0.5
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func normalizeWalk(request WalkRequest) (WalkRequest, error) {
	if request.Mode == "" {
		request.Mode = "continue"
	}
	if !slices.Contains([]string{"continue", "angle", "random"}, request.Mode) ||
		(request.Name == "" && request.Mode != "random") || (request.Name != "" && !memoPattern.MatchString(request.Name)) {
		return request, failure("INVALID_ARGUMENT", "Invalid discovery walk parameters", 422, false)
	}
	path := make([]string, 0, len(request.Path))
	seen := make(map[string]bool, len(request.Path))
	for _, name := range request.Path {
		if !memoPattern.MatchString(name) {
			return request, failure("INVALID_ARGUMENT", "Invalid discovery path memo name", 422, false)
		}
		if !seen[name] {
			path = append(path, name)
			seen[name] = true
		}
	}
	request.Path = path
	return request, nil
}

func (s *SearchService) Walk(ctx context.Context, request WalkRequest) (WalkResponse, error) {
	return s.walk(ctx, request, rand.IntN)
}

func (s *SearchService) walkIndexReady(ctx context.Context) error {
	if !s.Store.Settings.Embedding.Enabled {
		return failure("EMBEDDING_DISABLED", "Discovery requires an enabled document vector index", 503, false)
	}
	_, err := s.vectors(ctx)
	if err == sql.ErrNoRows {
		return failure("INDEX_NOT_READY", "Discovery requires a ready document vector index", 503, true)
	}
	return err
}

func (s *SearchService) StartWalk(ctx context.Context, name string) (result WalkResponse, err error) {
	if name == "" {
		return s.Walk(ctx, WalkRequest{Mode: "random"})
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			result, err = WalkResponse{}, ctx.Err()
		}
	}()
	if !memoPattern.MatchString(name) {
		return result, failure("INVALID_ARGUMENT", "Invalid discovery starting memo name", 422, false)
	}
	if err := s.verify(ctx); err != nil {
		return result, err
	}
	seed, err := s.liveSearchMemo(ctx, name)
	if err != nil {
		return result, err
	}
	if seed == nil {
		return result, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
	}
	if err := s.walkIndexReady(ctx); err != nil {
		return result, err
	}
	fresh, err := s.liveSearchMemo(ctx, name)
	if err != nil {
		return result, err
	}
	if fresh == nil {
		return result, failure("SOURCE_NOT_FOUND", "Source memo is unavailable", 404, false)
	}
	if fresh.SourceHash != seed.SourceHash {
		return result, failure("SOURCE_CHANGED", "Source memo changed during discovery; retry the request", 409, true)
	}
	if err := s.verify(ctx); err != nil {
		return result, err
	}
	return WalkResponse{Mode: "continue", Item: &SearchHit{Note: fresh.Note(s.Store.Settings), Matches: []SearchMatch{}},
		Path: []string{name}, Warnings: []string{}}, nil
}

// Inject the draw per call so tests can check every eligible note without shared random state.
func (s *SearchService) walk(ctx context.Context, request WalkRequest, intN func(int) int) (result WalkResponse, err error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			result, err = WalkResponse{}, ctx.Err()
		}
	}()
	request, err = normalizeWalk(request)
	result = WalkResponse{Mode: request.Mode, Path: request.Path, Warnings: []string{}}
	if err != nil {
		return result, err
	}
	if request.Name != "" && !slices.Contains(result.Path, request.Name) {
		result.Path = append(result.Path, request.Name)
	}
	if err := s.verify(ctx); err != nil {
		return result, err
	}
	var seed *Memo
	if request.Name != "" {
		seed, err = s.liveSearchMemo(ctx, request.Name)
		if err != nil {
			return result, err
		}
		if seed == nil {
			return result, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
		}
	}
	candidates := []searchCandidate{}
	if request.Mode != "random" {
		options := SearchRequest{State: "NORMAL"}
		if request.IncludeArchived {
			options.State = "ALL"
		}
		var semantic SearchSemantic
		candidates, semantic, err = s.allRelatedSemanticCandidates(ctx, *seed, options)
		if err != nil {
			return result, err
		}
		if !semantic.Used {
			return result, failure(semantic.Reason, "Semantic discovery requires an enabled, ready document vector index", 503,
				semantic.Reason != "EMBEDDING_DISABLED")
		}
	} else {
		if err := s.walkIndexReady(ctx); err != nil {
			return result, err
		}
		candidates, err = s.randomWalkCandidates(ctx, request, result.Path)
		if err != nil {
			return result, err
		}
	}
	visited := make(map[string]bool, len(result.Path))
	for _, name := range result.Path {
		visited[name] = true
	}
	// A note occupies one slot even when many chunks have positive cosine similarity.
	slots := map[string]int{}
	eligible := make([]searchCandidate, 0)
	for i, candidate := range candidates {
		if i%256 == 0 && ctx.Err() != nil {
			return WalkResponse{}, ctx.Err()
		}
		if visited[candidate.name] {
			continue
		}
		if slot, ok := slots[candidate.name]; ok {
			if candidate.score > eligible[slot].score {
				eligible[slot] = candidate
			}
			continue
		}
		slots[candidate.name] = len(eligible)
		eligible = append(eligible, candidate)
	}
	// A lazy random permutation is uniform over the notes that pass live source checks.
	for len(eligible) > 0 {
		if err := ctx.Err(); err != nil {
			return WalkResponse{}, err
		}
		slot := intN(len(eligible))
		candidate := eligible[slot]
		eligible[slot] = eligible[len(eligible)-1]
		eligible = eligible[:len(eligible)-1]
		memo, err := s.liveSearchMemo(ctx, candidate.name)
		if err != nil {
			return WalkResponse{}, err
		}
		if memo == nil || (!request.IncludeArchived && memo.State != "NORMAL") ||
			memo.SourceHash != candidate.sourceHash {
			continue
		}
		candidate.signals = []string{"random"}
		if request.Mode == "random" {
			if chunks := ChunkText(memo.Content); len(chunks) > 0 {
				candidate.chunk = chunks[0]
			}
		} else {
			candidate.signals = []string{"semantic"}
		}
		hit := SearchHit{Note: memo.Note(s.Store.Settings), Score: candidate.score, Matches: []SearchMatch{}}
		chunk := candidate.chunk
		if chunk.ByteStart >= 0 && chunk.ByteEnd > chunk.ByteStart && chunk.ByteEnd <= len(memo.Content) &&
			memo.Content[chunk.ByteStart:chunk.ByteEnd] == chunk.Content {
			hit.Matches = append(hit.Matches, SearchMatch{chunk.Ordinal, chunk.ByteStart, chunk.ByteEnd,
				chunk.LineStart, chunk.LineEnd, chunk.Content, candidate.signals})
		}
		result.Item = &hit
		break
	}
	if seed != nil {
		fresh, err := s.liveSearchMemo(ctx, seed.Name)
		if err != nil {
			return WalkResponse{}, err
		}
		if fresh == nil {
			return WalkResponse{}, failure("SOURCE_NOT_FOUND", "Source memo is unavailable", 404, false)
		}
		if fresh.SourceHash != seed.SourceHash {
			return WalkResponse{}, failure("SOURCE_CHANGED", "Source memo changed during discovery; retry the request", 409, true)
		}
	}
	if err := s.verify(ctx); err != nil {
		return WalkResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return WalkResponse{}, err
	}
	if result.Item != nil {
		result.Path = append(result.Path, result.Item.Note.Name)
	} else {
		result.Exhausted = true
	}
	return result, nil
}

func (s *SearchService) Open(ctx context.Context, name string) (Note, error) {
	if !memoPattern.MatchString(name) {
		return Note{}, failure("INVALID_ARGUMENT", "Invalid source memo name", 422, false)
	}
	if err := s.verify(ctx); err != nil {
		return Note{}, err
	}
	memo, err := s.liveSearchMemo(ctx, name)
	if err != nil {
		return Note{}, err
	}
	if memo == nil {
		return Note{}, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
	}
	if err := s.verify(ctx); err != nil {
		return Note{}, err
	}
	_, err = s.Store.state.ExecContext(ctx, `INSERT INTO discovery_reads (instance_id, user_name, memo_name, opened_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(instance_id, user_name, memo_name) DO UPDATE SET opened_at = excluded.opened_at`,
		s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, name, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Note{}, err
	}
	return memo.Note(s.Store.Settings), nil
}

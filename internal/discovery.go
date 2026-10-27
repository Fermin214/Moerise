package moerise

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

func sharesTopic(a, b Memo) bool {
	for _, left := range a.Tags {
		for _, right := range b.Tags {
			topic := strings.SplitN(left, "/", 2)[0]
			if topic != "" && topic == strings.SplitN(right, "/", 2)[0] {
				return true
			}
		}
	}
	return false
}

func (s *SearchService) recentReads() (map[string]bool, error) {
	settings := s.Store.Settings
	return namesQuery(s.Store.state, `SELECT memo_name FROM discovery_reads WHERE instance_id = ? AND user_name = ?
		AND julianday(opened_at) >= julianday('now', '-7 days') UNION SELECT i.memo_name FROM review_items i
		JOIN review_batches b ON b.id = i.batch_id WHERE b.instance_id = ? AND b.user_name = ?
		AND julianday(i.opened_at) >= julianday('now', '-7 days')`,
		settings.Memos.InstanceID, settings.Memos.User, settings.Memos.InstanceID, settings.Memos.User)
}

func (s *SearchService) randomWalkCandidates(ctx context.Context, request WalkRequest, path []string) ([]searchCandidate, error) {
	data, err := json.Marshal(path)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.index.QueryContext(ctx, `SELECT name, source_hash FROM memos
		WHERE instance_id = ? AND user_name = ? AND (? OR state = 'NORMAL')
		AND NOT EXISTS (SELECT 1 FROM json_each(?) WHERE value = memos.name) ORDER BY random() LIMIT ?`,
		s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, request.IncludeArchived, string(data), searchCandidates)
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
		candidate.score = 0.5
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func normalizeWalk(request WalkRequest) (WalkRequest, error) {
	if request.Mode == "" {
		request.Mode = "continue"
	}
	if !slices.Contains([]string{"continue", "angle", "random"}, request.Mode) || len(request.Path) > 100 ||
		(request.Name == "" && request.Mode != "random") || (request.Name != "" && !memoPattern.MatchString(request.Name)) {
		return request, failure("INVALID_ARGUMENT", "Invalid discovery walk parameters", 422, false)
	}
	path := make([]string, 0, len(request.Path))
	for _, name := range request.Path {
		if !memoPattern.MatchString(name) {
			return request, failure("INVALID_ARGUMENT", "Invalid discovery path memo name", 422, false)
		}
		if !slices.Contains(path, name) {
			path = append(path, name)
		}
	}
	request.Path = path
	return request, nil
}

func (s *SearchService) Walk(ctx context.Context, request WalkRequest) (WalkResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	request, err := normalizeWalk(request)
	result := WalkResponse{Mode: request.Mode, Path: request.Path, Warnings: []string{}}
	if err != nil {
		return result, err
	}
	if request.Name != "" && len(result.Path) < 100 && !slices.Contains(result.Path, request.Name) {
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
	if len(result.Path) >= 100 {
		result.Exhausted = true
		return result, s.verify(ctx)
	}
	recent, err := s.recentReads()
	if err != nil {
		return result, err
	}
	candidates := []searchCandidate{}
	if request.Mode != "random" {
		options := SearchRequest{State: "NORMAL"}
		if request.IncludeArchived {
			options.State = "ALL"
		}
		var semantic SearchSemantic
		candidates, semantic, err = s.relatedSemanticCandidates(ctx, *seed, options)
		if err != nil {
			return result, err
		}
		if !semantic.Used {
			return result, failure(semantic.Reason, "Semantic discovery requires an enabled, ready document vector index", 503,
				semantic.Reason != "EMBEDDING_DISABLED")
		}
	} else {
		candidates, err = s.randomWalkCandidates(ctx, request, result.Path)
		if err != nil {
			return result, err
		}
	}
	for i := range candidates {
		candidate := &candidates[i]
		if request.Mode != "random" {
			if request.Mode == "continue" {
				candidate.score += 0.2
			} else {
				candidate.score += 0.15
			}
		}
		if recent[candidate.name] {
			candidate.score -= 0.4
		}
	}
	// These upper bounds include topic bonuses; random ties retain their sampled order.
	slices.SortStableFunc(candidates, func(a, b searchCandidate) int {
		if a.score > b.score {
			return -1
		}
		if a.score < b.score {
			return 1
		}
		return 0
	})
	checked := map[string]bool{}
	for _, candidate := range candidates {
		if len(result.Path) >= 100 {
			break
		}
		if slices.Contains(result.Path, candidate.name) || checked[candidate.name] {
			continue
		}
		if result.Item != nil && candidate.score <= result.Item.Score {
			break
		}
		checked[candidate.name] = true
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
			sameTopic := sharesTopic(*seed, *memo)
			if sameTopic {
				candidate.signals = append(candidate.signals, "tag")
			}
			if request.Mode == "continue" && !sameTopic {
				candidate.score -= 0.2
			} else if request.Mode == "angle" && sameTopic {
				candidate.score -= 0.45
			}
		}
		if recent[memo.Name] {
			candidate.signals = append(candidate.signals, "recent_read")
		}
		hit := SearchHit{Note: memo.Note(s.Store.Settings), Score: candidate.score, Matches: []SearchMatch{}}
		chunk := candidate.chunk
		if chunk.ByteStart >= 0 && chunk.ByteEnd > chunk.ByteStart && chunk.ByteEnd <= len(memo.Content) &&
			memo.Content[chunk.ByteStart:chunk.ByteEnd] == chunk.Content {
			hit.Matches = append(hit.Matches, SearchMatch{chunk.Ordinal, chunk.ByteStart, chunk.ByteEnd,
				chunk.LineStart, chunk.LineEnd, chunk.Content, candidate.signals})
		}
		if result.Item == nil || hit.Score > result.Item.Score {
			result.Item = &hit
		}
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
	if result.Item != nil {
		result.Path = append(result.Path, result.Item.Note.Name)
	} else {
		result.Exhausted = true
	}
	if err := s.verify(ctx); err != nil {
		return WalkResponse{}, err
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

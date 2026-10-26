package moerise

import (
	"context"
	"slices"
	"strings"
	"time"
)

func (s *SearchService) Related(ctx context.Context, request RelatedRequest) (SearchResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result := SearchResponse{Mode: "semantic", Results: []SearchHit{}, Warnings: []string{}}
	if request.Limit == 0 {
		request.Limit = 10
	}
	if !memoPattern.MatchString(request.Name) || request.Limit < 1 || request.Limit > 20 {
		return result, searchArgumentError()
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	if err := s.verify(ctx); err != nil {
		return result, err
	}
	seed, err := s.live(ctx, request.Name)
	if err != nil {
		return result, err
	}
	if seed == nil {
		return result, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
	}
	candidates, err := s.relatedCandidates(ctx, *seed, request.IncludeArchived)
	if err != nil {
		return result, err
	}
	for _, candidate := range candidates {
		if candidate.Name == seed.Name {
			continue
		}
		memo, err := s.live(ctx, candidate.Name)
		if err != nil {
			return SearchResponse{}, err
		}
		if memo == nil || (!request.IncludeArchived && memo.State != "NORMAL") || memo.ContentHash != candidate.ContentHash {
			continue
		}
		candidate.Score = *candidate.Similarity
		if sharesTopic(*seed, *memo) {
			candidate.Score += 0.05
			candidate.Signals = append(candidate.Signals, "tag")
		}
		result.Results = append(result.Results, s.hit(*memo, candidate, ""))
		if len(result.Results) >= min(searchCandidateLimit, request.Limit*3) {
			break
		}
	}
	slices.SortStableFunc(result.Results, func(a, b SearchHit) int {
		if a.Score > b.Score {
			return -1
		}
		if a.Score < b.Score {
			return 1
		}
		return strings.Compare(a.Note.Name, b.Note.Name)
	})
	result.Results = result.Results[:min(request.Limit, len(result.Results))]
	if err := s.verify(ctx); err != nil {
		return SearchResponse{}, err
	}
	return result, nil
}

func sharesTopic(a, b Memo) bool {
	for _, left := range a.Tags {
		for _, right := range b.Tags {
			if strings.SplitN(left, "/", 2)[0] == strings.SplitN(right, "/", 2)[0] {
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

func (s *SearchService) Walk(ctx context.Context, request WalkRequest) (WalkResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result := WalkResponse{Path: []string{}, Warnings: []string{}}
	if request.Mode == "" {
		request.Mode = "continue"
	}
	if !slices.Contains([]string{"continue", "angle", "random"}, request.Mode) || len(request.Path) > 100 ||
		(request.Name == "" && request.Mode != "random") || (request.Name != "" && !memoPattern.MatchString(request.Name)) {
		return result, searchArgumentError()
	}
	for _, name := range request.Path {
		if !memoPattern.MatchString(name) {
			return result, searchArgumentError()
		}
		if !slices.Contains(result.Path, name) {
			result.Path = append(result.Path, name)
		}
	}
	if request.Name != "" && !slices.Contains(result.Path, request.Name) {
		result.Path = append(result.Path, request.Name)
	}
	result.Mode = request.Mode
	release, err := s.acquire(ctx)
	if err != nil {
		return result, err
	}
	defer release()
	if err := s.verify(ctx); err != nil {
		return result, err
	}
	var seed *Memo
	if request.Name != "" {
		seed, err = s.live(ctx, request.Name)
		if err != nil {
			return result, err
		}
		if seed == nil {
			return result, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
		}
	}
	if len(result.Path) >= 100 {
		result.Path, result.Exhausted = result.Path[:100], true
		return result, s.verify(ctx)
	}
	recent, err := s.recentReads()
	if err != nil {
		return result, err
	}
	candidates := []searchCandidate{}
	if request.Mode != "random" {
		candidates, err = s.relatedCandidates(ctx, *seed, request.IncludeArchived)
		if err != nil {
			return result, err
		}
	} else {
		rows, err := s.Store.index.QueryContext(ctx, `SELECT name, content_hash FROM memos WHERE instance_id = ?
			AND user_name = ? AND (? OR state = 'NORMAL') ORDER BY random() LIMIT ?`,
			s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, request.IncludeArchived, searchCandidateLimit)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var candidate searchCandidate
			if err := rows.Scan(&candidate.Name, &candidate.ContentHash); err != nil {
				rows.Close()
				return result, err
			}
			candidate.Signals, candidate.Score = []string{"random"}, 0.5
			candidates = append(candidates, candidate)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
	}
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.Similarity != nil {
			candidate.Score = *candidate.Similarity
			if request.Mode == "continue" {
				candidate.Score += 0.2
			} else {
				candidate.Score += 0.15
			}
		}
		if recent[candidate.Name] {
			candidate.Score -= 0.4
		}
	}
	// Bound each candidate's possible score before making source requests. Random
	// ties keep their sampled order; stop once no remaining candidate can win.
	slices.SortStableFunc(candidates, func(a, b searchCandidate) int {
		if a.Score > b.Score {
			return -1
		}
		if a.Score < b.Score {
			return 1
		}
		return 0
	})
	for _, candidate := range candidates {
		if slices.Contains(result.Path, candidate.Name) {
			continue
		}
		if result.Item != nil && candidate.Score <= result.Item.Score {
			break
		}
		memo, err := s.live(ctx, candidate.Name)
		if err != nil {
			return WalkResponse{}, err
		}
		if memo == nil || (!request.IncludeArchived && memo.State != "NORMAL") ||
			(candidate.Similarity != nil && memo.ContentHash != candidate.ContentHash) {
			continue
		}
		candidate.Score = 0.5
		if candidate.Similarity != nil {
			candidate.Score = *candidate.Similarity
			if seed != nil && sharesTopic(*seed, *memo) {
				if request.Mode == "continue" {
					candidate.Score += 0.2
				} else {
					candidate.Score -= 0.3
				}
				candidate.Signals = append(candidate.Signals, "tag")
			} else if request.Mode == "angle" {
				candidate.Score += 0.15
			}
		}
		if recent[memo.Name] {
			candidate.Score -= 0.4
			candidate.Signals = append(candidate.Signals, "recent_read")
		}
		hit := s.hit(*memo, candidate, "")
		if result.Item == nil || hit.Score > result.Item.Score {
			result.Item = &hit
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
		return Note{}, searchArgumentError()
	}
	if err := s.verify(ctx); err != nil {
		return Note{}, err
	}
	memo, err := s.live(ctx, name)
	if err != nil {
		return Note{}, err
	}
	if memo == nil {
		return Note{}, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
	}
	if err := s.verify(ctx); err != nil {
		return Note{}, err
	}
	_, err = s.Store.state.Exec(`INSERT INTO discovery_reads VALUES (?, ?, ?, ?)
		ON CONFLICT(instance_id, user_name, memo_name) DO UPDATE SET opened_at = excluded.opened_at`,
		s.Store.Settings.Memos.InstanceID, s.Store.Settings.Memos.User, name, time.Now().UTC().Format(time.RFC3339Nano))
	return memo.Note(s.Store.Settings), err
}

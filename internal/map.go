package moerise

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const mapRequestTimeout = 55 * time.Second

type MapService struct {
	Store *Store
	Notes *NoteService
	gate  chan struct{}
}

func NewMapService(store *Store, notes *NoteService) *MapService {
	return &MapService{Store: store, Notes: notes, gate: make(chan struct{}, 1)}
}

func mapRequestError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return failure("MAP_RESOURCE_LIMIT", "Map request exceeded its time budget; retry later", 503, true)
	}
	return err
}

func (s *MapService) Read(ctx context.Context) (_ MapResponse, result error) {
	ctx, cancel := context.WithTimeout(ctx, mapRequestTimeout)
	defer cancel()
	defer func() { result = mapRequestError(ctx, result) }()
	response := MapResponse{Status: "disabled", Algorithm: mapAlgorithm, Points: []MapPoint{}}
	if err := ctx.Err(); err != nil {
		return MapResponse{}, err
	}
	if !s.Store.Settings.Embedding.Enabled {
		return response, nil
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return MapResponse{}, ctx.Err()
	}
	if err := s.Notes.Verify(ctx); err != nil {
		return MapResponse{}, err
	}
	status, err := s.Store.VectorStatusContext(ctx)
	if err != nil {
		return MapResponse{}, err
	}
	response.ProjectionRevision = status.ProjectionRevision
	if !status.Ready {
		response.Status = "waiting"
		if building := status.Building; building != nil && building.SpaceHash == embeddingSpace(s.Store.Settings.Embedding) {
			response.Status = "building"
			if building.LastError != nil {
				response.Status = "unavailable"
			}
		}
		return response, ctx.Err()
	}
	snapshot, err := s.Store.mapSnapshot(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return MapResponse{}, mapIndexChanged()
	}
	if err != nil {
		return MapResponse{}, err
	}
	previous, err := s.Store.loadMapLayout(ctx, snapshot)
	if err != nil {
		return MapResponse{}, err
	}
	layout, err := buildMapLayout(ctx, snapshot, previous)
	if err != nil {
		return MapResponse{}, err
	}
	if err := s.verifyPoints(ctx, layout.points); err != nil {
		return MapResponse{}, err
	}
	if err := s.Notes.Verify(ctx); err != nil {
		return MapResponse{}, err
	}
	if err := s.Store.publishMapLayout(ctx, snapshot, layout); err != nil {
		return MapResponse{}, err
	}
	response.Status, response.LayoutVersion = "ready", layout.version
	response.LayoutFrame = hashBytes(canonicalJSON([]any{s.Store.Settings.Memos.InstanceID,
		s.Store.Settings.Memos.User, mapAlgorithm, snapshot.space, layout.basis}))
	if len(layout.points) == 0 {
		response.Status = "empty"
	}
	response.VectorGeneration, response.ProjectionRevision = snapshot.generation, snapshot.revision
	response.Points = layout.points
	return response, ctx.Err()
}

// Full official pagination verifies originals without spending one upstream request per map point.
func (s *MapService) verifyPoints(ctx context.Context, points []MapPoint) error {
	positions, seen := map[string]int{}, map[string]bool{}
	for i, point := range points {
		positions[point.Name] = i
	}
	var changed *Memo
	for _, state := range []string{"NORMAL", "ARCHIVED"} {
		token, visited := "", map[string]bool{}
		for page := 0; ; page++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if page >= s.Store.Settings.Memos.MaxPages {
				return upstreamError("PAGINATION_LIMIT", "Memos pagination exceeded the configured limit")
			}
			memos, next, err := s.Notes.Source.ListPage(ctx, state, nil, token)
			if err != nil {
				if cause := context.Cause(ctx); cause != nil {
					return cause
				}
				if healthErr := s.Store.SourceHealthContext(ctx, false, errorCode(err), ""); healthErr != nil {
					return healthErr
				}
				return err
			}
			for _, memo := range memos {
				if memo.Creator != s.Store.Settings.Memos.User {
					continue
				}
				if seen[memo.Name] {
					return upstreamError("PAGINATION_LOOP", "Memos repeated a source resource")
				}
				seen[memo.Name] = true
				position, exists := positions[memo.Name]
				if !exists || points[position].SourceHash != memo.SourceHash {
					if changed == nil {
						changed = &memo
					}
				} else {
					points[position].Tags = append([]string{}, memo.Tags...)
				}
			}
			if next == "" {
				break
			}
			if visited[next] {
				return upstreamError("PAGINATION_LOOP", "Memos repeated a pagination token")
			}
			visited[next], token = true, next
		}
	}
	name := ""
	if changed != nil {
		name = changed.Name
	} else {
		for _, point := range points {
			if !seen[point.Name] {
				name = point.Name
				break
			}
		}
	}
	if name != "" {
		if err := s.Store.RefreshContext(ctx, name, changed); err != nil {
			return err
		}
		return failure("MAP_SOURCE_CHANGED", "Map sources changed; retry after indexing completes", 409, true)
	}
	return ctx.Err()
}

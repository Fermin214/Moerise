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
	// Coordinates carry source hashes, not authoritative text. Validate every referenced note before publication.
	for i, point := range layout.points {
		if err := ctx.Err(); err != nil {
			return MapResponse{}, err
		}
		memo, err := s.Notes.Source.Get(ctx, point.Name)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return MapResponse{}, cause
			}
			if healthErr := s.Store.SourceHealthContext(ctx, false, errorCode(err), ""); healthErr != nil {
				return MapResponse{}, healthErr
			}
			return MapResponse{}, err
		}
		if memo != nil && memo.Creator != s.Store.Settings.Memos.User {
			memo = nil
		}
		if memo == nil || memo.SourceHash != point.SourceHash {
			if err := s.Store.RefreshContext(ctx, point.Name, memo); err != nil {
				return MapResponse{}, err
			}
			return MapResponse{}, failure("MAP_SOURCE_CHANGED", "A map note changed; retry after indexing completes", 409, true)
		}
		layout.points[i].Tags = append([]string{}, memo.Tags...)
	}
	if err := s.Notes.Verify(ctx); err != nil {
		return MapResponse{}, err
	}
	if err := s.Store.publishMapLayout(ctx, snapshot, layout); err != nil {
		return MapResponse{}, err
	}
	response.Status, response.LayoutVersion = "ready", layout.version
	if len(layout.points) == 0 {
		response.Status = "empty"
	}
	response.VectorGeneration, response.ProjectionRevision = snapshot.generation, snapshot.revision
	response.Points = layout.points
	return response, ctx.Err()
}

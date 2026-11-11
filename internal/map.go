package moerise

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"time"
)

const mapRequestTimeout = 55 * time.Second
const mapSnapshotInterval = 24 * time.Hour

type mapCache struct {
	key      string
	response MapResponse
}

type mapSchedule struct {
	key  string
	next time.Time
}

type MapService struct {
	Store    *Store
	gate     chan struct{}
	cache    atomic.Pointer[mapCache]
	schedule atomic.Pointer[mapSchedule]
}

func NewMapService(store *Store) *MapService {
	return &MapService{Store: store, gate: make(chan struct{}, 1)}
}

func (s *MapService) Reset() {
	s.cache.Store(nil)
	s.schedule.Store(nil)
}

func (s *MapService) cacheKey() string {
	return hashBytes(canonicalJSON([]string{s.Store.Settings.Memos.InstanceID,
		s.Store.Settings.Memos.User, embeddingSpace(s.Store.Settings.Embedding)}))
}

func mapRequestError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return failure("MAP_RESOURCE_LIMIT", "Map request exceeded its time budget; retry later", 503, true)
	}
	return err
}

func (s *MapService) response(layout *mapLayout, space string) MapResponse {
	result := MapResponse{Status: "ready", Algorithm: mapAlgorithm, LayoutVersion: layout.version,
		LayoutFrame: hashBytes(canonicalJSON([]any{s.Store.Settings.Memos.InstanceID,
			s.Store.Settings.Memos.User, mapAlgorithm, space, layout.basis})),
		VectorGeneration: layout.generation, ProjectionRevision: layout.revision,
		GeneratedAt: layout.generatedAt.Format(time.RFC3339Nano), Points: layout.points}
	if len(layout.points) == 0 {
		result.Status = "empty"
	}
	return result
}

// Read serves published trend data. The client API authenticates the session;
// individual notes and attachments still use the live official source APIs.
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
	key := s.cacheKey()
	cached := s.cache.Load()
	if cached != nil && cached.key == key {
		return cached.response, ctx.Err()
	}
	space := embeddingSpace(s.Store.Settings.Embedding)
	layout, err := s.Store.loadMapLayout(ctx, mapSnapshot{space: space, dimensions: s.Store.Settings.Embedding.Dimensions})
	if err != nil {
		return MapResponse{}, err
	}
	if layout != nil && !layout.generatedAt.IsZero() {
		response = s.response(layout, space)
		// A cold read must not overwrite a snapshot published concurrently.
		s.cache.CompareAndSwap(cached, &mapCache{key: key, response: response})
		return response, ctx.Err()
	}
	status, err := s.Store.VectorStatusContext(ctx)
	if err != nil {
		return MapResponse{}, err
	}
	response.Status, response.ProjectionRevision = "waiting", status.ProjectionRevision
	if status.Ready {
		response.Status = "building"
	} else if building := status.Building; building != nil && building.SpaceHash == space {
		response.Status = "building"
		if building.LastError != nil {
			response.Status = "unavailable"
		}
	}
	return response, ctx.Err()
}

// Update runs with the vector worker while indexing is idle. Ordinary changes
// are combined into a daily snapshot; unchanged generations need no vector scan.
func (s *MapService) Update(ctx context.Context) error {
	return s.update(ctx, time.Now().UTC())
}

func (s *MapService) update(ctx context.Context, now time.Time) error {
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if !s.Store.Settings.Embedding.Enabled {
		return nil
	}
	key := s.cacheKey()
	if schedule := s.schedule.Load(); schedule != nil && schedule.key == key && now.Before(schedule.next) {
		return ctx.Err()
	}
	// Bound retries without making page requests responsible for background work.
	s.schedule.Store(&mapSchedule{key, now.Add(5 * time.Second)})
	unlock, err := s.Store.lockBackground(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	space := embeddingSpace(s.Store.Settings.Embedding)
	previous, err := s.Store.loadMapLayout(ctx, mapSnapshot{space: space, dimensions: s.Store.Settings.Embedding.Dimensions})
	if err != nil {
		return err
	}
	if previous != nil && !previous.generatedAt.IsZero() {
		s.cache.Store(&mapCache{key, s.response(previous, space)})
		if due := previous.generatedAt.Add(mapSnapshotInterval); now.Before(due) {
			s.schedule.Store(&mapSchedule{key, due})
			return ctx.Err()
		}
	}
	status, err := s.Store.VectorStatusContext(ctx)
	if err != nil || !status.Ready {
		return err
	}
	if previous != nil && !previous.generatedAt.IsZero() && previous.generation == status.Active.ID && previous.revision == status.ProjectionRevision {
		s.schedule.Store(&mapSchedule{key, now.Add(mapSnapshotInterval)})
		return ctx.Err()
	}
	snapshot, err := s.Store.mapSnapshot(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return mapIndexChanged()
	}
	if err != nil {
		return err
	}
	layout, err := buildMapLayout(ctx, snapshot, previous)
	if err != nil {
		return err
	}
	layout.generatedAt = now
	if err := s.Store.publishMapLayout(ctx, snapshot, layout); err != nil {
		return err
	}
	s.cache.Store(&mapCache{key, s.response(layout, space)})
	s.schedule.Store(&mapSchedule{key, now.Add(mapSnapshotInterval)})
	return ctx.Err()
}

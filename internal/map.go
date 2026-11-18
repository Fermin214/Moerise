package moerise

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

const mapRequestTimeout = 55 * time.Second
const mapSnapshotInterval = time.Minute

type mapCache struct {
	key      string
	response MapResponse
}

type mapSchedule struct {
	key  string
	next time.Time
}

type mapActivity struct {
	key    string
	status string
}

type MapService struct {
	Store       *Store
	gate        chan struct{}
	cache       atomic.Pointer[mapCache]
	schedule    atomic.Pointer[mapSchedule]
	lastFailure atomic.Pointer[mapSchedule]
	activity    atomic.Pointer[mapActivity]
	epoch       atomic.Uint64
	workerURL   string
	scope       string
}

func NewMapService(store *Store) *MapService {
	return &MapService{Store: store, gate: make(chan struct{}, 1),
		workerURL: layoutWorkerURL(),
		scope:     hashBytes(canonicalJSON([]string{store.Settings.Memos.InstanceID, store.Settings.Memos.User}))}
}

func (s *MapService) Reset() {
	s.epoch.Add(1)
	s.cache.Store(nil)
	s.schedule.Store(nil)
	s.lastFailure.Store(nil)
	s.activity.Store(nil)
}

func (s *MapService) readState(response MapResponse, key string) MapResponse {
	if activity := s.activity.Load(); activity != nil && activity.key == key {
		response.UpdateState = activity.status
	}
	return response
}

func (s *MapService) cacheKey() string {
	return hashBytes(canonicalJSON([]any{s.Store.Settings.Memos.InstanceID,
		s.Store.Settings.Memos.User, embeddingSpace(s.Store.Settings.Embedding), starsAlgorithm, s.epoch.Load()}))
}

func mapRequestError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return failure("MAP_RESOURCE_LIMIT", "Map request exceeded its time budget; retry later", 503, true)
	}
	return err
}

func (s *MapService) response(layout *mapLayout, space string) MapResponse {
	algorithm := layout.algorithm
	var frame string
	if algorithm == starsAlgorithm {
		frame = hashBytes(canonicalJSON([]string{s.scope, space, algorithm, layout.frame}))
	} else {
		frame = hashBytes(canonicalJSON([]any{s.Store.Settings.Memos.InstanceID,
			s.Store.Settings.Memos.User, algorithm, space, layout.basis}))
	}
	result := MapResponse{Status: "ready", Algorithm: algorithm, LayoutVersion: layout.version,
		LayoutFrame:      frame,
		VectorGeneration: layout.generation, ProjectionRevision: layout.revision,
		GeneratedAt: layout.generatedAt.Format(time.RFC3339Nano), Points: layout.points,
		Topics: layout.topics, Links: layout.links}
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
	response := MapResponse{Status: "disabled", Algorithm: starsAlgorithm, Points: []MapPoint{}, Topics: []MapTopic{}, Links: []MapLink{}}
	if err := ctx.Err(); err != nil {
		return MapResponse{}, err
	}
	if !s.Store.Settings.Stargazing.Enabled {
		response.DisabledReason = "stargazing"
		return response, nil
	}
	if !s.Store.Settings.Embedding.Enabled {
		response.DisabledReason = "embedding"
		return response, nil
	}
	key := s.cacheKey()
	cached := s.cache.Load()
	if cached != nil && cached.key == key {
		return s.readState(cached.response, key), ctx.Err()
	}
	space := embeddingSpace(s.Store.Settings.Embedding)
	layout, _, err := s.Store.loadPublishedLayout(ctx, space)
	if err != nil {
		return MapResponse{}, err
	}
	if layout != nil && !layout.generatedAt.IsZero() {
		response = s.response(layout, space)
		// A cold read must not overwrite a snapshot published concurrently.
		s.cache.CompareAndSwap(cached, &mapCache{key: key, response: response})
		return s.readState(response, key), ctx.Err()
	}
	status, err := s.Store.VectorStatusContext(ctx)
	if err != nil {
		return MapResponse{}, err
	}
	response.Status, response.ProjectionRevision = "waiting", status.ProjectionRevision
	if status.Ready {
		response.Status = "building"
		if failed := s.lastFailure.Load(); failed != nil && failed.key == key {
			response.Status = "unavailable"
		}
	} else if building := status.Building; building != nil && building.SpaceHash == space {
		response.Status = "building"
		if building.LastError != nil {
			response.Status = "unavailable"
		}
	}
	return response, ctx.Err()
}

// Update prepares and publishes under short locks; layout computation owns no
// runtime or indexing lock. Published coordinates remain available throughout.
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
	job, err := s.prepare(ctx, now)
	if err != nil {
		return err
	}
	if job == nil {
		return ctx.Err()
	}
	previous := job.previous
	if job.token != "" {
		previous = nil
	}
	s.activity.Store(&mapActivity{job.key, "building"})
	started := time.Now()
	layout, err := s.computeStars(ctx, job.snapshot, previous)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.lastFailure.Store(&mapSchedule{job.key, now})
		s.activity.Store(&mapActivity{job.key, "unavailable"})
		return err
	}
	slog.Info("stars layout computed", "points", len(layout.points), "elapsed", time.Since(started))
	unlockRuntime := s.Store.lockRuntime()
	defer unlockRuntime()
	if !s.Store.Settings.Stargazing.Enabled || !s.Store.Settings.Embedding.Enabled || job.key != s.cacheKey() {
		return mapIndexChanged()
	}
	unlock, err := s.Store.lockBackground(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	layout.generatedAt = now
	if job.previous != nil && job.previous.algorithm == starsAlgorithm {
		layout.parentVersion = job.previous.version
	}
	layout.rebuildToken = job.token
	if err := job.store.publishStars(ctx, job.snapshot, layout); err != nil {
		return err
	}
	s.cache.Store(&mapCache{job.key, s.response(layout, job.snapshot.space)})
	s.lastFailure.Store(nil)
	s.activity.Store(nil)
	s.schedule.Store(&mapSchedule{job.key, now.Add(mapSnapshotInterval)})
	slog.Info("stars snapshot published", "points", len(layout.points), "revision", job.snapshot.revision)
	return ctx.Err()
}

type mapJob struct {
	store    *Store
	key      string
	snapshot mapSnapshot
	previous *mapLayout
	token    string
}

func (store *Store) loadPublishedLayout(ctx context.Context, space string) (*mapLayout, string, error) {
	layout, token, err := store.loadStars(ctx, space)
	if err == nil && layout == nil {
		layout, err = store.loadLegacyLayout(ctx, space)
	}
	return layout, token, err
}

func (s *MapService) prepare(ctx context.Context, now time.Time) (*mapJob, error) {
	unlockRuntime := s.Store.lockRuntime()
	defer unlockRuntime()
	if !s.Store.Settings.Stargazing.Enabled || !s.Store.Settings.Embedding.Enabled {
		return nil, nil
	}
	key := s.cacheKey()
	if schedule := s.schedule.Load(); schedule != nil && schedule.key == key && now.Before(schedule.next) {
		return nil, ctx.Err()
	}
	s.schedule.Store(&mapSchedule{key, now.Add(5 * time.Second)})
	unlock, err := s.Store.lockBackground(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// An immutable settings view can be used while the runtime changes providers.
	store := &Store{Settings: s.Store.Settings, state: s.Store.state, index: s.Store.index,
		background: s.Store.background}
	space := embeddingSpace(store.Settings.Embedding)
	previous, token, err := store.loadPublishedLayout(ctx, space)
	if err != nil {
		return nil, err
	}
	sameAlgorithm := previous != nil && previous.algorithm == starsAlgorithm
	if previous != nil && !previous.generatedAt.IsZero() {
		s.cache.Store(&mapCache{key, s.response(previous, space)})
		if due := previous.generatedAt.Add(mapSnapshotInterval); sameAlgorithm && token == "" && now.Before(due) {
			s.schedule.Store(&mapSchedule{key, due})
			return nil, ctx.Err()
		}
	}
	status, err := store.VectorStatusContext(ctx)
	if err != nil || !status.Ready {
		return nil, err
	}
	if sameAlgorithm && token == "" && previous.generation == status.Active.ID && previous.revision == status.ProjectionRevision {
		s.schedule.Store(&mapSchedule{key, now.Add(mapSnapshotInterval)})
		return nil, ctx.Err()
	}
	snapshot, err := store.mapSnapshot(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, mapIndexChanged()
	}
	if err != nil {
		return nil, err
	}
	return &mapJob{store, key, snapshot, previous, token}, nil
}

func (s *MapService) Rebuild(ctx context.Context) error {
	if !s.Store.Settings.Stargazing.Enabled {
		return failure("STARGAZING_DISABLED", "Enable stargazing before rebuilding stars", 409, false)
	}
	if !s.Store.Settings.Embedding.Enabled {
		return failure("EMBEDDING_DISABLED", "Enable embedding before rebuilding stars", 409, false)
	}
	if err := s.Store.requestStarsRebuild(ctx, embeddingSpace(s.Store.Settings.Embedding)); err != nil {
		return err
	}
	s.schedule.Store(nil)
	return nil
}

func (a *App) runMap(ctx context.Context) error {
	for {
		if err := a.Map.Update(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("stars snapshot failed", "code", errorCode(err))
		}
		if err := runtimePause(ctx, time.Second); err != nil {
			return err
		}
	}
}

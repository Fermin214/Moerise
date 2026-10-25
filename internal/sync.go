package moerise

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

type SyncService struct {
	Store  *Store
	Source Source
	wake   chan struct{}
	alive  atomic.Bool
}

func NewSyncService(store *Store, source Source) *SyncService {
	return &SyncService{Store: store, Source: source, wake: make(chan struct{}, 1)}
}

func (s *SyncService) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *SyncService) scan(ctx context.Context, run, job string, since *int64) error {
	for _, state := range []string{"NORMAL", "ARCHIVED"} {
		token, visited := "", map[string]bool{}
		for page := 0; ; page++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if page >= s.Store.Settings.Memos.MaxPages {
				return upstreamError("PAGINATION_LIMIT", "Memos pagination exceeded the configured limit")
			}
			memos, next, err := s.Source.ListPage(ctx, state, since, token)
			if err != nil {
				return err
			}
			if err := s.Store.Stage(run, memos); err != nil {
				return err
			}
			if err := s.Store.Heartbeat(job); err != nil {
				return err
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
	return nil
}

func (s *SyncService) Sync(ctx context.Context, job Job) (map[string]any, error) {
	version, err := s.Source.Identity(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Store.SourceHealth(true, "", version); err != nil {
		return nil, err
	}
	state, err := s.Store.Status()
	if err != nil {
		return nil, err
	}
	full := job.Mode == "full" || state.Watermark == nil
	started, run := time.Now().UTC(), newID()
	var since *int64
	if !full {
		watermark, err := time.Parse(time.RFC3339Nano, *state.Watermark)
		if err != nil {
			return nil, err
		}
		value := watermark.Unix() - int64(s.Store.Settings.Sync.OverlapSeconds)
		since = &value
	}
	defer s.Store.DiscardScan(run)
	if err := s.scan(ctx, run, job.ID, since); err != nil {
		return nil, err
	}
	absent := map[string]bool{}
	if full {
		catchup := started.Unix() - int64(s.Store.Settings.Sync.OverlapSeconds)
		if err := s.scan(ctx, run, job.ID, &catchup); err != nil {
			return nil, err
		}
		names, err := s.Store.Names()
		if err != nil {
			return nil, err
		}
		staged, err := s.Store.StagedNames(run)
		if err != nil {
			return nil, err
		}
		for name := range names {
			if staged[name] {
				continue
			}
			fresh, err := s.Source.Get(ctx, name)
			if err != nil {
				return nil, err
			}
			if fresh == nil {
				absent[name] = true
			} else if err := s.Store.Stage(run, []Memo{*fresh}); err != nil {
				return nil, err
			}
			if err := s.Store.Heartbeat(job.ID); err != nil {
				return nil, err
			}
		}
	}
	if _, err := s.Source.Identity(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Publish first: an interrupted watermark update must replay an already complete scan.
	result, err := s.Store.Publish(run, absent)
	if err != nil {
		return nil, err
	}
	if err := s.Store.Advance(started.Format("2006-01-02T15:04:05.000000+00:00"), full); err != nil {
		return nil, err
	}
	if full {
		result["mode"] = "full"
	} else {
		result["mode"] = "incremental"
	}
	return result, nil
}

func (s *SyncService) Schedule() error {
	state, err := s.Store.Status()
	if err != nil {
		return err
	}
	age := func(value *string) time.Duration {
		if value == nil {
			return time.Duration(1<<63 - 1)
		}
		parsed, err := time.Parse(time.RFC3339Nano, *value)
		if err != nil {
			return time.Duration(1<<63 - 1)
		}
		return time.Since(parsed)
	}
	if age(state.LastFull) >= time.Duration(s.Store.Settings.Sync.FullSeconds)*time.Second {
		_, err = s.Store.Enqueue("full", false)
	} else if age(state.LastIncremental) >= time.Duration(s.Store.Settings.Sync.PollSeconds)*time.Second {
		_, err = s.Store.Enqueue("incremental", false)
	}
	return err
}

func (s *SyncService) Run(ctx context.Context) error {
	s.alive.Store(true)
	defer s.alive.Store(false)
	if _, err := s.Store.Enqueue("full", false); err != nil {
		return err
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for ctx.Err() == nil {
		if err := s.Schedule(); err != nil {
			return err
		}
		job, err := s.Store.Claim()
		if err != nil {
			return err
		}
		if job != nil {
			result, err := s.Sync(ctx, *job)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				code := errorCode(err)
				if healthErr := s.Store.SourceHealth(false, code, ""); healthErr != nil {
					return healthErr
				}
				if retryErr := s.Store.Retry(*job, code); retryErr != nil {
					return retryErr
				}
				slog.Warn("sync failed", "code", code, "job", job.ID)
			} else {
				if err := s.Store.Complete(job.ID, result); err != nil {
					return err
				}
				slog.Info("sync succeeded", "mode", result["mode"], "observed", result["observed"], "changed", result["changed"], "removed", result["removed"])
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.wake:
		case <-timer.C:
		}
	}
	return ctx.Err()
}

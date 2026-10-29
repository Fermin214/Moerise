package moerise

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type App struct {
	Settings      Settings
	Store         *Store
	Source        Source
	Worker        *SyncService
	Records       *RecordService
	Reviews       *ReviewService
	Search        *SearchService
	AI            *AIService
	Feishu        *FeishuService
	Notifications *NotificationService
	Vector        *VectorIndexService
	Notes         *NoteService
	Sessions      *SessionService
	closers       []func()
	closeOnce     sync.Once
}

// NewApp 装配共享服务；传入的 Store 和 Source 由调用方关闭。
func NewApp(settings Settings, store *Store, source Source) *App {
	a := &App{Settings: settings, Store: store, Source: source, Worker: NewSyncService(store, source)}
	a.Notes = NewNoteService(settings, store, source)
	a.Records, a.Reviews = NewRecordService(store, source), NewReviewService(store, source)
	a.Search = NewSearchService(store, source, nil)
	a.AI = NewAIService(a.Search, nil)
	a.Notifications = NewNotificationService(store, a.Reviews, nil)
	a.Feishu = NewFeishuService(store, source, a.Records, a.Reviews, a.Notifications)
	if a.Feishu != nil {
		a.Feishu.Search, a.Feishu.AI = a.Search, a.AI
		if client, ok := a.Feishu.Client.(*FeishuClient); ok {
			a.closers = append(a.closers, client.http.CloseIdleConnections)
		}
	}
	a.Notifications.Transport = NewFeishuDelivery(store, a.Reviews, a.Feishu)
	return a
}

// OpenApp 负责 serve 使用的写锁、数据库和网络资源。
func OpenApp(settings Settings) (_ *App, result error) {
	var cleanup []func()
	defer func() {
		if result != nil {
			for i := len(cleanup) - 1; i >= 0; i-- {
				cleanup[i]()
			}
		}
	}()
	lock, err := AcquireWriter(settings.StateDir)
	if err != nil {
		return nil, err
	}
	cleanup = append(cleanup, func() { _ = lock.Close() })
	store, err := NewStore(settings)
	if err != nil {
		return nil, err
	}
	cleanup = append(cleanup, func() { _ = store.Close() })
	source := NewMemosClient(settings.Memos)
	cleanup = append(cleanup, source.Close)
	a := NewApp(settings, store, source)
	cleanup = append(cleanup, a.closers...)
	if settings.LLM.Enabled {
		provider, err := NewLLMProvider(settings.LLM)
		if err != nil {
			return nil, err
		}
		cleanup = append(cleanup, provider.Close)
		a.AI.Provider = provider
	}
	if settings.Embedding.Enabled {
		provider, err := NewEmbeddingProvider(settings.Embedding)
		if err != nil {
			return nil, err
		}
		cleanup = append(cleanup, provider.Close)
		a.Search.Provider = provider
		a.Vector = NewVectorIndexService(store, source, provider)
	}
	if settings.Web.Enabled {
		sessions, err := NewSessionService(store, source)
		if err != nil {
			return nil, err
		}
		a.Sessions = sessions
		cleanup = append(cleanup, sessions.Close)
	}
	a.closers = cleanup
	return a, nil
}

func (a *App) Close() {
	a.closeOnce.Do(func() {
		for i := len(a.closers) - 1; i >= 0; i-- {
			a.closers[i]()
		}
	})
}

func (a *App) Run(ctx context.Context, server *http.Server) error {
	workerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		name string
		err  error
	}
	done := make(chan outcome, 5)
	var workers sync.WaitGroup
	start := func(name string, run func(context.Context) error) {
		workers.Go(func() { done <- outcome{name, run(workerCtx)} })
	}
	start("sync", a.Worker.Run)
	start("notifications", a.Notifications.Run)
	if a.Feishu != nil {
		start("Feishu", a.Feishu.Run)
	}
	if a.Vector != nil {
		start("vector index", a.Vector.Run)
	}
	go func() { done <- outcome{"HTTP", server.ListenAndServe()} }()
	var result error
	select {
	case <-ctx.Done():
	case stopped := <-done:
		if stopped.err != nil && !errors.Is(stopped.err, http.ErrServerClosed) && !errors.Is(stopped.err, context.Canceled) {
			result = fmt.Errorf("%s stopped: %w", stopped.name, stopped.err)
		}
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		if result == nil {
			result = err
		}
	}
	cancel()
	workers.Wait()
	return result
}

package moerise

import (
	"context"
	"log/slog"
	"reflect"
	"time"
)

func (s *Store) lockRuntime() func() {
	if s.runtimeMu == nil {
		return func() {}
	}
	s.runtimeMu.RLock()
	return s.runtimeMu.RUnlock
}

func closeChannel(events *FeishuService, transport NotificationTransport) {
	if events != nil {
		if client, ok := events.Client.(*FeishuClient); ok {
			client.http.CloseIdleConnections()
		}
	}
	if delivery, ok := transport.(*FeishuDelivery); ok {
		delivery.http.CloseIdleConnections()
		if client, ok := delivery.images.(*FeishuClient); ok {
			client.http.CloseIdleConnections()
		}
	}
}

func (a *App) closeRuntime() {
	if a.AI.Provider != nil {
		a.AI.Provider.Close()
	}
	if a.Search.Provider != nil {
		a.Search.Provider.Close()
	}
	closeChannel(a.Feishu, a.Notifications.Transport)
}

func (a *App) UpdateSettings(ctx context.Context, update SettingsUpdate) (SettingsView, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !a.runtimeMu.TryLock() {
		select {
		case <-ctx.Done():
			return SettingsView{}, failure("SETTINGS_BUSY", "Active operations are still using the settings; retry later", 503, true)
		case <-ticker.C:
		}
	}
	defer a.runtimeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return SettingsView{}, err
	}
	settings, err := a.Store.prepareSettings(update)
	if err != nil {
		return SettingsView{}, err
	}
	old := a.Store.Settings
	llmChanged := !reflect.DeepEqual(old.LLM, settings.LLM)
	embeddingChanged := !reflect.DeepEqual(old.Embedding, settings.Embedding)
	channelChanged := !reflect.DeepEqual(old.Channel, settings.Channel)
	llm, embedding := a.AI.Provider, a.Search.Provider
	events, transport := a.Feishu, a.Notifications.Transport
	if llmChanged {
		llm = nil
	}
	if embeddingChanged {
		embedding = nil
	}
	if channelChanged {
		events, transport = nil, nil
	}
	committed := false
	defer func() {
		if !committed {
			if llmChanged && llm != nil {
				llm.Close()
			}
			if embeddingChanged && embedding != nil {
				embedding.Close()
			}
			if channelChanged {
				closeChannel(events, transport)
			}
		}
	}()
	if llmChanged {
		llm = nil
		if settings.LLM.Enabled {
			llm, err = NewLLMProvider(settings.LLM)
			if err != nil {
				return SettingsView{}, err
			}
		}
	}
	if embeddingChanged {
		embedding = nil
		if settings.Embedding.Enabled {
			embedding, err = NewEmbeddingProvider(settings.Embedding)
			if err != nil {
				return SettingsView{}, err
			}
		}
	}
	if channelChanged {
		events = newFeishuService(settings.Channel, a.Store, a.Source, a.Records, a.Reviews, a.Notifications)
		if events != nil {
			events.Search, events.AI = a.Search, a.AI
		}
		transport = newFeishuDelivery(settings.Channel, a.Store, a.Reviews, events)
	}
	now := time.Now()
	if old.daily != nil && now.Before(old.daily.Until) {
		settings.daily = old.daily
	} else if old.Timezone != settings.Timezone || !reflect.DeepEqual(old.Review, settings.Review) ||
		!reflect.DeepEqual(old.Notifications.Clocks(), settings.Notifications.Clocks()) {
		location, _ := time.LoadLocation(old.Timezone)
		day := now.In(location)
		until := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, location)
		settings.daily = &dailySettings{old.Timezone, old.Review, old.Notifications, until}
	} else {
		settings.daily = nil
	}
	if err := ctx.Err(); err != nil {
		return SettingsView{}, err
	}
	if err := a.Store.migrateFeishuQueue(); err != nil {
		return SettingsView{}, err
	}
	if err := a.Store.persistSettings(settings); err != nil {
		return SettingsView{}, err
	}
	oldLLM, oldEmbedding, oldEvents, oldTransport := a.AI.Provider, a.Search.Provider, a.Feishu, a.Notifications.Transport
	a.Store.Settings, a.Notes.Settings = settings, settings
	a.AI.Provider, a.Search.Provider = llm, embedding
	a.Feishu, a.Notifications.Transport = events, transport
	if embeddingChanged {
		a.Search.mu.Lock()
		a.Search.matrix = nil
		a.Search.mu.Unlock()
		a.Vector = nil
		if embedding != nil {
			a.Vector = NewVectorIndexService(a.Store, a.Source, embedding)
		}
	}
	committed = true
	if llmChanged && oldLLM != nil {
		oldLLM.Close()
	}
	if embeddingChanged && oldEmbedding != nil {
		oldEmbedding.Close()
	}
	if channelChanged {
		closeChannel(oldEvents, oldTransport)
	}
	return a.Store.settingsView(), nil
}

func runtimePause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *App) runVector(ctx context.Context) error {
	delay := time.Duration(0)
	for {
		if err := runtimePause(ctx, delay); err != nil {
			return err
		}
		unlock := a.Store.lockRuntime()
		worked, err := false, error(nil)
		if a.Vector != nil {
			worked, err = a.Vector.Step(ctx)
		}
		retry := time.Duration(a.Store.Settings.Sync.RetrySeconds) * time.Second
		unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch {
		case err != nil:
			slog.Warn("vector indexing failed", "code", errorCode(err))
			delay = retry
		case worked:
			delay = 0
		default:
			delay = time.Second
		}
	}
}

func (a *App) runFeishu(ctx context.Context) error {
	var initialized *FeishuService
	for {
		unlock := a.Store.lockRuntime()
		events := a.Feishu
		var worked bool
		var err error
		if events != nil {
			if initialized != events {
				err = events.prepare()
				if err == nil {
					initialized = events
				}
			}
			if err == nil {
				worked, err = events.processOne(ctx)
			}
		}
		unlock()
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !worked {
			if err := runtimePause(ctx, 500*time.Millisecond); err != nil {
				return err
			}
		}
	}
}

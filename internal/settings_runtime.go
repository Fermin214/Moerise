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
	if delivery, ok := transport.(*TelegramDelivery); ok {
		delivery.http.CloseIdleConnections()
	}
}

func (a *App) closeRuntime() {
	if a.LLM != nil {
		a.LLM.Close()
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
	replacements, err := a.prepareRuntime(settings)
	if err != nil {
		return SettingsView{}, err
	}
	settings.daily = a.Store.dailyTransition(settings, time.Now())
	if err := a.Store.saveRuntimeSettings(ctx, &settings); err != nil {
		replacements.close()
		return SettingsView{}, err
	}
	a.installRuntime(settings, replacements)
	return a.Store.settingsView(), nil
}

func (s *Store) saveRuntimeSettings(ctx context.Context, settings *Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.retainNotificationChannels(settings); err != nil {
		return err
	}
	return s.persistSettings(*settings)
}

func (s *Store) dailyTransition(settings Settings, now time.Time) *dailySettings {
	old := s.Settings
	if old.daily != nil && now.Before(old.daily.Until) {
		return old.daily
	}
	if old.Timezone != settings.Timezone || !reflect.DeepEqual(old.Review, settings.Review) ||
		!reflect.DeepEqual(old.Notifications.Clocks(), settings.Notifications.Clocks()) {
		location, _ := time.LoadLocation(old.Timezone)
		day := now.In(location)
		until := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, location)
		return &dailySettings{old.Timezone, old.Review, old.Notifications, until, s.productVersion}
	}
	return nil
}

type runtimeReplacements struct {
	llmChanged, embeddingChanged, channelChanged bool
	llm                                          LLMProvider
	embedding                                    EmbeddingProvider
	events                                       *FeishuService
	transport                                    NotificationTransport
}

func (a *App) prepareRuntime(settings Settings) (*runtimeReplacements, error) {
	old := a.Store.Settings
	replacements := &runtimeReplacements{
		llmChanged:       !reflect.DeepEqual(old.LLM, settings.LLM),
		embeddingChanged: !reflect.DeepEqual(old.Embedding, settings.Embedding),
		channelChanged:   !reflect.DeepEqual(old.Channel, settings.Channel),
	}
	var err error
	if replacements.llmChanged && settings.LLM.Enabled {
		replacements.llm, err = NewLLMProvider(settings.LLM)
		if err != nil {
			return nil, err
		}
	}
	if replacements.embeddingChanged && settings.Embedding.Enabled {
		replacements.embedding, err = NewEmbeddingProvider(settings.Embedding)
		if err != nil {
			replacements.close()
			return nil, err
		}
	}
	if replacements.channelChanged {
		replacements.events = newFeishuService(settings.Channel, a.Store, a.Source, a.Records, a.Notifications)
		replacements.transport = newNotificationTransport(settings.Channel, a.Store, a.Reviews, replacements.events)
	}
	return replacements, nil
}

func (a *App) installRuntime(settings Settings, replacements *runtimeReplacements) {
	if (replacements.embeddingChanged || a.Store.Settings.Stargazing != settings.Stargazing) && a.Map != nil {
		a.Map.Reset()
	}
	a.Store.Settings, a.Notes.Settings = settings, settings
	if replacements.llmChanged {
		if a.LLM != nil {
			a.LLM.Close()
		}
		a.LLM, a.Chat.Provider = replacements.llm, replacements.llm
	}
	if replacements.embeddingChanged {
		if a.Search.Provider != nil {
			a.Search.Provider.Close()
		}
		a.Search.Provider = replacements.embedding
		a.Search.mu.Lock()
		a.Search.matrix = nil
		a.Search.mu.Unlock()
		a.Vector = nil
		if a.Search.Provider != nil {
			a.Vector = NewVectorIndexService(a.Store, a.Source, a.Search.Provider)
		}
	}
	if replacements.channelChanged {
		closeChannel(a.Feishu, a.Notifications.Transport)
		a.Feishu, a.Notifications.Transport = replacements.events, replacements.transport
	}
}

func (r *runtimeReplacements) close() {
	if r.llm != nil {
		r.llm.Close()
	}
	if r.embedding != nil {
		r.embedding.Close()
	}
	closeChannel(r.events, r.transport)
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

// RunFeishu processes channel work with the same runtime replacement lock as serving.
func (a *App) RunFeishu(ctx context.Context) error {
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

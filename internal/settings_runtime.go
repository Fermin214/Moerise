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
	a.Notifications.closeResolvedTransports()
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
	embeddingChanged, channelChanged bool
	embedding                        EmbeddingProvider
	events                           *FeishuService
	transport                        NotificationTransport
}

func (a *App) prepareRuntime(settings Settings) (*runtimeReplacements, error) {
	old := a.Store.Settings
	replacements := &runtimeReplacements{
		embeddingChanged: !reflect.DeepEqual(old.Embedding, settings.Embedding),
		channelChanged:   !reflect.DeepEqual(old.Channel, settings.Channel),
	}
	var err error
	if replacements.embeddingChanged && settings.Embedding.Enabled {
		replacements.embedding, err = NewEmbeddingProvider(settings.Embedding)
		if err != nil {
			replacements.close()
			return nil, err
		}
	}
	if replacements.channelChanged {
		replacements.events = newFeishuService(settings.Channel, a.Store, a.Source, a.Records, a.Notifications)
		replacements.transport = newNotificationTransport(a.Store.channelForDelivery(settings.Channel), a.Store, a.Reviews, replacements.events)
	}
	return replacements, nil
}

func (a *App) installRuntime(settings Settings, replacements *runtimeReplacements) {
	if (replacements.embeddingChanged || a.Store.Settings.Stargazing.Enabled != settings.Stargazing.Enabled ||
		a.Store.Settings.Timezone != settings.Timezone) && a.Map != nil {
		a.Map.Reset()
	}
	oldChannel := a.Store.Settings.Channel
	if feishuReceiverConfig(oldChannel) != feishuReceiverConfig(settings.Channel) {
		a.Store.botStates.Delete([2]string{"feishu-app", configured(oldChannel.AppID)})
		a.Store.feishuGeneration++
	}
	a.Store.Settings, a.Notes.Settings = settings, settings
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
		a.Notifications.closeResolvedTransports()
		closeChannel(a.Feishu, a.Notifications.Transport)
		a.Feishu, a.Notifications.Transport = replacements.events, replacements.transport
		a.Notifications.transportKey = a.Notifications.channelKey()
	}
	a.Store.wakeBackground()
}

func feishuReceiverConfig(channel ChannelConfig) [6]string {
	return [6]string{channel.feishuMode(), configured(channel.AppID), configured(channel.AppSecret),
		configured(channel.VerificationToken), configured(channel.EncryptKey), configured(channel.UserOpenID)}
}

func (r *runtimeReplacements) close() {
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
			if err := waitRuntime(ctx, a.Store.vectorWake, time.Time{}); err != nil {
				return err
			}
			delay = 0
		}
		if worked {
			select {
			case a.Store.mapWake <- struct{}{}:
			default:
			}
		}
	}
}

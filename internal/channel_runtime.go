package moerise

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

type feishuBot struct {
	app    *App
	events *FeishuService
	config feishuSocketConfig
}

func (b *feishuBot) Run(ctx context.Context) error {
	if b.config.id == "" {
		<-ctx.Done()
	} else {
		b.app.connectFeishu(ctx, b.config, time.Minute)
	}
	return ctx.Err()
}

func (b *feishuBot) Send(ctx context.Context, peer, message, text string) (string, error) {
	id := derivedID(b.events.Store.Settings.CursorSecret, "feishu-reply", message)
	return b.events.Client.Reply(ctx, message, id, text)
}

func (a *App) RunChannels(ctx context.Context) error {
	active := map[string]*botConnection{}
	defer func() {
		for _, client := range active {
			client.stop()
		}
	}()
	if err := a.Notifications.recover(); err != nil {
		return err
	}
	for {
		unlock := a.Store.lockRuntime()
		channel := a.Store.Settings.Channel
		reconnect := false
		for _, kind := range []string{"qq", "telegram", "feishu-app"} {
			id := channel.botID(kind)
			credential := configured(channel.QQAppSecret)
			switch kind {
			case "telegram":
				credential = configured(channel.BotToken)
			case "feishu-app":
				credential = hashBytes(canonicalJSON(feishuReceiverConfig(channel)))
			}
			client := active[kind]
			enabled := a.Store.Settings.Web.StateEncryptionKey != "" && channel.botEnabled(kind)
			if client != nil && (!enabled || client.id != id || client.credential != credential) {
				client.cancel()
				unlock()
				<-client.done
				unlock = a.Store.lockRuntime()
				delete(active, kind)
				a.Store.botStates.Delete([2]string{kind, client.id})
				reconnect = true
				break
			}
			if enabled && client == nil {
				var bot BotChannel
				switch kind {
				case "qq":
					bot = newQQBot(id, configured(channel.QQAppSecret), a.Store)
				case "telegram":
					settings := a.Store.Settings
					key := derivedID(settings.CursorSecret, "telegram-start", settings.Memos.InstanceID, settings.Memos.User, id)
					bot = newTelegramBot(credential, key, botReceiverStore{a.Store}, a.Store)
				case "feishu-app":
					bot = &feishuBot{a, a.Feishu, a.Store.socketConfig()}
				}
				connectionCtx, cancel := context.WithCancel(ctx)
				client = &botConnection{bot: bot, id: id, credential: credential, cancel: cancel, done: make(chan struct{})}
				active[kind] = client
				go func(client *botConnection) {
					defer close(client.done)
					for connectionCtx.Err() == nil {
						_ = client.bot.Run(connectionCtx)
						if runtimePause(connectionCtx, time.Second) != nil {
							return
						}
					}
				}(client)
			}
		}
		if reconnect {
			unlock()
			continue
		}
		worked := false
		var failure error
		for kind, client := range active {
			progress, err := a.processBotMessage(ctx, kind, client.id, client.bot)
			worked = worked || progress
			if err != nil {
				failure = err
			}
		}
		if a.Store.Settings.Notifications.Enabled {
			created, err := a.Notifications.Schedule(ctx, time.Now())
			worked = worked || created > 0
			if err != nil {
				failure = err
			}
		}
		progress, err := a.Notifications.Deliver(ctx)
		worked = worked || progress
		if err != nil {
			failure = err
		}
		var next time.Time
		if !worked || failure != nil {
			next, err = a.channelDeadline(active, time.Now())
			if err != nil {
				failure = err
			}
		}
		unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if failure != nil {
			slog.Warn("channel work failed", "code", errorCode(failure))
			next = time.Now().Add(5 * time.Second)
		} else if worked {
			continue
		}
		if err := waitRuntime(ctx, a.Store.channelWake, next); err != nil {
			return err
		}
	}
}

func (a *App) channelDeadline(active map[string]*botConnection, now time.Time) (time.Time, error) {
	var next time.Time
	add := func(at time.Time) {
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	s := a.Store
	for kind, client := range active {
		var due sql.NullInt64
		err := s.state.QueryRow(`SELECT MIN(i.available_at) FROM bot_inbox i JOIN bot_bindings b
			USING(instance_id,user_name,kind,bot_id,peer)
			WHERE i.instance_id=? AND i.user_name=? AND i.kind=? AND i.bot_id=? AND i.phase IN ('pending','reply')`,
			s.Settings.Memos.InstanceID, s.Settings.Memos.User, kind, client.id).Scan(&due)
		if err != nil {
			return next, err
		}
		if due.Valid {
			add(time.UnixMilli(due.Int64))
		}
	}
	var delivery sql.NullFloat64
	err := s.state.QueryRow(`SELECT MIN(d.available_at) FROM delivery_logs d
		WHERE d.instance_id=? AND d.user_name=? AND d.status='pending' AND (d.kind='reply' OR ?)
		AND NOT EXISTS(SELECT 1 FROM delivery_logs p WHERE p.root_id=d.root_id AND p.part_ordinal<d.part_ordinal AND p.status!='sent')`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User, s.Settings.Notifications.Enabled).Scan(&delivery)
	if err != nil {
		return next, err
	}
	if delivery.Valid {
		add(time.UnixMilli(int64(delivery.Float64 * 1000)))
	}
	if s.Settings.Notifications.Enabled {
		settings := s.reviewSettings(now)
		location, err := time.LoadLocation(settings.Timezone)
		if err != nil {
			return next, err
		}
		date := now.In(location).Format(time.DateOnly)
		clocks := settings.Notifications.Clocks()
		var batch string
		err = s.state.QueryRow(`SELECT id,timezone FROM review_batches WHERE instance_id=? AND user_name=? AND local_date=?`,
			settings.Memos.InstanceID, settings.Memos.User, date).Scan(&batch, &settings.Timezone)
		if err != nil && err != sql.ErrNoRows {
			return next, err
		}
		if err == nil {
			location, err = time.LoadLocation(settings.Timezone)
			if err != nil {
				return next, err
			}
			slots, err := a.Reviews.slots(batch)
			if err != nil {
				return next, err
			}
			clocks = nil
			for _, slot := range slots {
				clocks = append(clocks, slot.Clock)
			}
		}
		for _, clock := range clocks {
			due, err := scheduledMoment(date, clock, location)
			if err != nil {
				return next, err
			}
			if due.After(now) {
				add(due)
			}
		}
		day := now.In(location)
		add(time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, location))
	}
	return next, nil
}

func waitRuntime(ctx context.Context, wake <-chan struct{}, at time.Time) error {
	var timer *time.Timer
	var timeout <-chan time.Time
	if !at.IsZero() {
		timer = time.NewTimer(max(time.Until(at), time.Millisecond))
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wake:
	case <-timeout:
	}
	return nil
}

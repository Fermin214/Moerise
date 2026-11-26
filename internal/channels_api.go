package moerise

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"
)

type channelCandidateFields struct {
	settings, secrets []string
}

var channelCandidates = map[string]channelCandidateFields{
	"qq":             {[]string{"qq_app_id"}, []string{"qq_app_secret"}},
	"telegram":       {[]string{"destination"}, []string{"bot_token"}},
	"feishu-app":     {[]string{"app_id", "feishu_mode", "user_open_id"}, []string{"app_secret", "verification_token", "encrypt_key"}},
	"feishu-webhook": {[]string{"webhook_images", "webhook_app_id"}, []string{"webhook_url", "webhook_secret", "webhook_app_secret"}},
}

func validateCheckFields(body settingsRequest, section string, allowedFields, allowedSecrets []string) error {
	bad := failure("INVALID_ARGUMENT", "Invalid candidate settings fields", 422, false)
	if body.Version < 1 {
		return bad
	}
	for name, raw := range body.Settings {
		var fields map[string]json.RawMessage
		if name != section || json.Unmarshal(raw, &fields) != nil || fields == nil {
			return bad
		}
		for field := range fields {
			if allowedFields != nil && !slices.Contains(allowedFields, field) {
				return bad
			}
		}
	}
	for name := range body.Secrets {
		if !slices.Contains(allowedSecrets, name) {
			return bad
		}
	}
	return nil
}

func (a *ClientAPI) channelsCheckRoute(w http.ResponseWriter, r *http.Request) (any, int, error) {
	if r.Method != http.MethodPost {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	var body settingsRequest
	if err := readClientJSON(w, r, 128*1024, []string{"version", "target", "settings", "secrets"}, &body); err != nil {
		return nil, 0, err
	}
	fields, ok := channelCandidates[body.Target]
	if !ok {
		return nil, 0, failure("INVALID_ARGUMENT", "Unknown channel to check", 422, false)
	}
	if err := validateCheckFields(body, "channel", fields.settings, fields.secrets); err != nil {
		return nil, 0, err
	}
	channel, err := a.channelCheckSnapshot(r.Context(), body.SettingsUpdate)
	if err != nil {
		return nil, 0, err
	}
	started := time.Now()
	if err := a.checkChannel(r.Context(), body.Target, channel); err != nil {
		return nil, 0, err
	}
	return ClientModelCheck{body.Version, "ok", time.Since(started).Milliseconds()}, http.StatusOK, nil
}

func (a *ClientAPI) channelCheckSnapshot(ctx context.Context, update SettingsUpdate) (ChannelConfig, error) {
	if err := a.lockTimedRequest(ctx); err != nil {
		return ChannelConfig{}, err
	}
	defer a.app.runtimeMu.RUnlock()
	settings, err := a.app.Store.prepareSettings(update)
	return settings.Channel, err
}

func (a *ClientAPI) checkChannel(ctx context.Context, target string, channel ChannelConfig) error {
	bad := failure("BOT_CREDENTIALS", "Channel credentials are incomplete", 422, false)
	if target == "feishu-webhook" {
		if configured(channel.WebhookURL) == "" {
			return bad
		}
		return nil
	}
	if !channel.botEnabled(target) {
		return bad
	}
	var client *http.Client
	var probe func(context.Context) (string, error)
	switch target {
	case "qq":
		bot := newQQBot(configured(channel.QQAppID), configured(channel.QQAppSecret), nil)
		client, probe = bot.http, bot.accessToken
	case "telegram":
		bot := newTelegramBot(configured(channel.BotToken), "", nil, nil)
		client, probe = bot.http, bot.identify
	default:
		feishu := NewFeishuClient(channel)
		client, probe = feishu.http, feishu.tenantToken
	}
	if a.channelCheckTransport != nil {
		client.Transport = a.channelCheckTransport
	} else if target == "qq" || target == "telegram" {
		client.Transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	defer client.CloseIdleConnections()
	_, err := probe(ctx)
	return err
}

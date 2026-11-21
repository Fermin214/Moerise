package moerise

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

func (c ChannelConfig) feishuMode() string {
	if c.FeishuMode != "" {
		return c.FeishuMode
	}
	if configured(c.VerificationToken) != "" || configured(c.EncryptKey) != "" {
		return "callback"
	}
	return "websocket"
}

func (c *ChannelConfig) migrateLegacy() {
	if c.FeishuMode != "" {
		return
	}
	if configured(c.Destination) != "" && configured(c.Destination) == configured(c.UserOpenID) {
		c.Destination = nil
	}
	if c.Kind != "telegram" {
		if !validTelegramToken(configured(c.BotToken)) {
			c.BotToken = nil
		}
		if !validTelegramChat(configured(c.Destination)) {
			c.Destination = nil
		}
	}
	hasUpload := c.Kind == "feishu-webhook" || configured(c.WebhookURL) != ""
	hasApp := c.Kind == "feishu-app" || len(configured(c.VerificationToken)) >= 16 && strings.HasPrefix(configured(c.UserOpenID), "ou_")
	if hasUpload && configured(c.WebhookAppID) == "" && configured(c.AppID) != "" && len(configured(c.AppSecret)) >= 16 {
		c.WebhookImages, c.WebhookAppID, c.WebhookAppSecret = true, c.AppID, c.AppSecret
	}
	if !hasApp {
		c.AppID, c.AppSecret, c.VerificationToken, c.EncryptKey, c.UserOpenID = nil, nil, nil, nil, nil
	} else if c.Kind != "feishu-app" && (configured(c.AppID) == "" || len(configured(c.AppSecret)) < 16) {
		c.AppSecret = nil
	}
	c.FeishuMode = c.feishuMode()
}

func (c ChannelConfig) botID(kind string) string {
	switch kind {
	case "qq":
		return configured(c.QQAppID)
	case "telegram":
		bot, _, _ := strings.Cut(configured(c.BotToken), ":")
		return bot
	case "feishu-app":
		return configured(c.AppID)
	}
	return ""
}

func (c ChannelConfig) botEnabled(kind string) bool {
	switch kind {
	case "qq":
		return c.botID(kind) != "" && configured(c.QQAppSecret) != ""
	case "telegram":
		return validTelegramToken(configured(c.BotToken))
	case "feishu-app":
		return c.botID(kind) != "" && configured(c.AppSecret) != ""
	}
	return false
}

type botStatus struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
	Peer   string `json:"peer"`
	Link   string `json:"link"`
}

func (s *Store) setBotStatus(kind, botID, state, detail, link string) {
	s.botStates.Store([2]string{kind, botID}, botStatus{State: state, Detail: detail, Link: link})
}

func (s *Store) botStatuses() map[string]botStatus {
	channel := s.Settings.Channel
	result := map[string]botStatus{}
	for _, kind := range []string{"qq", "telegram", "feishu-app"} {
		botID := channel.botID(kind)
		status := botStatus{State: "disabled"}
		if channel.botEnabled(kind) {
			status.State = "connecting"
			if kind == "feishu-app" && channel.feishuMode() == "callback" {
				status.State = "awaiting_verification"
			}
			if value, ok := s.botStates.Load([2]string{kind, botID}); ok {
				status = value.(botStatus)
			}
			status.Peer, _, _ = s.botPeer(kind, botID)
			if kind == "feishu-app" && configured(channel.UserOpenID) != "" {
				status.Peer = configured(channel.UserOpenID)
			}
			if kind == "telegram" && s.Settings.Web.StateEncryptionKey == "" {
				status.State, status.Detail = "error", "STATE_KEY_REQUIRED"
			}
		}
		result[kind] = status
	}
	return result
}

func (s *Store) botPeer(kind, botID string) (string, time.Time, error) {
	var peer string
	var since int64
	err := s.state.QueryRow(`SELECT peer,bound_at FROM bot_bindings WHERE instance_id=? AND user_name=? AND kind=? AND bot_id=?`,
		s.Settings.Memos.InstanceID, s.Settings.Memos.User, kind, botID).Scan(&peer, &since)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	return peer, time.UnixMilli(since), err
}

func (s *Store) bindBotPeer(tx *sql.Tx, kind, botID, peer string, at time.Time) (string, error) {
	instance, user := s.Settings.Memos.InstanceID, s.Settings.Memos.User
	if _, err := tx.Exec(`INSERT INTO bot_bindings VALUES (?,?,?,?,?,?) ON CONFLICT DO NOTHING`, instance, user, kind, botID, peer, at.UnixMilli()); err != nil {
		return "", err
	}
	var bound string
	err := tx.QueryRow(`SELECT peer FROM bot_bindings WHERE instance_id=? AND user_name=? AND kind=? AND bot_id=?`, instance, user, kind, botID).Scan(&bound)
	return bound, err
}

func (s *Store) initializeBots() error {
	_, err := s.state.Exec(`UPDATE bot_inbox SET phase='failed',error_code='PROCESS_INTERRUPTED' WHERE phase='sending'`)
	return err
}

func (s *Store) channelForDelivery(c ChannelConfig) ChannelConfig {
	result := ChannelConfig{Kind: c.Kind}
	switch c.Kind {
	case "qq", "telegram", "feishu-app":
		peer, _, _ := s.botPeer(c.Kind, c.botID(c.Kind))
		switch c.Kind {
		case "qq":
			result.QQAppID, result.QQAppSecret = c.QQAppID, c.QQAppSecret
		case "telegram":
			result.BotToken = c.BotToken
			if configured(c.Destination) != "" {
				peer = configured(c.Destination)
			}
		case "feishu-app":
			result.AppID, result.AppSecret = c.AppID, c.AppSecret
			if configured(c.UserOpenID) != "" {
				peer = configured(c.UserOpenID)
			}
			result.UserOpenID = new(peer)
		}
		result.Destination = new(peer)
	case "feishu-webhook":
		result.WebhookURL, result.WebhookSecret = c.WebhookURL, c.WebhookSecret
		if c.WebhookImages {
			result.AppID, result.AppSecret = c.WebhookAppID, c.WebhookAppSecret
		}
	}
	return result
}

func (s *Store) deliveryChannel() ChannelConfig {
	return s.channelForDelivery(s.Settings.Channel)
}

package moerise

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type telegramBot struct {
	token, id, startKey string
	receiver            botReceiver
	store               *Store
	baseURL             string
	http                *http.Client
}

const (
	telegramPollTimeout     = 45 * time.Second
	telegramUpdateRetention = 24 * time.Hour
	telegramIDReset         = 7 * 24 * time.Hour
)

func newTelegramBot(token, startKey string, receiver botReceiver, store *Store) *telegramBot {
	id, _, _ := strings.Cut(token, ":")
	return &telegramBot{token: token, id: id, startKey: startKey, receiver: receiver, store: store,
		baseURL: "https://api.telegram.org", http: botHTTPClient(telegramPollTimeout)}
}

func (bot *telegramBot) request(ctx context.Context, method string, params, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bot.baseURL+"/bot"+bot.token+"/"+method,
		bytes.NewReader(canonicalJSON(params)))
	if err != nil {
		return failure("BOT_CREDENTIALS", "Invalid Telegram bot token", 422, false)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := bot.http.Do(req)
	if err != nil {
		return upstreamError("BOT_CONNECTION", "Cannot reach Telegram")
	}
	defer response.Body.Close()
	var envelope struct {
		OK         *bool           `json:"ok"`
		Result     json.RawMessage `json:"result"`
		Code       int             `json:"error_code"`
		Parameters struct {
			RetryAfter int32 `json:"retry_after"`
		} `json:"parameters"`
	}
	err = botJSON(response.Body, 8<<20, &envelope)
	switch {
	case envelope.Code == 429 || response.StatusCode == 429:
		seconds := envelope.Parameters.RetryAfter
		if seconds <= 0 {
			seconds = 60
		}
		return &notificationRetryError{error: upstreamError("BOT_RATE_LIMITED", "Telegram rate limit reached"),
			after: time.Duration(seconds) * time.Second}
	case envelope.Code == 401 || envelope.Code == 404 || response.StatusCode == 401 || response.StatusCode == 404:
		return failure("BOT_CREDENTIALS", "Check the Telegram bot token", 422, false)
	case response.StatusCode == 409 || envelope.Code == 409:
		return upstreamError("TELEGRAM_CONFLICT", "Telegram polling conflicts with another receiver")
	case response.StatusCode >= 500:
		return upstreamError("BOT_CONNECTION", "Telegram unavailable")
	case err != nil || response.StatusCode != 200 || envelope.OK == nil || !*envelope.OK ||
		len(envelope.Result) == 0 || string(envelope.Result) == "null" || json.Unmarshal(envelope.Result, target) != nil:
		return upstreamError("BOT_PROTOCOL", "Invalid Telegram response")
	}
	return nil
}

var telegramBotUsername = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

func (bot *telegramBot) identify(ctx context.Context) (string, error) {
	var account struct {
		ID       int64  `json:"id"`
		IsBot    bool   `json:"is_bot"`
		Username string `json:"username"`
	}
	if err := bot.request(ctx, "getMe", struct{}{}, &account); err != nil {
		return "", err
	}
	if !account.IsBot || account.ID <= 0 || strconv.FormatInt(account.ID, 10) != bot.id || !telegramBotUsername.MatchString(account.Username) {
		return "", upstreamError("BOT_PROTOCOL", "Unexpected Telegram bot identity")
	}
	return "https://t.me/" + account.Username + "?start=" + url.QueryEscape(bot.startKey), nil
}

func (bot *telegramBot) checkWebhook(ctx context.Context) error {
	var webhook struct {
		URL *string `json:"url"`
	}
	if err := bot.request(ctx, "getWebhookInfo", struct{}{}, &webhook); err != nil {
		return err
	}
	if webhook.URL == nil {
		return upstreamError("BOT_PROTOCOL", "Invalid Telegram webhook information")
	}
	if *webhook.URL != "" {
		return failure("TELEGRAM_WEBHOOK", "Telegram bot has an external webhook", 409, false)
	}
	return nil
}

func (bot *telegramBot) Run(ctx context.Context) error {
	delay := time.Second
	link := ""
	for ctx.Err() == nil {
		bot.receiver.Status("telegram", bot.id, "connecting", "", link)
		nextLink, err := bot.identify(ctx)
		if err == nil {
			link = nextLink
			err = bot.checkWebhook(ctx)
		}
		if err == nil {
			cursor, storageErr := bot.receiver.Cursor(ctx, "telegram", bot.id)
			if storageErr != nil {
				err = upstreamError("BOT_STORAGE", "Cannot read the Telegram polling position")
			} else {
				bot.receiver.Status("telegram", bot.id, "connected", "", link)
				for ctx.Err() == nil && err == nil {
					cursor, err = bot.poll(ctx, cursor)
					if err == nil {
						delay = time.Second
					}
				}
			}
		}
		if ctx.Err() != nil {
			break
		}
		if errorCode(err) == "TELEGRAM_CONFLICT" {
			if conflict := bot.checkWebhook(ctx); conflict != nil {
				err = conflict
			}
		}
		bot.receiver.Status("telegram", bot.id, "error", errorCode(err), link)
		if !waitBotRetry(ctx, botRetryDelay(err, delay)) {
			break
		}
		delay = min(2*delay, time.Minute)
	}
	bot.receiver.Status("telegram", bot.id, "disconnected", "", "")
	return ctx.Err()
}

func (bot *telegramBot) poll(ctx context.Context, cursor botCursor) (botCursor, error) {
	var updates []struct {
		ID      *int64          `json:"update_id"`
		Message json.RawMessage `json:"message"`
	}
	started := time.Now()
	var offset int64
	if saved, ok := strings.CutPrefix(cursor.Session, "update:"); ok {
		stamp, err := time.Parse(time.RFC3339Nano, saved)
		if err == nil && !stamp.After(started) && started.Add(telegramPollTimeout).Before(stamp.Add(telegramIDReset)) {
			offset = cursor.Sequence
		}
	}
	err := bot.request(ctx, "getUpdates", map[string]any{"offset": offset, "limit": 25, "timeout": 30, "allowed_updates": []string{"message"}}, &updates)
	if err != nil {
		return cursor, err
	}
	for _, update := range updates {
		if update.ID == nil || *update.ID < 0 || *update.ID >= 1<<52 {
			return cursor, upstreamError("BOT_PROTOCOL", "Invalid Telegram update identifier")
		}
		stamp := started.Add(-telegramUpdateRetention)
		var message *botMessage
		var incoming telegramIncomingMessage
		if json.Unmarshal(update.Message, &incoming) == nil {
			date := time.Unix(incoming.Date, 0)
			if date.After(stamp) && !date.After(time.Now()) {
				stamp = date
			}
			message = bot.privateMessage(incoming)
		}
		next := botCursor{Sequence: *update.ID + 1, Session: "update:" + stamp.UTC().Format(time.RFC3339Nano)}
		if err := bot.receiver.Accept(ctx, "telegram", bot.id, next, message); err != nil {
			return cursor, upstreamError("BOT_STORAGE", "Cannot commit the Telegram update")
		}
		cursor = next
	}
	return cursor, nil
}

type telegramIncomingMessage struct {
	ID   int64 `json:"message_id"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From struct {
		ID    int64 `json:"id"`
		IsBot *bool `json:"is_bot"`
	} `json:"from"`
	Date    int64           `json:"date"`
	Text    *string         `json:"text"`
	Caption *string         `json:"caption"`
	Photo   json.RawMessage `json:"photo"`
	File    json.RawMessage `json:"document"`
}

func (bot *telegramBot) privateMessage(incoming telegramIncomingMessage) *botMessage {
	if incoming.ID <= 0 || incoming.Date <= 0 ||
		incoming.Chat.Type != "private" || incoming.From.ID <= 0 || incoming.From.IsBot == nil ||
		*incoming.From.IsBot || incoming.Chat.ID != incoming.From.ID {
		return nil
	}
	message := &botMessage{ID: strconv.FormatInt(incoming.ID, 10), Peer: strconv.FormatInt(incoming.Chat.ID, 10),
		At: time.Unix(incoming.Date, 0), Kind: "attachment"}
	if incoming.Text == nil || incoming.Caption != nil || len(incoming.Photo) > 0 || len(incoming.File) > 0 {
		return message
	}
	text := *incoming.Text
	first := strings.Fields(text)
	if strings.HasPrefix(text, "/start") && len(first) > 0 && (first[0] == "/start" || strings.HasPrefix(first[0], "/start@")) {
		if bot.startKey == "" || subtle.ConstantTimeCompare([]byte(text), []byte("/start "+bot.startKey)) != 1 {
			return nil
		}
		message.Kind, message.Bind = "start", true
	} else {
		message.Kind, message.Text = "text", text
	}
	return message
}

func (bot *telegramBot) Send(ctx context.Context, peer, _ string, text string) (string, error) {
	delivery := &TelegramDelivery{Store: bot.store, config: ChannelConfig{BotToken: &bot.token, Destination: &peer},
		baseURL: bot.baseURL, http: bot.http}
	receipt, err := delivery.sendReviewText(ctx, Notification{}, text)
	if errorCode(err) == "SEND_OUTCOME_UNKNOWN" {
		return "", upstreamError("DELIVERY_UNKNOWN", "Telegram returned no confirmed message receipt")
	}
	return receipt, err
}

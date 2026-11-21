package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type botCursor struct {
	Session  string
	Sequence int64
}

type botMessage struct {
	Peer, ID, Text, Kind, Index, Digest string
	At                                  time.Time
	Bind                                bool
}

type botReceiver interface {
	Cursor(context.Context, string, string) (botCursor, error)
	Accept(context.Context, string, string, botCursor, *botMessage) error
	Status(kind, botID, state, detail, link string)
}

func botHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func botJSON(reader io.Reader, limit int64, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit || json.Unmarshal(data, target) != nil {
		return upstreamError("BOT_PROTOCOL", "Invalid bot platform response")
	}
	return nil
}

func waitBotRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func botRetryDelay(err error, delay time.Duration) time.Duration {
	var limited *notificationRetryError
	if errors.As(err, &limited) {
		return max(delay, limited.after)
	}
	return delay
}

type qqBot struct {
	appID, secret string
	receiver      botReceiver
	store         *Store
	baseURL       string
	http          *http.Client
	tokenMu       sync.Mutex
	token         string
	refreshAt     time.Time
}

func newQQBot(appID, secret string, store *Store) *qqBot {
	return &qqBot{appID: appID, secret: secret, receiver: botReceiverStore{store}, store: store,
		baseURL: "https://api.bot.qq.com", http: botHTTPClient(15 * time.Second)}
}

func (bot *qqBot) request(ctx context.Context, method, path, token string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(canonicalJSON(payload))
	}
	req, err := http.NewRequestWithContext(ctx, method, bot.baseURL+path, body)
	if err != nil {
		return nil, upstreamError("BOT_PROTOCOL", "Invalid QQ request")
	}
	if token != "" {
		req.Header.Set("Authorization", "QQBot "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return bot.http.Do(req)
}

func qqRateLimit(header http.Header) error {
	wait := time.Minute
	if seconds, err := strconv.ParseInt(header.Get("Retry-After"), 10, 32); err == nil && seconds > 0 {
		wait = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(header.Get("Retry-After")); err == nil && time.Until(at) > 0 {
		wait = time.Until(at)
	} else if reset, err := strconv.ParseInt(header.Get("X-Rate-Limit-Reset-After"), 10, 32); err == nil && reset > 0 {
		wait = time.Duration(reset) * time.Second
	}
	return &notificationRetryError{error: upstreamError("BOT_RATE_LIMITED", "QQ rate limit reached"), after: wait}
}

func (bot *qqBot) accessToken(ctx context.Context) (string, error) {
	bot.tokenMu.Lock()
	defer bot.tokenMu.Unlock()
	if bot.token != "" && time.Now().Before(bot.refreshAt) {
		return bot.token, nil
	}
	response, err := bot.request(ctx, http.MethodPost, "/app/getAppAccessToken", "",
		map[string]string{"appId": bot.appID, "clientSecret": bot.secret})
	if err != nil {
		return "", upstreamError("BOT_CONNECTION", "Cannot reach QQ")
	}
	defer response.Body.Close()
	var result struct {
		Code    int         `json:"code"`
		Token   string      `json:"access_token"`
		Expires json.Number `json:"expires_in"`
	}
	err = botJSON(response.Body, 64<<10, &result)
	switch {
	case response.StatusCode == 429 || result.Code == 100001:
		return "", qqRateLimit(response.Header)
	case response.StatusCode == 401 || response.StatusCode == 403 ||
		result.Code == 100007 || result.Code == 100016 || result.Code == 10004:
		return "", failure("BOT_CREDENTIALS", "Check the QQ AppID and AppSecret", 422, false)
	case response.StatusCode >= 500:
		return "", upstreamError("BOT_CONNECTION", "QQ token service unavailable")
	}
	expires, numberErr := result.Expires.Int64()
	if err != nil || response.StatusCode != 200 || result.Code != 0 || result.Token == "" || numberErr != nil || expires <= 0 || expires > 1<<31-1 {
		return "", upstreamError("BOT_PROTOCOL", "Invalid QQ token response")
	}
	bot.token = result.Token
	bot.refreshAt = time.Now().Add(time.Duration(expires-min(expires/2, 60)) * time.Second)
	return bot.token, nil
}

func (bot *qqBot) invalidate(token string) {
	bot.tokenMu.Lock()
	defer bot.tokenMu.Unlock()
	if bot.token == token {
		bot.token = ""
	}
}

func (bot *qqBot) Send(ctx context.Context, peer, messageID, text string) (string, error) {
	if peer == "" || peer == "." || peer == ".." || strings.ContainsAny(peer, "/\\?#") {
		return "", failure("CHANNEL_REJECTED", "Invalid QQ private chat address", 422, false)
	}
	bot.store.qqSendMu.Lock()
	defer bot.store.qqSendMu.Unlock()
	key := hashBytes(canonicalJSON([]string{"qq", bot.appID}))
	if remaining := bot.store.providerDelay(key); remaining > 0 {
		return "", &notificationRetryError{error: upstreamError("BOT_RATE_LIMITED", "QQ rate limit reached"), after: remaining}
	}
	receipt, err := bot.send(ctx, peer, messageID, text)
	var limited *notificationRetryError
	if errors.As(err, &limited) && errorCode(err) == "BOT_RATE_LIMITED" {
		bot.store.deferProvider(key, limited.after)
	}
	return receipt, err
}

func (bot *qqBot) send(ctx context.Context, peer, messageID, text string) (string, error) {
	token, err := bot.accessToken(ctx)
	if err != nil {
		if errorCode(err) == "BOT_CONNECTION" || errorCode(err) == "BOT_PROTOCOL" {
			return "", &notificationRetryError{error: err, after: 10 * time.Second}
		}
		return "", err
	}
	payload := map[string]any{"msg_type": 0, "content": text}
	if messageID != "" {
		payload["msg_id"], payload["msg_seq"] = messageID, 1
	}
	response, err := bot.request(ctx, http.MethodPost, "/v2/users/"+url.PathEscape(peer)+"/messages", token, payload)
	unknown := upstreamError("DELIVERY_UNKNOWN", "QQ returned no confirmed message receipt")
	if err != nil {
		return "", unknown
	}
	defer response.Body.Close()
	var result struct {
		Code int    `json:"code"`
		ID   string `json:"id"`
	}
	err = botJSON(response.Body, 128<<10, &result)
	switch {
	case response.StatusCode == 429 || result.Code == 40034100:
		return "", qqRateLimit(response.Header)
	case response.StatusCode == 401:
		bot.invalidate(token)
		return "", &notificationRetryError{error: upstreamError("BOT_CREDENTIALS", "QQ access token expired"), after: time.Second}
	case response.StatusCode >= 500 || response.StatusCode >= 300 && response.StatusCode < 400:
		return "", unknown
	case result.Code == 304103 || result.Code == 40034005 || result.Code == 40034128:
		return "", failure("QQ_REPLY_EXPIRED", "QQ reply window has closed", 422, false)
	case result.Code == 40054005:
		return "", unknown
	case result.Code != 0:
		return "", failure("QQ_REJECTED_"+strconv.Itoa(result.Code), "QQ rejected the message", 422, false)
	case response.StatusCode >= 400:
		return "", failure("CHANNEL_REJECTED", "QQ rejected the message", 422, false)
	case err != nil || result.ID == "":
		return "", unknown
	default:
		return result.ID, nil
	}
}

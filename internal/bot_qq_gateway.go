package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type qqEnvelope struct {
	Op       *int            `json:"op"`
	Sequence *int64          `json:"s"`
	Event    string          `json:"t"`
	Data     json.RawMessage `json:"d"`
}

func (bot *qqBot) gateway(ctx context.Context, token string) (string, error) {
	response, err := bot.request(ctx, http.MethodGet, "/gateway", token, nil)
	if err != nil {
		return "", upstreamError("BOT_CONNECTION", "Cannot reach QQ gateway")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 401, 403:
		bot.invalidate(token)
		return "", upstreamError("BOT_CREDENTIALS", "QQ gateway rejected the credentials")
	case 429:
		return "", qqRateLimit(response.Header)
	}
	var result struct {
		URL  string `json:"url"`
		Code int    `json:"code"`
	}
	err = botJSON(response.Body, 64<<10, &result)
	address, urlErr := url.Parse(result.URL)
	if err != nil || response.StatusCode != 200 || result.Code != 0 || urlErr != nil || address.Host == "" ||
		address.Scheme != "wss" || address.User != nil || address.Fragment != "" {
		return "", upstreamError("BOT_PROTOCOL", "Invalid QQ gateway address")
	}
	return address.String(), nil
}

func (bot *qqBot) Run(ctx context.Context) error {
	delay := time.Second
	lost := false
	for ctx.Err() == nil {
		bot.receiver.Status("qq", bot.appID, "connecting", "", "")
		ready, err := bot.connect(ctx, &lost)
		if ctx.Err() != nil {
			break
		}
		if ready {
			delay = time.Second
		}
		bot.receiver.Status("qq", bot.appID, "error", errorCode(err), "")
		if !waitBotRetry(ctx, botRetryDelay(err, delay)) {
			break
		}
		delay = min(2*delay, time.Minute)
	}
	bot.receiver.Status("qq", bot.appID, "disconnected", "", "")
	return ctx.Err()
}

func (bot *qqBot) connect(ctx context.Context, lost *bool) (bool, error) {
	cursor, err := bot.receiver.Cursor(ctx, "qq", bot.appID)
	if err != nil {
		return false, upstreamError("BOT_STORAGE", "Cannot read the QQ session")
	}
	token, err := bot.accessToken(ctx)
	if err != nil {
		return false, err
	}
	gateway, err := bot.gateway(ctx, token)
	if err != nil {
		return false, err
	}
	dialer := *websocket.DefaultDialer
	if transport, ok := bot.http.Transport.(*http.Transport); ok {
		dialer.TLSClientConfig = transport.TLSClientConfig
	}
	connection, response, err := dialer.DialContext(ctx, gateway, nil)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return false, upstreamError("BOT_CONNECTION", "Cannot open QQ WebSocket")
	}
	defer connection.Close()
	stopClose := context.AfterFunc(ctx, func() { connection.Close() })
	defer stopClose()
	connection.SetReadLimit(2 << 20)
	connection.SetReadDeadline(time.Now().Add(15 * time.Second))
	var hello qqEnvelope
	var settings struct {
		Interval int64 `json:"heartbeat_interval"`
	}
	if err := connection.ReadJSON(&hello); err != nil || hello.Op == nil || *hello.Op != 10 ||
		json.Unmarshal(hello.Data, &settings) != nil || settings.Interval <= 0 || settings.Interval > 600000 {
		return false, upstreamError("BOT_PROTOCOL", "Invalid QQ gateway greeting")
	}
	connection.SetReadDeadline(time.Time{})
	identify := map[string]any{"token": "QQBot " + token, "intents": 1 << 25, "shard": []int{0, 1}}
	op := 2
	if cursor.Session != "" {
		op = 6
		identify = map[string]any{"token": "QQBot " + token, "session_id": cursor.Session, "seq": cursor.Sequence}
	}
	if err := qqWrite(connection, op, identify); err != nil {
		return false, err
	}
	ready, err := bot.readEvents(ctx, connection, cursor, time.Duration(settings.Interval)*time.Millisecond, *lost)
	var closed *websocket.CloseError
	if errors.As(err, &closed) {
		switch {
		case closed.Code == 4006 || closed.Code == 4007 || closed.Code >= 4900 && closed.Code <= 4913:
			err = upstreamError("QQ_SESSION_LOST", "QQ could not resume the previous session")
		case closed.Code == 4004 || closed.Code == 4014 || closed.Code == 4914 || closed.Code == 4915:
			bot.invalidate(token)
			err = upstreamError("BOT_CREDENTIALS", "QQ rejected gateway access")
		}
	}
	if errorCode(err) == "QQ_SESSION_LOST" {
		if cursor.Session != "" || ready {
			*lost = true
		}
		if persistErr := bot.receiver.Accept(ctx, "qq", bot.appID, botCursor{}, nil); persistErr != nil {
			return ready, upstreamError("BOT_STORAGE", "Cannot reset the QQ session")
		}
	}
	if errorCode(err) == "SYNC_INTERNAL" {
		err = upstreamError("BOT_CONNECTION", "QQ gateway disconnected")
	}
	return ready, err
}

func qqWrite(connection *websocket.Conn, op int, data any) error {
	connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := connection.WriteJSON(map[string]any{"op": op, "d": data}); err != nil {
		return upstreamError("BOT_CONNECTION", "Cannot write to QQ gateway")
	}
	return nil
}

func (bot *qqBot) readEvents(ctx context.Context, connection *websocket.Conn, cursor botCursor, interval time.Duration, lost bool) (bool, error) {
	type reading struct {
		event qqEnvelope
		err   error
	}
	done := make(chan struct{})
	defer close(done)
	reads := make(chan reading)
	go func() {
		for {
			var event qqEnvelope
			err := connection.ReadJSON(&event)
			select {
			case reads <- reading{event, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	login := time.NewTimer(30 * time.Second)
	defer login.Stop()
	ready, awaitingAck := false, false
	heartbeat := func() error {
		var seq any
		if cursor.Session != "" {
			seq = cursor.Sequence
		}
		awaitingAck = true
		return qqWrite(connection, 1, seq)
	}
	for {
		select {
		case <-ctx.Done():
			return ready, ctx.Err()
		case <-login.C:
			return ready, upstreamError("BOT_CONNECTION", "QQ gateway login timed out")
		case <-ticker.C:
			if awaitingAck {
				return ready, upstreamError("BOT_CONNECTION", "QQ gateway heartbeat timed out")
			}
			if err := heartbeat(); err != nil {
				return ready, err
			}
		case result := <-reads:
			if result.err != nil {
				return ready, result.err
			}
			event := result.event
			if event.Op == nil {
				return ready, upstreamError("BOT_PROTOCOL", "Missing QQ gateway opcode")
			}
			switch *event.Op {
			case 0:
				if event.Sequence == nil || *event.Sequence < 0 {
					return ready, upstreamError("BOT_PROTOCOL", "Missing QQ gateway sequence")
				}
				if event.Event == "READY" {
					var session struct {
						ID string `json:"session_id"`
					}
					if json.Unmarshal(event.Data, &session) != nil || session.ID == "" {
						return ready, upstreamError("BOT_PROTOCOL", "Missing QQ gateway session")
					}
					cursor.Session = session.ID
				} else if cursor.Session == "" {
					return ready, upstreamError("BOT_PROTOCOL", "Unexpected QQ gateway dispatch before login")
				} else if *event.Sequence <= cursor.Sequence {
					continue
				}
				cursor.Sequence = *event.Sequence
				var message *botMessage
				if event.Event == "C2C_MESSAGE_CREATE" {
					message = qqPrivateMessage(event.Data)
				}
				if err := bot.receiver.Accept(ctx, "qq", bot.appID, cursor, message); err != nil {
					return ready, upstreamError("BOT_STORAGE", "Cannot commit the QQ event")
				}
				if event.Event == "READY" || event.Event == "RESUMED" {
					ready = true
					login.Stop()
					detail := ""
					if lost {
						detail = "QQ_SESSION_LOST"
					}
					bot.receiver.Status("qq", bot.appID, "connected", detail, "")
				}
			case 1:
				if err := heartbeat(); err != nil {
					return ready, err
				}
			case 7:
				return ready, upstreamError("BOT_CONNECTION", "QQ requested a reconnect")
			case 9:
				return ready, upstreamError("QQ_SESSION_LOST", "QQ could not resume the previous session")
			case 11:
				awaitingAck = false
			}
		}
	}
}

type qqMessageContent struct {
	Content     *string `json:"content"`
	Type        *int    `json:"message_type"`
	Attachments []struct {
		Name string `json:"filename"`
		Type string `json:"content_type"`
		Size int64  `json:"size"`
	} `json:"attachments"`
	Ark      map[string]any     `json:"ark_data"`
	Elements []qqMessageContent `json:"msg_elements"`
}

func qqPrivateMessage(data json.RawMessage) *botMessage {
	var sender struct {
		ID     string `json:"id"`
		Author struct {
			OpenID string `json:"user_openid"`
			Bot    *bool  `json:"bot"`
		} `json:"author"`
		Timestamp time.Time `json:"timestamp"`
	}
	if json.Unmarshal(data, &sender) != nil || sender.ID == "" || sender.Timestamp.IsZero() ||
		sender.Author.OpenID == "" || sender.Author.Bot == nil || *sender.Author.Bot {
		return nil
	}
	message := &botMessage{ID: sender.ID, Peer: sender.Author.OpenID, At: sender.Timestamp, Bind: true, Kind: "unsupported"}
	var body struct {
		qqMessageContent
		Scene struct {
			Ext []string `json:"ext"`
		} `json:"message_scene"`
	}
	if json.Unmarshal(data, &body) != nil {
		return message
	}
	reference := ""
	for _, field := range body.Scene.Ext {
		name, value, _ := strings.Cut(field, "=")
		switch name {
		case "msg_idx":
			message.Index = value
		case "ref_msg_idx":
			reference = value
		}
	}
	if (body.Type == nil || *body.Type == 0) && reference == "" && len(body.Elements) == 0 && body.Ark == nil {
		if len(body.Attachments) > 0 {
			message.Kind = "attachment"
		} else if body.Content != nil {
			message.Kind, message.Text = "text", *body.Content
		}
	}
	if message.Kind == "text" {
		message.Digest = hashBytes(canonicalJSON([]string{message.Kind, message.Text}))
	} else {
		message.Digest = hashBytes(canonicalJSON([]any{message.Kind, body.qqMessageContent, reference}))
	}
	return message
}

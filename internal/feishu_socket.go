package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type feishuSocketConfig struct {
	id, secret string
	generation uint64
}

func (s *Store) socketConfig() feishuSocketConfig {
	channel := s.Settings.Channel
	if !channel.botEnabled("feishu-app") || channel.feishuMode() != "websocket" {
		return feishuSocketConfig{}
	}
	return feishuSocketConfig{configured(channel.AppID), configured(channel.AppSecret), s.feishuGeneration}
}

func (a *App) connectFeishu(ctx context.Context, config feishuSocketConfig, retryDelay time.Duration, options ...larkws.ClientOption) {
	handler := dispatcher.NewEventDispatcher("", "").OnCustomizedEvent("im.message.receive_v1", func(ctx context.Context, req *larkevent.EventReq) error {
		unlock := a.Store.lockRuntime()
		defer unlock()
		if ctx.Err() != nil || config != a.Store.socketConfig() || a.Feishu == nil {
			return nil
		}
		var event feishuEvent
		if len(req.Body) > 1<<20 || json.Unmarshal(req.Body, &event) != nil {
			return failure("INVALID_ARGUMENT", "Invalid Feishu event", 422, false)
		}
		_, err := a.Feishu.receiveEvent(event)
		return err
	})
	status := func(state, detail string) {
		unlock := a.Store.lockRuntime()
		defer unlock()
		if ctx.Err() == nil && config == a.Store.socketConfig() {
			a.Store.setBotStatus("feishu-app", config.id, state, detail, "")
		}
	}
	reportError := func(err error) {
		code := "FEISHU_CONNECTION_ERROR"
		var clientErr *larkws.ClientError
		if errors.As(err, &clientErr) {
			code = "FEISHU_REJECTED_" + strconv.Itoa(clientErr.Code)
		}
		status("error", code)
	}
	httpClient := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	connected := false
	ready := func() {
		connected = true
		status("connected", "")
	}
	options = append(options, larkws.WithEventHandler(handler),
		larkws.WithLogger(larkcore.NewDefaultLogger(larkcore.LogLevelError+1)), larkws.WithHttpClient(httpClient),
		larkws.WithOnError(reportError), larkws.WithOnReconnecting(func() { status("connecting", "") }),
		larkws.WithOnReconnected(ready), larkws.WithOnReady(ready))
	var client *larkws.Client
	for ctx.Err() == nil {
		if client == nil {
			client = larkws.NewClient(config.id, config.secret, options...)
		}
		status("connecting", "")
		err := client.Start(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			reportError(err)
		}
		if connected {
			client, connected = nil, false
		}
		if runtimePause(ctx, retryDelay) != nil {
			return
		}
	}
}

package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChannelCandidateIsReadOnlyAndScoped(t *testing.T) {
	api, fixture := clientFixture(t)
	cookie, identity := settingsSession(t, api)
	var hits atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(remote.Close)
	origin := fixture.settings.Web.PublicOrigin
	path := "/api/app/v1/channels/check"
	call := func(body string) *httptest.ResponseRecorder {
		return clientCall(api, "POST", path, body, cookie, origin, identity.CSRFToken)
	}
	body := fmt.Sprintf(`{"version":1,"target":"feishu-webhook","secrets":{"webhook_url":{"action":"replace","value":%q}}}`, remote.URL)
	assertSettingsError(t, clientCall(api, "POST", path, body, cookie, origin, ""), 403, "CSRF_REJECTED")
	assertSettingsError(t, call(`{"version":1,"target":"feishu-webhook","settings":{"channel":{"qq_app_id":"123"}}}`), 422, "INVALID_ARGUMENT")
	assertSettingsError(t, call(`{"version":1,"target":"qq","secrets":{"bot_token":{"action":"clear"}}}`), 422, "INVALID_ARGUMENT")
	assertSettingsError(t, call(`{"version":2,"target":"feishu-webhook"}`), 409, "SETTINGS_CONFLICT")
	assertSettingsError(t, call(`{"version":1,"target":"feishu-webhook","secrets":{"webhook_url":{"action":"replace","value":"http://u@localhost/path"}}}`), 422, "INVALID_ARGUMENT")
	response := call(body)
	if response.Code != 200 || hits.Load() != 0 || api.app.Store.productVersion != 1 ||
		api.app.Store.Settings.Channel.WebhookURL != nil {
		t.Fatalf("webhook check modified runtime or reached destination: %d %s", response.Code, response.Body.String())
	}
}

func TestChannelCandidateProbesReleaseSettingsLock(t *testing.T) {
	targets := []struct {
		name, fields, secrets, url, request, reply, failure string
	}{
		{"qq", `{"qq_app_id":"123"}`, `{"qq_app_secret":{"action":"replace","value":"candidate-secret-qq"}}`,
			"https://api.bot.qq.com/app/getAppAccessToken", `{"appId":"123","clientSecret":"candidate-secret-qq"}`,
			`{"access_token":"candidate-token","expires_in":7200}`, "BOT_CONNECTION"},
		{"telegram", `{}`, `{"bot_token":{"action":"replace","value":"123:abcdefghijklmnopqrst"}}`,
			"https://api.telegram.org/bot123:abcdefghijklmnopqrst/getMe", `{}`,
			`{"ok":true,"result":{"id":123,"is_bot":true,"username":"candidate_bot"}}`, "BOT_CONNECTION"},
		{"feishu-app", `{"app_id":"cli_candidate"}`, `{"app_secret":{"action":"replace","value":"candidate-secret-feishu"}}`,
			"https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal", `{"app_id":"cli_candidate","app_secret":"candidate-secret-feishu"}`,
			`{"code":0,"tenant_access_token":"candidate-token","expire":7200}`, "CHANNEL_AUTH"},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			api, fixture := clientFixture(t)
			cookie, identity := settingsSession(t, api)
			origin := fixture.settings.Web.PublicOrigin
			api.app.Store.Settings.Channel.WebhookURL = new("https://webhook.example.invalid/hook")
			before := api.app.Store.Settings.Channel
			ctx, cancel := context.WithCancel(context.Background())
			var workers sync.WaitGroup
			t.Cleanup(func() {
				cancel()
				workers.Wait()
			})
			started, resume := make(chan struct{}), make(chan struct{})
			var hits atomic.Int32
			api.channelCheckTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				var compact bytes.Buffer
				if err != nil || json.Compact(&compact, body) != nil || req.URL.String() != target.url ||
					req.Method != http.MethodPost || compact.String() != target.request {
					t.Errorf("unexpected channel check request: %s %s %s (%v)", req.Method, req.URL, body, err)
				}
				switch hits.Add(1) {
				case 1:
					close(started)
					select {
					case <-resume:
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(target.reply))}, nil
				case 2:
					return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				default:
					<-req.Context().Done()
					return nil, req.Context().Err()
				}
			})
			check := func(ctx context.Context, version int) *httptest.ResponseRecorder {
				body := fmt.Sprintf(`{"version":%d,"target":%q,"settings":{"channel":%s},"secrets":%s}`,
					version, target.name, target.fields, target.secrets)
				request := httptest.NewRequestWithContext(ctx, "POST", origin+"/api/app/v1/channels/check", strings.NewReader(body))
				request.AddCookie(cookie)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Origin", origin)
				request.Header.Set("X-CSRF-Token", identity.CSRFToken)
				response := httptest.NewRecorder()
				api.ServeHTTP(response, request)
				return response
			}
			patch := func(body string) *httptest.ResponseRecorder {
				return clientCall(api, "PATCH", clientSettingsPath, body, cookie, origin, identity.CSRFToken)
			}
			probed := make(chan *httptest.ResponseRecorder, 1)
			workers.Go(func() { probed <- check(ctx, 1) })
			select {
			case <-started:
			case response := <-probed:
				t.Fatalf("probe failed before reaching the remote: %d %s", response.Code, response.Body.String())
			case <-time.After(5 * time.Second):
				t.Fatal("probe never reached the remote")
			}
			saved := make(chan *httptest.ResponseRecorder, 1)
			workers.Go(func() { saved <- patch(`{"version":1,"settings":{"timezone":"Asia/Tokyo"}}`) })
			select {
			case response := <-saved:
				if response.Code != 200 {
					t.Fatalf("concurrent settings save: %d %s", response.Code, response.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("channel probe blocked a settings save")
			}
			close(resume)
			response := <-probed
			if response.Code != 200 || !strings.Contains(response.Body.String(), `"version":1`) {
				t.Fatalf("unexpected probe result: %d %s", response.Code, response.Body.String())
			}
			assertSettingsError(t, patch(`{"version":1,"settings":{"timezone":"UTC"}}`), 409, "SETTINGS_CONFLICT")
			assertSettingsError(t, check(ctx, 2), 503, target.failure)
			deadline, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			assertSettingsError(t, check(deadline, 2), 503, "CHANNEL_CHECK_TIMEOUT")
			if hits.Load() != 3 || api.app.Store.productVersion != 2 || !reflect.DeepEqual(before, api.app.Store.Settings.Channel) {
				t.Fatal("channel checks made extra requests or changed the active channel or version")
			}
		})
	}
}

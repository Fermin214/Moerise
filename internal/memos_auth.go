package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const memosAuthPath = "/memos.api.v1.AuthService/"

type memosCredentials struct {
	access         string
	accessExpires  time.Time
	refresh        string
	refreshExpires time.Time
}

type memosAuthClient struct {
	client *http.Client
	base   string
}

func newMemosAuthClient(config MemosConfig) *memosAuthClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &memosAuthClient{
		base: strings.TrimRight(config.BaseURL, "/"),
		client: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(config.TimeoutSeconds * float64(time.Second)),
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func sessionUnauthorized() error {
	return failure("SESSION_UNAUTHORIZED", "Please sign in again", http.StatusUnauthorized, false)
}

func accountNotBound() error {
	return failure("ACCOUNT_NOT_BOUND", "This account is not bound to this instance", http.StatusForbidden, false)
}

func sessionUnavailable() error {
	return upstreamError("SESSION_UNAVAILABLE", "The session service is unavailable")
}

func (m *memosAuthClient) request(ctx context.Context, path string, body any, credentials memosCredentials) ([]byte, *http.Response, error) {
	method := http.MethodGet
	var input []byte
	if body != nil {
		method = http.MethodPost
		input, _ = json.Marshal(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, bytes.NewReader(input))
	if err != nil {
		return nil, nil, upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos authentication request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
	}
	if credentials.access != "" {
		req.Header.Set("Authorization", "Bearer "+credentials.access)
	}
	if credentials.refresh != "" {
		req.AddCookie(&http.Cookie{Name: "memos_refresh", Value: credentials.refresh})
	}
	response, err := m.client.Do(req)
	if err != nil {
		return nil, nil, upstreamError("UPSTREAM_UNAVAILABLE", "Memos is unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return nil, response, upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos authentication response")
	}
	if path == memosAuthPath+"SignIn" && response.StatusCode >= 400 && response.StatusCode < 500 {
		var rejected struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &rejected) == nil {
			if rejected.Code == "invalid_argument" &&
				(rejected.Message == "unmatched username and password" || rejected.Message == "invalid credentials") {
				return nil, response, sessionUnauthorized()
			}
			if (rejected.Code == "permission_denied" && rejected.Message == "password signin is not allowed") ||
				rejected.Code == "failed_precondition" {
				return nil, response, failure("PASSWORD_LOGIN_UNSUPPORTED", "Memos requires an unsupported sign-in method", http.StatusNotImplemented, false)
			}
		}
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return nil, response, sessionUnauthorized()
	case response.StatusCode == http.StatusTooManyRequests:
		return nil, response, failure("LOGIN_RATE_LIMITED", "Please retry later", http.StatusTooManyRequests, true)
	case response.StatusCode >= 300:
		return nil, response, upstreamError("UPSTREAM_UNAVAILABLE", "Memos authentication failed")
	}
	return data, response, nil
}

func (m *memosAuthClient) token(ctx context.Context, method string, body any, previous memosCredentials) (memosCredentials, error) {
	data, response, err := m.request(ctx, memosAuthPath+method, body, previous)
	if err != nil {
		return memosCredentials{}, err
	}
	var token struct {
		AccessToken          string    `json:"accessToken"`
		AccessTokenExpiresAt time.Time `json:"accessTokenExpiresAt"`
		ExpiresAt            time.Time `json:"expiresAt"`
	}
	now := time.Now()
	if err := json.Unmarshal(data, &token); err != nil {
		return memosCredentials{}, upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos access credential")
	}
	expiresAt := token.ExpiresAt
	if method == "SignIn" {
		expiresAt = token.AccessTokenExpiresAt
	}
	if token.AccessToken == "" || strings.ContainsAny(token.AccessToken, "\r\n") || !expiresAt.After(now) {
		return memosCredentials{}, upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos access credential")
	}
	var refresh *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == "memos_refresh" {
			if refresh != nil {
				return memosCredentials{}, upstreamError("UPSTREAM_PROTOCOL", "Ambiguous Memos refresh credential")
			}
			refresh = cookie
		}
	}
	if refresh == nil || refresh.Value == "" || refresh.Valid() != nil || refresh.MaxAge < 0 {
		return memosCredentials{}, upstreamError("UPSTREAM_PROTOCOL", "Missing Memos refresh credential")
	}
	expires := refresh.Expires
	if refresh.MaxAge > 0 {
		expires = now.Add(time.Duration(refresh.MaxAge) * time.Second)
	}
	if !expires.After(now) {
		return memosCredentials{}, upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos refresh expiry")
	}
	return memosCredentials{token.AccessToken, expiresAt, refresh.Value, expires}, nil
}

func (m *memosAuthClient) signIn(ctx context.Context, username, password string) (memosCredentials, error) {
	return m.token(ctx, "SignIn", map[string]any{
		"passwordCredentials": map[string]string{"username": username, "password": password},
	}, memosCredentials{})
}

func (m *memosAuthClient) refresh(ctx context.Context, credentials memosCredentials) (memosCredentials, error) {
	// 续期只携带对应会话的 Cookie，避免混入后台 PAT。
	return m.token(ctx, "RefreshToken", struct{}{}, memosCredentials{refresh: credentials.refresh})
}

func (m *memosAuthClient) signOut(ctx context.Context, credentials memosCredentials) error {
	if credentials.refresh == "" {
		return nil
	}
	if credentials.access == "" || !credentials.accessExpires.After(time.Now()) {
		refreshed, err := m.refresh(ctx, credentials)
		if err != nil {
			return err
		}
		credentials = refreshed
	}
	_, _, err := m.request(ctx, memosAuthPath+"SignOut", struct{}{}, credentials)
	return err
}

func (m *memosAuthClient) identity(ctx context.Context, credentials memosCredentials) (SessionUser, error) {
	data, _, err := m.request(ctx, "/api/v1/auth/me", nil, memosCredentials{access: credentials.access})
	if err != nil {
		return SessionUser{}, err
	}
	var identity struct {
		User struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			State       string `json:"state"`
		} `json:"user"`
	}
	if err := json.Unmarshal(data, &identity); err != nil || !userPattern.MatchString(identity.User.Name) {
		return SessionUser{}, upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos user identity")
	}
	if identity.User.State != "" && identity.User.State != "NORMAL" {
		return SessionUser{}, sessionUnauthorized()
	}
	return SessionUser{Name: identity.User.Name, DisplayName: identity.User.DisplayName}, nil
}

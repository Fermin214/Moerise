package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestMemosAuthRealProtocol(t *testing.T) {
	address := os.Getenv("MOERISE_TEST_AUTH_MEMOS_URL")
	if address == "" {
		t.Skip("设置 MOERISE_TEST_AUTH_MEMOS_URL 可连接独立空白 Memos 容器验证官方认证协议")
	}
	ctx := context.Background()
	auth := newMemosAuthClient(MemosConfig{BaseURL: address, TimeoutSeconds: 5})
	defer auth.client.CloseIdleConnections()
	username, password := "session-owner", "session-password-"+time.Now().Format("150405.000000000")
	data, _, err := auth.request(ctx, "/api/v1/users",
		map[string]string{"username": username, "password": password, "role": "ADMIN"}, memosCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	var user struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &user); err != nil || user.Name == "" {
		t.Fatal("Memos 未返回创建账户的资源名")
	}
	if _, err := auth.signIn(ctx, username, password+"wrong"); errorCode(err) != "SESSION_UNAUTHORIZED" {
		t.Fatalf("real password rejection: %v", err)
	}
	credentials, err := auth.signIn(ctx, username, password)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = auth.signOut(ctx, credentials) }()
	identity, err := auth.identity(ctx, credentials)
	if err != nil || identity.Name != user.Name {
		t.Fatalf("real user identity: %v", err)
	}
	rotated, err := auth.refresh(ctx, credentials)
	if err != nil || rotated.refresh == credentials.refresh {
		t.Fatalf("real rotation: %v", err)
	}
	old := credentials
	credentials = rotated
	if _, err := auth.refresh(ctx, old); errorCode(err) != "SESSION_UNAUTHORIZED" {
		t.Fatalf("old real refresh credential accepted: %v", err)
	}
	fixture := newSessionAuthFixture(t)
	settings := fixture.settings
	settings.Memos.BaseURL, settings.Memos.PublicURL, settings.Memos.User = address, address, user.Name
	patData, _, err := auth.request(ctx, "/api/v1/"+user.Name+"/personalAccessTokens",
		map[string]any{"description": "session integration", "expiresInDays": 0}, credentials)
	if err != nil {
		t.Fatal(err)
	}
	var pat struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(patData, &pat); err != nil || pat.Token == "" {
		t.Fatal("Memos 未返回 PAT")
	}
	settings.Memos.Token = pat.Token
	store := sessionTestStore(t, settings)
	service := sessionTestService(t, store)
	raw, public, err := service.Login(ctx, username, password)
	if err != nil || public.User.Name != user.Name {
		t.Fatalf("real service login: %v", err)
	}
	if bytes.Contains(mustSessionJSON(t, public), []byte(pat.Token)) {
		t.Fatal("公开身份包含后台 PAT")
	}
	service.Close()
	store.Close()
	store = sessionTestStore(t, settings)
	service = sessionTestService(t, store)
	if _, err := service.Authenticate(ctx, raw); err != nil {
		t.Fatalf("real service restart: %v", err)
	}
	if err := service.Logout(ctx, raw); err != nil {
		t.Fatalf("real service logout: %v", err)
	}
	if _, err := service.Authenticate(ctx, raw); errorCode(err) != "SESSION_UNAUTHORIZED" {
		t.Fatalf("real local revocation: %v", err)
	}
	if err := auth.signOut(ctx, credentials); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.refresh(ctx, credentials); errorCode(err) != "SESSION_UNAUTHORIZED" {
		t.Fatalf("real upstream signout: %v", err)
	}
}

func TestMemosAuthRejectsMalformedAndRedirectedCredentials(t *testing.T) {
	for _, test := range []struct {
		name, body, cookie string
		status             int
	}{
		{"missing access", `{}`, "memos_refresh=secret; Max-Age=3600", 200},
		{"expired access", fmt.Sprintf(`{"accessToken":"secret","accessTokenExpiresAt":%q}`, time.Unix(1, 0).UTC().Format(time.RFC3339)), "memos_refresh=secret; Max-Age=3600", 200},
		{"missing refresh", fmt.Sprintf(`{"accessToken":"secret","accessTokenExpiresAt":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339)), "", 200},
		{"wrong cookie", fmt.Sprintf(`{"accessToken":"secret","accessTokenExpiresAt":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339)), "other=secret; Max-Age=3600", 200},
		{"missing expiry", fmt.Sprintf(`{"accessToken":"secret","accessTokenExpiresAt":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339)), "memos_refresh=secret", 200},
		{"trailing JSON", `{} {}`, "memos_refresh=secret; Max-Age=3600", 200},
		{"redirect", `{}`, "", 307},
	} {
		t.Run(test.name, func(t *testing.T) {
			redirected := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.cookie != "" {
					w.Header().Add("Set-Cookie", test.cookie)
				}
				if test.status == 307 {
					w.Header().Set("Location", target.URL)
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			auth := newMemosAuthClient(MemosConfig{BaseURL: server.URL, TimeoutSeconds: 2})
			defer auth.client.CloseIdleConnections()
			if _, err := auth.signIn(context.Background(), "owner", "correct"); err == nil || redirected {
				t.Fatalf("invalid credentials accepted or redirected: %v", err)
			}
		})
	}
}

func TestMemosAuthLoginIdentityAndUpstreamRevocation(t *testing.T) {
	fixture := newSessionAuthFixture(t)
	store := sessionTestStore(t, fixture.settings)
	service := sessionTestService(t, store)
	if _, _, err := service.Login(context.Background(), "owner", "wrong"); errorCode(err) != "SESSION_UNAUTHORIZED" {
		t.Fatalf("wrong password: %v", err)
	}
	if _, _, err := service.Login(context.Background(), "other", "correct"); errorCode(err) != "ACCOUNT_NOT_BOUND" {
		t.Fatalf("different account: %v", err)
	}
	if sessionCount(t, store) != 0 {
		t.Fatal("failed login persisted a session")
	}
	raw, _ := sessionTestLogin(t, service)
	fixture.mu.Lock()
	clear(fixture.cookies)
	fixture.mu.Unlock()
	// 上游撤销刷新会话后，短期访问令牌仍可能有效。
	if _, err := service.Authenticate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	service.Close()
	service = sessionTestService(t, store)
	if _, err := service.Authenticate(context.Background(), raw); errorCode(err) != "SESSION_UNAUTHORIZED" {
		t.Fatalf("revoked upstream refresh accepted after restart: %v", err)
	}
	if sessionCount(t, store) != 0 {
		t.Fatal("upstream rejection left a browser session")
	}
}

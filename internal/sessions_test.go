package moerise

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type sessionAuthFixture struct {
	mu                    sync.Mutex
	server                *httptest.Server
	settings              Settings
	refreshes, signouts   int
	sequence              int
	cookies               map[string]string
	access                map[string]string
	userStatus, patStatus int
	refreshStatus         int
	signoutStatus         int
	userName              string
	refreshHook           func()
	signoutHook           func()
}

func newSessionAuthFixture(t *testing.T) *sessionAuthFixture {
	t.Helper()
	root := t.TempDir()
	settings := DefaultSettings()
	settings.Memos.InstanceID = "session-instance"
	settings.Memos.User, settings.Memos.Token = "users/owner", strings.Repeat("p", 40)
	settings.StateDir, settings.IndexDir = filepath.Join(root, "state"), filepath.Join(root, "index")
	settings.CursorSecret = strings.Repeat("c", 40)
	settings.AccessTokens = []AccessToken{{Name: "reader", Token: strings.Repeat("r", 40), Scopes: []string{"notes:read"}}}
	settings.Web = WebConfig{true, "https://notes.example.com", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{31}, 32))}
	fixture := &sessionAuthFixture{cookies: map[string]string{}, access: map[string]string{}, settings: settings}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serve))
	fixture.settings.Memos.BaseURL = fixture.server.URL
	fixture.settings.Memos.PublicURL = fixture.server.URL
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *sessionAuthFixture) serve(w http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	if request.URL.Path == memosAuthPath+"RefreshToken" && f.refreshHook != nil {
		hook := f.refreshHook
		f.mu.Unlock()
		hook()
		f.mu.Lock()
	}
	if request.URL.Path == memosAuthPath+"SignOut" && f.signoutHook != nil {
		hook := f.signoutHook
		f.mu.Unlock()
		hook()
		f.mu.Lock()
	}
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	switch request.URL.Path {
	case "/api/v1/instance/profile":
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "0.31.0"})
	case "/api/v1/auth/me":
		name := f.access[token]
		status := f.userStatus
		if token == f.settings.Memos.Token {
			name, status = f.settings.Memos.User, f.patStatus
		} else if f.userName != "" {
			name = f.userName
		}
		if name == "" {
			status = http.StatusUnauthorized
		}
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]string{
			"name": name, "displayName": "Owner", "state": "NORMAL",
		}})
	case memosAuthPath + "SignIn", memosAuthPath + "RefreshToken", memosAuthPath + "SignOut":
		if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" ||
			request.Header.Get("Connect-Protocol-Version") != "1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		name := ""
		if strings.HasSuffix(request.URL.Path, "SignIn") {
			var body struct {
				PasswordCredentials struct {
					Username string `json:"username"`
					Password string `json:"password"`
				} `json:"passwordCredentials"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body.PasswordCredentials.Password != "correct" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":"invalid_argument","message":"unmatched username and password"}`))
				return
			}
			name = "users/" + body.PasswordCredentials.Username
		} else {
			cookie, err := request.Cookie("memos_refresh")
			if err != nil || f.cookies[cookie.Value] == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if strings.HasSuffix(request.URL.Path, "SignOut") {
				f.signouts++
				if f.signoutStatus != 0 {
					w.WriteHeader(f.signoutStatus)
					return
				}
				if f.access[token] == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				delete(f.cookies, cookie.Value)
				_, _ = w.Write([]byte("{}"))
				return
			}
			f.refreshes++
			if f.refreshStatus != 0 {
				w.WriteHeader(f.refreshStatus)
				return
			}
			name = f.cookies[cookie.Value]
			delete(f.cookies, cookie.Value)
		}
		f.sequence++
		refresh, access := fmt.Sprintf("refresh-secret-%d", f.sequence), fmt.Sprintf("access-secret-%d", f.sequence)
		f.cookies[refresh], f.access[access] = name, name
		http.SetCookie(w, &http.Cookie{Name: "memos_refresh", Value: refresh, Path: "/",
			Expires: time.Now().Add(24 * time.Hour), HttpOnly: true})
		expiryField := "expiresAt"
		if strings.HasSuffix(request.URL.Path, "SignIn") {
			expiryField = "accessTokenExpiresAt"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": access, expiryField: time.Now().Add(15 * time.Minute)})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func sessionTestStore(t *testing.T, settings Settings) *Store {
	t.Helper()
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sessionTestService(t *testing.T, store *Store) *SessionService {
	t.Helper()
	source := NewMemosClient(store.Settings.Memos)
	t.Cleanup(source.Close)
	service, err := NewSessionService(store, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

func sessionTestLogin(t *testing.T, service *SessionService) (string, *SessionIdentity) {
	t.Helper()
	raw, identity, err := service.Login(context.Background(), "owner", "correct")
	if err != nil {
		t.Fatal(err)
	}
	return raw, identity
}

func sessionCount(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.state.QueryRow("SELECT COUNT(*) FROM client_sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSessionRestartSerialRefreshAndPrivateIdentity(t *testing.T) {
	fixture := newSessionAuthFixture(t)
	store := sessionTestStore(t, fixture.settings)
	service := sessionTestService(t, store)
	raw, identity := sessionTestLogin(t, service)
	hash, _ := sessionHash(raw)
	var ciphertext []byte
	var storedHash string
	if err := store.state.QueryRow("SELECT session_hash, refresh_ciphertext FROM client_sessions").Scan(&storedHash, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if storedHash != hash || storedHash == raw || bytes.Contains(ciphertext, []byte("refresh-secret")) {
		t.Fatal("database contains raw browser or refresh credentials")
	}
	public, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	_ = json.Unmarshal(public, &fields)
	var userFields map[string]any
	_ = json.Unmarshal(mustSessionJSON(t, identity.User), &userFields)
	if len(fields) != 5 || len(userFields) != 2 || fields["csrf_token"] == "" ||
		bytes.Contains(public, []byte(hash)) || bytes.Contains(public, []byte("secret")) || bytes.Contains(public, []byte(raw)) {
		t.Fatalf("unexpected public identity: %s", public)
	}
	service.Close()
	store.Close()
	store = sessionTestStore(t, fixture.settings)
	service = sessionTestService(t, store)
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			got, err := service.Authenticate(context.Background(), raw)
			if err != nil || got.User.Name != fixture.settings.Memos.User || got.CSRFToken != identity.CSRFToken {
				t.Errorf("restored identity: %v", err)
			}
		})
	}
	group.Wait()
	fixture.mu.Lock()
	refreshes := fixture.refreshes
	fixture.mu.Unlock()
	if refreshes != 2 {
		t.Fatalf("login and concurrent restart issued %d refreshes, want 2", refreshes)
	}
	if err := service.CheckActive(context.Background(), identity); err != nil {
		t.Fatal("a successful refresh invalidated an active identity")
	}
	service.Close()
	service.Close()
	if _, err := service.Authenticate(context.Background(), raw); errorCode(err) != "SESSION_UNAVAILABLE" {
		t.Fatalf("closed service accepted authentication: %v", err)
	}
}

func mustSessionJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSessionCredentialFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  func(*sessionAuthFixture)
		status  int
		retains bool
	}{
		{"user rejected", func(f *sessionAuthFixture) { f.userStatus = 401 }, 401, false},
		{"user forbidden", func(f *sessionAuthFixture) { f.userStatus = 403 }, 401, false},
		{"different account", func(f *sessionAuthFixture) { f.userName = "users/other" }, 403, false},
		{"PAT rejected", func(f *sessionAuthFixture) { f.patStatus = 401 }, 503, true},
		{"user service unavailable", func(f *sessionAuthFixture) { f.userStatus = 503 }, 503, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionAuthFixture(t)
			store := sessionTestStore(t, fixture.settings)
			service := sessionTestService(t, store)
			raw, _ := sessionTestLogin(t, service)
			fixture.mu.Lock()
			test.change(fixture)
			fixture.mu.Unlock()
			_, err := service.Authenticate(context.Background(), raw)
			serviceErr, ok := err.(*ServiceError)
			if !ok || serviceErr.Status != test.status {
				t.Fatalf("failure: %v, want HTTP %d", err, test.status)
			}
			if (sessionCount(t, store) == 1) != test.retains {
				t.Fatal("wrong retention after authentication failure")
			}
			fixture.mu.Lock()
			fixture.userStatus, fixture.patStatus, fixture.userName = 0, 0, ""
			fixture.mu.Unlock()
			_, err = service.Authenticate(context.Background(), raw)
			if (err == nil) != test.retains {
				t.Fatalf("unexpected recovery: %v", err)
			}
		})
	}
	t.Run("network failure retains refresh", func(t *testing.T) {
		fixture := newSessionAuthFixture(t)
		store := sessionTestStore(t, fixture.settings)
		service := sessionTestService(t, store)
		raw, _ := sessionTestLogin(t, service)
		service.Close()
		service = sessionTestService(t, store)
		original := service.auth.client.Transport
		service.auth.client.Transport = sessionFailingTransport{}
		_, err := service.Authenticate(context.Background(), raw)
		if serviceErr, ok := err.(*ServiceError); !ok || serviceErr.Status != 503 || sessionCount(t, store) != 1 {
			t.Fatalf("network failure: %v", err)
		}
		service.auth.client.Transport = original
		if _, err := service.Authenticate(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	})
}

type sessionFailingTransport struct{}

func (sessionFailingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.DeadlineExceeded
}

func TestSessionRefreshLogoutAndRevision(t *testing.T) {
	for _, externalDelete := range []bool{false, true} {
		t.Run(fmt.Sprintf("external-delete-%t", externalDelete), func(t *testing.T) {
			fixture := newSessionAuthFixture(t)
			store := sessionTestStore(t, fixture.settings)
			service := sessionTestService(t, store)
			raw, oldIdentity := sessionTestLogin(t, service)
			service.Close()
			service = sessionTestService(t, store)
			started, resume := make(chan struct{}), make(chan struct{})
			fixture.mu.Lock()
			fixture.refreshHook = func() { close(started); <-resume }
			fixture.mu.Unlock()
			result := make(chan error, 1)
			go func() { _, err := service.Authenticate(context.Background(), raw); result <- err }()
			<-started
			var loggedOut chan error
			if externalDelete {
				if _, err := store.state.Exec("DELETE FROM client_sessions"); err != nil {
					t.Fatal(err)
				}
			} else {
				loggedOut = make(chan error, 1)
				go func() { loggedOut <- service.Logout(context.Background(), raw) }()
			}
			close(resume)
			err := <-result
			if externalDelete && errorCode(err) != "SESSION_UNAUTHORIZED" {
				t.Fatalf("refresh recreated a revoked session: %v", err)
			}
			if !externalDelete {
				if err != nil {
					t.Fatal(err)
				}
				if err := <-loggedOut; err != nil {
					t.Fatal(err)
				}
			}
			if sessionCount(t, store) != 0 {
				t.Fatal("logout left or resurrected the session")
			}
			if err := service.CheckActive(context.Background(), oldIdentity); errorCode(err) != "SESSION_UNAUTHORIZED" {
				t.Fatalf("write check accepted revoked identity: %v", err)
			}
			if _, err := service.Authenticate(context.Background(), raw); errorCode(err) != "SESSION_UNAUTHORIZED" {
				t.Fatalf("revoked cookie accepted: %v", err)
			}
			if err := service.Logout(context.Background(), raw); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("local deletion precedes failed upstream cleanup", func(t *testing.T) {
		fixture := newSessionAuthFixture(t)
		store := sessionTestStore(t, fixture.settings)
		service := sessionTestService(t, store)
		raw, identity := sessionTestLogin(t, service)
		fixture.mu.Lock()
		fixture.signoutStatus = 503
		fixture.signoutHook = func() {
			if sessionCount(t, store) != 0 {
				t.Error("upstream cleanup preceded local revocation")
			}
			if err := service.CheckActive(context.Background(), identity); errorCode(err) != "SESSION_UNAUTHORIZED" {
				t.Errorf("write check during signout: %v", err)
			}
		}
		fixture.mu.Unlock()
		if err := service.Logout(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSessionRotationPersistenceFailure(t *testing.T) {
	fixture := newSessionAuthFixture(t)
	store := sessionTestStore(t, fixture.settings)
	service := sessionTestService(t, store)
	raw, _ := sessionTestLogin(t, service)
	service.Close()
	service = sessionTestService(t, store)
	if _, err := store.state.Exec(`CREATE TRIGGER fail_session_rotation BEFORE UPDATE ON client_sessions
		BEGIN SELECT RAISE(ABORT, 'rotation failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(context.Background(), raw); errorCode(err) != "SESSION_UNAVAILABLE" {
		t.Fatalf("rotation persistence failure: %v", err)
	}
	hash, _ := sessionHash(raw)
	if sessionCount(t, store) != 0 || service.cached(hash).token != "" {
		t.Fatal("uncommitted access credential was published")
	}
}

func TestSessionLocalIdentityAndOfflineLogout(t *testing.T) {
	fixture := newSessionAuthFixture(t)
	store := sessionTestStore(t, fixture.settings)
	service := sessionTestService(t, store)
	raw, original := sessionTestLogin(t, service)
	service.Close()
	service = sessionTestService(t, store)
	service.auth.client.Transport = sessionFailingTransport{}
	local, err := service.LocalIdentity(context.Background(), raw)
	if err != nil || local.CSRFToken != original.CSRFToken || local.User.Name != original.User.Name {
		t.Fatalf("offline local identity: %v", err)
	}
	if err := service.Logout(context.Background(), raw); err != nil {
		t.Fatalf("offline logout after restart: %v", err)
	}
	if _, err := service.LocalIdentity(context.Background(), raw); errorCode(err) != "SESSION_UNAUTHORIZED" {
		t.Fatalf("local identity after logout: %v", err)
	}
	if _, err := service.LocalIdentity(context.Background(), "invalid"); errorCode(err) != "SESSION_UNAUTHORIZED" {
		t.Fatalf("malformed local identity: %v", err)
	}
}

func TestSessionLocalRevocationFailure(t *testing.T) {
	fixture := newSessionAuthFixture(t)
	store := sessionTestStore(t, fixture.settings)
	service := sessionTestService(t, store)
	raw, _ := sessionTestLogin(t, service)
	if _, err := store.state.Exec(`CREATE TRIGGER fail_session_delete BEFORE DELETE ON client_sessions
		BEGIN SELECT RAISE(ABORT, 'delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(context.Background(), raw); errorCode(err) != "SESSION_UNAVAILABLE" {
		t.Fatalf("failed local revocation claimed success: %v", err)
	}
	fixture.mu.Lock()
	signouts := fixture.signouts
	fixture.mu.Unlock()
	if signouts != 0 || sessionCount(t, store) != 1 {
		t.Fatal("failed local revocation attempted upstream signout")
	}
}

func TestSessionEncryptionBindingAndRecovery(t *testing.T) {
	for _, mode := range []string{"wrong key", "ciphertext swap", "source changed", "restore and rebuild"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSessionAuthFixture(t)
			store := sessionTestStore(t, fixture.settings)
			service := sessionTestService(t, store)
			raw, _ := sessionTestLogin(t, service)
			switch mode {
			case "wrong key":
				service.Close()
				store.Settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
				service = sessionTestService(t, store)
			case "ciphertext swap":
				other, _ := sessionTestLogin(t, service)
				hash, _ := sessionHash(raw)
				otherHash, _ := sessionHash(other)
				if _, err := store.state.Exec(`UPDATE client_sessions SET refresh_ciphertext =
					(SELECT refresh_ciphertext FROM client_sessions WHERE session_hash = ?) WHERE session_hash = ?`,
					otherHash, hash); err != nil {
					t.Fatal(err)
				}
			case "source changed":
				service.Close()
				store.Settings.Memos.BaseURL = "http://127.0.0.1:1"
				service = sessionTestService(t, store)
			case "restore and rebuild":
				backup := filepath.Join(t.TempDir(), "backup")
				if _, err := Backup(fixture.settings, backup); err != nil {
					t.Fatal(err)
				}
				service.Close()
				store.Close()
				if err := RebuildIndex(fixture.settings); err != nil {
					t.Fatal(err)
				}
				store = sessionTestStore(t, fixture.settings)
				service = sessionTestService(t, store)
				if _, err := service.Authenticate(context.Background(), raw); err != nil {
					t.Fatalf("index rebuild lost the session: %v", err)
				}
				service.Close()
				store.Close()
				if err := Restore(fixture.settings, backup, filepath.Join(t.TempDir(), "safety")); err != nil {
					t.Fatal(err)
				}
				store = sessionTestStore(t, fixture.settings)
				service = sessionTestService(t, store)
			}
			if _, err := service.Authenticate(context.Background(), raw); errorCode(err) != "SESSION_UNAUTHORIZED" {
				t.Fatalf("%s accepted a stale session: %v", mode, err)
			}
		})
	}
}

func TestWebOriginCanonicalization(t *testing.T) {
	for _, item := range []struct {
		input, want string
	}{
		{"https://NOTES.example.com:443/", "https://notes.example.com"},
		{"http://LOCALHOST:80", "http://localhost"},
		{"http://127.0.0.1:015300", "http://127.0.0.1:15300"},
		{"http://[0:0:0:0:0:0:0:1]:80", "http://[::1]"},
		{"https://[::ffff:127.0.0.1]:443", "https://[::ffff:7f00:1]"},
		{"https://xn--fsqu00a.example:8443", "https://xn--fsqu00a.example:8443"},
	} {
		got, err := canonicalWebOrigin(item.input)
		if err != nil || got != item.want {
			t.Errorf("%q: origin=%q want=%q err=%v", item.input, got, item.want, err)
		}
	}
	for _, input := range []string{
		"https://notes.example.com/path", "https://notes.example.com:",
		"http://notes.example.com", "https://notes.example.com:65536",
		"https://例子.example", "https://127.1", "https://0x7f000001",
		"https://[::1%25zone]", "https://notes.example.com/#",
	} {
		if got, err := canonicalWebOrigin(input); err == nil {
			t.Errorf("unmatchable origin accepted: %q -> %q", input, got)
		}
	}
}

func TestSessionBoundRequestsFollowDurableRevocation(t *testing.T) {
	fixture := newSessionAuthFixture(t)
	store := sessionTestStore(t, fixture.settings)
	service := sessionTestService(t, store)
	raw, identity := sessionTestLogin(t, service)
	ctx, release, err := service.BindRequest(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := store.state.Exec(`CREATE TRIGGER fail_session_delete BEFORE DELETE ON client_sessions
		BEGIN SELECT RAISE(ABORT, 'delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(t.Context(), raw); errorCode(err) != "SESSION_UNAVAILABLE" || ctx.Err() != nil {
		t.Fatalf("failed revocation canceled a valid session: %v %v", err, ctx.Err())
	}
	if _, err := store.state.Exec("DROP TRIGGER fail_session_delete"); err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(t.Context(), raw); err != nil || ctx.Err() == nil {
		t.Fatalf("durable revocation did not cancel its requests: %v %v", err, ctx.Err())
	}
	if _, release, err := service.BindRequest(t.Context(), identity); errorCode(err) != "SESSION_UNAUTHORIZED" {
		if release != nil {
			release()
		}
		t.Fatalf("request registered after logout: %v", err)
	}
	release()
	service.mu.Lock()
	count := len(service.active)
	service.mu.Unlock()
	if count != 0 {
		t.Fatal("request release retained a session registration")
	}
}

package moerise

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SessionUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

type SessionIdentity struct {
	InstanceID string      `json:"instance_id"`
	User       SessionUser `json:"user"`
	Timezone   string      `json:"timezone"`
	ExpiresAt  time.Time   `json:"expires_at"`
	CSRFToken  string      `json:"csrf_token"`

	sessionHash string
	binding     string
	revision    int64
}

type sessionRow struct {
	hash, instance, user, binding, csrf string
	ciphertext                          []byte
	expires                             int64
	revision                            int64
}

type sessionAccess struct {
	token   string
	expires time.Time
}

type sessionLock struct {
	mu   sync.Mutex
	refs int
}

type sessionRequest struct {
	cancel context.CancelFunc
}

type SessionService struct {
	store    *Store
	source   Source
	settings Settings
	auth     *memosAuthClient
	aead     cipher.AEAD
	binding  string

	mu        sync.Mutex
	locks     map[string]*sessionLock
	access    map[string]sessionAccess
	active    map[string]map[*sessionRequest]struct{}
	nextClean time.Time
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	pending   sync.WaitGroup
	closeOnce sync.Once
}

func stateEncryptionKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, errors.New("web.state_encryption_key must be a base64 encoded 32-byte random key")
	}
	return key, nil
}

func (config *WebConfig) validate() error {
	if !config.Enabled {
		return nil
	}
	origin, err := canonicalWebOrigin(config.PublicOrigin)
	if err != nil {
		return err
	}
	config.PublicOrigin = origin
	_, err = stateEncryptionKey(config.StateEncryptionKey)
	return err
}

func canonicalWebOrigin(value string) (string, error) {
	bad := errors.New("invalid configuration at web.public_origin")
	if !validOrigin(value) || strings.ContainsAny(value, `\#`) {
		return "", bad
	}
	origin, _ := url.Parse(value)
	if (origin.Path != "" && origin.Path != "/") || strings.HasSuffix(origin.Host, ":") {
		return "", bad
	}
	scheme, host := strings.ToLower(origin.Scheme), strings.ToLower(origin.Hostname())
	address, err := netip.ParseAddr(strings.TrimSuffix(host, "."))
	if err == nil {
		if address.Zone() != "" || (address.Is6() && !strings.HasPrefix(origin.Host, "[")) {
			return "", bad
		}
		host = address.String()
		if address.Is4In6() {
			// 浏览器将映射 IPv4 的 IPv6 地址也序列化为十六进制。
			ip := address.As16()
			host = "::ffff:" + strconv.FormatUint(uint64(ip[12])<<8|uint64(ip[13]), 16) +
				":" + strconv.FormatUint(uint64(ip[14])<<8|uint64(ip[15]), 16)
		}
	} else {
		labels := strings.Split(strings.TrimSuffix(host, "."), ".")
		if len(host) > 254 {
			return "", bad
		}
		for _, label := range labels {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return "", bad
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z') && !(ch >= '0' && ch <= '9') && ch != '-' {
					return "", bad
				}
			}
		}
		last := labels[len(labels)-1]
		digits := "0123456789"
		if strings.HasPrefix(last, "0x") {
			last, digits = last[2:], "0123456789abcdef"
		}
		// 非标准数字主机会被浏览器当成 IPv4 重写或拒绝。
		if strings.Trim(last, digits) == "" {
			return "", bad
		}
	}
	if scheme == "http" && strings.TrimSuffix(host, ".") != "localhost" && !address.Unmap().IsLoopback() {
		return "", errors.New("web.public_origin requires HTTPS or a loopback HTTP address")
	}
	port := origin.Port()
	if port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return "", bad
		}
		port = strconv.FormatUint(number, 10)
		if (scheme == "http" && number == 80) || (scheme == "https" && number == 443) {
			port = ""
		}
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

func sessionBinding(config MemosConfig) string {
	address, _ := url.Parse(strings.TrimRight(config.BaseURL, "/"))
	address.Scheme = strings.ToLower(address.Scheme)
	host := strings.ToLower(address.Hostname())
	port := address.Port()
	if (address.Scheme == "http" && port == "80") || (address.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		address.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		address.Host = "[" + host + "]"
	} else {
		address.Host = host
	}
	return hashBytes([]byte(config.InstanceID + "\x00" + config.User + "\x00" + address.String()))
}

func NewSessionService(store *Store, source Source) (*SessionService, error) {
	if store == nil || source == nil {
		return nil, errors.New("session service requires a store and source")
	}
	settings := store.Settings
	if !settings.Web.Enabled {
		return nil, errors.New("web sessions are disabled")
	}
	if err := settings.Web.validate(); err != nil {
		return nil, err
	}
	key, err := stateEncryptionKey(settings.Web.StateEncryptionKey)
	if err != nil {
		return nil, err
	}
	key, err = hkdf.Key(sha256.New, key, nil, "moerise/state/client_sessions/refresh/v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &SessionService{
		store: store, source: source, settings: settings, auth: newMemosAuthClient(settings.Memos),
		aead: aead, binding: sessionBinding(settings.Memos), locks: map[string]*sessionLock{},
		access: map[string]sessionAccess{}, active: map[string]map[*sessionRequest]struct{}{}, ctx: ctx, cancel: cancel,
	}, nil
}

func (s *SessionService) operation(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, sessionUnavailable()
	}
	s.pending.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() { stop(); cancel(); s.pending.Done() }, nil
}

func (s *SessionService) lock(ctx context.Context, hash string) (func(), error) {
	s.mu.Lock()
	lock := s.locks[hash]
	if lock == nil {
		lock = &sessionLock{}
		s.locks[hash] = lock
	}
	lock.refs++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.locks, hash)
		}
		s.mu.Unlock()
	}
	for {
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		if lock.mu.TryLock() {
			return func() { lock.mu.Unlock(); release() }, nil
		}
		if err := runtimePause(ctx, 10*time.Millisecond); err != nil {
			release()
			return nil, err
		}
	}
}

func (s *SessionService) cached(hash string) sessionAccess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.access[hash]
}

func (s *SessionService) cache(hash string, credentials memosCredentials) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if credentials.access == "" {
		delete(s.access, hash)
		return
	}
	if len(s.access) >= 1024 {
		for key := range s.access {
			delete(s.access, key)
			break
		}
	}
	s.access[hash] = sessionAccess{credentials.access, credentials.accessExpires}
}

func rawSessionID() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", sessionUnavailable()
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func sessionHash(rawID string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(rawID)
	if err != nil || len(data) != 32 || base64.RawURLEncoding.EncodeToString(data) != rawID {
		return "", sessionUnauthorized()
	}
	return hashBytes([]byte(rawID)), nil
}

func (s *SessionService) encrypt(row sessionRow, credentials memosCredentials) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, sessionUnavailable()
	}
	// 版本、会话摘要和来源绑定共同防止密文被替换到其他会话。
	aad := []byte("client_sessions/refresh/v1\x00" + row.hash + "\x00" + row.binding)
	return s.aead.Seal(append([]byte{1}, nonce...), nonce, []byte(credentials.refresh), aad), nil
}

func (s *SessionService) decrypt(row sessionRow) (memosCredentials, error) {
	if len(row.ciphertext) < 1+s.aead.NonceSize()+s.aead.Overhead() || row.ciphertext[0] != 1 {
		return memosCredentials{}, sessionUnauthorized()
	}
	aad := []byte("client_sessions/refresh/v1\x00" + row.hash + "\x00" + row.binding)
	refresh, err := s.aead.Open(nil, row.ciphertext[1:1+s.aead.NonceSize()],
		row.ciphertext[1+s.aead.NonceSize():], aad)
	if err != nil || len(refresh) == 0 {
		return memosCredentials{}, sessionUnauthorized()
	}
	access := s.cached(row.hash)
	return memosCredentials{access.token, access.expires, string(refresh), time.Unix(row.expires, 0).UTC()}, nil
}

func (s *SessionService) load(ctx context.Context, hash string) (sessionRow, error) {
	row := sessionRow{hash: hash}
	err := s.store.state.QueryRowContext(ctx, `SELECT instance_id, user_name, source_binding_hash,
		refresh_ciphertext, refresh_expires_at, csrf_token, revision FROM client_sessions WHERE session_hash = ?`,
		hash).Scan(&row.instance, &row.user, &row.binding, &row.ciphertext, &row.expires, &row.csrf, &row.revision)
	if errors.Is(err, sql.ErrNoRows) {
		return row, sessionUnauthorized()
	}
	if err != nil {
		return row, sessionUnavailable()
	}
	if row.instance != s.settings.Memos.InstanceID || row.user != s.settings.Memos.User ||
		row.binding != s.binding || row.expires <= time.Now().Unix() {
		if err := s.revoke(hash); err != nil {
			return row, err
		}
		return row, sessionUnauthorized()
	}
	return row, nil
}

func (s *SessionService) revoke(hash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.store.state.ExecContext(ctx, "DELETE FROM client_sessions WHERE session_hash = ?", hash); err != nil {
		return sessionUnavailable()
	}
	s.mu.Lock()
	delete(s.access, hash)
	requests := s.active[hash]
	delete(s.active, hash)
	s.mu.Unlock()
	for request := range requests {
		request.cancel()
	}
	return nil
}

func (s *SessionService) clean(ctx context.Context) {
	s.mu.Lock()
	if time.Now().Before(s.nextClean) {
		s.mu.Unlock()
		return
	}
	s.nextClean = time.Now().Add(time.Minute)
	removed := 0
	for hash, access := range s.access {
		if !access.expires.After(time.Now()) {
			delete(s.access, hash)
			removed++
			if removed == 64 {
				break
			}
		}
	}
	s.mu.Unlock()
	rows, err := s.store.state.QueryContext(ctx,
		"SELECT session_hash FROM client_sessions WHERE refresh_expires_at <= ? LIMIT 64", time.Now().Unix())
	if err != nil {
		return
	}
	var expired []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			break
		}
		expired = append(expired, hash)
	}
	rows.Close()
	for _, hash := range expired {
		unlock, err := s.lock(ctx, hash)
		if err != nil {
			return
		}
		_, _ = s.load(ctx, hash)
		unlock()
	}
}

func (s *SessionService) public(row sessionRow, user SessionUser) *SessionIdentity {
	return &SessionIdentity{
		InstanceID: row.instance, User: user, Timezone: s.settings.Timezone,
		ExpiresAt: time.Unix(row.expires, 0).UTC(), CSRFToken: row.csrf,
		sessionHash: row.hash, binding: row.binding, revision: row.revision,
	}
}

func (s *SessionService) checkSource(ctx context.Context) error {
	if _, err := s.source.Identity(ctx); err != nil {
		return upstreamError("UPSTREAM_AUTH_UNAVAILABLE", "The configured Memos source is unavailable")
	}
	return nil
}

func (s *SessionService) cleanupCredentials(credentials memosCredentials) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.settings.Memos.TimeoutSeconds*float64(time.Second)))
	defer cancel()
	_ = s.auth.signOut(ctx, credentials)
}

func (s *SessionService) Login(ctx context.Context, username, password string) (string, *SessionIdentity, error) {
	ctx, done, err := s.operation(ctx)
	if err != nil {
		return "", nil, err
	}
	defer done()
	s.clean(ctx)
	credentials, err := s.auth.signIn(ctx, username, password)
	if err != nil {
		return "", nil, err
	}
	persisted := false
	defer func() {
		if !persisted {
			s.cleanupCredentials(credentials)
		}
	}()
	user, err := s.auth.identity(ctx, credentials)
	if err != nil {
		return "", nil, err
	}
	if user.Name != s.settings.Memos.User {
		return "", nil, accountNotBound()
	}
	if err := s.checkSource(ctx); err != nil {
		return "", nil, err
	}
	refreshed, err := s.auth.refresh(ctx, credentials)
	if err != nil {
		return "", nil, err
	}
	credentials = refreshed
	user, err = s.auth.identity(ctx, credentials)
	if err != nil {
		return "", nil, err
	}
	if user.Name != s.settings.Memos.User {
		return "", nil, accountNotBound()
	}
	rawID, err := rawSessionID()
	if err != nil {
		return "", nil, err
	}
	csrf, err := rawSessionID()
	if err != nil {
		return "", nil, err
	}
	hash, _ := sessionHash(rawID)
	row := sessionRow{hash: hash, instance: s.settings.Memos.InstanceID, user: user.Name,
		binding: s.binding, expires: credentials.refreshExpires.Unix(), csrf: csrf, revision: 1}
	ciphertext, err := s.encrypt(row, credentials)
	if err != nil {
		return "", nil, err
	}
	_, err = s.store.state.ExecContext(ctx, `INSERT INTO client_sessions
		(session_hash, instance_id, user_name, source_binding_hash, refresh_ciphertext,
		refresh_expires_at, csrf_token, created_at, updated_at, revision) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		hash, row.instance, row.user, row.binding, ciphertext, row.expires, csrf, nowText(), nowText())
	if err != nil {
		return "", nil, sessionUnavailable()
	}
	s.cache(hash, credentials)
	persisted = true
	return rawID, s.public(row, user), nil
}

func (s *SessionService) rejected(hash string, err error) error {
	if errorCode(err) == "SESSION_UNAUTHORIZED" || errorCode(err) == "ACCOUNT_NOT_BOUND" {
		if revokeErr := s.revoke(hash); revokeErr != nil {
			return revokeErr
		}
	}
	return err
}

func (s *SessionService) Authenticate(ctx context.Context, rawID string) (*SessionIdentity, error) {
	ctx, done, err := s.operation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	hash, err := sessionHash(rawID)
	if err != nil {
		return nil, err
	}
	s.clean(ctx)
	unlock, err := s.lock(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer unlock()
	row, err := s.load(ctx, hash)
	if err != nil {
		return nil, err
	}
	credentials, err := s.decrypt(row)
	if err != nil {
		return nil, s.rejected(hash, err)
	}
	if !credentials.accessExpires.After(time.Now().Add(30 * time.Second)) {
		refreshed, err := s.auth.refresh(ctx, credentials)
		if err != nil {
			// 成功响应若格式异常，旧刷新凭据可能已被消耗，需重新登录。
			if errorCode(err) == "UPSTREAM_PROTOCOL" {
				if revokeErr := s.revoke(hash); revokeErr != nil {
					return nil, revokeErr
				}
			}
			return nil, s.rejected(hash, err)
		}
		// 浏览器 Cookie 只在登录时设置，续期不能延长该次登录的有效期。
		expires := min(row.expires, refreshed.refreshExpires.Unix())
		ciphertext, err := s.encrypt(row, refreshed)
		if err == nil {
			var result sql.Result
			result, err = s.store.state.ExecContext(ctx, `UPDATE client_sessions SET refresh_ciphertext = ?,
				refresh_expires_at = ?, updated_at = ?, revision = revision + 1
				WHERE session_hash = ? AND revision = ? AND source_binding_hash = ?`,
				ciphertext, expires, nowText(), hash, row.revision, s.binding)
			if err == nil {
				var count int64
				count, err = result.RowsAffected()
				if err == nil && count != 1 {
					err = sessionUnauthorized()
				}
			}
		}
		if err != nil {
			revokeErr := s.revoke(hash)
			s.cleanupCredentials(refreshed)
			if revokeErr != nil {
				return nil, revokeErr
			}
			if errorCode(err) == "SESSION_UNAUTHORIZED" {
				return nil, err
			}
			return nil, sessionUnavailable()
		}
		row.expires, row.revision = expires, row.revision+1
		credentials = refreshed
		s.cache(hash, credentials)
	}
	user, err := s.auth.identity(ctx, credentials)
	if err != nil {
		return nil, s.rejected(hash, err)
	}
	if user.Name != row.user {
		return nil, s.rejected(hash, accountNotBound())
	}
	if err := s.checkSource(ctx); err != nil {
		return nil, err
	}
	identity := s.public(row, user)
	if err := s.checkActive(ctx, identity); err != nil {
		return nil, err
	}
	return identity, nil
}

func (s *SessionService) checkActive(ctx context.Context, identity *SessionIdentity) error {
	if identity == nil || identity.sessionHash == "" || identity.binding != s.binding {
		return sessionUnauthorized()
	}
	row, err := s.load(ctx, identity.sessionHash)
	if err != nil {
		return err
	}
	if row.revision < identity.revision || row.csrf != identity.CSRFToken {
		return sessionUnauthorized()
	}
	return nil
}

func (s *SessionService) CheckActive(ctx context.Context, identity *SessionIdentity) error {
	ctx, done, err := s.operation(ctx)
	if err != nil {
		return err
	}
	defer done()
	if identity == nil {
		return sessionUnauthorized()
	}
	unlock, err := s.lock(ctx, identity.sessionHash)
	if err != nil {
		return err
	}
	defer unlock()
	return s.checkActive(ctx, identity)
}

func (s *SessionService) BindRequest(ctx context.Context, identity *SessionIdentity) (context.Context, func(), error) {
	ctx, done, err := s.operation(ctx)
	if err != nil {
		return nil, nil, err
	}
	if identity == nil {
		done()
		return nil, nil, sessionUnauthorized()
	}
	hash := identity.sessionHash
	unlock, err := s.lock(ctx, hash)
	if err != nil {
		done()
		return nil, nil, err
	}
	defer unlock()
	if err := s.checkActive(ctx, identity); err != nil {
		done()
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	request := &sessionRequest{cancel}
	s.mu.Lock()
	if s.active[hash] == nil {
		s.active[hash] = map[*sessionRequest]struct{}{}
	}
	s.active[hash][request] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.active[hash], request)
			if len(s.active[hash]) == 0 {
				delete(s.active, hash)
			}
			s.mu.Unlock()
			cancel()
			done()
		})
	}, nil
}

func (s *SessionService) LocalIdentity(ctx context.Context, rawID string) (*SessionIdentity, error) {
	ctx, done, err := s.operation(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	hash, err := sessionHash(rawID)
	if err != nil {
		return nil, err
	}
	unlock, err := s.lock(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer unlock()
	row, err := s.load(ctx, hash)
	if err != nil {
		return nil, err
	}
	return s.public(row, SessionUser{Name: row.user}), nil
}

func (s *SessionService) Logout(ctx context.Context, rawID string) error {
	ctx, done, err := s.operation(ctx)
	if err != nil {
		return err
	}
	defer done()
	hash, err := sessionHash(rawID)
	if err != nil {
		return nil
	}
	unlock, err := s.lock(ctx, hash)
	if err != nil {
		return err
	}
	row, err := s.load(ctx, hash)
	if err != nil {
		unlock()
		if errorCode(err) == "SESSION_UNAUTHORIZED" {
			return nil
		}
		return err
	}
	credentials, _ := s.decrypt(row)
	if err := s.revoke(hash); err != nil {
		unlock()
		return err
	}
	unlock()
	// 上游清理即使需要刷新，也不能重建已经删除的本地会话。
	_ = s.auth.signOut(ctx, credentials)
	return nil
}

func (s *SessionService) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		s.pending.Wait()
		s.auth.client.CloseIdleConnections()
		s.mu.Lock()
		clear(s.access)
		s.mu.Unlock()
	})
}

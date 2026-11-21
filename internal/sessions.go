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

	"golang.org/x/sync/singleflight"
)

type SessionUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

type SessionIdentity struct {
	InstanceID        string      `json:"instance_id"`
	User              SessionUser `json:"user"`
	Timezone          string      `json:"timezone"`
	ExpiresAt         time.Time   `json:"expires_at"`
	CSRFToken         string      `json:"csrf_token"`
	StargazingEnabled bool        `json:"stargazing_enabled"`

	sessionHash string
	binding     string
	revision    int64
	authEpoch   uint64
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

const defaultSessionCacheTTL = 5 * time.Minute

type cachedSession struct {
	identity *SessionIdentity
	expires  time.Time
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
	cacheTTL time.Duration

	mu           sync.Mutex
	locks        map[string]*sessionLock
	access       map[string]sessionAccess
	sessions     map[string]cachedSession
	refreshGroup singleflight.Group
	active       map[string]map[*sessionRequest]struct{}
	revokeGen    uint64
	evictEpoch   uint64
	nextClean    time.Time
	closed       bool
	ctx          context.Context
	cancel       context.CancelFunc
	pending      sync.WaitGroup
	closeOnce    sync.Once
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
		aead: aead, binding: sessionBinding(settings.Memos), cacheTTL: defaultSessionCacheTTL,
		locks: map[string]*sessionLock{}, access: map[string]sessionAccess{},
		sessions: map[string]cachedSession{}, active: map[string]map[*sessionRequest]struct{}{},
		evictEpoch: 1, ctx: ctx, cancel: cancel,
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

func cloneIdentity(identity *SessionIdentity) *SessionIdentity {
	if identity == nil {
		return nil
	}
	cloned := *identity
	return &cloned
}

func (s *SessionService) cachedIdentity(hash string) (*SessionIdentity, bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	entry, ok := s.sessions[hash]
	if !ok || !now.Before(entry.expires) || entry.identity == nil ||
		entry.identity.binding != s.binding || !entry.identity.ExpiresAt.After(now) ||
		(entry.identity.authEpoch != 0 && entry.identity.authEpoch != s.evictEpoch) {
		delete(s.sessions, hash)
		return nil, false
	}
	access, ok := s.access[hash]
	if !ok || access.token == "" || !access.expires.After(now.Add(30*time.Second)) {
		delete(s.sessions, hash)
		return nil, false
	}
	return cloneIdentity(entry.identity), true
}

func (s *SessionService) epochs() (uint64, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revokeGen, s.evictEpoch
}

func (s *SessionService) storeSessionCache(hash string, identity *SessionIdentity, credentials memosCredentials, checkEpoch bool, revGen, evEpoch uint64) {
	var until time.Time
	hasIdentity := identity != nil && credentials.access != ""
	if hasIdentity {
		now := time.Now()
		ttl := s.cacheTTL
		if ttl <= 0 {
			ttl = defaultSessionCacheTTL
		}
		until = now.Add(ttl)
		if accessLimit := credentials.accessExpires.Add(-30 * time.Second); accessLimit.Before(until) {
			until = accessLimit
		}
		if identity.ExpiresAt.Before(until) {
			until = identity.ExpiresAt
		}
		hasIdentity = until.After(now)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (checkEpoch && (s.revokeGen != revGen || s.evictEpoch != evEpoch)) {
		return
	}
	if credentials.access == "" {
		delete(s.access, hash)
	} else {
		if len(s.access) >= 1024 {
			for key := range s.access {
				delete(s.access, key)
				break
			}
		}
		s.access[hash] = sessionAccess{credentials.access, credentials.accessExpires}
	}
	if !hasIdentity {
		return
	}
	identity.authEpoch = s.evictEpoch
	if len(s.sessions) >= 1024 {
		for key := range s.sessions {
			delete(s.sessions, key)
			break
		}
	}
	s.sessions[hash] = cachedSession{cloneIdentity(identity), until}
}

func (s *SessionService) ClearCache() {
	s.mu.Lock()
	clear(s.sessions)
	clear(s.access)
	s.mu.Unlock()
}

func (s *SessionService) ExpireCache() {
	s.mu.Lock()
	clear(s.sessions)
	s.mu.Unlock()
}

func (s *SessionService) EvictAllAndCancel() {
	s.mu.Lock()
	s.evictEpoch++
	clear(s.sessions)
	clear(s.access)
	var requests []*sessionRequest
	for hash, active := range s.active {
		for req := range active {
			requests = append(requests, req)
		}
		delete(s.active, hash)
	}
	s.mu.Unlock()
	for _, req := range requests {
		req.cancel()
	}
}

func (s *SessionService) cached(hash string) sessionAccess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.access[hash]
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
	s.revokeGen++
	delete(s.access, hash)
	delete(s.sessions, hash)
	requests := s.active[hash]
	delete(s.active, hash)
	s.mu.Unlock()
	s.refreshGroup.Forget(hash)
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
		if code := errorCode(err); code == "UPSTREAM_AUTH" || code == "IDENTITY_MISMATCH" {
			s.EvictAllAndCancel()
			return err
		}
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
	identity := s.public(row, user)
	s.storeSessionCache(hash, identity, credentials, false, 0, 0)
	persisted = true
	return rawID, identity, nil
}

func (s *SessionService) rejected(hash string, err error) error {
	code := errorCode(err)
	if code == "SESSION_UNAUTHORIZED" || code == "ACCOUNT_NOT_BOUND" || code == "IDENTITY_MISMATCH" {
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
	if identity, ok := s.cachedIdentity(hash); ok {
		return identity, nil
	}
	ch := s.refreshGroup.DoChan(hash, func() (any, error) {
		if identity, ok := s.cachedIdentity(hash); ok {
			return identity, nil
		}
		timeout := max(30*time.Second, time.Duration(s.settings.Memos.TimeoutSeconds*float64(time.Second))*3)
		sfCtx, sfCancel := context.WithTimeout(s.ctx, timeout)
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) > timeout {
			sfCancel()
			sfCtx, sfCancel = context.WithDeadline(s.ctx, deadline)
		}
		defer sfCancel()
		unlock, err := s.lock(sfCtx, hash)
		if err != nil {
			return nil, err
		}
		defer unlock()
		if identity, ok := s.cachedIdentity(hash); ok {
			return identity, nil
		}
		revGen, evEpoch := s.epochs()
		row, err := s.load(sfCtx, hash)
		if err != nil {
			return nil, err
		}
		credentials, err := s.decrypt(row)
		if err != nil {
			return nil, s.rejected(hash, err)
		}
		if !credentials.accessExpires.After(time.Now().Add(30 * time.Second)) {
			refreshed, err := s.auth.refresh(sfCtx, credentials)
			if err != nil {
				if errorCode(err) == "UPSTREAM_PROTOCOL" {
					if revokeErr := s.revoke(hash); revokeErr != nil {
						return nil, revokeErr
					}
				}
				return nil, s.rejected(hash, err)
			}
			expires := min(row.expires, refreshed.refreshExpires.Unix())
			ciphertext, err := s.encrypt(row, refreshed)
			if err == nil {
				var result sql.Result
				result, err = s.store.state.ExecContext(sfCtx, `UPDATE client_sessions SET refresh_ciphertext = ?,
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
		}
		user, err := s.auth.identity(sfCtx, credentials)
		if err != nil {
			return nil, s.rejected(hash, err)
		}
		if user.Name != row.user {
			return nil, s.rejected(hash, accountNotBound())
		}
		if err := s.checkSource(sfCtx); err != nil {
			return nil, s.rejected(hash, err)
		}
		identity := s.public(row, user)
		identity.authEpoch = evEpoch
		s.storeSessionCache(hash, identity, credentials, true, revGen, evEpoch)
		return identity, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		return cloneIdentity(result.Val.(*SessionIdentity)), nil
	}
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
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			done()
			return nil, nil, sessionUnavailable()
		}
		if identity.authEpoch != 0 && identity.authEpoch != s.evictEpoch {
			s.mu.Unlock()
			done()
			return nil, nil, sessionUnauthorized()
		}
		revGen, evEpoch := s.revokeGen, s.evictEpoch
		s.mu.Unlock()
		if err := s.checkActive(ctx, identity); err != nil {
			done()
			return nil, nil, err
		}
		reqCtx, cancel := context.WithCancel(ctx)
		request := &sessionRequest{cancel}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			cancel()
			done()
			return nil, nil, sessionUnavailable()
		}
		if s.evictEpoch != evEpoch || (identity.authEpoch != 0 && identity.authEpoch != s.evictEpoch) {
			s.mu.Unlock()
			cancel()
			done()
			return nil, nil, sessionUnauthorized()
		}
		if s.revokeGen != revGen {
			s.mu.Unlock()
			cancel()
			continue
		}
		if s.active[hash] == nil {
			s.active[hash] = map[*sessionRequest]struct{}{}
		}
		s.active[hash][request] = struct{}{}
		s.mu.Unlock()
		var once sync.Once
		return reqCtx, func() {
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
		clear(s.sessions)
		s.mu.Unlock()
	})
}

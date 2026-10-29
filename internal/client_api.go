package moerise

import (
	"bytes"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type loginWindow struct {
	start time.Time
	count int
}

type ClientAPI struct {
	app    *App
	mu     sync.Mutex
	limits map[string]loginWindow
}

func NewClientAPI(app *App) *ClientAPI {
	return &ClientAPI{app: app, limits: map[string]loginWindow{}}
}

func NewHandler(app *App) (http.Handler, error) {
	machine, err := NewAPI(app)
	if err != nil {
		return nil, err
	}
	client := NewClientAPI(app)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/app/v1" || strings.HasPrefix(r.URL.Path, "/api/app/v1/") {
			client.ServeHTTP(w, r)
			return
		}
		machine.ServeHTTP(w, r)
	}), nil
}

func writeClientError(w http.ResponseWriter, err error, id string) {
	var service *ServiceError
	if !errors.As(err, &service) {
		service = &ServiceError{"INTERNAL", "Internal service error", 500, true}
	}
	writeJSON(w, service.Status, map[string]any{"error": map[string]any{
		"code": service.Code, "message": service.Message, "retryable": service.Retryable, "request_id": id,
	}})
}

func (a *ClientAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := newID()
	w.Header().Set("X-Request-ID", id)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	defer func() {
		if recovered := recover(); recovered != nil {
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			writeClientError(w, errors.New("client handler panic"), id)
		}
	}()
	result, status, err := a.route(w, r)
	if err != nil {
		writeClientError(w, err, id)
	} else if status == -1 {
		return
	} else if status == http.StatusNoContent {
		w.WriteHeader(status)
	} else {
		writeJSON(w, status, result)
	}
}

func (a *ClientAPI) cookieName() string {
	if strings.HasPrefix(a.app.Settings.Web.PublicOrigin, "https://") {
		return "__Host-moerise_session"
	}
	return "moerise_local_session"
}

func (a *ClientAPI) cookie(w http.ResponseWriter, rawID string, expires time.Time) {
	cookie := &http.Cookie{Name: a.cookieName(), Value: rawID, Path: "/", HttpOnly: true,
		Secure: strings.HasPrefix(a.app.Settings.Web.PublicOrigin, "https://"), SameSite: http.SameSiteLaxMode,
		Expires: expires}
	if rawID == "" {
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0)
	} else {
		cookie.MaxAge = max(1, int(time.Until(expires).Seconds()))
	}
	http.SetCookie(w, cookie)
}

func (a *ClientAPI) rawSession(r *http.Request) (string, error) {
	cookies := r.CookiesNamed(a.cookieName())
	if len(cookies) != 1 {
		return "", sessionUnauthorized()
	}
	return cookies[0].Value, nil
}

func (a *ClientAPI) origin(r *http.Request) error {
	if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != a.app.Settings.Web.PublicOrigin {
		return failure("ORIGIN_REJECTED", "Request origin is not allowed", 403, false)
	}
	return nil
}

func checkCSRF(r *http.Request, identity *SessionIdentity) error {
	value := r.Header.Get("X-CSRF-Token")
	if len(r.Header.Values("X-CSRF-Token")) != 1 || value == "" ||
		!hmac.Equal([]byte(value), []byte(identity.CSRFToken)) {
		return failure("CSRF_REJECTED", "Invalid CSRF token", 403, false)
	}
	return nil
}

func readClientJSON(w http.ResponseWriter, r *http.Request, size int64, fields []string, target any) error {
	bad := failure("INVALID_ARGUMENT", "Invalid request parameters", 422, false)
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return failure("UNSUPPORTED_MEDIA_TYPE", "Expected application/json", 415, false)
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, size))
	if err != nil || !utf8.Valid(data) {
		return bad
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return bad
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] || !slices.Contains(fields, key) {
			return bad
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return bad
		}
	}
	if strictJSON(bytes.NewReader(data), target) != nil {
		return bad
	}
	return nil
}

func (a *ClientAPI) allowLogin(r *http.Request, username string) bool {
	address, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		address = r.RemoteAddr
	}
	keys := []string{"ip:" + address, "account:" + hashBytes([]byte(strings.ToLower(strings.TrimSpace(username))))}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if len(a.limits) > 2048 {
		for key, entry := range a.limits {
			if now.Sub(entry.start) >= time.Minute {
				delete(a.limits, key)
			}
		}
		if len(a.limits) > 2048 {
			return false
		}
	}
	for _, key := range keys {
		entry := a.limits[key]
		if now.Sub(entry.start) < time.Minute && entry.count >= 10 {
			return false
		}
	}
	for _, key := range keys {
		entry := a.limits[key]
		if now.Sub(entry.start) >= time.Minute {
			entry = loginWindow{start: now}
		}
		entry.count++
		a.limits[key] = entry
	}
	return true
}

func (a *ClientAPI) route(w http.ResponseWriter, r *http.Request) (any, int, error) {
	notFound := failure("NOT_FOUND", "Not Found", 404, false)
	if !a.app.Settings.Web.Enabled || a.app.Sessions == nil {
		return nil, 0, notFound
	}
	if len(r.Header.Values("Authorization")) != 0 {
		return nil, 0, failure("FORBIDDEN", "Client requests require a browser session", 403, false)
	}
	if r.URL.RawQuery != "" && !(slices.Contains([]string{"/api/app/v1/notes", "/api/app/v1/tags", "/api/app/v1/calendar"}, r.URL.Path) && r.Method == http.MethodGet) &&
		!(r.URL.Path == "/api/app/v1/admin/notifications" && r.Method == http.MethodGet) {
		return nil, 0, failure("INVALID_ARGUMENT", "Unexpected query parameters", 422, false)
	}
	path, method := r.URL.Path, r.Method
	if method != http.MethodGet && method != http.MethodHead {
		if err := a.origin(r); err != nil {
			return nil, 0, err
		}
	}
	if path == "/api/app/v1/capabilities" {
		if method != http.MethodGet {
			return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
		}
		return map[string]any{"login_methods": []string{"password"}, "notes": "ready"}, 200, nil
	}
	if path == "/api/app/v1/session" && method == http.MethodPost {
		var body ClientLogin
		if err := readClientJSON(w, r, 8192, []string{"username", "password"}, &body); err != nil {
			return nil, 0, err
		}
		if body.Username == "" || len(body.Username) > 256 || body.Password == "" || len(body.Password) > 4096 {
			return nil, 0, failure("INVALID_ARGUMENT", "Username and password are required", 422, false)
		}
		if !a.allowLogin(r, body.Username) {
			w.Header().Set("Retry-After", "60")
			return nil, 0, failure("RATE_LIMITED", "Too many login attempts", 429, true)
		}
		rawID, identity, err := a.app.Sessions.Login(r.Context(), body.Username, body.Password)
		if err != nil {
			return nil, 0, err
		}
		a.cookie(w, rawID, identity.ExpiresAt)
		return identity, 200, nil
	}
	rawID, cookieErr := a.rawSession(r)
	if path == "/api/app/v1/session" && method == http.MethodDelete {
		if cookieErr != nil {
			a.cookie(w, "", time.Time{})
			return nil, 204, nil
		}
		identity, err := a.app.Sessions.LocalIdentity(r.Context(), rawID)
		if err != nil && errorCode(err) != "SESSION_UNAUTHORIZED" {
			return nil, 0, err
		}
		if identity != nil {
			if err := checkCSRF(r, identity); err != nil {
				return nil, 0, err
			}
			if err := a.app.Sessions.Logout(r.Context(), rawID); err != nil {
				return nil, 0, err
			}
		}
		a.cookie(w, "", time.Time{})
		return nil, 204, nil
	}
	if cookieErr != nil {
		return nil, 0, cookieErr
	}
	identity, err := a.app.Sessions.Authenticate(r.Context(), rawID)
	if err != nil {
		return nil, 0, err
	}
	ctx, release, err := a.app.Sessions.BindRequest(r.Context(), identity)
	if err != nil {
		return nil, 0, err
	}
	defer release()
	r = r.WithContext(ctx)
	if method != http.MethodGet && method != http.MethodHead {
		if err := checkCSRF(r, identity); err != nil {
			return nil, 0, err
		}
		if err := a.app.Sessions.CheckActive(r.Context(), identity); err != nil {
			return nil, 0, err
		}
	}
	switch path {
	case "/api/app/v1/session":
		if method == http.MethodGet {
			return identity, 200, nil
		}
	case "/api/app/v1/notes":
		if method == http.MethodGet {
			query, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil {
				return nil, 0, failure("INVALID_ARGUMENT", "Invalid list parameters", 422, false)
			}
			args, err := parseNoteList(query, true)
			if err != nil {
				return nil, 0, err
			}
			result, err := a.app.Notes.ListLive(r.Context(), args)
			for i := range result.Notes {
				result.Notes[i] = clientNote(result.Notes[i])
			}
			return result, 200, err
		}
		if method == http.MethodPost {
			var body ClientCreateNote
			if err := readClientJSON(w, r, 12*1024*1024, []string{"content", "attachments"}, &body); err != nil {
				return nil, 0, err
			}
			if err := a.app.Sessions.CheckActive(r.Context(), identity); err != nil {
				return nil, 0, err
			}
			note, status, err := a.app.Records.Create(r.Context(), r.Header.Get("Idempotency-Key"),
				CreateNote{Content: body.Content, Attachments: body.Attachments})
			return clientNote(note), status, err
		}
	case "/api/app/v1/tags", "/api/app/v1/calendar":
		if method != http.MethodGet {
			break
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return nil, 0, failure("INVALID_ARGUMENT", "Invalid aggregation parameters", 422, false)
		}
		for key, values := range query {
			if len(values) != 1 || (key != "state" && !(path == "/api/app/v1/calendar" && key == "month")) {
				return nil, 0, failure("INVALID_ARGUMENT", "Invalid aggregation parameters", 422, false)
			}
		}
		if path == "/api/app/v1/tags" {
			tags, err := a.app.Notes.Tags(r.Context(), query.Get("state"))
			return map[string]any{"tags": tags}, 200, err
		}
		days, err := a.app.Notes.Calendar(r.Context(), query.Get("state"), query.Get("month"))
		return map[string]any{"month": query.Get("month"), "days": days}, 200, err
	default:
		if strings.HasPrefix(path, "/api/app/v1/notes/") {
			return a.noteRoute(w, r)
		}
		if path == "/api/app/v1/admin/notifications" || strings.HasPrefix(path, "/api/app/v1/admin/notifications/") {
			return a.notifications(w, r, identity)
		}
		return nil, 0, notFound
	}
	return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
}

func (a *ClientAPI) notifications(w http.ResponseWriter, r *http.Request, identity *SessionIdentity) (any, int, error) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/app/v1/admin/notifications")
	if tail == "" && r.Method == http.MethodGet {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) > 1 || (len(query) == 1 && len(query["status"]) != 1) {
			return nil, 0, failure("INVALID_ARGUMENT", "Invalid notification parameters", 422, false)
		}
		items, err := a.app.Notifications.List(query.Get("status"))
		return map[string]any{"notifications": items}, 200, err
	}
	parts := strings.Split(strings.TrimPrefix(tail, "/"), "/")
	if len(parts) == 1 && parts[0] != "" && r.Method == http.MethodGet {
		result, err := a.app.Notifications.Detail(parts[0])
		return result, 200, err
	}
	if len(parts) == 2 && parts[1] == "resolve" && r.Method == http.MethodPost {
		var body struct {
			Decision string `json:"decision"`
			Receipt  string `json:"receipt"`
		}
		if err := readClientJSON(w, r, 65536, []string{"decision", "receipt"}, &body); err != nil {
			return nil, 0, err
		}
		if err := a.app.Sessions.CheckActive(r.Context(), identity); err != nil {
			return nil, 0, err
		}
		result, err := a.app.Notifications.Resolve(r.Context(), parts[0], body.Decision, body.Receipt, identity.User.Name)
		return result, 200, err
	}
	return nil, 0, failure("NOT_FOUND", "Not Found", 404, false)
}

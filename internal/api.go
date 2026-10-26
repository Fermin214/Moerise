package moerise

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

type API struct {
	Settings   Settings
	Store      *Store
	Source     Source
	Worker     *SyncService
	Records    *RecordService
	Reviews    *ReviewService
	MCP        http.Handler
	generation string
}

func NewAPI(settings Settings, store *Store, source Source, worker *SyncService) (*API, error) {
	generation, err := store.Generation()
	api := &API{Settings: settings, Store: store, Source: source, Worker: worker,
		Records: NewRecordService(store, source), Reviews: NewReviewService(store, source), generation: generation}
	api.MCP = api.newMCPHandler()
	return api, err
}

func (a *API) cursorContext() []string {
	return []string{a.Settings.Memos.InstanceID, a.Settings.Memos.User, a.generation}
}
func (a *API) encodeCursor(state string, after []string) string {
	raw, _ := json.Marshal([]any{1, a.cursorContext(), state, after})
	sign := hmac.New(sha256.New, []byte(a.Settings.CursorSecret))
	sign.Write(raw)
	return base64.RawURLEncoding.EncodeToString(append(sign.Sum(nil), raw...))
}
func (a *API) decodeCursor(token, state string) ([]string, error) {
	invalid := failure("INVALID_PAGE_TOKEN", "Page token is invalid or expired", 422, false)
	data, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(data) < sha256.Size {
		return nil, invalid
	}
	signature, raw := data[:sha256.Size], data[sha256.Size:]
	sign := hmac.New(sha256.New, []byte(a.Settings.CursorSecret))
	sign.Write(raw)
	if !hmac.Equal(signature, sign.Sum(nil)) {
		return nil, invalid
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) != 4 {
		return nil, invalid
	}
	var version int
	var context, after []string
	var boundState string
	if json.Unmarshal(parts[0], &version) != nil || json.Unmarshal(parts[1], &context) != nil ||
		json.Unmarshal(parts[2], &boundState) != nil || json.Unmarshal(parts[3], &after) != nil ||
		version != 1 || !slices.Equal(context, a.cursorContext()) || boundState != state || len(after) != 2 {
		return nil, invalid
	}
	if _, err := time.Parse(time.RFC3339Nano, after[0]); err != nil {
		return nil, invalid
	}
	return after, nil
}

func (a *API) authenticate(request *http.Request, scope string) (*AccessToken, error) {
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, failure("UNAUTHENTICATED", "A Bearer token is required", 401, false)
	}
	provided := sha256.Sum256([]byte(parts[1]))
	var matched *AccessToken
	for i := range a.Settings.AccessTokens {
		expected := sha256.Sum256([]byte(a.Settings.AccessTokens[i].Token))
		if hmac.Equal(provided[:], expected[:]) {
			matched = &a.Settings.AccessTokens[i]
		}
	}
	if matched == nil {
		return nil, failure("UNAUTHENTICATED", "Invalid Bearer token", 401, false)
	}
	if scope != "" && !slices.Contains(matched.Scopes, scope) {
		return nil, failure("FORBIDDEN", "This token does not permit this operation", 403, false)
	}
	return matched, nil
}

func (a *API) verifySource(ctx context.Context) error {
	version, err := a.Source.Identity(ctx)
	if err != nil {
		if healthErr := a.Store.SourceHealth(false, errorCode(err), ""); healthErr != nil {
			return healthErr
		}
		return err
	}
	return a.Store.SourceHealth(true, "", version)
}
func (a *API) liveMemo(ctx context.Context, name string) (*Memo, error) {
	memo, err := a.Source.Get(ctx, name)
	if err != nil {
		if healthErr := a.Store.SourceHealth(false, errorCode(err), ""); healthErr != nil {
			return nil, healthErr
		}
		return nil, err
	}
	if err := a.Store.Refresh(name, memo); err != nil {
		return nil, err
	}
	return memo, nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		http.Error(writer, "Internal service error", 500)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(buffer.Bytes())
}

func writeError(writer http.ResponseWriter, err error, requestID string) {
	var service *ServiceError
	if !errors.As(err, &service) {
		slog.Error("request failed", "request_id", requestID)
		service = &ServiceError{"INTERNAL", "Internal service error", 500, true}
	}
	if service.Status == 401 {
		writer.Header().Set("WWW-Authenticate", "Bearer")
	}
	writeJSON(writer, service.Status, map[string]any{"error": map[string]any{
		"code": service.Code, "message": service.Message, "retryable": service.Retryable, "request_id": requestID,
	}})
}

func (a *API) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	id := newID()
	writer.Header().Set("X-Request-ID", id)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	defer func() {
		if recover() != nil {
			writeError(writer, errors.New("handler panic"), id)
		}
	}()
	value, status, err := a.route(writer, request)
	if err != nil {
		writeError(writer, err, id)
		return
	}
	if status != 0 {
		writeJSON(writer, status, value)
	}
}

func (a *API) route(writer http.ResponseWriter, request *http.Request) (any, int, error) {
	path, method := request.URL.Path, request.Method
	if path == "/mcp" {
		if _, err := a.authenticate(request, ""); err != nil {
			return nil, 0, err
		}
		a.MCP.ServeHTTP(privateMCPResponse{writer}, request)
		return nil, 0, nil
	}
	if path == "/api/ext/v1/reviews" || strings.HasPrefix(path, "/api/ext/v1/reviews/") {
		return a.reviewRoute(writer, request)
	}
	badMethod := func() (any, int, error) {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	switch path {
	case "/docs":
		if method != "GET" {
			return badMethod()
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write(docsHTML)
		return nil, 0, nil
	case "/openapi.json":
		if method != "GET" {
			return badMethod()
		}
		return openAPISpec(), 200, nil
	case "/health/live":
		if method != "GET" {
			return badMethod()
		}
		return map[string]string{"status": "ok", "version": Version}, 200, nil
	case "/health/ready":
		if method != "GET" {
			return badMethod()
		}
		state, err := a.Store.Status()
		if err != nil {
			return nil, 0, err
		}
		if state.SourceOK && state.LastFull != nil && a.Worker.alive.Load() {
			return map[string]string{"status": "ready"}, 200, nil
		}
		return map[string]string{"status": "degraded"}, 503, nil
	}
	scope := ""
	switch {
	case path == "/api/ext/v1/notes":
		switch method {
		case "GET":
			scope = "notes:read"
		case "POST":
			scope = "notes:write"
		default:
			return badMethod()
		}
	case path == "/api/ext/v1/me" ||
		(strings.HasPrefix(path, "/api/ext/v1/notes/") && strings.Count(strings.TrimPrefix(path, "/api/ext/v1/notes/"), "/") == 0):
		if method != "GET" {
			return badMethod()
		}
		scope = "notes:read"
	case path == "/api/ext/v1/sync":
		switch method {
		case "GET":
			scope = "sync:read"
		case "POST":
			scope = "sync:write"
		default:
			return badMethod()
		}
	case strings.HasPrefix(path, "/api/ext/v1/jobs/") && strings.Count(strings.TrimPrefix(path, "/api/ext/v1/jobs/"), "/") == 0:
		if method != "GET" {
			return badMethod()
		}
		scope = "sync:read"
	default:
		return nil, 0, failure("NOT_FOUND", "Not Found", 404, false)
	}
	access, err := a.authenticate(request, scope)
	if err != nil {
		return nil, 0, err
	}
	switch path {
	case "/api/ext/v1/me":
		if err := a.verifySource(request.Context()); err != nil {
			return nil, 0, err
		}
		return map[string]any{"instance_id": a.Settings.Memos.InstanceID, "user": a.Settings.Memos.User,
			"credential": access.Name, "scopes": access.Scopes, "timezone": a.Settings.Timezone, "api_version": "v1"}, 200, nil
	case "/api/ext/v1/notes":
		if method == "POST" {
			var body CreateNote
			if err := strictJSON(http.MaxBytesReader(writer, request.Body, 12*1024*1024), &body); err != nil {
				return nil, 0, failure("INVALID_ARGUMENT", "Request parameters are invalid", 422, false)
			}
			return a.Records.Create(request.Context(), request.Header.Get("Idempotency-Key"), body)
		}
		return a.notes(request)
	case "/api/ext/v1/sync":
		if method == "GET" {
			status, err := a.Store.Status()
			return status, 200, err
		}
		var body struct {
			Mode string `json:"mode"`
		}
		body.Mode = "incremental"
		if err := strictJSON(http.MaxBytesReader(writer, request.Body, 65536), &body); err != nil ||
			(body.Mode != "full" && body.Mode != "incremental") {
			return nil, 0, failure("INVALID_ARGUMENT", "Request parameters are invalid", 422, false)
		}
		job, err := a.Store.Enqueue(body.Mode, true)
		if err != nil {
			return nil, 0, err
		}
		a.Worker.Wake()
		return map[string]Job{"job": job}, 202, nil
	}
	if strings.HasPrefix(path, "/api/ext/v1/jobs/") {
		id := strings.TrimPrefix(path, "/api/ext/v1/jobs/")
		if !uuidPattern.MatchString(id) {
			return nil, 0, failure("INVALID_ARGUMENT", "Request parameters are invalid", 422, false)
		}
		job, err := a.Store.Job(strings.ToLower(id))
		return map[string]Job{"job": job}, 200, err
	}
	if err := a.verifySource(request.Context()); err != nil {
		return nil, 0, err
	}
	memo, err := a.liveMemo(request.Context(), "memos/"+strings.TrimPrefix(path, "/api/ext/v1/notes/"))
	if err != nil {
		return nil, 0, err
	}
	if memo == nil {
		return nil, 0, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
	}
	return memo.Note(a.Settings), 200, nil
}

func (a *API) notes(request *http.Request) (any, int, error) {
	query := request.URL.Query()
	state := query.Get("state")
	if state == "" {
		state = "NORMAL"
	}
	size := 20
	var err error
	if query.Has("page_size") {
		size, err = strconv.Atoi(query.Get("page_size"))
	}
	token := query.Get("page_token")
	if err != nil || size < 1 || size > 100 || (state != "NORMAL" && state != "ARCHIVED") || len(token) > 4096 {
		return nil, 0, failure("INVALID_ARGUMENT", "Request parameters are invalid", 422, false)
	}
	var cursor []string
	if token != "" {
		cursor, err = a.decodeCursor(token, state)
		if err != nil {
			return nil, 0, err
		}
	}
	if err := a.verifySource(request.Context()); err != nil {
		return nil, 0, err
	}
	rows, err := a.Store.Page(state, size+1, cursor)
	if err != nil {
		return nil, 0, err
	}
	result := []Note{}
	var consumed []string
	for _, memo := range rows[:min(size, len(rows))] {
		consumed = []string{memo.UpdateTime, memo.Name}
		fresh, err := a.liveMemo(request.Context(), memo.Name)
		if err != nil {
			return nil, 0, err
		}
		if fresh != nil && fresh.State == state {
			result = append(result, fresh.Note(a.Settings))
		}
	}
	next := ""
	if len(rows) > size && consumed != nil {
		next = a.encodeCursor(state, consumed)
	}
	return map[string]any{"notes": result, "next_page_token": next}, 200, nil
}

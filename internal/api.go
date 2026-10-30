package moerise

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
)

type API struct {
	*App
	MCP http.Handler
}

func NewAPI(app *App) (*API, error) {
	api := &API{App: app}
	api.MCP = api.newMCPHandler()
	return api, nil
}

func (a *API) cursorContext() []string {
	return a.Notes.cursorContext()
}
func (a *API) encodeCursor(state string, after []string) string {
	return a.Notes.encodeCursor(state, after)
}
func (a *API) decodeCursor(token, state string) ([]string, error) {
	return a.Notes.decodeCursor(token, state)
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
	return a.Notes.Verify(ctx)
}
func (a *API) liveMemo(ctx context.Context, name string) (*Memo, error) {
	return a.Notes.fresh(ctx, name)
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
	a.runtimeMu.RLock()
	defer a.runtimeMu.RUnlock()
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
	if path == "/hooks/feishu" && a.Feishu != nil {
		return a.Feishu.Receive(request)
	}
	if path == "/mcp" {
		if _, err := a.authenticate(request, ""); err != nil {
			return nil, 0, err
		}
		a.MCP.ServeHTTP(privateMCPResponse{writer}, request)
		return nil, 0, nil
	}
	if path == "/api/ext/v1" || strings.HasPrefix(path, "/api/ext/v1/") {
		if _, err := a.authenticate(request, ""); err != nil {
			return nil, 0, err
		}
		if !externalReadOperation(method, path) {
			return nil, 0, failure("FORBIDDEN", "External API permits only read operations", 403, false)
		}
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
	isRelated := strings.HasPrefix(path, "/api/ext/v1/notes/") &&
		strings.HasSuffix(strings.TrimPrefix(path, "/api/ext/v1/notes/"), "/related")
	switch {
	case isRelated:
		if method != "GET" {
			return badMethod()
		}
		scope = "notes:read"
	case path == "/api/ext/v1/search":
		if method != "POST" {
			return badMethod()
		}
		scope = "notes:read"
	case path == "/api/ext/v1/search/index":
		if method != "GET" {
			return badMethod()
		}
		scope = "notes:read"
	case path == "/api/ext/v1/discovery/walk":
		if method != "POST" {
			return badMethod()
		}
		scope = "notes:read"
	case path == "/api/ext/v1/notes":
		scope = "notes:read"
	case path == "/api/ext/v1/me" ||
		(strings.HasPrefix(path, "/api/ext/v1/notes/") && strings.Count(strings.TrimPrefix(path, "/api/ext/v1/notes/"), "/") == 0):
		if method != "GET" {
			return badMethod()
		}
		scope = "notes:read"
	case path == "/api/ext/v1/sync":
		scope = "sync:read"
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
	if isRelated {
		return a.related(request)
	}
	switch path {
	case "/api/ext/v1/search/index":
		result, err := a.Store.VectorStatus()
		return result, 200, err
	case "/api/ext/v1/discovery/walk":
		return a.discoveryRoute(writer, request)
	case "/api/ext/v1/search":
		var body SearchRequest
		if err := strictJSON(http.MaxBytesReader(writer, request.Body, 65536), &body); err != nil {
			return nil, 0, failure("INVALID_ARGUMENT", "Invalid search parameters", 422, false)
		}
		result, err := a.Search.Search(request.Context(), body)
		return result, 200, err
	case "/api/ext/v1/me":
		if err := a.verifySource(request.Context()); err != nil {
			return nil, 0, err
		}
		return map[string]any{"instance_id": a.Settings.Memos.InstanceID, "user": a.Settings.Memos.User,
			"credential": access.Name, "scopes": access.Scopes, "timezone": a.Store.Settings.Timezone, "api_version": "v1"}, 200, nil
	case "/api/ext/v1/notes":
		return a.notes(request)
	case "/api/ext/v1/sync":
		status, err := a.Store.Status()
		return status, 200, err
	}
	if strings.HasPrefix(path, "/api/ext/v1/jobs/") {
		id := strings.TrimPrefix(path, "/api/ext/v1/jobs/")
		if !uuidPattern.MatchString(id) {
			return nil, 0, failure("INVALID_ARGUMENT", "Request parameters are invalid", 422, false)
		}
		job, err := a.Store.Job(strings.ToLower(id))
		return map[string]Job{"job": job}, 200, err
	}
	note, err := a.Notes.Get(request.Context(), "memos/"+strings.TrimPrefix(path, "/api/ext/v1/notes/"))
	return note, 200, err
}

func (a *API) notes(request *http.Request) (any, int, error) {
	args, err := parseNoteList(request.URL.Query(), false)
	if err != nil {
		return nil, 0, err
	}
	page, err := a.Notes.ListIndexed(request.Context(), args)
	return page, 200, err
}

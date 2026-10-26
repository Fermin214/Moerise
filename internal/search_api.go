package moerise

import (
	"net/http"
	"strconv"
	"strings"
)

func (a *API) searchRoute(writer http.ResponseWriter, request *http.Request) (any, int, error) {
	path, method := request.URL.Path, request.Method
	scope := "notes:read"
	if path == "/api/ext/v1/discovery/open" {
		scope = "discovery:write"
	}
	if _, err := a.authenticate(request, scope); err != nil {
		return nil, 0, err
	}
	badMethod := func() (any, int, error) {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	decode := func(target any) error {
		if err := strictJSON(http.MaxBytesReader(writer, request.Body, 65536), target); err != nil {
			return searchArgumentError()
		}
		return nil
	}
	switch path {
	case "/api/ext/v1/search":
		if method != "POST" {
			return badMethod()
		}
		var body SearchRequest
		if err := decode(&body); err != nil {
			return nil, 0, err
		}
		result, err := a.Search.Search(request.Context(), body)
		return result, 200, err
	case "/api/ext/v1/search/index":
		if method != "GET" {
			return badMethod()
		}
		result, err := a.Search.IndexStatus()
		return result, 200, err
	case "/api/ext/v1/discovery/walk":
		if method != "POST" {
			return badMethod()
		}
		var body WalkRequest
		if err := decode(&body); err != nil {
			return nil, 0, err
		}
		result, err := a.Search.Walk(request.Context(), body)
		return result, 200, err
	case "/api/ext/v1/discovery/open":
		if method != "POST" {
			return badMethod()
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := decode(&body); err != nil {
			return nil, 0, err
		}
		note, err := a.Search.Open(request.Context(), body.Name)
		return note, 200, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/ext/v1/notes/"), "/")
	if len(parts) != 2 || parts[1] != "related" {
		return nil, 0, failure("NOT_FOUND", "Not Found", 404, false)
	}
	if method != "GET" {
		return badMethod()
	}
	body := RelatedRequest{Name: "memos/" + parts[0]}
	query := request.URL.Query()
	if query.Has("limit") {
		limit, err := strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 20 {
			return nil, 0, searchArgumentError()
		}
		body.Limit = limit
	}
	if query.Has("include_archived") {
		if query.Get("include_archived") != "true" && query.Get("include_archived") != "false" {
			return nil, 0, searchArgumentError()
		}
		body.IncludeArchived = query.Get("include_archived") == "true"
	}
	result, err := a.Search.Related(request.Context(), body)
	return result, 200, err
}

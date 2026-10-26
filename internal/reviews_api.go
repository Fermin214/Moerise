package moerise

import (
	"net/http"
	"strings"
	"time"
)

func (a *API) reviewRoute(writer http.ResponseWriter, request *http.Request) (any, int, error) {
	path := strings.TrimPrefix(request.URL.Path, "/api/ext/v1/reviews/")
	parts := strings.Split(path, "/")
	method, scope := request.Method, "reviews:read"
	if method == "POST" {
		scope = "reviews:write"
	} else if method != "GET" {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	if _, err := a.authenticate(request, scope); err != nil {
		return nil, 0, err
	}
	if method == "POST" {
		var body struct{}
		if err := strictJSON(http.MaxBytesReader(writer, request.Body, 65536), &body); err != nil {
			return nil, 0, failure("INVALID_ARGUMENT", "Expected an empty JSON object", 422, false)
		}
	}
	ctx, at := request.Context(), time.Now()
	var batch ReviewBatch
	var err error
	switch {
	case path == "daily" && method == "GET":
		batch, err = a.Reviews.ReadDaily(ctx, at)
	case path == "daily" && method == "POST":
		batch, err = a.Reviews.Daily(ctx, at)
	case path == "daily/replace" && method == "POST":
		batch, err = a.Reviews.Replace(ctx, request.Header.Get("Idempotency-Key"), at)
	case len(parts) == 1 && method == "GET":
		batch, err = a.Reviews.Get(ctx, parts[0])
	case len(parts) == 4 && parts[1] == "items" && method == "POST" && (parts[3] == "open" || parts[3] == "skip"):
		batch, err = a.Reviews.Mark(ctx, parts[0], "memos/"+parts[2], parts[3], at)
	default:
		return nil, 0, failure("NOT_FOUND", "Not Found", 404, false)
	}
	return batch, 200, err
}

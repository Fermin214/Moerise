package moerise

import (
	"net/http"
	"strings"
	"time"
)

func (a *API) reviewRoute(writer http.ResponseWriter, request *http.Request) (any, int, error) {
	path := strings.TrimPrefix(request.URL.Path, "/api/ext/v1/reviews/")
	parts := strings.Split(path, "/")
	if request.Method != "GET" {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	if _, err := a.authenticate(request, "reviews:read"); err != nil {
		return nil, 0, err
	}
	ctx, at := request.Context(), time.Now()
	var batch ReviewBatch
	var err error
	switch {
	case path == "daily":
		batch, err = a.Reviews.ReadDaily(ctx, at)
	case len(parts) == 1:
		batch, err = a.Reviews.Get(ctx, parts[0])
	default:
		return nil, 0, failure("NOT_FOUND", "Not Found", 404, false)
	}
	return batch, 200, err
}

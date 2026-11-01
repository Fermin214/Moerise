package moerise

import (
	"context"
	"errors"
	"net/http"
)

func isMapRead(r *http.Request) bool {
	return r.URL.Path == "/api/app/v1/map" && r.Method == http.MethodGet
}

func (a *ClientAPI) mapRoute(r *http.Request) (any, int, error) {
	if r.Method != http.MethodGet {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	result, err := a.app.Map.Read(r.Context())
	if errors.Is(err, context.Canceled) {
		err = failure("MAP_REQUEST_CANCELED", "Map request was canceled", 503, true)
	}
	return result, http.StatusOK, err
}

package moerise

import (
	"context"
	"errors"
	"net/http"
)

func walkError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return failure("WALK_RESOURCE_LIMIT", "Walk request exceeds the transport budget", 413, true)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return failure("WALK_RESOURCE_LIMIT", "Walk request exceeded its time budget; retry the request", 503, true)
	}
	return err
}

func (a *API) discoveryRoute(writer http.ResponseWriter, request *http.Request) (any, int, error) {
	invalid := failure("INVALID_ARGUMENT", "Invalid discovery parameters", 422, false)
	var body WalkRequest
	if err := strictJSON(http.MaxBytesReader(writer, request.Body, 65536), &body); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, walkError(err)
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, 0, walkError(err)
		}
		return nil, 0, invalid
	}
	result, err := a.Search.Walk(request.Context(), body)
	return result, 200, walkError(err)
}

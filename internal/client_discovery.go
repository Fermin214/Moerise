package moerise

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

func (a *ClientAPI) discoveryRoute(w http.ResponseWriter, r *http.Request) (any, int, error) {
	if r.URL.Path == "/api/app/v1/reviews/daily" {
		if r.Method != http.MethodGet {
			return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
		}
		batch, err := a.app.Reviews.Daily(r.Context(), time.Now())
		for i := range batch.Items {
			if batch.Items[i].Note != nil {
				note := clientNote(*batch.Items[i].Note)
				batch.Items[i].Note = &note
			}
		}
		return batch, 200, err
	}
	if r.Method != http.MethodPost {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	// The HTTP budget bounds memory use without imposing a product path length.
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, 0, failure("WALK_RESOURCE_LIMIT", "Walk request exceeds the transport budget", 413, true)
	}
	if err != nil {
		return nil, 0, failure("INVALID_ARGUMENT", "Invalid walk request", 422, false)
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	var result WalkResponse
	if r.URL.Path == "/api/app/v1/walks/start" {
		var body struct {
			Name string `json:"name"`
		}
		if err := readClientJSON(w, r, 1024*1024, []string{"name"}, &body); err != nil {
			return nil, 0, err
		}
		result, err = a.app.Search.StartWalk(r.Context(), body.Name)
	} else {
		var body struct {
			Name string   `json:"name"`
			Path []string `json:"path"`
		}
		if err := readClientJSON(w, r, 1024*1024, []string{"name", "path"}, &body); err != nil {
			return nil, 0, err
		}
		if body.Name == "" {
			return nil, 0, failure("INVALID_ARGUMENT", "A walk seed is required", 422, false)
		}
		result, err = a.app.Search.Walk(r.Context(), WalkRequest{Name: body.Name, Path: body.Path})
	}
	if result.Item != nil {
		result.Item.Note = clientNote(result.Item.Note)
	}
	return result, 200, walkError(err)
}

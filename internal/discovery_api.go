package moerise

import "net/http"

func (a *API) discoveryRoute(writer http.ResponseWriter, request *http.Request) (any, int, error) {
	invalid := failure("INVALID_ARGUMENT", "Invalid discovery parameters", 422, false)
	if request.URL.Path == "/api/ext/v1/discovery/walk" {
		var body WalkRequest
		if err := strictJSON(http.MaxBytesReader(writer, request.Body, 65536), &body); err != nil {
			return nil, 0, invalid
		}
		result, err := a.Search.Walk(request.Context(), body)
		return result, 200, err
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := strictJSON(http.MaxBytesReader(writer, request.Body, 65536), &body); err != nil {
		return nil, 0, invalid
	}
	result, err := a.Search.Open(request.Context(), body.Name)
	return result, 200, err
}

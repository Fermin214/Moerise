package moerise

import (
	"net/http"
	"strings"
)

func (a *API) notificationRoute(writer http.ResponseWriter, request *http.Request) (any, int, error) {
	scope := "reviews:read"
	if request.Method == "POST" {
		scope = "reviews:write"
	} else if request.Method != "GET" {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	access, err := a.authenticate(request, scope)
	if err != nil {
		return nil, 0, err
	}
	if request.URL.Path == "/api/ext/v1/notifications" && request.Method == "GET" {
		items, err := a.Notifications.List(request.URL.Query().Get("status"))
		return map[string]any{"notifications": items}, 200, err
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/ext/v1/notifications/"), "/")
	if len(parts) == 1 && request.Method == "GET" {
		result, err := a.Notifications.Detail(parts[0])
		return result, 200, err
	}
	if len(parts) == 2 && parts[1] == "resolve" && request.Method == "POST" {
		var body struct {
			Decision string `json:"decision"`
			Receipt  string `json:"receipt"`
		}
		if err := strictJSON(http.MaxBytesReader(writer, request.Body, 65536), &body); err != nil {
			return nil, 0, failure("INVALID_ARGUMENT", "Invalid notification resolution", 422, false)
		}
		result, err := a.Notifications.Resolve(request.Context(), parts[0], body.Decision, body.Receipt, access.Name)
		return result, 200, err
	}
	return nil, 0, failure("NOT_FOUND", "Not Found", 404, false)
}

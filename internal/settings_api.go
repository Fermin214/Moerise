package moerise

import (
	"bytes"
	"encoding/json"
	"net/http"
)

type settingsRequest SettingsUpdate

func (request *settingsRequest) UnmarshalJSON(data []byte) error {
	bad := failure("INVALID_ARGUMENT", "Invalid settings request", 422, false)
	if err := checkSettingsJSON(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return bad
	}
	type plain settingsRequest
	if strictJSON(bytes.NewReader(data), (*plain)(request)) != nil {
		return bad
	}
	// 精确检查操作字段名，避免 encoding/json 接受大小写变体。
	var raw struct {
		Secrets map[string]map[string]json.RawMessage `json:"secrets"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return bad
	}
	for _, operation := range raw.Secrets {
		for field := range operation {
			if field != "action" && field != "value" {
				return bad
			}
		}
	}
	return nil
}

// 在解码为 map 前校验，避免重复字段被覆盖。
func checkSettingsJSON(decoder *json.Decoder) error {
	bad := failure("INVALID_ARGUMENT", "Invalid settings request", 422, false)
	token, err := decoder.Token()
	if err != nil || token == nil {
		return bad
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return bad
			}
			seen[name] = true
			if err := checkSettingsJSON(decoder); err != nil {
				return err
			}
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return bad
		}
	case json.Delim('['):
		for decoder.More() {
			if err := checkSettingsJSON(decoder); err != nil {
				return err
			}
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return bad
		}
	}
	return nil
}

func hasSettingsFields(fields map[string]json.RawMessage) bool {
	for _, value := range fields {
		var object map[string]json.RawMessage
		if json.Unmarshal(value, &object) != nil || hasSettingsFields(object) {
			return true
		}
	}
	return false
}

func (a *ClientAPI) settingsRoute(w http.ResponseWriter, r *http.Request) (any, int, error) {
	switch r.Method {
	case http.MethodGet:
		return a.app.Store.settingsView(), http.StatusOK, nil
	case http.MethodPatch:
		var body settingsRequest
		if err := readClientJSON(w, r, 128*1024, []string{"version", "settings", "secrets"}, &body); err != nil {
			return nil, 0, err
		}
		if body.Version < 1 || len(body.Secrets) == 0 && !hasSettingsFields(body.Settings) {
			return nil, 0, failure("INVALID_ARGUMENT", "A positive version and settings changes are required", 422, false)
		}
		view, err := a.app.UpdateSettings(r.Context(), SettingsUpdate(body))
		return view, http.StatusOK, err
	default:
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
}

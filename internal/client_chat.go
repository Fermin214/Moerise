package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const chatRequestTimeout = 115 * time.Second

func isChatStream(r *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/app/v1/"), "/")
	return r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "chats" && parts[2] == "messages"
}

func chatQuery(r *http.Request, allowed ...string) (url.Values, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, failure("INVALID_ARGUMENT", "Invalid chat query", 422, false)
	}
	for key, values := range query {
		found := false
		for _, name := range allowed {
			found = found || key == name
		}
		if !found || len(values) != 1 {
			return nil, failure("INVALID_ARGUMENT", "Invalid chat query", 422, false)
		}
	}
	return query, nil
}

func (a *ClientAPI) chatRoute(w http.ResponseWriter, r *http.Request, identity *SessionIdentity) (any, int, error) {
	path := strings.TrimPrefix(r.URL.Path, "/api/app/v1/")
	service := a.app.Chat
	if path == "chat-materials" && r.Method == http.MethodGet {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return nil, 0, failure("INVALID_ARGUMENT", "Invalid material query", 422, false)
		}
		input, err := parseChatSelection(query)
		if err != nil {
			return nil, 0, err
		}
		result, err := service.Select(r.Context(), input)
		for i := range result.Notes {
			result.Notes[i] = clientNote(result.Notes[i])
		}
		return map[string]any{"notes": result.Notes, "next_page_token": result.NextPageToken,
			"character_limit": ChatCharacterLimit}, 200, err
	}
	if path == "chat-materials/resolve" && r.Method == http.MethodPost {
		var input struct {
			Names []string `json:"names"`
		}
		if err := readClientJSON(w, r, 128*1024, []string{"names"}, &input); err != nil {
			return nil, 0, err
		}
		result, err := service.Materials(r.Context(), input.Names, nil)
		for i := range result.Notes {
			result.Notes[i] = clientNote(result.Notes[i])
		}
		return result, 200, err
	}
	if path == "chats" {
		switch r.Method {
		case http.MethodGet:
			query, err := chatQuery(r, "page_size", "page_token")
			if err != nil {
				return nil, 0, err
			}
			size := 20
			if query.Has("page_size") {
				size, err = strconv.Atoi(query.Get("page_size"))
				if err != nil || size < 1 || size > 100 {
					return nil, 0, failure("INVALID_ARGUMENT", "Invalid chat page size", 422, false)
				}
			}
			stamp, id := "", ""
			if token := query.Get("page_token"); token != "" {
				var cursor struct {
					Kind, Time, ID string
					Context        []string
				}
				if a.app.Notes.readCursor(token, &cursor) != nil || cursor.Kind != "chats-v1" ||
					!slices.Equal(cursor.Context, a.app.Notes.liveContext()) ||
					!uuidPattern.MatchString(cursor.ID) {
					return nil, 0, failure("INVALID_PAGE_TOKEN", "Invalid chat page token", 422, false)
				}
				if _, err := time.Parse(time.RFC3339Nano, cursor.Time); err != nil {
					return nil, 0, failure("INVALID_PAGE_TOKEN", "Invalid chat page token", 422, false)
				}
				stamp, id = cursor.Time, cursor.ID
			}
			items, err := a.app.Store.listChats(stamp, id, size)
			next := ""
			if len(items) == size {
				last := items[len(items)-1]
				next = a.app.Notes.signCursor(struct {
					Kind, Time, ID string
					Context        []string
				}{"chats-v1", last.UpdatedAt, last.ID, a.app.Notes.liveContext()})
			}
			return map[string]any{"chats": items, "next_page_token": next, "character_limit": ChatCharacterLimit}, 200, err
		case http.MethodPost:
			var input ChatCreate
			if err := readClientJSON(w, r, 128*1024, []string{"request_id", "title", "names"}, &input); err != nil {
				return nil, 0, err
			}
			if err := a.app.Sessions.CheckActive(r.Context(), identity); err != nil {
				return nil, 0, err
			}
			chat, created, err := service.Create(r.Context(), input)
			status := 200
			if created {
				status = 201
			}
			return chat, status, err
		}
	}
	parts := strings.Split(path, "/")
	if len(parts) >= 2 && parts[0] == "chats" {
		if !uuidPattern.MatchString(parts[1]) {
			return nil, 0, failure("INVALID_RESOURCE", "Invalid chat resource", 422, false)
		}
		id := parts[1]
		if len(parts) == 2 {
			switch r.Method {
			case http.MethodGet:
				result, err := a.app.Store.chat(id)
				return result, 200, err
			case http.MethodPatch:
				var input ChatUpdate
				if err := readClientJSON(w, r, 128*1024, []string{"version", "title", "names"}, &input); err != nil {
					return nil, 0, err
				}
				if err := a.app.Sessions.CheckActive(r.Context(), identity); err != nil {
					return nil, 0, err
				}
				result, err := service.Update(r.Context(), id, input)
				return result, 200, err
			case http.MethodDelete:
				var input struct {
					Version int64 `json:"version"`
				}
				if err := readClientJSON(w, r, 4096, []string{"version"}, &input); err != nil {
					return nil, 0, err
				}
				if err := a.app.Sessions.CheckActive(r.Context(), identity); err != nil {
					return nil, 0, err
				}
				return nil, 204, service.Delete(r.Context(), id, input.Version)
			}
		}
		if len(parts) == 3 && parts[2] == "messages" && r.Method == http.MethodPost {
			var input ChatGenerate
			if err := readClientJSON(w, r, 64*1024, []string{"request_id", "version", "kind", "content"}, &input); err != nil {
				return nil, 0, err
			}
			if err := a.app.Sessions.CheckActive(r.Context(), identity); err != nil {
				return nil, 0, err
			}
			return a.chatStream(w, r, id, input)
		}
		if len(parts) == 5 && parts[2] == "messages" && r.Method == http.MethodPost {
			requestID := parts[3]
			if !idempotencyPattern.MatchString(requestID) {
				return nil, 0, failure("INVALID_RESOURCE", "Invalid chat request resource", 422, false)
			}
			switch parts[4] {
			case "cancel":
				var input struct{}
				if err := readClientJSON(w, r, 4096, nil, &input); err != nil {
					return nil, 0, err
				}
				if err := a.app.Sessions.CheckActive(r.Context(), identity); err != nil {
					return nil, 0, err
				}
				turn, err := service.Cancel(id, requestID)
				return turn, 200, err
			case "save":
				var input struct {
					Version int64 `json:"version"`
				}
				if err := readClientJSON(w, r, 4096, []string{"version"}, &input); err != nil {
					return nil, 0, err
				}
				if err := a.app.Sessions.CheckActive(r.Context(), identity); err != nil {
					return nil, 0, err
				}
				note, status, err := service.Save(r.Context(), id, requestID, input.Version)
				return clientNote(note), status, err
			}
		}
	}
	return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
}

func (a *ClientAPI) chatStream(w http.ResponseWriter, r *http.Request, chatID string, input ChatGenerate) (result any, status int, err error) {
	started := false
	write := func(kind string, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if !started {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(200)
			started = true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data); err != nil {
			return err
		}
		return http.NewResponseController(w).Flush()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}
			err = failure("CHAT_INTERRUPTED", "Chat generation was interrupted", 500, true)
		}
		if started {
			if err != nil && r.Context().Err() == nil {
				public := chatPublicError(err)
				var service *ServiceError
				if typed, ok := public.(*ServiceError); ok {
					service = typed
				} else {
					service = &ServiceError{"CHAT_FAILED", "Chat generation failed", 500, true}
				}
				_ = write("error", map[string]any{"error": map[string]any{"code": service.Code,
					"message": service.Message, "retryable": service.Retryable, "request_id": w.Header().Get("X-Request-ID")}, "turn": result})
			}
			result, status, err = nil, -1, nil
		}
	}()
	result, err = a.app.Chat.Generate(r.Context(), chatID, input, func(event ChatEvent) error {
		return write(event.Type, event)
	})
	if !started && err == nil {
		return nil, 0, failure("CHAT_FAILED", "Chat did not start a stream", 500, true)
	}
	return result, 200, err
}

func chatRequestContext(r *http.Request) (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), chatRequestTimeout)
	return r.WithContext(ctx), cancel
}

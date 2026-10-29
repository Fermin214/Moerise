package moerise

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// 客户端只接收 Core 同源文件路径，来源的内部地址和扩展字段留在服务端。
func clientNote(note Note) Note {
	note.URL = "/notes/" + strings.TrimPrefix(note.Name, "memos/")
	attachments := make([]map[string]any, 0, len(note.Attachments))
	for _, item := range note.Attachments {
		name, _ := item["name"].(string)
		if !attachmentPattern.MatchString(name) {
			continue
		}
		size := int64(0)
		switch value := item["size"].(type) {
		case json.Number:
			size, _ = value.Int64()
		case string:
			size, _ = strconv.ParseInt(value, 10, 64)
		case float64:
			size = int64(value)
		case int64:
			size = value
		case int:
			size = int64(value)
		}
		attachments = append(attachments, map[string]any{
			"name": name, "filename": item["filename"], "type": item["type"], "size": size,
			"content_url": "/api/app/v1/notes/" + strings.TrimPrefix(note.Name, "memos/") +
				"/attachments/" + strings.TrimPrefix(name, "attachments/") + "/content",
		})
	}
	note.Attachments = attachments
	return note
}

func (a *ClientAPI) noteRoute(w http.ResponseWriter, r *http.Request) (any, int, error) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/app/v1/notes/"), "/")
	if len(parts) == 4 && parts[1] == "attachments" && parts[3] == "content" &&
		(r.Method == http.MethodGet || r.Method == http.MethodHead) {
		if err := a.serveAttachment(w, r, "memos/"+parts[0], "attachments/"+parts[2]); err != nil {
			return nil, 0, err
		}
		return nil, -1, nil
	}
	if len(parts) != 1 || !memoPattern.MatchString("memos/"+parts[0]) {
		return nil, 0, failure("NOT_FOUND", "Not Found", 404, false)
	}
	name := "memos/" + parts[0]
	switch r.Method {
	case http.MethodGet:
		note, err := a.app.Notes.Get(r.Context(), name)
		return clientNote(note), 200, err
	case http.MethodPatch:
		var body UpdateNote
		if err := readClientJSON(w, r, 12*1024*1024,
			[]string{"content", "state", "source_hash", "attachments", "remove_attachments"}, &body); err != nil {
			return nil, 0, err
		}
		note, err := a.app.Notes.Update(r.Context(), name, r.Header.Get("Idempotency-Key"), body)
		return clientNote(note), 200, err
	case http.MethodDelete:
		var body struct {
			SourceHash string `json:"source_hash"`
		}
		if err := readClientJSON(w, r, 4096, []string{"source_hash"}, &body); err != nil {
			return nil, 0, err
		}
		if err := a.app.Notes.Delete(r.Context(), name, body.SourceHash); err != nil {
			return nil, 0, err
		}
		return nil, http.StatusNoContent, nil
	}
	return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
}

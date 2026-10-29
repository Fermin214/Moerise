package moerise

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var attachmentPattern = regexp.MustCompile(`^attachments/[a-zA-Z0-9][a-zA-Z0-9-]{0,35}$`)

type attachmentSource interface {
	GetAttachment(context.Context, string) (map[string]any, error)
	UploadAttachment(context.Context, string, string, CreateAttachment) error
}

func (m *MemosClient) DeleteAttachment(ctx context.Context, name string) error {
	if !attachmentPattern.MatchString(name) {
		return failure("INVALID_ARGUMENT", "Invalid attachment resource", 422, false)
	}
	var response map[string]any
	return m.do(ctx, http.MethodDelete, "/api/v1/"+name, nil, nil, &response)
}

func (m *MemosClient) GetAttachment(ctx context.Context, name string) (map[string]any, error) {
	if !attachmentPattern.MatchString(name) {
		return nil, failure("INVALID_ARGUMENT", "Invalid attachment resource", 422, false)
	}
	var raw map[string]any
	err := m.request(ctx, "/api/v1/"+name, nil, &raw)
	if errorCode(err) == "SOURCE_NOT_FOUND" {
		return nil, nil
	}
	return raw, err
}

func (m *MemosClient) DownloadAttachment(ctx context.Context, attachment map[string]any) (CreateAttachment, error) {
	name, _ := attachment["name"].(string)
	filename, _ := attachment["filename"].(string)
	kind, _ := attachment["type"].(string)
	if !attachmentPattern.MatchString(name) || filename == "" {
		return CreateAttachment{}, upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos image resource")
	}
	request, err := http.NewRequestWithContext(ctx, "GET", m.config.BaseURL+"/file/"+name+"/"+url.PathEscape(filename), nil)
	if err != nil {
		return CreateAttachment{}, err
	}
	request.Header.Set("Authorization", "Bearer "+m.config.Token)
	response, err := m.client.Do(request)
	if err != nil {
		return CreateAttachment{}, upstreamError("UPSTREAM_UNAVAILABLE", "Memos image download failed")
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		return CreateAttachment{}, failure("SOURCE_NOT_FOUND", "Memos image is unavailable", 404, false)
	}
	if response.StatusCode != 200 {
		return CreateAttachment{}, upstreamError("UPSTREAM_UNAVAILABLE", "Memos image download failed")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 10*1024*1024+1))
	if err != nil {
		return CreateAttachment{}, upstreamError("UPSTREAM_UNAVAILABLE", "Memos image download failed")
	}
	if len(data) == 0 || len(data) > 10*1024*1024 {
		return CreateAttachment{}, failure("CHANNEL_IMAGE_SIZE", "Image must contain 1 byte to 10 MiB", 422, false)
	}
	return CreateAttachment{Filename: filename, Type: kind, Content: data}, nil
}

func (m *MemosClient) UploadAttachment(ctx context.Context, name, memo string, input CreateAttachment) error {
	if !attachmentPattern.MatchString(name) || !memoPattern.MatchString(memo) {
		return failure("INVALID_ARGUMENT", "Invalid attachment or memo resource", 422, false)
	}
	body, _ := json.Marshal(map[string]any{"filename": input.Filename, "type": input.Type,
		"content": input.Content, "memo": memo})
	var response map[string]any
	return m.do(ctx, http.MethodPost, "/api/v1/attachments",
		url.Values{"attachmentId": {strings.TrimPrefix(name, "attachments/")}}, body, &response)
}

func (s *RecordService) attachmentName(key string, position int) (string, error) {
	settings := s.Store.Settings
	_, err := s.Store.state.Exec(`INSERT OR IGNORE INTO attachment_writes
		(instance_id, user_name, idempotency_key, position, attachment_name) VALUES (?, ?, ?, ?, ?)`,
		settings.Memos.InstanceID, settings.Memos.User, key, position,
		"attachments/"+derivedID(settings.CursorSecret, "attachment", settings.Memos.InstanceID, settings.Memos.User, key, strconv.Itoa(position)))
	if err != nil {
		return "", err
	}
	var name string
	err = s.Store.state.QueryRow(`SELECT attachment_name FROM attachment_writes
		WHERE instance_id = ? AND user_name = ? AND idempotency_key = ? AND position = ?`,
		settings.Memos.InstanceID, settings.Memos.User, key, position).Scan(&name)
	return name, err
}

func (s *RecordService) attach(ctx context.Context, key string, memo *Memo, files []CreateAttachment) (*Memo, error) {
	source, ok := s.Source.(attachmentSource)
	if !ok {
		return nil, upstreamError("UPSTREAM_PROTOCOL", "Source does not support attachments")
	}
	names := []string{}
	for position, file := range files {
		name, err := s.attachmentName(key, position)
		if err != nil {
			return nil, err
		}
		names = append(names, name)
		if err := s.verify(ctx); err != nil {
			return nil, err
		}
		// Recheck the parent before each upload, including resumed partial requests.
		parent, err := s.Source.Get(ctx, memo.Name)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			return nil, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
		}
		raw, err := source.GetAttachment(ctx, name)
		if err != nil {
			return nil, err
		}
		if raw == nil {
			if err := source.UploadAttachment(ctx, name, memo.Name, file); err != nil {
				return nil, err
			}
			raw, err = source.GetAttachment(ctx, name)
			if err != nil {
				return nil, err
			}
		}
		// Memos may strip image metadata and change size; the parent is the ownership boundary.
		if raw == nil || raw["name"] != name || raw["memo"] != memo.Name ||
			raw["filename"] != file.Filename || raw["type"] != file.Type {
			return nil, failure("WRITE_CONFLICT", "The reserved attachment differs from this request", 409, false)
		}
	}
	fresh, err := s.Source.Get(ctx, memo.Name)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
	}
	for _, name := range names {
		found := false
		for _, attachment := range fresh.Attachments {
			if attachment["name"] == name {
				found = true
			}
		}
		if !found {
			return nil, upstreamError("UPSTREAM_PROTOCOL", "Memos did not link the uploaded attachment")
		}
	}
	return fresh, nil
}

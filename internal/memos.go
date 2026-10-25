package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Source interface {
	Identity(context.Context) (string, error)
	Get(context.Context, string) (*Memo, error)
	ListPage(context.Context, string, *int64, string) ([]Memo, string, error)
}

type MemosClient struct {
	config MemosConfig
	client *http.Client
}

func NewMemosClient(config MemosConfig) *MemosClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &MemosClient{config, &http.Client{
		Transport: transport, Timeout: time.Duration(config.TimeoutSeconds * float64(time.Second)),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (m *MemosClient) Close() { m.client.CloseIdleConnections() }

func (m *MemosClient) request(ctx context.Context, path string, query url.Values, target any) error {
	address := m.config.BaseURL + path
	if len(query) > 0 {
		address += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos request")
	}
	req.Header.Set("Authorization", "Bearer "+m.config.Token)
	response, err := m.client.Do(req)
	if err != nil {
		return upstreamError("UPSTREAM_UNAVAILABLE", "Memos is unavailable")
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == 401 || response.StatusCode == 403:
		return upstreamError("UPSTREAM_AUTH", "Memos rejected the configured credential")
	case response.StatusCode == 404:
		return failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
	case response.StatusCode == 400:
		return upstreamError("UPSTREAM_REQUEST_REJECTED", "Memos rejected the adapter request")
	case response.StatusCode >= 300:
		return upstreamError("UPSTREAM_UNAVAILABLE", "Memos request failed")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32*1024*1024+1))
	if err != nil || len(data) > 32*1024*1024 {
		return upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos response")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return upstreamError("UPSTREAM_PROTOCOL", "Memos returned invalid JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return upstreamError("UPSTREAM_PROTOCOL", "Memos returned invalid JSON")
	}
	return nil
}

func (m *MemosClient) Identity(ctx context.Context) (string, error) {
	var profile struct {
		Version string `json:"version"`
	}
	if err := m.request(ctx, "/api/v1/instance/profile", nil, &profile); err != nil {
		return "", err
	}
	var identity struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := m.request(ctx, "/api/v1/auth/me", nil, &identity); err != nil {
		return "", err
	}
	if identity.User.Name != m.config.User {
		return "", upstreamError("IDENTITY_MISMATCH", "Memos account differs from the configured binding")
	}
	if !strings.HasPrefix(profile.Version, "0.31.") {
		return "", upstreamError("UNSUPPORTED_UPSTREAM", "This adapter requires Memos 0.31.x")
	}
	return profile.Version, nil
}

func (m *MemosClient) Get(ctx context.Context, name string) (*Memo, error) {
	if !memoPattern.MatchString(name) {
		return nil, failure("INVALID_RESOURCE", "Invalid memo resource name", 422, false)
	}
	var raw map[string]any
	if err := m.request(ctx, "/api/v1/"+name, nil, &raw); err != nil {
		if errorCode(err) == "SOURCE_NOT_FOUND" {
			return nil, nil
		}
		return nil, err
	}
	memo, err := normalizeMemo(raw)
	if err != nil {
		return nil, err
	}
	if memo.Name != name {
		return nil, upstreamError("UPSTREAM_PROTOCOL", "Memos returned a different resource")
	}
	if memo.Creator != m.config.User {
		return nil, nil
	}
	return &memo, nil
}

func (m *MemosClient) ListPage(ctx context.Context, state string, since *int64, token string) ([]Memo, string, error) {
	filter := "creator == '" + m.config.User + "'"
	if since != nil {
		filter += ` && updated_ts >= timestamp("` + time.Unix(*since, 0).UTC().Format(time.RFC3339) + `")`
	}
	query := url.Values{"state": {state}, "filter": {filter}, "orderBy": {"update_time asc"}, "pageSize": {strconv.Itoa(m.config.PageSize)}}
	if token != "" {
		query.Set("pageToken", token)
	}
	var response struct {
		Memos         []map[string]any `json:"memos"`
		NextPageToken string           `json:"nextPageToken"`
	}
	if err := m.request(ctx, "/api/v1/memos", query, &response); err != nil {
		return nil, "", err
	}
	result := []Memo{}
	for _, raw := range response.Memos {
		memo, err := normalizeMemo(raw)
		if err != nil {
			return nil, "", err
		}
		if memo.Creator == m.config.User {
			result = append(result, memo)
		}
	}
	return result, response.NextPageToken, nil
}

func normalizeMemo(raw map[string]any) (Memo, error) {
	protocol := func() (Memo, error) {
		return Memo{}, upstreamError("UPSTREAM_PROTOCOL", "Memos returned an invalid memo")
	}
	if raw == nil {
		return protocol()
	}
	if _, ok := raw["state"]; !ok {
		raw["state"] = "NORMAL"
	}
	if _, ok := raw["visibility"]; !ok {
		raw["visibility"] = "PRIVATE"
	}
	var upstream struct {
		Name        string           `json:"name"`
		Creator     string           `json:"creator"`
		Content     string           `json:"content"`
		State       string           `json:"state"`
		Visibility  string           `json:"visibility"`
		CreateTime  string           `json:"createTime"`
		UpdateTime  string           `json:"updateTime"`
		Tags        []string         `json:"tags"`
		Attachments []map[string]any `json:"attachments"`
		Relations   []map[string]any `json:"relations"`
		Pinned      bool             `json:"pinned"`
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return protocol()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&upstream); err != nil {
		return protocol()
	}
	created, err := time.Parse(time.RFC3339Nano, upstream.CreateTime)
	if err != nil {
		return protocol()
	}
	updated, err := time.Parse(time.RFC3339Nano, upstream.UpdateTime)
	if err != nil || !memoPattern.MatchString(upstream.Name) || !userPattern.MatchString(upstream.Creator) ||
		(upstream.State != "NORMAL" && upstream.State != "ARCHIVED") {
		return protocol()
	}
	if upstream.Tags == nil {
		upstream.Tags = []string{}
	}
	if upstream.Attachments == nil {
		upstream.Attachments = []map[string]any{}
	}
	if upstream.Relations == nil {
		upstream.Relations = []map[string]any{}
	}
	memo := Memo{upstream.Name, upstream.Creator, upstream.Content, upstream.State, upstream.Visibility,
		created.UTC().Format("2006-01-02T15:04:05.000000+00:00"), updated.UTC().Format("2006-01-02T15:04:05.000000+00:00"),
		upstream.Tags, upstream.Attachments, upstream.Relations, upstream.Pinned, "", hashBytes([]byte(upstream.Content))}
	payload := map[string]any{
		"name": memo.Name, "creator": memo.Creator, "content": memo.Content, "state": memo.State,
		"visibility": memo.Visibility, "create_time": memo.CreateTime, "update_time": memo.UpdateTime,
		"tags": memo.Tags, "attachments": memo.Attachments, "relations": memo.Relations, "pinned": memo.Pinned,
	}
	memo.SourceHash = hashBytes(canonicalJSON(payload))
	return memo, nil
}

// Source hashes use sorted keys, UTF-8 strings, and stable JSON separators.
func canonicalJSON(value any) []byte {
	data, _ := json.Marshal(value)
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	_ = decoder.Decode(&normalized)
	var render func(any) string
	render = func(v any) string {
		switch item := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(item))
			for key := range item {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			parts := []string{}
			for _, key := range keys {
				parts = append(parts, render(key)+": "+render(item[key]))
			}
			return "{" + strings.Join(parts, ", ") + "}"
		case []any:
			parts := []string{}
			for _, child := range item {
				parts = append(parts, render(child))
			}
			return "[" + strings.Join(parts, ", ") + "]"
		case string:
			var buffer strings.Builder
			buffer.WriteByte('"')
			for _, character := range item {
				switch character {
				case '"', '\\':
					buffer.WriteByte('\\')
					buffer.WriteRune(character)
				case '\b':
					buffer.WriteString(`\b`)
				case '\f':
					buffer.WriteString(`\f`)
				case '\n':
					buffer.WriteString(`\n`)
				case '\r':
					buffer.WriteString(`\r`)
				case '\t':
					buffer.WriteString(`\t`)
				default:
					if character < 0x20 {
						fmt.Fprintf(&buffer, `\u%04x`, character)
					} else {
						buffer.WriteRune(character)
					}
				}
			}
			buffer.WriteByte('"')
			return buffer.String()
		default:
			encoded, _ := json.Marshal(item)
			return string(encoded)
		}
	}
	return []byte(render(normalized))
}

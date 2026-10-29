package moerise

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strconv"
	"time"
)

type LiveSource interface {
	ListLivePage(ctx context.Context, state string, size int, token string) ([]Memo, string, error)
}

var _ LiveSource = (*MemosClient)(nil)

const (
	memosLiveMaxRetainedMemos = 10000
	memosLiveMaxRetainedBytes = 32 << 20
)

type memosLiveLimits struct {
	maxMemos int
	maxBytes int
}

type memosLiveCursor struct {
	Kind    string `json:"kind"`
	Binding string `json:"binding"`
	Created string `json:"created"`
	Name    string `json:"name"`
}

// ListLivePage 按创建时间、资源名依次倒序；调用方须签名封装对外返回的游标。
func (m *MemosClient) ListLivePage(ctx context.Context, state string, size int, token string) ([]Memo, string, error) {
	if state == "" {
		state = "NORMAL"
	}
	if (state != "NORMAL" && state != "ARCHIVED") || size < 1 || size > 1000 {
		return nil, "", failure("INVALID_ARGUMENT", "Invalid live list state or page size", 422, false)
	}
	if !userPattern.MatchString(m.config.User) || m.config.PageSize < 1 || m.config.PageSize > 1000 || m.config.MaxPages < 1 {
		return nil, "", upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos live list configuration")
	}
	binding := hashBytes(canonicalJSON([]string{m.config.InstanceID, m.config.BaseURL, m.config.User, state, "created_desc"}))
	cursor := memosLiveCursor{}
	if token != "" {
		invalid := failure("INVALID_PAGE_TOKEN", "Invalid live list page token", 422, false)
		if len(token) > 2048 {
			return nil, "", invalid
		}
		data, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return nil, "", invalid
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cursor); err != nil {
			return nil, "", invalid
		}
		created, err := time.Parse(time.RFC3339Nano, cursor.Created)
		if err != nil || cursor.Kind != "memos-live-v1" || cursor.Binding != binding ||
			!memoPattern.MatchString(cursor.Name) ||
			created.UTC().Format("2006-01-02T15:04:05.000000+00:00") != cursor.Created {
			return nil, "", invalid
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, "", invalid
		}
	}
	limits := memosLiveLimits{memosLiveMaxRetainedMemos, memosLiveMaxRetainedBytes}
	items, pages, err := m.scanLivePage(ctx, state, size, cursor, limits)
	if err != nil {
		return nil, "", err
	}
	// Memos 在同时间条目内仍按 offset 翻页，重复扫描用于发现收集边界条目时的位移。
	if pages > 1 {
		again, _, err := m.scanLivePage(ctx, state, size, cursor, limits)
		if err != nil {
			return nil, "", err
		}
		if len(items) != len(again) {
			return nil, "", failure("SOURCE_CHANGED", "Memos changed during live pagination", 409, true)
		}
		for i := range items {
			if items[i].Name != again[i].Name || items[i].SourceHash != again[i].SourceHash {
				return nil, "", failure("SOURCE_CHANGED", "Memos changed during live pagination", 409, true)
			}
		}
	}
	next := ""
	if len(items) > size {
		last := items[size-1]
		data, _ := json.Marshal(memosLiveCursor{"memos-live-v1", binding, last.CreateTime, last.Name})
		next = base64.RawURLEncoding.EncodeToString(data)
		items = items[:size]
	}
	return items, next, nil
}

func (m *MemosClient) scanLivePage(ctx context.Context, state string, size int, cursor memosLiveCursor, limits memosLiveLimits) ([]Memo, int, error) {
	filter := "creator == '" + m.config.User + "'"
	if cursor.Created != "" {
		created, _ := time.Parse(time.RFC3339Nano, cursor.Created)
		filter += ` && created_ts <= timestamp("` + created.UTC().Format(time.RFC3339Nano) + `")`
	}
	query := url.Values{
		"state": {state}, "filter": {filter}, "orderBy": {"create_time desc"},
		"pageSize": {strconv.Itoa(m.config.PageSize)},
	}
	result := []Memo{}
	seenNames := map[string]bool{}
	seenTokens := map[string]bool{}
	retainedMemos, retainedBytes := 0, 0
	limitError := func() error {
		return upstreamError("LIVE_LIST_LIMIT", "Memos live list exceeded the retained result budget")
	}
	// 原始 JSON 字节估算保留对象大小；游标排除的资源名也会留在去重集合中。
	retain := func(raw map[string]any) error {
		data, err := json.Marshal(raw)
		if err != nil {
			return upstreamError("UPSTREAM_PROTOCOL", "Memos returned an invalid live list memo")
		}
		if retainedMemos >= limits.maxMemos || len(data) > limits.maxBytes-retainedBytes {
			return limitError()
		}
		retainedMemos++
		retainedBytes += len(data)
		return nil
	}
	previousTime, cutoff := "", ""
	for page := 1; page <= m.config.MaxPages; page++ {
		var response struct {
			Memos         []map[string]any `json:"memos"`
			NextPageToken string           `json:"nextPageToken"`
		}
		if err := m.request(ctx, "/api/v1/memos", query, &response); err != nil {
			return nil, page, err
		}
		for _, raw := range response.Memos {
			memo, err := normalizeMemo(raw)
			if err != nil {
				return nil, page, err
			}
			if memo.Creator != m.config.User || memo.State != state ||
				(cursor.Created != "" && memo.CreateTime > cursor.Created) ||
				(previousTime != "" && memo.CreateTime > previousTime) {
				return nil, page, upstreamError("UPSTREAM_PROTOCOL", "Memos returned an invalid live list order or scope")
			}
			if seenNames[memo.Name] {
				return nil, page, failure("SOURCE_CHANGED", "Memos changed during live pagination", 409, true)
			}
			previousTime = memo.CreateTime
			if cutoff != "" && memo.CreateTime < cutoff {
				if len(result) == size {
					if err := retain(raw); err != nil {
						return nil, page, err
					}
					result = append(result, memo)
				}
				return finishLivePage(result, size), page, nil
			}
			if err := retain(raw); err != nil {
				return nil, page, err
			}
			seenNames[memo.Name] = true
			if cursor.Created != "" && memo.CreateTime == cursor.Created && memo.Name >= cursor.Name {
				continue
			}
			result = append(result, memo)
			// 收集返回边界所在时间的全部条目；v0.31.0 接受 name 排序参数，
			// 但同时间条目实际仍按内部 ID 排序。
			if len(result) == size {
				cutoff = memo.CreateTime
			}
		}
		if response.NextPageToken == "" {
			return finishLivePage(result, size), page, nil
		}
		if seenTokens[response.NextPageToken] {
			return nil, page, upstreamError("UPSTREAM_PROTOCOL", "Memos repeated a live list page token")
		}
		if len(response.NextPageToken) > limits.maxBytes-retainedBytes {
			return nil, page, limitError()
		}
		retainedBytes += len(response.NextPageToken)
		seenTokens[response.NextPageToken] = true
		query.Set("pageToken", response.NextPageToken)
	}
	return nil, m.config.MaxPages, upstreamError("UPSTREAM_PAGINATION_LIMIT", "Memos live list exceeded the page budget")
}

func finishLivePage(items []Memo, size int) []Memo {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreateTime == items[j].CreateTime {
			return items[i].Name > items[j].Name
		}
		return items[i].CreateTime > items[j].CreateTime
	})
	if len(items) > size+1 {
		// 复制短前缀，避免一致性复查时仍引用完整同时间组的正文和附件。
		items = append([]Memo(nil), items[:size+1]...)
	}
	return items
}

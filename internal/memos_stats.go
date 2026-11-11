package moerise

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"time"
)

type SourceStats struct {
	Name    string         `json:"name"`
	Total   int            `json:"totalMemoCount"`
	Tags    map[string]int `json:"tagCount"`
	Created []string       `json:"memoCreatedTimestamps"`
	Updated []string       `json:"memoUpdatedTimestamps"`
}

type AggregateStatsSource interface {
	Stats(context.Context, string, LiveFilter) (SourceStats, error)
}

// Stats 使用官方按状态聚合接口；creator 条件固定到实例作者。
func (m *MemosClient) Stats(ctx context.Context, state string, filter LiveFilter) (SourceStats, error) {
	if (state != "NORMAL" && state != "ARCHIVED") || !userPattern.MatchString(m.config.User) {
		return SourceStats{}, upstreamError("UPSTREAM_PROTOCOL", "Invalid Memos statistics scope")
	}
	var response struct {
		Stats []SourceStats `json:"stats"`
	}
	query := url.Values{"state": {state}, "filter": {"creator == '" + m.config.User + "'" + filter.cel()}}
	if err := m.request(ctx, "/api/v1/users:stats", query, &response); err != nil {
		return SourceStats{}, err
	}
	if len(response.Stats) == 0 {
		return SourceStats{Name: m.config.User + "/stats", Tags: map[string]int{}}, nil
	}
	if len(response.Stats) != 1 || response.Stats[0].Name != m.config.User+"/stats" {
		return SourceStats{}, upstreamError("UPSTREAM_PROTOCOL", "Memos returned different statistics owners")
	}
	return response.Stats[0], nil
}

// 计数与逐篇时间戳互相校验，避免将不完整元数据作为完整统计。
func validateSourceStats(stats SourceStats, user string, filter LiveFilter) (string, error) {
	protocol := upstreamError("UPSTREAM_PROTOCOL", "Memos returned invalid statistics")
	if stats.Name != user+"/stats" || stats.Total < 0 ||
		len(stats.Created) != stats.Total || len(stats.Updated) != stats.Total {
		return "", protocol
	}
	if stats.Total > memosLiveMaxRetainedMemos {
		return "", upstreamError("LIVE_LIST_LIMIT", "Source statistics exceeded the result budget")
	}
	for _, count := range stats.Tags {
		if count < 1 || count > stats.Total {
			return "", protocol
		}
	}
	versions := make([]string, stats.Total)
	for i, createdText := range stats.Created {
		created, err := time.Parse(time.RFC3339Nano, createdText)
		if err != nil || (!filter.CreatedFrom.IsZero() && created.Before(filter.CreatedFrom)) ||
			(!filter.CreatedUntil.IsZero() && !created.Before(filter.CreatedUntil)) {
			return "", protocol
		}
		updated, err := time.Parse(time.RFC3339Nano, stats.Updated[i])
		if err != nil {
			return "", protocol
		}
		versions[i] = created.UTC().Format(time.RFC3339Nano) + "/" + updated.UTC().Format(time.RFC3339Nano)
	}
	sort.Strings(versions)
	tags := stats.Tags
	if tags == nil {
		tags = map[string]int{}
	}
	data, err := json.Marshal([]any{stats.Name, stats.Total, tags, versions})
	if err != nil {
		return "", protocol
	}
	if len(data) > memosLiveMaxRetainedBytes {
		return "", upstreamError("LIVE_LIST_LIMIT", "Source statistics exceeded the byte budget")
	}
	return hashBytes(data), nil
}

func (s *NoteService) aggregateStats(ctx context.Context, state string, filter LiveFilter) (SourceStats, error) {
	args, err := normalizeLiveList(NoteList{State: state})
	if err != nil {
		return SourceStats{}, err
	}
	source := s.Source.(AggregateStatsSource)
	if err := s.Verify(ctx); err != nil {
		return SourceStats{}, err
	}
	first, err := source.Stats(ctx, args.State, filter)
	if err != nil {
		return SourceStats{}, err
	}
	version, err := validateSourceStats(first, s.Settings.Memos.User, filter)
	if err != nil {
		return SourceStats{}, err
	}
	again, err := source.Stats(ctx, args.State, filter)
	if err != nil {
		return SourceStats{}, err
	}
	rechecked, err := validateSourceStats(again, s.Settings.Memos.User, filter)
	if err != nil {
		return SourceStats{}, err
	}
	if version != rechecked {
		return SourceStats{}, failure("SOURCE_CHANGED", "Memos statistics changed during aggregation", 409, true)
	}
	if err := s.Verify(ctx); err != nil {
		return SourceStats{}, err
	}
	return first, ctx.Err()
}

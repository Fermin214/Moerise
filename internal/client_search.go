package moerise

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"
)

const clientSearchTokenBytes = 128 * 1024

type ClientSearchRequest struct {
	Query     string   `json:"query,omitempty"`
	Mode      string   `json:"mode,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	State     string   `json:"state,omitempty"`
	Date      string   `json:"date,omitempty"`
	PageSize  int      `json:"page_size,omitempty"`
	PageToken string   `json:"page_token,omitempty"`
}

type ClientSearchPage struct {
	NotePage
	Mode     string         `json:"mode"`
	Semantic SearchSemantic `json:"semantic"`
}

type clientSearchSource struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

type clientSearchIndex struct {
	Generation string `json:"generation"`
	Revision   int64  `json:"revision"`
	Vector     string `json:"vector"`
	Space      string `json:"space"`
	Enabled    bool   `json:"enabled"`
}

type clientSearchCursor struct {
	Kind     string               `json:"kind"`
	Context  []string             `json:"context"`
	Filter   string               `json:"filter"`
	Expires  int64                `json:"expires"`
	Index    clientSearchIndex    `json:"index"`
	Sources  []clientSearchSource `json:"sources"`
	Mode     string               `json:"mode"`
	Semantic SearchSemantic       `json:"semantic"`
}

func (s *SearchService) clientIndex(ctx context.Context) (clientSearchIndex, error) {
	index := clientSearchIndex{Space: embeddingSpace(s.Store.Settings.Embedding),
		Enabled: s.Store.Settings.Embedding.Enabled}
	err := s.Store.index.QueryRowContext(ctx, `SELECT p.generation, p.revision, COALESCE(
		(SELECT id FROM vector_generations WHERE instance_id = p.instance_id
			AND user_name = ? AND status = 'active'), '')
		FROM projection_meta p WHERE p.instance_id = ?`,
		s.Store.Settings.Memos.User, s.Store.Settings.Memos.InstanceID).
		Scan(&index.Generation, &index.Revision, &index.Vector)
	return index, err
}

func (a *ClientAPI) searchRoute(w http.ResponseWriter, r *http.Request) (any, int, error) {
	if r.Method != http.MethodPost {
		return nil, 0, failure("METHOD_NOT_ALLOWED", "Method Not Allowed", 405, false)
	}
	body := ClientSearchRequest{Mode: "keyword", State: "NORMAL", PageSize: 20}
	if err := readClientJSON(w, r, 192*1024,
		[]string{"query", "mode", "tags", "state", "date", "page_size", "page_token"}, &body); err != nil {
		return nil, 0, err
	}
	if body.PageSize < 1 || body.PageSize > 100 || body.Mode == "" || body.State == "" {
		return nil, 0, failure("INVALID_ARGUMENT", "Invalid search parameters", 422, false)
	}
	page, err := a.app.Search.ClientPage(r.Context(), a.app.Notes, body)
	for i := range page.Notes {
		page.Notes[i] = clientNote(page.Notes[i])
	}
	return page, 200, err
}

func (s *SearchService) ClientPage(ctx context.Context, notes *NoteService, input ClientSearchRequest) (ClientSearchPage, error) {
	result := ClientSearchPage{NotePage: NotePage{Notes: []Note{}}, Mode: input.Mode}
	if input.Mode == "" {
		input.Mode = "keyword"
	}
	if input.PageSize == 0 {
		input.PageSize = 20
	}
	args, err := normalizeLiveList(NoteList{Query: input.Query, Tags: input.Tags, State: input.State,
		Date: input.Date, PageSize: input.PageSize})
	if err != nil {
		return result, err
	}
	if !slices.Contains([]string{"keyword", "semantic", "hybrid"}, input.Mode) || len(input.PageToken) > clientSearchTokenBytes {
		return result, failure("INVALID_ARGUMENT", "Invalid client search parameters", 422, false)
	}
	if input.Mode == "keyword" {
		args.PageToken = input.PageToken
		result.NotePage, err = notes.ListLive(ctx, args)
		result.Mode, result.Semantic.Reason = "keyword", "KEYWORD_MODE"
		return result, err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return result, failure("INVALID_ARGUMENT", "Enhanced search requires a query", 422, false)
	}
	filter, err := notes.liveFilter(args)
	if err != nil {
		return result, err
	}
	request := SearchRequest{Query: query, Mode: input.Mode, Tags: args.Tags, State: args.State,
		createdFrom: &filter.CreatedFrom, createdUntil: &filter.CreatedUntil}
	if args.Date == "" {
		request.createdFrom, request.createdUntil = nil, nil
	}
	binding := hashBytes(canonicalJSON([]any{query, input.Mode, args.Tags, args.State, args.Date, notes.Settings.Timezone}))
	index, err := s.clientIndex(ctx)
	if err != nil {
		return result, err
	}
	cursor := clientSearchCursor{Kind: "client-search-v1", Context: notes.liveContext(), Filter: binding,
		Expires: time.Now().Add(10 * time.Minute).Unix(), Index: index}
	if input.PageToken != "" {
		if notes.readCursorLimit(input.PageToken, &cursor, clientSearchTokenBytes) != nil ||
			cursor.Kind != "client-search-v1" || !slices.Equal(cursor.Context, notes.liveContext()) ||
			cursor.Filter != binding || cursor.Expires <= time.Now().Unix() || cursor.Index != index ||
			len(cursor.Sources) == 0 || len(cursor.Sources) > 3*searchCandidates {
			return result, failure("INVALID_PAGE_TOKEN", "Search page token is invalid or expired", 422, false)
		}
	}
	if err := notes.Verify(ctx); err != nil {
		return result, err
	}
	if input.PageToken == "" {
		candidates, mode, semantic, err := s.rankedCandidates(ctx, request)
		if err != nil {
			return result, err
		}
		cursor.Mode, cursor.Semantic = mode, semantic
		seen := map[string]bool{}
		for _, candidate := range candidates {
			if !seen[candidate.name] {
				seen[candidate.name] = true
				cursor.Sources = append(cursor.Sources, clientSearchSource{candidate.name, candidate.sourceHash})
			}
		}
	}
	result.Mode, result.Semantic = cursor.Mode, cursor.Semantic
	consumed := 0
	for _, source := range cursor.Sources {
		if len(result.Notes) >= args.PageSize {
			break
		}
		consumed++
		memo, err := notes.fresh(ctx, source.Name)
		if err != nil {
			return ClientSearchPage{}, err
		}
		if memo == nil || memo.Name != source.Name || memo.SourceHash != source.Hash ||
			!searchAccepts(request, memo.State, memo.Tags) || !searchDateAccepts(request, memo.CreateTime) {
			continue
		}
		result.Notes = append(result.Notes, memo.Note(notes.Settings))
	}
	if err := notes.Verify(ctx); err != nil {
		return ClientSearchPage{}, err
	}
	current, err := s.clientIndex(ctx)
	if err != nil {
		return ClientSearchPage{}, err
	}
	if current != index {
		return ClientSearchPage{}, failure("SOURCE_CHANGED", "Search sources changed; refresh the search", 409, true)
	}
	if consumed < len(cursor.Sources) {
		// 只携带签名的排序和来源摘要，不在分页状态中保存笔记正文。
		cursor.Sources = cursor.Sources[consumed:]
		result.NextPageToken = notes.signCursor(cursor)
		if len(result.NextPageToken) > clientSearchTokenBytes {
			return ClientSearchPage{}, failure("SEARCH_RESOURCE_LIMIT", "Search pagination exceeded its transport budget", 503, true)
		}
	}
	return result, nil
}

package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type privateMCPResponse struct{ http.ResponseWriter }

func (w privateMCPResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w privateMCPResponse) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
}
func (w privateMCPResponse) Write(data []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	return w.ResponseWriter.Write(data)
}

type reviewBatchArgs struct {
	BatchID string `json:"batch_id" jsonschema:"Review batch UUID"`
}

type memoArgs struct {
	Name string `json:"name" jsonschema:"Memos resource name, such as memos/abc"`
}

type listMemosArgs struct {
	State     string `json:"state,omitempty" jsonschema:"NORMAL (default) or ARCHIVED"`
	PageSize  *int   `json:"page_size,omitempty" jsonschema:"Maximum notes per page, default 20, range 1 to 100"`
	PageToken string `json:"page_token,omitempty" jsonschema:"Opaque next_page_token from a previous notes page"`
}

func addDomainTool[I, O any](server *mcp.Server, name, description string,
	call func(context.Context, I) (O, error)) {
	destructive := false
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true,
			IdempotentHint: true, DestructiveHint: &destructive}},
		func(ctx context.Context, _ *mcp.CallToolRequest, args I) (*mcp.CallToolResult, any, error) {
			batch, err := call(ctx, args)
			if err == nil {
				return nil, batch, nil
			}
			var service *ServiceError
			if !errors.As(err, &service) {
				service = &ServiceError{"INTERNAL", "Internal service error", 500, true}
			}
			body, _ := json.Marshal(map[string]any{"error": map[string]any{
				"code": service.Code, "message": service.Message, "retryable": service.Retryable,
			}})
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil, nil
		})
}

func (a *API) newMCPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		access, err := a.authenticate(request, "")
		if err != nil {
			return nil
		}
		server := mcp.NewServer(&mcp.Implementation{Name: "moerise", Version: Version}, nil)
		if slices.Contains(access.Scopes, "notes:read") {
			addDomainTool(server, "get_memo", "读取指定本人笔记的最新全文与附件，不记录阅读。",
				func(ctx context.Context, args memoArgs) (Note, error) {
					return a.Notes.Get(ctx, args.Name)
				})
			addDomainTool(server, "list_memos", "分页读取本人笔记，默认正常状态；复用笔记 REST 接口的分页游标，不记录阅读。",
				func(ctx context.Context, args listMemosArgs) (NotePage, error) {
					list := NoteList{State: args.State, PageSize: 20, PageToken: args.PageToken}
					if list.State == "" {
						list.State = "NORMAL"
					}
					if args.PageSize != nil {
						if *args.PageSize < 1 || *args.PageSize > 100 {
							return NotePage{}, failure("INVALID_ARGUMENT", "Invalid page size", 422, false)
						}
						list.PageSize = *args.PageSize
					}
					return a.Notes.ListIndexed(ctx, list)
				})
			addDomainTool(server, "semantic_walk", "随机开始或在语义候选中随机前进；同次路径去重，不按标签或阅读历史加权，不记录阅读。",
				func(ctx context.Context, args WalkRequest) (WalkResponse, error) {
					result, err := a.Search.Walk(ctx, args)
					return result, walkError(err)
				})
			addDomainTool(server, "get_related_memos", "从指定本人笔记寻找相关笔记，排除自身、合并重复片段并复查来源。",
				func(ctx context.Context, args RelatedRequest) (RelatedResponse, error) {
					return a.Search.Related(ctx, args)
				})
			addDomainTool(server, "semantic_search", "按中文关键词、语义与标签检索本人笔记，返回命中位置并复查来源。",
				func(ctx context.Context, args SearchRequest) (SearchResponse, error) {
					return a.Search.Search(ctx, args)
				})
		}
		if slices.Contains(access.Scopes, "reviews:read") {
			addDomainTool(server, "get_review_batch", "读取已有回顾批次，复查来源原文与作者。",
				func(ctx context.Context, args reviewBatchArgs) (ReviewBatch, error) {
					return a.Reviews.Get(ctx, args.BatchID)
				})
			addDomainTool(server, "read_daily_review", "读取已有当日回顾；尚未创建时返回 REVIEW_NOT_FOUND。",
				func(ctx context.Context, _ struct{}) (ReviewBatch, error) {
					return a.Reviews.ReadDaily(ctx, time.Now())
				})
		}
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 65536,
		CrossOriginProtection: http.NewCrossOriginProtection()})
}

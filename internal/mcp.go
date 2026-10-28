package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
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

func (a *API) addAITool(server *mcp.Server, name, description string, insight, timeline bool) {
	destructive := false
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: false, DestructiveHint: &destructive}},
		func(ctx context.Context, call *mcp.CallToolRequest, _ AIRequest) (*mcp.CallToolResult, any, error) {
			request, err := readAIRequest(bytes.NewReader(call.Params.Arguments))
			if err == nil {
				request.Stream = false
				request.Timeline = request.Timeline || timeline
				var result AIResult
				result, err = a.invokeAI(ctx, insight, request, nil)
				if err == nil && !result.Completed {
					err = aiOutputError()
				}
				if err == nil {
					return &mcp.CallToolResult{StructuredContent: result}, result, nil
				}
			}
			service := aiPublicError(err)
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
		if slices.Contains(access.Scopes, "notes:read") && slices.Contains(access.Scopes, "ai:generate") {
			a.addAITool(server, "analyze_memos", "分析明确选定的本人笔记并返回可核对的引用；调用模型并产生用量，不保存分析结果。", true, false)
			a.addAITool(server, "ask_memos", "根据本人笔记回答问题并返回引用及证据缺口；调用模型并产生用量，不保存回答。", false, false)
			a.addAITool(server, "get_topic_timeline", "根据本人笔记生成主题时间线，区分记录时间与事件时间；调用模型并产生用量，不保存结果。", false, true)
		}
		if slices.Contains(access.Scopes, "notes:read") {
			addDomainTool(server, "get_memo", "读取指定本人笔记的最新全文与附件，不记录阅读。",
				func(ctx context.Context, args memoArgs) (Note, error) {
					if !memoPattern.MatchString(args.Name) {
						return Note{}, failure("INVALID_RESOURCE", "Invalid memo resource name", 422, false)
					}
					if err := a.verifySource(ctx); err != nil {
						return Note{}, err
					}
					memo, err := a.liveMemo(ctx, args.Name)
					if err != nil {
						return Note{}, err
					}
					if memo == nil {
						return Note{}, failure("SOURCE_NOT_FOUND", "Memo is unavailable", 404, false)
					}
					return memo.Note(a.Settings), nil
				})
			addDomainTool(server, "list_memos", "分页读取本人笔记，默认正常状态；复用笔记 REST 接口的分页游标，不记录阅读。",
				func(ctx context.Context, args listMemosArgs) (any, error) {
					query := url.Values{"state": {args.State}, "page_token": {args.PageToken}}
					if args.PageSize != nil {
						query.Set("page_size", strconv.Itoa(*args.PageSize))
					}
					request, err := http.NewRequestWithContext(ctx, http.MethodGet, "/api/ext/v1/notes?"+query.Encode(), nil)
					if err != nil {
						return nil, err
					}
					result, _, err := a.notes(request)
					return result, err
				})
			addDomainTool(server, "semantic_walk", "沿语义继续话题、换角度或随机跳转；按路径去重，不记录阅读。",
				a.Search.Walk)
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

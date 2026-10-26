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

type reviewReplaceArgs struct {
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Stable key reused for retries of the same replacement"`
}

type reviewMarkArgs struct {
	BatchID string `json:"batch_id" jsonschema:"Review batch UUID"`
	Name    string `json:"name" jsonschema:"Memos resource name, such as memos/abc"`
}

func addServiceTool[I, O any](server *mcp.Server, name, description string, readOnly bool,
	call func(context.Context, I) (O, error)) {
	destructive := false
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly,
			IdempotentHint: readOnly || name == "open_review_memo" || name == "skip_review_memo", DestructiveHint: &destructive}},
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
			addServiceTool(server, "semantic_search", "混合中文关键词、语义与标签检索；返回原文位置与降级原因，不记录阅读。", true, a.Search.Search)
			addServiceTool(server, "get_related_memos", "按语义寻找相关笔记，复查作者和原文；需配置 embedding。", true, a.Search.Related)
			addServiceTool(server, "semantic_walk", "沿语义漫步：continue 继续话题，angle 换角度，random 随机跳转；path 去重，不改变阅读状态。", true, a.Search.Walk)
		}
		if slices.Contains(access.Scopes, "discovery:write") {
			addServiceTool(server, "open_discovery_memo", "打开并持久记录最近阅读，供后续漫步降权；会改变产品状态。", false,
				func(ctx context.Context, args struct {
					Name string `json:"name"`
				}) (Note, error) {
					return a.Search.Open(ctx, args.Name)
				})
		}
		if slices.Contains(access.Scopes, "reviews:read") {
			addServiceTool(server, "get_review_batch", "读取已有回顾批次，复查来源原文与作者。", true,
				func(ctx context.Context, args reviewBatchArgs) (ReviewBatch, error) {
					return a.Reviews.Get(ctx, args.BatchID)
				})
			addServiceTool(server, "read_daily_review", "读取已有当日回顾；尚未创建时返回 REVIEW_NOT_FOUND。", true,
				func(ctx context.Context, _ struct{}) (ReviewBatch, error) {
					return a.Reviews.ReadDaily(ctx, time.Now())
				})
		}
		if slices.Contains(access.Scopes, "reviews:write") {
			addServiceTool(server, "get_daily_review", "获取当日回顾；首次调用会创建并持久保存批次及选中历史。", false,
				func(ctx context.Context, _ struct{}) (ReviewBatch, error) {
					return a.Reviews.Daily(ctx, time.Now())
				})
			addServiceTool(server, "replace_daily_review", "换一组并持久保存新批次；同日相同幂等键重试返回同一批次。", false,
				func(ctx context.Context, args reviewReplaceArgs) (ReviewBatch, error) {
					return a.Reviews.Replace(ctx, args.IdempotencyKey, time.Now())
				})
			for _, action := range []string{"open", "skip"} {
				addServiceTool(server, action+"_review_memo", "持久记录回顾条目的 "+action+" 操作，保留首次发生时间。", false,
					func(ctx context.Context, args reviewMarkArgs) (ReviewBatch, error) {
						return a.Reviews.Mark(ctx, args.BatchID, args.Name, action, time.Now())
					})
			}
		}
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 65536,
		CrossOriginProtection: http.NewCrossOriginProtection()})
}

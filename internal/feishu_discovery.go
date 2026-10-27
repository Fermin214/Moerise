package moerise

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *FeishuService) renderWalk(ctx context.Context, resource string) (string, error) {
	id, encoded, ok := strings.Cut(resource, ".")
	if !ok {
		return "", upstreamError("CHANNEL_QUEUE_KEY", "Invalid encrypted discovery request")
	}
	sealed, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", err
	}
	body, err := s.unseal("walk:"+id, sealed)
	if err != nil {
		return "", err
	}
	var request WalkRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return "", err
	}
	result, err := s.Search.Walk(ctx, request)
	switch errorCode(err) {
	case "SOURCE_NOT_FOUND":
		return "笔记已删除或不可访问。", nil
	case "EMBEDDING_DISABLED", "INDEX_NOT_READY", "NO_SOURCE_VECTORS":
		return "语义漫步暂不可用（" + errorCode(err) + "）。可发送“随机跳转”继续浏览。", nil
	}
	if err != nil {
		return "", err
	}
	var text strings.Builder
	text.WriteString("笔记漫步\n")
	if result.Item != nil {
		note := result.Item.Note
		fmt.Fprintf(&text, "\n%s\n%s\n%s\n%s\n", note.CreateTime, excerpt(note.Content, 300), note.Name, note.URL)
	} else {
		text.WriteString("没有符合条件的可访问笔记。\n")
	}
	if len(result.Path) > 0 {
		text.WriteString("\n路径：" + strings.Join(result.Path, ",") +
			"\n继续时将路径复制到命令末尾，可避免重复。\n")
	}
	text.WriteString("\n回复“查看 memos/资源标识”打开并记录阅读。")
	return text.String(), nil
}

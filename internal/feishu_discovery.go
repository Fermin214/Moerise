package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type discoveryReference struct {
	Names    []string `json:"names"`
	Path     []string `json:"path,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Persist only references. Replies are rendered against the live source when sent.
func discoveryReferences(hits []SearchHit, path, warnings []string) (string, string, error) {
	reference := discoveryReference{Names: []string{}, Path: path, Warnings: warnings}
	for _, hit := range hits {
		reference.Names = append(reference.Names, hit.Note.Name)
	}
	data, err := json.Marshal(reference)
	return "discovery", string(data), err
}

func (s *FeishuService) renderDiscovery(ctx context.Context, resource string) (string, error) {
	var reference discoveryReference
	if json.Unmarshal([]byte(resource), &reference) != nil || len(reference.Names) > 20 || len(reference.Path) > 100 {
		return "", searchArgumentError()
	}
	if err := s.Search.verify(ctx); err != nil {
		return "", err
	}
	var text strings.Builder
	text.WriteString("笔记发现\n")
	count := 0
	for _, name := range reference.Names {
		if !memoPattern.MatchString(name) {
			return "", searchArgumentError()
		}
		memo, err := s.Search.live(ctx, name)
		if err != nil {
			return "", err
		}
		if memo == nil || memo.State != "NORMAL" {
			continue
		}
		count++
		fmt.Fprintf(&text, "\n%s\n%s\n%s\n%s\n", memo.CreateTime, excerpt(memo.Content, 300), memo.Name, memo.Note(s.Store.Settings).URL)
	}
	if count == 0 {
		text.WriteString("没有符合条件的可访问笔记。\n")
	}
	if len(reference.Warnings) > 0 {
		text.WriteString("\n语义检索暂不可用，本次使用关键词检索。\n")
	}
	if len(reference.Path) > 0 {
		text.WriteString("\n路径：" + strings.Join(reference.Path, ",") + "\n继续时将路径复制到命令末尾，可避免重复。\n")
	}
	text.WriteString("\n回复“查看 memos/资源标识”打开并记录阅读。")
	if err := s.Search.verify(ctx); err != nil {
		return "", err
	}
	return text.String(), nil
}

package moerise

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestFeishuQueuedWalkUnavailableReplies(t *testing.T) {
	for _, reason := range []string{"EMBEDDING_DISABLED", "INDEX_NOT_READY", "NO_SOURCE_VECTORS"} {
		t.Run(reason, func(t *testing.T) {
			search, index, memos := discoveryFixture(t)
			service := &FeishuService{Store: search.Store, Search: search}
			sealed, err := service.seal("walk:om_unavailable",
				canonicalJSON(WalkRequest{Name: "memos/a", Mode: "continue"}))
			if err != nil {
				t.Fatal(err)
			}
			resource := "om_unavailable." + base64.RawURLEncoding.EncodeToString(sealed)
			switch reason {
			case "EMBEDDING_DISABLED":
				search.Store.Settings.Embedding.Enabled = false
			case "INDEX_NOT_READY":
				search.Store.Settings.Embedding.Model = stringPointer("different-model")
			case "NO_SOURCE_VECTORS":
				memos["memos/a"]["content"] = " \n\t"
				refreshIndexMemo(t, search.Store, memos["memos/a"])
				finishIndex(t, index)
			}
			text, err := service.render(context.Background(), "walk", resource)
			if err != nil || !strings.Contains(text, reason) || strings.Contains(text, "随机跳转") ||
				strings.Contains(text, "seed") {
				t.Fatalf("unavailable walk must reply without a result: %q %v", text, err)
			}
		})
	}
}

func TestFeishuQueuedWalkDoesNotSuggestRetiredCommands(t *testing.T) {
	search, _, _ := discoveryFixture(t)
	service := &FeishuService{Store: search.Store, Search: search}
	sealed, err := service.seal("walk:om_legacy", canonicalJSON(WalkRequest{Name: "memos/a", Path: []string{"memos/a"}}))
	if err != nil {
		t.Fatal(err)
	}
	text, err := service.render(context.Background(), "walk", "om_legacy."+base64.RawURLEncoding.EncodeToString(sealed))
	if err != nil || !strings.Contains(text, "笔记漫步") {
		t.Fatalf("legacy walk recovery: %q %v", text, err)
	}
	for _, hint := range []string{"随机跳转", "命令", "回复", "查看 memos/", "记录阅读"} {
		if strings.Contains(text, hint) {
			t.Fatalf("legacy walk suggests a retired command: %q", text)
		}
	}
}

package moerise

import (
	"context"
	"strings"
	"testing"
)

func TestFeishuWalkUnavailableReplies(t *testing.T) {
	for _, reason := range []string{"EMBEDDING_DISABLED", "INDEX_NOT_READY", "NO_SOURCE_VECTORS"} {
		t.Run(reason, func(t *testing.T) {
			search, index, memos := discoveryFixture(t)
			service := &FeishuService{Store: search.Store, Search: search}
			kind, resource, err := service.execute(context.Background(),
				feishuMessage{ID: "om_unavailable", Type: "text",
					Content: string(canonicalJSON(map[string]string{"text": "继续这个话题 memos/a"}))})
			if err != nil {
				t.Fatal(err)
			}
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
			text, err := service.render(context.Background(), kind, resource)
			if err != nil || !strings.Contains(text, reason) || !strings.Contains(text, "随机跳转") ||
				strings.Contains(text, "seed") {
				t.Fatalf("unavailable walk must reply without a result: %q %v", text, err)
			}
		})
	}
}

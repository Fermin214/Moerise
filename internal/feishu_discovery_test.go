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
			if err != nil || !strings.Contains(text, reason) || !strings.Contains(text, "随机跳转") ||
				strings.Contains(text, "seed") {
				t.Fatalf("unavailable walk must reply without a result: %q %v", text, err)
			}
		})
	}
}

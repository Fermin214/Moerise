package moerise

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// 常规联调的短笔记无法覆盖 32 MiB 响应边界。
func TestMemosOversizedPagesShrinkWithoutLosingNotes(t *testing.T) {
	settings := testSettings(t)
	settings.Memos.PageSize = 1000
	content := strings.Repeat("x", 60*1024)
	var requested []int
	client := fakeClient(settings, func(r *http.Request) (int, any, error) {
		query := r.URL.Query()
		if r.URL.Path != "/api/v1/memos" || query.Get("state") != "NORMAL" ||
			query.Get("filter") != "creator == 'users/owner'" || query.Get("orderBy") != "update_time asc" {
			t.Fatalf("pagination changed source scope: %s", r.URL)
		}
		size, _ := strconv.Atoi(query.Get("pageSize"))
		offset, _ := strconv.Atoi(query.Get("pageToken"))
		requested = append(requested, size)
		rows := []any{}
		for i := offset; i < min(offset+size, 600); i++ {
			rows = append(rows, rawMemo(fmt.Sprintf("note-%03d", i), content))
		}
		next := ""
		if offset+size < 600 {
			next = strconv.Itoa(offset + size)
		}
		return 200, map[string]any{"memos": rows, "nextPageToken": next}, nil
	})
	defer client.Close()
	seen := map[string]bool{}
	token := ""
	for {
		notes, next, err := client.ListPage(context.Background(), "NORMAL", nil, token)
		if err != nil {
			t.Fatal(err)
		}
		for _, note := range notes {
			if seen[note.Name] || note.Content != content {
				t.Fatalf("duplicate or changed memo: %s", note.Name)
			}
			seen[note.Name] = true
		}
		if next == "" {
			break
		}
		token = next
	}
	if len(seen) != 600 || !slices.Equal(requested, []int{1000, 500, 500}) {
		t.Fatalf("notes=%d requested page sizes=%v", len(seen), requested)
	}
}

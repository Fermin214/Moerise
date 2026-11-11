package moerise

import (
	"context"
	"net/http"
	"testing"
)

type paginationRestartSource struct {
	Source
	list func(string, *int64, string) ([]Memo, string, error)
}

func (s paginationRestartSource) ListPage(_ context.Context, state string, since *int64, token string) ([]Memo, string, error) {
	return s.list(state, since, token)
}

// 追补失败后必须重扫完整范围，并清除前一遍已消失的暂存笔记。
func TestSyncPaginationRestartDiscardsBothScanPasses(t *testing.T) {
	for _, phase := range []string{"full", "catchup"} {
		t.Run(phase, func(t *testing.T) {
			settings := testSettings(t)
			store := testStore(t, settings)
			client := fakeClient(settings, func(r *http.Request) (int, any, error) {
				if status, body, ok := identityResponse(r); ok {
					return status, body, nil
				}
				t.Errorf("unexpected source request: %s", r.URL)
				return 500, nil, nil
			})
			old, fresh, gone := testMemo(t, "old", "outside catchup"), testMemo(t, "fresh", "new"), testMemo(t, "gone", "deleted during retry")
			restarted, fullReads := false, 0
			source := paginationRestartSource{Source: client, list: func(state string, since *int64, token string) ([]Memo, string, error) {
				names, err := store.Names()
				if err != nil || len(names) != 0 {
					t.Fatalf("partial scan published: %v %v", names, err)
				}
				if state == "ARCHIVED" {
					return nil, "", nil
				}
				if since == nil && token == "" {
					fullReads++
				}
				if !restarted && ((phase == "full") == (since == nil)) {
					if token == "" {
						return []Memo{gone}, "oversized-next", nil
					}
					restarted = true
					return nil, "", errMemosPaginationRestart
				}
				if since == nil {
					return []Memo{old, fresh}, "", nil
				}
				return []Memo{fresh}, "", nil
			}}
			if _, err := NewSyncService(store, source).Sync(t.Context(), Job{Mode: "full"}); err != nil {
				t.Fatal(err)
			}
			names, err := store.Names()
			if err != nil || len(names) != 2 || !names[old.Name] || !names[fresh.Name] || names[gone.Name] || fullReads != 2 {
				t.Fatalf("restart lost the full scan or retained deleted data: %v reads=%d error=%v", names, fullReads, err)
			}
			status, err := store.Status()
			if err != nil || status.Watermark == nil {
				t.Fatalf("completed scan did not advance watermark: %v", err)
			}
		})
	}
}

func TestSyncMinimumPageRestartStopsWithoutProgress(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	client := fakeClient(settings, func(r *http.Request) (int, any, error) {
		status, body, _ := identityResponse(r)
		return status, body, nil
	})
	attempts := 0
	source := paginationRestartSource{Source: client, list: func(string, *int64, string) ([]Memo, string, error) {
		attempts++
		return nil, "", errMemosPaginationRestartAtMinimum
	}}
	_, err := NewSyncService(store, source).Sync(t.Context(), Job{Mode: "full"})
	if err != errMemosResponseTooLarge || attempts != 2 {
		t.Fatalf("minimum-page retry did not terminate: attempts=%d error=%v", attempts, err)
	}
	status, err := store.Status()
	if err != nil || status.Watermark != nil {
		t.Fatalf("failed scan advanced watermark: %+v %v", status, err)
	}
}

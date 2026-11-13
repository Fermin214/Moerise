package moerise

import (
	"net/http"
	"strings"
	"testing"
)

func sourceFixture(t *testing.T, memos map[string]map[string]any) (*Store, Source) {
	t.Helper()
	settings := testSettings(t)
	store := testStore(t, settings)
	for _, memo := range memos {
		refreshIndexMemo(t, store, memo)
	}
	source := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if raw := memos[strings.TrimPrefix(r.URL.Path, "/api/v1/")]; raw != nil {
			return 200, raw, nil
		}
		return 404, nil, nil
	})
	return store, source
}

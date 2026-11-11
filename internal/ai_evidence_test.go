package moerise

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func evidenceFixture(t *testing.T, memos map[string]map[string]any) *EvidenceService {
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
	return NewEvidenceService(NewSearchService(store, source, nil))
}

func legacyEvidence(t *testing.T, search *SearchService, name, id string) Evidence {
	t.Helper()
	memo, err := search.Source.Get(t.Context(), name)
	if err != nil || memo == nil {
		t.Fatal("legacy source fixture", err)
	}
	note := memo.Note(search.Store.Settings)
	return Evidence{ID: id, Name: name, SourceHash: note.SourceHash, Content: note.Content,
		ByteStart: 0, ByteEnd: len(note.Content), LineStart: 1,
		LineEnd: 1 + strings.Count(strings.TrimSuffix(note.Content, "\n"), "\n"),
		URL:     note.URL, CreateTime: note.CreateTime, UpdateTime: note.UpdateTime}
}

func TestLegacyEvidenceVerifiesIdentityAndRejectsForgedFragments(t *testing.T) {
	memos := map[string]map[string]any{
		"memos/seed": rawMemo("seed", "\u6c49\nsecond\n"),
		"memos/copy": rawMemo("copy", "another note"),
	}
	service := evidenceFixture(t, memos)
	ctx := context.Background()
	collected := []Evidence{legacyEvidence(t, service.Search, "memos/seed", "E1"), legacyEvidence(t, service.Search, "memos/copy", "E2")}
	for _, alter := range []func(*Evidence){
		func(e *Evidence) { e.ByteStart = -1 },
		func(e *Evidence) { e.ByteStart = 1 },
		func(e *Evidence) { e.ByteStart = e.ByteEnd },
		func(e *Evidence) { e.ByteEnd++ },
		func(e *Evidence) { e.ByteEnd = 2 },
		func(e *Evidence) { e.LineStart++ },
		func(e *Evidence) { e.LineEnd++ },
		func(e *Evidence) { e.Content = "invented" },
		func(e *Evidence) { e.URL = "https://foreign.example/" },
		func(e *Evidence) { e.ID = "E0" },
		func(e *Evidence) { e.Name = "../seed" },
		func(e *Evidence) { e.CreateTime = "2019-01-01T00:00:00Z" },
		func(e *Evidence) { e.UpdateTime = "2019-01-01T00:00:00Z" },
	} {
		items := slices.Clone(collected)
		alter(&items[0])
		if err := service.Verify(ctx, items); errorCode(err) != "INVALID_EVIDENCE" {
			t.Fatalf("forged fragment accepted: %+v %v", items[0], err)
		}
	}
	for _, when := range []int{1, 2} {
		calls := 0
		service.Search.Source = fakeClient(service.Search.Store.Settings, func(r *http.Request) (int, any, error) {
			if strings.HasSuffix(r.URL.Path, "/auth/me") {
				calls++
				if calls == when {
					return 200, map[string]any{"user": map[string]string{"name": "users/other"}}, nil
				}
			}
			if status, body, ok := identityResponse(r); ok {
				return status, body, nil
			}
			return 200, memos[strings.TrimPrefix(r.URL.Path, "/api/v1/")], nil
		})
		if err := service.Verify(ctx, collected); errorCode(err) != "IDENTITY_MISMATCH" {
			t.Fatalf("identity change escaped source checks: %v", err)
		}
	}
}

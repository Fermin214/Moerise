package moerise

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
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
	return NewEvidenceService(NewSearchService(store, source, nil), 32*1024)
}

func evidenceNames(items []Evidence) []string {
	names := []string{}
	for _, item := range items {
		if !slices.Contains(names, item.Name) {
			names = append(names, item.Name)
		}
	}
	return names
}

func TestEvidenceSelectedSourcesAndRelatedHistory(t *testing.T) {
	ctx := context.Background()
	memos := map[string]map[string]any{
		"memos/seed": rawMemo("seed", "garden notes"),
		"memos/copy": rawMemo("copy", "garden notes"),
	}
	for i := range 5 {
		name := fmt.Sprintf("history%d", i)
		memos["memos/"+name] = rawMemo(name, "garden notes")
	}
	service := evidenceFixture(t, memos)
	selected, err := service.Collect(ctx, AIRequest{Names: []string{"memos/seed", "memos/copy"}})
	if err != nil || !slices.Equal(evidenceNames(selected.Items), []string{"memos/seed", "memos/copy"}) ||
		selected.Items[0].Content != selected.Items[1].Content || selected.Items[0].ID != "E1" || selected.Items[1].ID != "E2" {
		t.Fatalf("explicit sources or equal-text provenance lost: %+v %v", selected, err)
	}
	single, err := service.Collect(ctx, AIRequest{Names: []string{"memos/seed"}})
	if err != nil || len(evidenceNames(single.Items)) != 4 || single.Items[0].Name != "memos/seed" ||
		single.Semantic.Reason != "EMBEDDING_DISABLED" {
		t.Fatalf("required seed and at most three history notes: %+v %v", single, err)
	}
	answer, err := service.Collect(ctx, AIRequest{Question: "  garden  "})
	if err != nil || len(answer.Items) != 7 || answer.Semantic.Reason != "EMBEDDING_DISABLED" {
		t.Fatalf("shared hybrid search lost matching sources: %+v %v", answer, err)
	}
	// Repeated passages at different original offsets remain independently citable.
	memos["memos/seed"]["content"] = strings.Repeat(strings.Repeat("x", 2046)+"\n\n", 2)
	repeated, err := service.Collect(ctx, AIRequest{Names: []string{"memos/seed", "memos/copy"}})
	if err != nil || len(repeated.Items) != 3 || repeated.Items[0].Content != repeated.Items[1].Content ||
		repeated.Items[0].ByteStart == repeated.Items[1].ByteStart {
		t.Fatalf("source positions collapsed by body equality: %+v %v", repeated, err)
	}
}

func TestEvidenceFiltersApplyToSeedAndHistory(t *testing.T) {
	memos := map[string]map[string]any{}
	for _, name := range []string{"seed", "match", "archived", "other-tag", "early", "until"} {
		memos["memos/"+name] = rawMemo(name, "garden notes")
		memos["memos/"+name]["tags"] = []string{"topic"}
	}
	memos["memos/archived"]["state"] = "ARCHIVED"
	memos["memos/other-tag"]["tags"] = []string{"other"}
	memos["memos/early"]["createTime"] = "2020-01-01T07:59:59+08:00"
	memos["memos/until"]["createTime"] = "2020-01-02T08:00:00+08:00"
	service := evidenceFixture(t, memos)
	ctx := context.Background()
	request := AIRequest{Names: []string{"memos/seed"}, Tags: []string{"topic"},
		From: "2020-01-01T08:00:00+08:00", Until: "2020-01-02T00:00:00Z"}
	got, err := service.Collect(ctx, request)
	if err != nil || !slices.Equal(evidenceNames(got.Items), []string{"memos/seed", "memos/match"}) {
		t.Fatalf("filters did not restrict history: %+v %v", got, err)
	}
	for _, name := range []string{"archived", "other-tag", "early", "until"} {
		request.Names = []string{"memos/" + name}
		got, err := service.Collect(ctx, request)
		if err != nil || len(got.Items) != 0 || !slices.Contains(got.Omitted, request.Names[0]) {
			t.Fatalf("filtered seed returned evidence: %+v %v", got, err)
		}
	}
	request.Names, request.State = []string{"memos/archived"}, "ARCHIVED"
	got, err = service.Collect(ctx, request)
	if err != nil || !slices.Equal(evidenceNames(got.Items), []string{"memos/archived"}) {
		t.Fatalf("explicit archived source unavailable: %+v %v", got, err)
	}
	request.Names, request.State = []string{"memos/seed", "memos/archived"}, "ALL"
	got, err = service.Collect(ctx, request)
	if err != nil || len(got.Items) != 2 {
		t.Fatalf("explicit ALL unavailable: %+v %v", got, err)
	}
}

func TestEvidenceBudgetCountsJSONAndOnlyCropsRequiredSeed(t *testing.T) {
	ctx := context.Background()
	text := strings.Repeat("\u6c49\U0001f331\"\n<>&\t", 300)
	memos := map[string]map[string]any{
		"memos/seed": rawMemo("seed", text),
		"memos/tiny": rawMemo("tiny", "small"),
	}
	service := evidenceFixture(t, memos)
	full, err := service.Collect(ctx, AIRequest{Names: []string{"memos/tiny", "memos/seed"}})
	if err != nil {
		t.Fatal(err)
	}
	tinyJSON, _ := json.Marshal(full.Items[:1])
	service.Budget = len(tinyJSON)
	multi, err := service.Collect(ctx, AIRequest{Names: []string{"memos/seed", "memos/tiny"}})
	if err != nil || len(multi.Items) != 1 || multi.Items[0].Name != "memos/tiny" ||
		multi.Items[0].ID != "E1" || !slices.Contains(multi.Omitted, "memos/seed") {
		t.Fatalf("optional evidence was cropped or prevented a later fitting source: %+v %v", multi, err)
	}
	service.Budget = 512
	cropped, err := service.Collect(ctx, AIRequest{Names: []string{"memos/seed"}})
	if err != nil || len(cropped.Items) != 1 || cropped.Items[0].Name != "memos/seed" ||
		!slices.Contains(cropped.Omitted, "memos/seed") {
		t.Fatalf("oversized required seed was lost: %+v %v", cropped, err)
	}
	item := cropped.Items[0]
	data, _ := json.Marshal(cropped.Items)
	if len(data) > service.Budget || !utf8.ValidString(item.Content) || item.ByteEnd >= len(text) ||
		item.Content != text[item.ByteStart:item.ByteEnd] || item.LineEnd != strings.Count(strings.TrimSuffix(item.Content, "\n"), "\n")+1 {
		t.Fatalf("crop broke the JSON byte budget or original positions: %s", data)
	}
	if err := service.Verify(ctx, cropped.Items); err != nil {
		t.Fatal(err)
	}
	service.Budget = 1
	empty, err := service.Collect(ctx, AIRequest{Names: []string{"memos/seed"}})
	if errorCode(err) != "EVIDENCE_BUDGET_EXCEEDED" || len(empty.Items) != 0 {
		t.Fatalf("impossible required seed budget: %+v %v", empty, err)
	}
	service.Budget = len(tinyJSON)
	exact, err := service.Collect(ctx, AIRequest{Names: []string{"memos/tiny"}})
	data, _ = json.Marshal(exact.Items)
	if err != nil || len(exact.Items) != 1 || len(data) != service.Budget || len(exact.Omitted) != 0 {
		t.Fatalf("exact JSON budget rejected complete fragment: %+v %v", exact, err)
	}
	service.Budget--
	optional, err := service.Collect(ctx, AIRequest{Question: "small"})
	if err != nil || len(optional.Items) != 0 || !slices.Contains(optional.Omitted, "memos/tiny") {
		t.Fatalf("question evidence was shortened to fit: %+v %v", optional, err)
	}
}

func TestEvidenceDatesPrecedeEveryRecallAndNoteLimit(t *testing.T) {
	store, index, memos, _ := indexFixture(t)
	for name := range memos {
		delete(memos, name)
		if err := store.Refresh(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 105 {
		name := fmt.Sprintf("a%03d", i)
		raw := rawMemo(name, "\u82b1 needle")
		raw["tags"], raw["createTime"] = []string{"needle"}, "2010-01-01T00:00:00Z"
		memos["memos/"+name] = raw
		refreshIndexMemo(t, store, raw)
	}
	for _, name := range []string{"seed", "z-target"} {
		raw := rawMemo(name, "\u82b1 needle")
		raw["tags"] = []string{"needle"}
		memos["memos/"+name] = raw
		refreshIndexMemo(t, store, raw)
	}
	store.Settings.Embedding.BatchSize = 20
	provider := &searchEmbedding{dims: store.Settings.Embedding.Dimensions}
	index.Provider = provider
	finishIndex(t, index)
	service := NewEvidenceService(NewSearchService(store, index.Source, provider), 4096)
	ctx := context.Background()
	options, err := normalizeEvidence(AIRequest{Question: "needle", From: "2020-01-01T08:00:00+08:00", Until: "2021-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"needle", "\u82b1"} {
		for _, mode := range []string{"keyword", "semantic", "hybrid"} {
			options.Query, options.Mode, options.Limit = query, mode, 1
			got, err := service.Search.Search(ctx, options)
			if err != nil || len(got.Results) != 1 || got.Results[0].Note.Name != "memos/seed" {
				t.Fatalf("%s/%s filtered after 100 candidates or note cap: %+v %v", mode, query, got, err)
			}
		}
	}
	got, err := service.Collect(ctx, AIRequest{Question: "needle", From: "2020-01-01T00:00:00Z"})
	if err != nil || !slices.Equal(evidenceNames(got.Items), []string{"memos/seed", "memos/z-target"}) || !got.Semantic.Used {
		t.Fatalf("shared question recall applied dates too late: %+v %v", got, err)
	}
	calls := len(provider.purposes)
	got, err = service.Collect(ctx, AIRequest{Names: []string{"memos/seed"}, From: "2020-01-01T00:00:00Z"})
	if err != nil || !slices.Equal(evidenceNames(got.Items), []string{"memos/seed", "memos/z-target"}) ||
		!got.Semantic.Used || len(provider.purposes) != calls {
		t.Fatalf("related history applied dates too late or called query model: %+v %v", got, err)
	}
	wire, _ := json.Marshal(options)
	if strings.Contains(string(wire), "created") || strings.Contains(string(wire), "2020") {
		t.Fatalf("private dates changed the public search contract: %s", wire)
	}
	var public SearchRequest
	if err := json.Unmarshal([]byte(`{"query":"needle","from":"2020-01-01T00:00:00Z"}`), &public); err == nil {
		t.Fatal("public search accepted an evidence-only parameter")
	}
}

func TestEvidenceDateFractionBoundaries(t *testing.T) {
	memos := map[string]map[string]any{
		"memos/a": rawMemo("a", "needle"),
		"memos/b": rawMemo("b", "needle"),
		"memos/c": rawMemo("c", "needle"),
	}
	memos["memos/a"]["createTime"] = "2020-01-01T00:00:00.000001Z"
	memos["memos/b"]["createTime"] = "2020-01-01T08:00:00.000002+08:00"
	memos["memos/c"]["createTime"] = "2020-01-01T00:00:00.000003Z"
	service := evidenceFixture(t, memos)
	got, err := service.Collect(context.Background(), AIRequest{Question: "needle",
		From: "2020-01-01T00:00:00.000001001Z", Until: "2020-01-01T00:00:00.000003Z"})
	if err != nil || !slices.Equal(evidenceNames(got.Items), []string{"memos/b"}) {
		t.Fatalf("sub-millisecond interval lost half-open boundaries: %+v %v", got, err)
	}
}

func TestEvidenceRejectsInvalidParameters(t *testing.T) {
	service := evidenceFixture(t, map[string]map[string]any{})
	requests := []AIRequest{
		{}, {Question: " \t"}, {Question: "\xff"}, {Question: strings.Repeat("x", 257)},
		{Names: []string{"../seed"}}, {Names: []string{"memos/a", "memos/a"}},
		{Question: "x", Names: make([]string, 21)}, {Question: "x", State: "DELETED"},
		{Question: "x", Tags: []string{"bad tag"}}, {Question: "x", From: "2020-01-01"},
		{Question: "x", Until: "invalid"}, {Question: "x", From: "2020-01-02T00:00:00Z", Until: "2020-01-01T00:00:00Z"},
		{Question: "x", From: "2020-01-01T08:00:00+08:00", Until: "2020-01-01T00:00:00Z"},
		{Names: []string{"memos/a"}, Question: " "},
	}
	for _, request := range requests {
		got, err := service.Collect(context.Background(), request)
		serviceError, ok := err.(*ServiceError)
		if !ok || serviceError.Status != 422 || len(got.Items) != 0 {
			t.Fatalf("invalid parameters accepted: %+v %+v %v", request, got, err)
		}
	}
	got, err := service.Collect(context.Background(), AIRequest{Question: "  " + strings.Repeat("\U0001f331", 256) + "  "})
	if err != nil || len(got.Items) != 0 {
		t.Fatalf("legal 256-rune/1024-byte trimmed question rejected: %+v %v", got, err)
	}
}

func TestEvidenceSourceMutationDuringCollectionAndBeforeReuse(t *testing.T) {
	for _, phase := range []string{"collect", "reuse"} {
		for _, change := range []string{"edit", "delete", "foreign", "archive", "tags", "date", "outage"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				memos := map[string]map[string]any{
					"memos/a": rawMemo("a", "private original"),
					"memos/b": rawMemo("b", "another original"),
				}
				service := evidenceFixture(t, memos)
				mutate := func() {
					switch change {
					case "edit":
						memos["memos/a"]["content"] = "changed"
					case "delete":
						delete(memos, "memos/a")
					case "foreign":
						memos["memos/a"]["creator"] = "users/other"
					case "archive":
						memos["memos/a"]["state"] = "ARCHIVED"
					case "tags":
						memos["memos/a"]["tags"] = []string{"other"}
					case "date":
						memos["memos/a"]["createTime"] = "2019-01-01T00:00:00Z"
					}
				}
				reads, armed := 0, false
				service.Search.Source = fakeClient(service.Search.Store.Settings, func(r *http.Request) (int, any, error) {
					if status, body, ok := identityResponse(r); ok {
						return status, body, nil
					}
					name := strings.TrimPrefix(r.URL.Path, "/api/v1/")
					if name == "memos/a" {
						reads++
						if phase == "collect" && reads == 2 {
							armed = true
							mutate()
						}
					}
					if change == "outage" && armed && name == "memos/b" {
						return 503, nil, nil
					}
					if raw := memos[name]; raw != nil {
						return 200, raw, nil
					}
					return 404, nil, nil
				})
				ctx := context.Background()
				got, err := service.Collect(ctx, AIRequest{Names: []string{"memos/a", "memos/b"}})
				if phase == "reuse" {
					if err != nil {
						t.Fatal(err)
					}
					armed = true
					mutate()
					err = service.Verify(ctx, got.Items)
				} else if len(got.Items) != 0 {
					t.Fatal("source failure leaked collected text")
				}
				expected := "SOURCE_CHANGED"
				if change == "delete" || change == "foreign" {
					expected = "SOURCE_NOT_FOUND"
				} else if change == "outage" {
					expected = "UPSTREAM_UNAVAILABLE"
				}
				if errorCode(err) != expected {
					t.Fatalf("source mutation: expected %s, got %v", expected, err)
				}
				if expected == "SOURCE_CHANGED" {
					serviceError := err.(*ServiceError)
					if serviceError.Status != 409 || !serviceError.Retryable {
						t.Fatal("changed source must be retryable")
					}
				}
			})
		}
	}
}

func TestEvidenceVerifiesIdentityAndRejectsForgedFragmentsAndCitations(t *testing.T) {
	memos := map[string]map[string]any{
		"memos/seed": rawMemo("seed", "\u6c49\nsecond\n"),
		"memos/copy": rawMemo("copy", "another note"),
	}
	service := evidenceFixture(t, memos)
	ctx := context.Background()
	collected, err := service.Collect(ctx, AIRequest{Names: []string{"memos/seed", "memos/copy"}})
	if err != nil {
		t.Fatal(err)
	}
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
		items := slices.Clone(collected.Items)
		alter(&items[0])
		if err := service.Verify(ctx, items); errorCode(err) != "INVALID_EVIDENCE" {
			t.Fatalf("forged fragment accepted: %+v %v", items[0], err)
		}
	}
	if err := ValidateCitations([]string{"E2", "E1", "E2"}, collected.Items); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "E0", "E3", "E01", "e1", "memos/seed", "E1:0:3"} {
		if err := ValidateCitations([]string{id}, collected.Items); errorCode(err) != "INVALID_CITATION" {
			t.Fatalf("invalid citation accepted: %q %v", id, err)
		}
	}
	wrongIDs := slices.Clone(collected.Items)
	wrongIDs[0].ID = "E99"
	if err := ValidateCitations([]string{"E99"}, wrongIDs); errorCode(err) != "INVALID_CITATION" {
		t.Fatal("citation validation trusted an arbitrary evidence ID")
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
		if err := service.Verify(ctx, collected.Items); errorCode(err) != "IDENTITY_MISMATCH" {
			t.Fatalf("identity change escaped source checks: %v", err)
		}
	}
}

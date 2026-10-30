package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type recordMutationFixture struct {
	t          *testing.T
	store      *Store
	client     *MemosClient
	memo       map[string]any
	files      map[string]map[string]any
	posts      map[string]int
	patches    int
	deletes    int
	memoPosts  int
	lost       string
	failBefore string
}

func newRecordMutationFixture(t *testing.T) *recordMutationFixture {
	t.Helper()
	settings := testSettings(t)
	f := &recordMutationFixture{t: t, store: testStore(t, settings), memo: rawMemo("existing", "original #tag"),
		files: map[string]map[string]any{}, posts: map[string]int{}}
	f.client = fakeClient(settings, f.handle)
	return f
}

func (f *recordMutationFixture) handle(r *http.Request) (int, any, error) {
	if status, body, ok := identityResponse(r); ok {
		return status, body, nil
	}
	if r.Method != "GET" && f.failBefore == r.Method {
		f.failBefore = ""
		return 503, map[string]any{}, nil
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	switch {
	case r.Method == "POST" && name == "memos":
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Fatal(err)
		}
		f.memoPosts++
		f.memo = rawMemo(r.URL.Query().Get("memoId"), body["content"])
		return f.reply("CREATE_MEMO", f.memo)
	case r.Method == "GET" && strings.HasPrefix(name, "attachments/"):
		if raw := f.files[name]; raw != nil {
			return 200, raw, nil
		}
		return 404, nil, nil
	case r.Method == "POST" && name == "attachments":
		var body struct {
			Filename string `json:"filename"`
			Type     string `json:"type"`
			Memo     string `json:"memo"`
			Content  []byte `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Fatal(err)
		}
		name = "attachments/" + r.URL.Query().Get("attachmentId")
		f.posts[name]++
		if !attachmentPattern.MatchString(name) || f.files[name] != nil {
			f.t.Fatal("duplicate or unstable attachment resource")
		}
		raw := map[string]any{"name": name, "filename": body.Filename, "type": body.Type, "memo": body.Memo, "size": len(body.Content)}
		f.files[name] = raw
		f.memo["attachments"] = append(f.attachments(), raw)
		return f.reply("POST", raw)
	case r.Method == "DELETE" && strings.HasPrefix(name, "attachments/"):
		f.deletes++
		delete(f.files, name)
		f.memo["attachments"] = slices.DeleteFunc(slices.Clone(f.attachments()), func(raw map[string]any) bool { return raw["name"] == name })
		return f.reply("DELETE_ATTACHMENT", map[string]any{})
	case r.Method == "PATCH" && name == "memos/existing":
		f.patches++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Fatal(err)
		}
		mask := []string{}
		for _, field := range []string{"content", "state"} {
			if value, ok := body[field]; ok {
				f.memo[field] = value
				mask = append(mask, field)
			}
		}
		mask = append(mask, "update_time")
		if r.URL.Query().Get("updateMask") != strings.Join(mask, ",") {
			f.t.Fatalf("incorrect update mask: %s", r.URL.RawQuery)
		}
		if _, ok := body["createTime"]; ok {
			f.t.Fatal("mutation rewrote create_time")
		}
		f.memo["updateTime"] = "2026-10-29T01:00:00Z"
		return f.reply("PATCH", f.memo)
	case r.Method == "DELETE" && name == "memos/existing":
		f.deletes++
		f.memo = nil
		return f.reply("DELETE_MEMO", map[string]any{})
	case r.Method == "GET" && strings.HasPrefix(name, "memos/"):
		if f.memo != nil && f.memo["name"] == name {
			return 200, f.memo, nil
		}
		return 404, nil, nil
	}
	f.t.Fatalf("unexpected source request: %s %s", r.Method, r.URL)
	return 500, nil, nil
}

func (f *recordMutationFixture) reply(operation string, raw any) (int, any, error) {
	if f.lost == operation {
		f.lost = ""
		return 0, nil, errors.New("response lost after commit")
	}
	return 200, raw, nil
}

func (f *recordMutationFixture) attachments() []map[string]any {
	items, _ := f.memo["attachments"].([]map[string]any)
	return items
}

func (f *recordMutationFixture) add(name string) {
	raw := map[string]any{"name": name, "filename": "old.txt", "type": "text/plain", "memo": "memos/existing"}
	f.files[name] = raw
	f.memo["attachments"] = append(f.attachments(), raw)
}

func (f *recordMutationFixture) noteService() *NoteService {
	return NewNoteService(f.store.Settings, f.store, f.client)
}

func (f *recordMutationFixture) sourceHash() string {
	memo, err := normalizeMemo(f.memo)
	if err != nil {
		f.t.Fatal(err)
	}
	return memo.SourceHash
}

func TestRecordMutationPreservesContentAttachmentsAndCreation(t *testing.T) {
	f := newRecordMutationFixture(t)
	f.add("attachments/keep")
	f.add("attachments/remove")
	files := []CreateAttachment{{"new.txt", "text/plain", []byte("new file")}}
	input := UpdateNote{Content: stringPointer("  native #z\n#tag  "), State: "ARCHIVED",
		SourceHash: f.sourceHash(), Attachments: &files, RemoveAttachments: []string{"attachments/remove"}}
	note, err := f.noteService().Update(context.Background(), "memos/existing", "edit", input)
	if err != nil || note.Content != *input.Content || note.State != "ARCHIVED" ||
		note.CreateTime != "2020-01-01T00:00:00.000000+00:00" || len(note.Attachments) != 2 ||
		!slices.ContainsFunc(note.Attachments, func(raw map[string]any) bool { return raw["name"] == "attachments/keep" }) ||
		f.patches != 1 || f.deletes != 1 {
		t.Fatalf("incorrect mutation: %+v patches=%d deletes=%d err=%v", note, f.patches, f.deletes, err)
	}
	if _, err := f.noteService().Update(context.Background(), "memos/existing", "edit", input); err != nil || f.patches != 1 || f.deletes != 1 {
		t.Fatalf("successful replay changed the source: %v", err)
	}
	input.Content = stringPointer("different")
	if _, err := f.noteService().Update(context.Background(), "memos/existing", "edit", input); errorCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("changed request reused mutation key: %v", err)
	}
}

func TestRecordMutationRecoversLostResponsesAndRestart(t *testing.T) {
	for _, operation := range []string{"POST", "PATCH", "DELETE_ATTACHMENT"} {
		t.Run(operation, func(t *testing.T) {
			f := newRecordMutationFixture(t)
			f.add("attachments/remove")
			files := []CreateAttachment{{"new.txt", "text/plain", []byte("private file")}}
			input := UpdateNote{Content: stringPointer("private updated content"), SourceHash: f.sourceHash(),
				Attachments: &files, RemoveAttachments: []string{"attachments/remove"}}
			f.lost = operation
			if _, err := f.noteService().Update(context.Background(), "memos/existing", "resume", input); errorCode(err) != "UPSTREAM_UNAVAILABLE" {
				t.Fatalf("lost response: %v", err)
			}
			settings := f.store.Settings
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			f.store, err = NewStore(settings)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.store.Close() })
			note, err := f.noteService().Update(context.Background(), "memos/existing", "resume", input)
			if err != nil || note.Content != *input.Content || len(note.Attachments) != 1 || len(f.posts) != 1 || f.patches != 1 || f.deletes != 1 {
				t.Fatalf("recovery: %+v posts=%v patches=%d deletes=%d err=%v", note, f.posts, f.patches, f.deletes, err)
			}
			for _, count := range f.posts {
				if count != 1 {
					t.Fatal("recovery created another attachment")
				}
			}
			var journal string
			if err := f.store.state.QueryRow("SELECT last_error FROM note_writes WHERE idempotency_key LIKE '~update:%'").Scan(&journal); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(journal, "private") || strings.Contains(journal, "new.txt") {
				t.Fatal("mutation journal retained private input")
			}
		})
	}
}

func TestRecordMutationRejectsForeignStaleEmptyAndUnrelatedRemoval(t *testing.T) {
	for _, scenario := range []string{"foreign", "stale", "empty", "unrelated", "too-many", "invalid-state", "missing-key"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRecordMutationFixture(t)
			input := UpdateNote{Content: stringPointer("changed"), SourceHash: f.sourceHash()}
			key, want := "edit", "INVALID_ARGUMENT"
			switch scenario {
			case "foreign":
				f.memo["creator"] = "users/other"
				want = "SOURCE_NOT_FOUND"
			case "stale":
				f.memo["content"] = "external edit"
				want = "SOURCE_CHANGED"
			case "empty":
				input.Content = stringPointer(" \n")
			case "unrelated":
				input.RemoveAttachments = []string{"attachments/foreign"}
			case "too-many":
				files := slices.Repeat([]CreateAttachment{{"a.txt", "text/plain", []byte("new")}}, 11)
				input.Attachments = &files
			case "invalid-state":
				input.State = "DELETED"
			case "missing-key":
				key = ""
			}
			if _, err := f.noteService().Update(context.Background(), "memos/existing", key, input); errorCode(err) != want {
				t.Fatalf("expected %s, got %v", want, err)
			}
			if len(f.posts)+f.patches+f.deletes != 0 {
				t.Fatal("invalid mutation reached a source write")
			}
		})
	}
}

func TestRecordMutationAllowsAttachmentOnlyAndStopsExternalEditOnRetry(t *testing.T) {
	f := newRecordMutationFixture(t)
	f.add("attachments/keep")
	files := []CreateAttachment{{"new.txt", "text/plain", []byte("new")}}
	input := UpdateNote{Content: stringPointer(""), SourceHash: f.sourceHash(), Attachments: &files}
	f.failBefore = "PATCH"
	if _, err := f.noteService().Update(context.Background(), "memos/existing", "partial", input); errorCode(err) != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("failed patch: %v", err)
	}
	f.memo["content"] = "external change after upload"
	if _, err := f.noteService().Update(context.Background(), "memos/existing", "partial", input); errorCode(err) != "SOURCE_CHANGED" || f.patches != 0 {
		t.Fatalf("retry overwrote external content: %v", err)
	}
	f2 := newRecordMutationFixture(t)
	f2.add("attachments/keep")
	note, err := f2.noteService().Update(context.Background(), "memos/existing", "clear", UpdateNote{Content: stringPointer(""), SourceHash: f2.sourceHash()})
	if err != nil || note.Content != "" || len(note.Attachments) != 1 {
		t.Fatalf("attachment-only edit: %+v %v", note, err)
	}
}

func TestRecordMutationDeleteChecksOwnerHashAndProjection(t *testing.T) {
	for _, scenario := range []string{"success", "stale", "foreign", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRecordMutationFixture(t)
			hash := f.sourceHash()
			memo, _ := normalizeMemo(f.memo)
			if err := f.store.Refresh(memo.Name, &memo); err != nil {
				t.Fatal(err)
			}
			want := ""
			switch scenario {
			case "stale":
				f.memo["content"] = "changed"
				want = "SOURCE_CHANGED"
			case "foreign":
				f.memo["creator"] = "users/other"
			case "missing":
				f.memo = nil
			}
			err := f.noteService().Delete(context.Background(), "memos/existing", hash)
			if (want == "" && err != nil) || (want != "" && errorCode(err) != want) {
				t.Fatalf("delete expected %s: %v", want, err)
			}
			if scenario == "success" {
				names, _ := f.store.Names()
				if len(names) != 0 || f.deletes != 1 {
					t.Fatal("deleted source survived in projection")
				}
				if err := f.noteService().Delete(context.Background(), "memos/existing", hash); err != nil || f.deletes != 1 {
					t.Fatalf("repeated delete was not idempotent: calls=%d err=%v", f.deletes, err)
				}
			} else if f.deletes != 0 {
				t.Fatal("invalid delete reached upstream")
			}
		})
	}
}

func TestRecordMutationDeleteDoesNotHideSourceFailures(t *testing.T) {
	for _, scenario := range []string{"unavailable", "identity", "invalid-hash"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRecordMutationFixture(t)
			hash := f.sourceHash()
			f.memo = nil
			original := f.client.client.Transport
			f.client.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if scenario == "identity" && strings.HasSuffix(r.URL.Path, "/auth/me") {
					return nil, errors.New("identity unavailable")
				}
				if scenario == "unavailable" && r.URL.Path == "/api/v1/memos/existing" {
					return nil, errors.New("source unavailable")
				}
				return original.RoundTrip(r)
			})
			want := "UPSTREAM_UNAVAILABLE"
			if scenario == "invalid-hash" {
				hash, want = "", "INVALID_ARGUMENT"
			}
			if err := f.noteService().Delete(context.Background(), "memos/existing", hash); errorCode(err) != want || f.deletes != 0 {
				t.Fatalf("source failure treated as deletion: %v deletes=%d", err, f.deletes)
			}
		})
	}
}

func TestRecordMutationAttachmentDeletionPreconditions(t *testing.T) {
	for _, scenario := range []struct {
		name, message, want string
		code, status        int
		retryable           bool
	}{
		{"referenced", "attachment keep is referenced by memo content", "ATTACHMENT_REFERENCED", 9, 400, false},
		{"invalid-reference", "memo contains an invalid managed attachment reference", "ATTACHMENT_DELETE_REJECTED", 9, 400, false},
		{"bad-adapter-request", "invalid attachment id", "UPSTREAM_REQUEST_REJECTED", 3, 400, true},
		{"source-down", "unavailable", "UPSTREAM_UNAVAILABLE", 14, 503, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newRecordMutationFixture(t)
			f.add("attachments/keep")
			f.client = fakeClient(f.store.Settings, func(r *http.Request) (int, any, error) {
				if r.Method == http.MethodDelete && r.URL.Path == "/api/v1/attachments/keep" {
					return scenario.status, map[string]any{"code": scenario.code, "message": scenario.message}, nil
				}
				return f.handle(r)
			})
			_, err := f.noteService().Update(t.Context(), "memos/existing", "remove",
				UpdateNote{SourceHash: f.sourceHash(), RemoveAttachments: []string{"attachments/keep"}})
			var service *ServiceError
			if !errors.As(err, &service) || service.Code != scenario.want || service.Retryable != scenario.retryable {
				t.Fatalf("deletion error classification: %+v", err)
			}
			if !scenario.retryable && service.Status != 422 {
				t.Fatalf("precondition did not return a correctable status: %+v", service)
			}
			if f.deletes != 0 || len(f.attachments()) != 1 {
				t.Fatal("rejected deletion changed the source")
			}
		})
	}
}

func TestRecordMutationAttachmentPreflightBeforeAnyWrite(t *testing.T) {
	destination := "/file/attachments/keep/picture.png"
	image := "![picture](" + destination + ")"
	for _, scenario := range []struct {
		name, content string
		referenced    bool
	}{
		{"unchanged-content", image, true},
		{"changed-content", image + "\nchanged caption", true},
		{"reference-image", "![picture][pic]\n\n[pic]: " + destination, true},
		{"angle-image", "![picture](<" + destination + ">)", true},
		{"filename-free", "![picture](/file/attachments/keep)", true},
		{"image-in-link", "[" + image + "](https://example.org)", true},
		{"inline-code", "`" + image + "`", false},
		{"fenced-code", "```markdown\n" + image + "\n```", false},
		{"indented-code", "    " + image, false},
		{"escaped-image", "\\" + image, false},
		{"plain-link", "[download](" + destination + ")", false},
		{"raw-url", destination, false},
		{"gfm-url", "https://example.org/" + image, false},
		{"invalid-reference-definition", "![picture][pic]\n\n[pic]: " + destination + "(", false},
		{"removed-reference", "corrected caption", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newRecordMutationFixture(t)
			f.add("attachments/keep")
			f.memo["content"] = image
			originalHash := f.sourceHash()
			files := []CreateAttachment{{"new.txt", "text/plain", []byte("new file")}}
			input := UpdateNote{Content: stringPointer(scenario.content), State: "ARCHIVED",
				SourceHash: originalHash, Attachments: &files, RemoveAttachments: []string{"attachments/keep"}}
			note, err := f.noteService().Update(t.Context(), "memos/existing", "preflight", input)
			if !scenario.referenced {
				if err != nil || note.Content != scenario.content || len(note.Attachments) != 1 {
					t.Fatalf("non-image text blocked deletion: %+v %v", note, err)
				}
				return
			}
			if errorCode(err) != "ATTACHMENT_REFERENCED" || f.sourceHash() != originalHash ||
				len(f.posts)+f.patches+f.deletes != 0 {
				t.Fatalf("reference rejection reached an upstream write: posts=%v patches=%d deletes=%d err=%v",
					f.posts, f.patches, f.deletes, err)
			}
			hash, err := f.store.savedNoteHash("~update:" + hashBytes([]byte("preflight")))
			if err != nil || hash != "" {
				t.Fatalf("preflight rejection reserved the mutation key: %q %v", hash, err)
			}
			input.Content = stringPointer("corrected caption")
			note, err = f.noteService().Update(t.Context(), "memos/existing", "preflight", input)
			if err != nil || note.Content != *input.Content || len(note.Attachments) != 1 {
				t.Fatalf("corrected draft could not reuse its version and key: %+v %v", note, err)
			}
		})
	}
}

func TestMemosAttachmentPreflightUsesSourceOrigin(t *testing.T) {
	for _, scenario := range []struct {
		name, origin, destination, want string
	}{
		{"same-origin", "https://memos.example", "https://MEMOS.example:443/file/attachments/keep/picture.png", "ATTACHMENT_REFERENCED"},
		{"foreign-origin", "https://memos.example", "https://elsewhere.example/file/attachments/keep/picture.png", ""},
		{"foreign-port", "https://memos.example", "https://memos.example:444/file/attachments/keep/picture.png", ""},
		{"missing-origin", "", "https://memos.example/file/attachments/keep/picture.png", "ATTACHMENT_DELETE_REJECTED"},
		{"protocol-relative", "https://memos.example", "//memos.example/file/attachments/keep/picture.png", "ATTACHMENT_DELETE_REJECTED"},
		{"foreign-protocol-relative", "https://memos.example", "//elsewhere.example/file/attachments/keep/picture.png", ""},
		{"query", "", "/file/attachments/keep/picture.png?download=1", "ATTACHMENT_DELETE_REJECTED"},
		{"fragment", "", "/file/attachments/keep/picture.png#part", "ATTACHMENT_DELETE_REJECTED"},
		{"encoded", "", "/file/attachments/keep/picture%2Epng", "ATTACHMENT_DELETE_REJECTED"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			settings := testSettings(t)
			client := fakeClient(settings, func(r *http.Request) (int, any, error) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/instance/profile" {
					t.Fatalf("unexpected preflight request: %s %s", r.Method, r.URL)
				}
				return 200, map[string]any{"version": "0.31.0", "instanceUrl": scenario.origin}, nil
			})
			err := client.CheckAttachmentRemoval(t.Context(), "![picture]("+scenario.destination+")", []string{"attachments/keep"})
			if (scenario.want == "" && err != nil) || (scenario.want != "" && errorCode(err) != scenario.want) {
				t.Fatalf("source origin classification: want=%s error=%v", scenario.want, err)
			}
		})
	}
}

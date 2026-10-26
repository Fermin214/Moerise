package moerise

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentRecordsUseOneResource(t *testing.T) {
	settings := testSettings(t)
	settings.AccessTokens[1].Scopes = append(settings.AccessTokens[1].Scopes, "notes:write")
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	store := testStore(t, settings)
	var created map[string]any
	posts := 0
	client := fakeClient(settings, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		if request.Method == "POST" {
			posts++
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				return 0, nil, err
			}
			id := request.URL.Query().Get("memoId")
			if !uuidPattern.MatchString(id) || body["visibility"] != "PRIVATE" {
				t.Error("missing reserved resource ID or private visibility")
			}
			created = rawMemo(id, body["content"])
			created["tags"] = []string{"a", "z"}
			return 200, created, nil
		}
		if created == nil {
			return 404, nil, nil
		}
		return 200, created, nil
	})
	api, err := NewAPI(settings, store, client, NewSyncService(store, client))
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 12)
	for range 12 {
		group.Go(func() {
			request := httptest.NewRequest("POST", "/api/ext/v1/notes",
				strings.NewReader(`{"content":"concurrent note","tags":["z","a","a"]}`))
			request.Header.Set("Authorization", "Bearer "+settings.AccessTokens[1].Token)
			request.Header.Set("Idempotency-Key", "same-key")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			results <- response
		})
	}
	group.Wait()
	close(results)
	first := 0
	for response := range results {
		if response.Code == 201 {
			first++
		} else if response.Code != 200 {
			t.Fatalf("concurrent record: %d %s", response.Code, response.Body.String())
		}
	}
	if first != 1 || posts != 1 || created["content"] != "#a #z\n\nconcurrent note" {
		t.Fatalf("duplicate or incorrect writes: first=%d posts=%d content=%v", first, posts, created["content"])
	}
	_, _, err = api.Records.Create(context.Background(), "same-key", CreateNote{Content: "concurrent note", Tags: []string{"a", "z"}})
	if err != nil || posts != 1 {
		t.Fatal("normalized tags did not preserve idempotency")
	}
	_, _, err = api.Records.Create(context.Background(), "same-key", CreateNote{Content: "different"})
	if errorCode(err) != "IDEMPOTENCY_CONFLICT" || posts != 1 {
		t.Fatal("conflicting request reused an idempotency key")
	}
}

func TestRecordRejectsInvalidRequestsBeforeSourceAccess(t *testing.T) {
	settings := testSettings(t)
	settings.AccessTokens[1].Scopes = append(settings.AccessTokens[1].Scopes, "notes:write")
	store := testStore(t, settings)
	client := fakeClient(settings, func(*http.Request) (int, any, error) {
		t.Error("invalid record reached Memos")
		return 500, nil, nil
	})
	api, err := NewAPI(settings, store, client, NewSyncService(store, client))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		key, body string
		token     int
		status    int
	}{
		{"key", `{"content":"private"}`, 0, 403},
		{"", `{"content":"private"}`, 1, 422},
		{"invalid key", `{"content":"private"}`, 1, 422},
		{"key", `{"content":"private","creator":"users/other"}`, 1, 422},
		{"key", `{"content":"private","visibility":"PUBLIC"}`, 1, 422},
		{"key", `{"content":"private","tags":["a #public"]}`, 1, 422},
		{"key", `{"content":"private","tags":["v\u00b2"]}`, 1, 422},
		{"key", `{"content":"private","tags":null}`, 1, 422},
		{"key", `{"content":"private","attachments":null}`, 1, 422},
		{"key", `{"content":"private","attachments":[{"filename":"a.txt","type":"text/plain","content":null}]}`, 1, 422},
		{"key", `{"content":"private","attachments":[{"filename":"a.txt","type":"text/plain","content":[1,2]}]}`, 1, 422},
		{"key", `{"content":"private","attachments":[{"filename":"../a.txt","type":"text/plain","content":"YQ=="}]}`, 1, 422},
		{"key", `{"content":"private","attachments":[{"filename":"a.txt","type":"text/plain","content":""}]}`, 1, 422},
		{"key", `{"content":"private","attachments":[{"filename":"a.txt","type":"text/plain","content":"not base64"}]}`, 1, 422},
		{"key", `{"content":"private","attachments":[{"filename":"a.txt","type":"text/plain; charset=utf-8","content":"YQ=="}]}`, 1, 422},
		{"key", `{"content":" "}`, 1, 422},
		{"key", `{"content":"` + strings.Repeat("x", 65536) + `","tags":["a"]}`, 1, 422},
	} {
		request := httptest.NewRequest("POST", "/api/ext/v1/notes", strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer "+settings.AccessTokens[test.token].Token)
		request.Header.Set("Idempotency-Key", test.key)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("invalid request: %d %s", response.Code, response.Body.String())
		}
	}
	var rows int
	if err := store.state.QueryRow("SELECT COUNT(*) FROM note_writes").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("invalid request persisted state: rows=%d err=%v", rows, err)
	}
}

func TestRecordIdentityChangeCannotPublish(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_write", true: "after_write"}[afterWrite], func(t *testing.T) {
			settings := testSettings(t)
			store := testStore(t, settings)
			identities, posts := 0, 0
			var created map[string]any
			client := fakeClient(settings, func(request *http.Request) (int, any, error) {
				if strings.HasSuffix(request.URL.Path, "/profile") {
					return 200, map[string]string{"version": "0.31.0"}, nil
				}
				if strings.HasSuffix(request.URL.Path, "/auth/me") {
					identities++
					name := "users/other"
					if afterWrite && identities == 1 {
						name = "users/owner"
					}
					return 200, map[string]any{"user": map[string]string{"name": name}}, nil
				}
				if request.Method == "POST" {
					posts++
					created = rawMemo(request.URL.Query().Get("memoId"), "private")
					return 200, created, nil
				}
				if created == nil {
					return 404, nil, nil
				}
				return 200, created, nil
			})
			note, _, err := NewRecordService(store, client).Create(context.Background(), "identity", CreateNote{Content: "private"})
			if errorCode(err) != "IDENTITY_MISMATCH" || note.Content != "" {
				t.Fatalf("identity change returned content: %+v %v", note, err)
			}
			names, _ := store.Names()
			if len(names) != 0 || (!afterWrite && posts != 0) || (afterWrite && posts != 1) {
				t.Fatal("identity change published a note or wrote under another identity")
			}
			var succeeded int
			if err := store.state.QueryRow("SELECT COUNT(*) FROM note_writes WHERE status = 'succeeded'").Scan(&succeeded); err != nil || succeeded != 0 {
				t.Fatal("identity change confirmed a write")
			}
		})
	}
}

func TestRecordUntrustedResponseRecoversWithoutAnotherWrite(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	var created map[string]any
	posts := 0
	client := fakeClient(settings, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		if request.Method == "POST" {
			posts++
			data, _ := io.ReadAll(request.Body)
			var body map[string]string
			_ = json.Unmarshal(data, &body)
			created = rawMemo(request.URL.Query().Get("memoId"), body["content"])
			wrong := rawMemo("wrong-resource", "untrusted")
			wrong["creator"] = "users/other"
			return 200, wrong, nil
		}
		if created == nil {
			return 404, nil, nil
		}
		return 200, created, nil
	})
	records := NewRecordService(store, client)
	note, _, err := records.Create(context.Background(), "unknown", CreateNote{Content: "private"})
	if errorCode(err) != "WRITE_OUTCOME_UNKNOWN" || note.Content != "" {
		t.Fatalf("untrusted write response accepted: %+v %v", note, err)
	}
	var status string
	if err := store.state.QueryRow("SELECT status FROM note_writes WHERE idempotency_key = 'unknown'").Scan(&status); err != nil || status != "unknown" {
		t.Fatal("uncertain write was not persisted")
	}
	note, code, err := records.Create(context.Background(), "unknown", CreateNote{Content: "private"})
	if err != nil || code != 200 || note.Content != "private" || posts != 1 {
		t.Fatalf("reconciliation failed: %+v status=%d posts=%d err=%v", note, code, posts, err)
	}
}

func TestRecordConflictDoesNotAdoptAnotherAuthorsResource(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	posted := false
	client := fakeClient(settings, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		if request.Method == "POST" {
			posted = true
			return 409, nil, nil
		}
		if !posted {
			return 404, nil, nil
		}
		raw := rawMemo(strings.TrimPrefix(request.URL.Path, "/api/v1/memos/"), "private")
		raw["creator"] = "users/other"
		return 200, raw, nil
	})
	note, _, err := NewRecordService(store, client).Create(context.Background(), "collision", CreateNote{Content: "private"})
	if errorCode(err) != "WRITE_CONFLICT" || note.Content != "" {
		t.Fatalf("another author's resource was accepted: %+v %v", note, err)
	}
	names, _ := store.Names()
	if len(names) != 0 {
		t.Fatal("foreign resource reached the projection")
	}
}

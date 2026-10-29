package moerise

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type clientFileSource struct {
	Source
	parent  *Memo
	file    map[string]any
	content []byte
	opened  int
	method  string
	ranged  string
	onOpen  func()
}

func (s *clientFileSource) Get(_ context.Context, name string) (*Memo, error) {
	if s.parent == nil || s.parent.Name != name {
		return nil, nil
	}
	parent := *s.parent
	return &parent, nil
}

func (s *clientFileSource) GetAttachment(_ context.Context, name string) (map[string]any, error) {
	if s.file["name"] != name {
		return nil, nil
	}
	return s.file, nil
}

func (s *clientFileSource) OpenAttachment(_ context.Context, _ map[string]any, method, byteRange string) (*http.Response, error) {
	s.opened++
	s.method, s.ranged = method, byteRange
	if s.onOpen != nil {
		s.onOpen()
	}
	data, status := s.content, http.StatusOK
	header := http.Header{"Content-Length": {"8"}}
	if byteRange == "bytes=2-4" {
		data, status = data[2:5], http.StatusPartialContent
		header.Set("Content-Length", "3")
		header.Set("Content-Range", "bytes 2-4/8")
	}
	return &http.Response{
		StatusCode: status, Header: header, ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func TestClientProtectedAttachmentBoundary(t *testing.T) {
	for _, scenario := range []string{"get", "head", "range", "active-content", "unrelated", "foreign", "changed", "invalid-range", "anonymous", "bearer"} {
		t.Run(scenario, func(t *testing.T) {
			api, _ := clientFixture(t)
			rawID, _, err := api.app.Sessions.Login(t.Context(), "owner", "correct")
			if err != nil {
				t.Fatal(err)
			}
			file := map[string]any{"name": "attachments/picture", "memo": "memos/parent", "filename": "picture.png", "type": "image/png"}
			raw := liveRawMemo("parent", "")
			raw["attachments"] = []map[string]any{file}
			parent, err := normalizeMemo(raw)
			if err != nil {
				t.Fatal(err)
			}
			source := &clientFileSource{Source: api.app.Notes.Source, parent: &parent, file: file, content: []byte("01234567")}
			api.app.Notes.Source = source
			method, target, want := http.MethodGet, "/api/app/v1/notes/parent/attachments/picture/content", 200
			if scenario == "head" {
				method = http.MethodHead
			}
			if scenario == "active-content" {
				file["type"], file["filename"] = "image/svg+xml", "untrusted.svg"
				parent.Attachments[0]["type"], parent.Attachments[0]["filename"] = file["type"], file["filename"]
			}
			if scenario == "unrelated" {
				file = map[string]any{"name": "attachments/picture", "memo": "memos/other", "filename": "picture.png", "type": "image/png"}
				source.file, want = file, 404
			}
			if scenario == "foreign" {
				source.parent.Creator, want = "users/other", 404
			}
			if scenario == "changed" {
				source.onOpen = func() { source.parent.SourceHash = "changed" }
				want = 409
			}
			request := httptest.NewRequest(method, "https://notes.example.com"+target, nil)
			if scenario != "anonymous" {
				request.AddCookie(&http.Cookie{Name: api.cookieName(), Value: rawID})
			} else {
				want = 401
			}
			if scenario == "bearer" {
				request.Header.Set("Authorization", "Bearer external")
				want = 403
			}
			if scenario == "range" {
				request.Header.Set("Range", "bytes=2-4")
				want = 206
			}
			if scenario == "invalid-range" {
				request.Header.Set("Range", "bytes=0-1,3-4")
				want = 416
			}
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			if response.Code != want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
			}
			if want >= 400 {
				if strings.Contains(response.Body.String(), "01234567") ||
					(scenario != "changed" && source.opened != 0) {
					t.Fatal("file opened or returned outside the parent/session boundary")
				}
				return
			}
			if response.Header().Get("Cache-Control") != "no-store" ||
				response.Header().Get("X-Content-Type-Options") != "nosniff" || source.method != method {
				t.Fatalf("file response headers/method: %v %s", response.Header(), source.method)
			}
			if scenario == "active-content" {
				if response.Header().Get("Content-Type") != "application/octet-stream" ||
					!strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
					t.Fatal("active content was served inline")
				}
			}
			if scenario == "head" && response.Body.Len() != 0 {
				t.Fatal("HEAD returned a file body")
			}
			if scenario == "range" && (response.Body.String() != "234" ||
				source.ranged != "bytes=2-4" || response.Header().Get("Content-Range") != "bytes 2-4/8") {
				t.Fatal("range was not preserved")
			}
		})
	}
}

func TestClientNoteAttachmentDTO(t *testing.T) {
	note := clientNote(Note{Name: "memos/parent", Attachments: []map[string]any{{
		"name": "attachments/picture", "filename": "picture.png", "type": "image/png",
		"size": "8", "externalLink": "https://internal.example/private", "content": "private",
	}}})
	if len(note.Attachments) != 1 || len(note.Attachments[0]) != 5 ||
		note.Attachments[0]["content_url"] != "/api/app/v1/notes/parent/attachments/picture/content" ||
		note.Attachments[0]["size"] != int64(8) || note.URL != "/notes/parent" {
		t.Fatalf("unexpected browser DTO: %+v", note)
	}
}

package moerise

import (
	"context"
	"net/http"
	"testing"
)

func TestDeletedPartialRecordIsNotRecreated(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	input := CreateNote{Content: "partial", Attachments: []CreateAttachment{{"a.txt", "text/plain", []byte("file")}}}
	_, hash, err := prepareNote(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.reserveNote("partial", hash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.state.Exec("UPDATE note_writes SET source_created = 1"); err != nil {
		t.Fatal(err)
	}
	client := fakeClient(settings, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		if request.Method != "GET" {
			t.Error("deleted partial note was recreated")
		}
		return 404, nil, nil
	})
	note, _, err := NewRecordService(store, client).Create(context.Background(), "partial", input)
	if errorCode(err) != "SOURCE_NOT_FOUND" || note.Content != "" {
		t.Fatalf("deleted partial result: %+v %v", note, err)
	}
}

func TestRecordAttachmentOnlyCreationAndLostUploadReplay(t *testing.T) {
	for _, mediaType := range []string{"image/png", "application/pdf"} {
		t.Run(mediaType, func(t *testing.T) {
			f := newRecordMutationFixture(t)
			f.memo = nil
			f.lost = "POST"
			input := CreateNote{Attachments: []CreateAttachment{{"file.bin", mediaType, []byte("bytes")}}}
			records := NewRecordService(f.store, f.client)
			if _, _, err := records.Create(context.Background(), "file-only", input); errorCode(err) != "WRITE_OUTCOME_UNKNOWN" {
				t.Fatalf("lost upload response: %v", err)
			}
			note, _, err := NewRecordService(f.store, f.client).Create(context.Background(), "file-only", input)
			if err != nil || note.Content != "" || len(note.Attachments) != 1 || f.memoPosts != 1 || len(f.posts) != 1 {
				t.Fatalf("attachment-only creation: %+v posts=%v memos=%d err=%v", note, f.posts, f.memoPosts, err)
			}
			for _, count := range f.posts {
				if count != 1 {
					t.Fatal("attachment replay wrote a second resource")
				}
			}
		})
	}
}

func TestRecordAttachmentLimits(t *testing.T) {
	file := CreateAttachment{"file.bin", "application/octet-stream", []byte("bytes")}
	for _, files := range [][]CreateAttachment{
		{CreateAttachment{"file.bin", "application/octet-stream", make([]byte, 8*1024*1024+1)}},
		{file, file, file, file, file, file, file, file, file, file, file},
	} {
		if _, _, err := prepareNote(CreateNote{Attachments: files}); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("attachment limit accepted: %v", err)
		}
	}
}

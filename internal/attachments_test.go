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

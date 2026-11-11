package moerise

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestSearchSourceDatabaseWaitRespectsDeadline(t *testing.T) {
	for _, stage := range []string{"identity", "identity-error", "memo", "memo-error"} {
		t.Run(stage, func(t *testing.T) {
			notes, source := newNotesFilterFixture(t)
			service := NewSearchService(notes.Store, source, nil)
			db := notes.Store.state
			if stage == "memo" {
				db = notes.Store.index
			}
			if stage == "identity-error" {
				source.failCheck = 1
			}
			if stage == "memo-error" {
				source.getError = "memos/z"
			}
			connection, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if stage == "identity" || stage == "identity-error" {
					done <- service.verify(ctx)
				} else {
					_, err := service.liveSearchMemo(ctx, "memos/z")
					done <- err
				}
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("database wait ignored deadline: %v", err)
				}
			case <-time.After(500 * time.Millisecond):
				connection.Close()
				<-done
				t.Fatal("database wait continued after request deadline")
			}
			connection.Close()
			if stage == "memo" {
				var count int
				if err := notes.Store.index.QueryRow("SELECT COUNT(*) FROM memos").Scan(&count); err != nil || count != 0 {
					t.Fatalf("expired request changed the index: count=%d err=%v", count, err)
				}
			}
		})
	}
}

func TestNoteReadersRejectMismatchedSourceName(t *testing.T) {
	for _, reader := range []string{"notes", "search"} {
		t.Run(reader, func(t *testing.T) {
			notes, source := newNotesFilterFixture(t)
			other := testMemo(t, "other", "unrelated original")
			source.fresh["memos/requested"] = &other
			var memo *Memo
			var err error
			if reader == "notes" {
				memo, err = notes.fresh(t.Context(), "memos/requested")
			} else {
				memo, err = NewSearchService(notes.Store, source, nil).liveSearchMemo(t.Context(), "memos/requested")
			}
			if err != nil || memo != nil {
				t.Fatalf("reader accepted an unrelated source: memo=%+v err=%v", memo, err)
			}
			var payload string
			err = notes.Store.index.QueryRow("SELECT payload_json FROM memos WHERE name = ?", other.Name).Scan(&payload)
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unrelated source reached the index: %q %v", payload, err)
			}
		})
	}
}

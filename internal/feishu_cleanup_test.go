package moerise

import (
	"bytes"
	"database/sql"
	"errors"
	"testing"
)

func assertLegacyAIContent(t *testing.T, store *Store, cleaned bool) {
	t.Helper()
	var request, result []byte
	var stamp *string
	if err := store.state.QueryRow(`SELECT request, result, content_cleaned_at
		FROM feishu_ai_generations WHERE message_id = 'om_ai'`).Scan(&request, &result, &stamp); err != nil {
		t.Fatal(err)
	}
	var parts, content int
	if err := store.state.QueryRow(`SELECT COUNT(*), COALESCE(SUM(length(payload)), 0)
		FROM feishu_ai_parts WHERE message_id = 'om_ai'`).Scan(&parts, &content); err != nil {
		t.Fatal(err)
	}
	if cleaned {
		if len(request) != 0 || result != nil || stamp == nil || content != 0 || parts == 0 {
			t.Fatalf("completed content retained: request=%d result=%d parts=%d bytes=%d stamp=%v", len(request), len(result), parts, content, stamp)
		}
	} else if len(request) == 0 || len(result) == 0 || stamp != nil || parts > 0 && content == 0 {
		t.Fatalf("recoverable content removed: request=%d result=%d parts=%d bytes=%d stamp=%v", len(request), len(result), parts, content, stamp)
	}
}

func TestLegacyAICleanupOnlyAfterAllPartsConfirmed(t *testing.T) {
	service, platform, _, calls := feishuAIFixture(t, longFeishuAIOutput())
	receiveFeishuAI(t, service, "洞察 memos/seed")
	processLegacyFeishuAI(t, service)
	chat := testChat(t, service.Store, "retained-chat")
	deliverFeishuAI(t, service)
	assertLegacyAIContent(t, service.Store, false)
	platform.mode = "unknown"
	deliverFeishuAI(t, service)
	assertLegacyAIContent(t, service.Store, false)
	unknown, err := service.Notifications.List("unknown")
	if err != nil || len(unknown) != 1 {
		t.Fatalf("unknown send: %+v %v", unknown, err)
	}
	if _, err := service.Notifications.Resolve(t.Context(), unknown[0].ID, "sent", "om_part_2", "operator"); err != nil {
		t.Fatal(err)
	}
	assertLegacyAIContent(t, service.Store, false)
	platform.mode = ""
	for range 20 {
		progress, err := service.Notifications.Deliver(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !progress {
			break
		}
	}
	assertLegacyAIContent(t, service.Store, true)
	detail, err := service.Notifications.Detail(unknown[0].ID)
	if err != nil || len(detail.Resolutions) != 1 || len(detail.Attempts) != 1 || detail.Notification.Status != "sent" {
		t.Fatalf("cleanup lost delivery history: %+v %v", detail, err)
	}
	if _, err := service.Store.chat(chat.ID); err != nil {
		t.Fatalf("cleanup affected Web chat: %v", err)
	}
	beforeCalls := platform.calls
	restartFeishuAI(t, service)
	receiveFeishuAI(t, service, "洞察 memos/seed")
	if progress, err := service.processOne(t.Context()); progress || err != nil || platform.calls != beforeCalls || *calls != 1 {
		t.Fatalf("cleaned event replayed: progress=%v calls=%d err=%v", progress, platform.calls, err)
	}
	service.Store.Settings.Web.StateEncryptionKey = feishuQueueMigrationKey()
	if err := service.Store.migrateFeishuQueue(); err != nil {
		t.Fatalf("empty cleaned payload was treated as corrupt ciphertext: %v", err)
	}
	assertLegacyAIContent(t, service.Store, true)
}

func TestLegacyAICleanupSharesFinalNotificationTransaction(t *testing.T) {
	service, platform, _, _ := feishuAIFixture(t, insightOutput)
	receiveFeishuAI(t, service, "洞察 memos/seed")
	processLegacyFeishuAI(t, service)
	platform.mode = "unknown"
	deliverFeishuAI(t, service)
	unknown, err := service.Notifications.List("unknown")
	if err != nil || len(unknown) != 1 {
		t.Fatal(err)
	}
	before := feishuQueueSnapshot(t, service.Store, true)
	rollback := errors.New("rollback")
	err = transaction(service.Store.state, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE notifications SET status = 'sent' WHERE id = ?", unknown[0].ID); err != nil {
			return err
		}
		var cleaned bool
		if err := tx.QueryRow("SELECT content_cleaned_at IS NOT NULL FROM feishu_ai_generations").Scan(&cleaned); err != nil || !cleaned {
			t.Fatalf("cleanup was outside final transaction: %v", err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) || !bytes.Equal(before, feishuQueueSnapshot(t, service.Store, true)) {
		t.Fatalf("rollback lost recoverable content or metadata: %v", err)
	}
	if _, err := service.Notifications.Resolve(t.Context(), unknown[0].ID, "not_sent", "", "operator"); err != nil {
		t.Fatal(err)
	}
	assertLegacyAIContent(t, service.Store, false)
	platform.mode = ""
	deliverFeishuAI(t, service)
	assertLegacyAIContent(t, service.Store, true)
}

func TestLegacyAICleanupUpgradePreservesUnfinishedOrDamagedRecords(t *testing.T) {
	for _, state := range []string{"sent", "unknown", "pending", "missing-part", "ordinal-gap"} {
		t.Run(state, func(t *testing.T) {
			service, platform, _, _ := feishuAIFixture(t, insightOutput)
			store := service.Store
			if _, err := store.state.Exec(`DROP TRIGGER feishu_ai_content_cleanup;
				DROP INDEX notifications_source_event;
				ALTER TABLE feishu_ai_generations DROP COLUMN content_cleaned_at;
				DELETE FROM schema_migrations WHERE version = '020'`); err != nil {
				t.Fatal(err)
			}
			// The fixture and delivery use the new reader; retain a NULL column until migration replay.
			if _, err := store.state.Exec("ALTER TABLE feishu_ai_generations ADD COLUMN content_cleaned_at TEXT"); err != nil {
				t.Fatal(err)
			}
			receiveFeishuAI(t, service, "洞察 memos/seed")
			processLegacyFeishuAI(t, service)
			if state == "sent" || state == "ordinal-gap" {
				deliverFeishuAI(t, service)
			} else if state == "unknown" {
				platform.mode = "unknown"
				deliverFeishuAI(t, service)
			} else if state == "missing-part" {
				if _, err := store.state.Exec("DELETE FROM notifications"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "ordinal-gap" {
				if _, err := store.state.Exec("UPDATE feishu_ai_parts SET ordinal = 1"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.state.Exec("ALTER TABLE feishu_ai_generations DROP COLUMN content_cleaned_at"); err != nil {
				t.Fatal(err)
			}
			settings := store.Settings
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = testStore(t, settings)
			assertLegacyAIContent(t, store, state == "sent")
			if err := migrate(store.state, "state"); err != nil {
				t.Fatal(err)
			}
			assertLegacyAIContent(t, store, state == "sent")
		})
	}
}

func TestLegacyAICleanupRetainsOrphanedNotification(t *testing.T) {
	service, _, _, _ := feishuAIFixture(t, longFeishuAIOutput())
	receiveFeishuAI(t, service, "洞察 memos/seed")
	processLegacyFeishuAI(t, service)
	store := service.Store
	var orphan string
	if err := store.state.QueryRow(`SELECT notification_id FROM feishu_ai_parts
		WHERE message_id = 'om_ai' ORDER BY ordinal DESC LIMIT 1`).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	if _, err := store.state.Exec("DELETE FROM feishu_ai_parts WHERE notification_id = ?", orphan); err != nil {
		t.Fatal(err)
	}
	if _, err := store.state.Exec(`UPDATE notifications SET status = 'sent'
		WHERE source_event = 'om_ai' AND id <> ?`, orphan); err != nil {
		t.Fatal(err)
	}
	assertLegacyAIContent(t, store, false)
	var status string
	if err := store.state.QueryRow("SELECT status FROM notifications WHERE id = ?", orphan).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("orphaned notification status changed: %q %v", status, err)
	}
	if _, err := store.state.Exec(`DROP TRIGGER feishu_ai_content_cleanup;
		DROP INDEX notifications_source_event;
		ALTER TABLE feishu_ai_generations DROP COLUMN content_cleaned_at;
		DELETE FROM schema_migrations WHERE version = '020'`); err != nil {
		t.Fatal(err)
	}
	settings := store.Settings
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertLegacyAIContent(t, testStore(t, settings), false)
}

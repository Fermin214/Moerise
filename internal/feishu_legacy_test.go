package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func seedLegacyFeishuRecord(t *testing.T, service *FeishuService, id, text, status string) []byte {
	t.Helper()
	message := feishuMessage{ID: id, Type: "text", Content: string(canonicalJSON(map[string]string{"text": text}))}
	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := service.seal(id, payload)
	if err != nil {
		t.Fatal(err)
	}
	config := service.Store.Settings.Channel
	_, err = service.Store.state.Exec(`INSERT INTO channel_events
		(message_id, event_id, event_hash, app_id, sender_open_id, payload, status, reply_uuid, available_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`, id, "event_"+id, hashBytes(payload),
		configured(config.AppID), configured(config.UserOpenID), sealed, status, newID(), nowText())
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestFeishuLegacyRecordRecovery(t *testing.T) {
	for _, writeState := range []string{"unreserved", "pending", "unknown", "succeeded", "verbatim"} {
		t.Run(writeState, func(t *testing.T) {
			settings := feishuSettings(t)
			store := testStore(t, settings)
			const id = "om_legacy_record"
			const text = " \t记录 \n backlog #原文\r\n第二段 \t\n"
			const legacyText = "backlog #原文\r\n第二段"
			key := "feishu:" + hashBytes([]byte(id))
			memos := map[string]map[string]any{}
			offline, loseResponse := false, false
			posts := 0
			client := fakeClient(settings, func(request *http.Request) (int, any, error) {
				if offline {
					return 503, nil, nil
				}
				if status, body, ok := identityResponse(request); ok {
					return status, body, nil
				}
				if request.Method == "POST" && request.URL.Path == "/api/v1/memos" {
					var body map[string]string
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					name := request.URL.Query().Get("memoId")
					memos["memos/"+name] = rawMemo(name, body["content"])
					posts++
					if loseResponse {
						return 503, nil, nil
					}
					return 200, memos["memos/"+name], nil
				}
				if memo := memos[strings.TrimPrefix(request.URL.Path, "/api/v1/")]; memo != nil {
					return 200, memo, nil
				}
				return 404, nil, nil
			})
			newService := func() *FeishuService {
				reviews := NewReviewService(store, client)
				return NewFeishuService(store, client, NewRecordService(store, client), reviews,
					NewNotificationService(store, reviews, nil))
			}
			service := newService()
			eventState, expectedText := "pending", legacyText
			if writeState != "unreserved" {
				offline, loseResponse = writeState == "pending", writeState == "unknown"
				if writeState == "verbatim" {
					expectedText = text
				}
				note, _, err := service.Records.Create(context.Background(), key, CreateNote{Content: expectedText})
				switch writeState {
				case "pending":
					if errorCode(err) != "UPSTREAM_UNAVAILABLE" {
						t.Fatalf("seed disconnected write: %v", err)
					}
				case "unknown":
					if errorCode(err) != "WRITE_OUTCOME_UNKNOWN" {
						t.Fatalf("seed uncertain write: %v", err)
					}
					eventState = "processing"
				default:
					if err != nil {
						t.Fatal(err)
					}
					eventState = "processing"
					if writeState == "succeeded" {
						expectedText = "edited after the accepted write"
						memos[note.Name]["content"] = expectedText
					}
				}
			}
			loseResponse = false
			sealed := seedLegacyFeishuRecord(t, service, id, text, eventState)
			restartFeishuAI(t, service)
			offline = true
			if progress, err := service.processOne(context.Background()); err != nil || !progress {
				t.Fatalf("disconnected replay: %t %v", progress, err)
			}
			var status, code string
			var payload []byte
			if err := store.state.QueryRow("SELECT status, COALESCE(last_error, ''), payload FROM channel_events WHERE message_id = ?", id).
				Scan(&status, &code, &payload); err != nil || status != "pending" || code != "UPSTREAM_UNAVAILABLE" || !bytes.Equal(payload, sealed) {
				t.Fatalf("disconnected event lost its retryable payload: status=%s code=%s err=%v", status, code, err)
			}
			reopen := func() {
				t.Helper()
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store = testStore(t, settings)
				service = newService()
				restartFeishuAI(t, service)
				if _, err := store.state.Exec("UPDATE channel_events SET available_at = 0 WHERE status = 'pending'"); err != nil {
					t.Fatal(err)
				}
			}
			reopen()
			body := testFeishuEvent(settings, id, "ou_owner", text)
			if _, _, err := service.Receive(httptest.NewRequest("POST", "/hooks/feishu", bytes.NewReader(body))); err != nil {
				t.Fatalf("old delivery hash changed across upgrade: %v", err)
			}
			offline = false
			if progress, err := service.processOne(context.Background()); err != nil || !progress {
				t.Fatalf("recovered replay: %t %v", progress, err)
			}
			var kind, name string
			if err := store.state.QueryRow("SELECT status, result_kind, resource_name, payload FROM channel_events WHERE message_id = ?", id).
				Scan(&status, &kind, &name, &payload); err != nil || status != "processed" || kind != "saved" || len(payload) != 0 {
				t.Fatalf("legacy record not recovered: status=%s kind=%s err=%v", status, kind, err)
			}
			if posts != 1 || memos[name]["content"] != expectedText {
				t.Fatalf("legacy write changed or duplicated: posts=%d content=%q want=%q", posts, memos[name]["content"], expectedText)
			}
			var writes int
			if err := store.state.QueryRow("SELECT COUNT(*) FROM note_writes").Scan(&writes); err != nil || writes != 1 {
				t.Fatalf("legacy idempotency key was replaced: writes=%d err=%v", writes, err)
			}
			if progress, err := service.processOne(context.Background()); err != nil || progress {
				t.Fatalf("completed event repeated: %t %v", progress, err)
			}

			// New queued text must retain its semantics through the same outage and restart.
			const newID = "om_new_record"
			body = testFeishuEvent(settings, newID, "ou_owner", text)
			if _, _, err := service.Receive(httptest.NewRequest("POST", "/hooks/feishu", bytes.NewReader(body))); err != nil {
				t.Fatal(err)
			}
			offline = true
			if progress, err := service.processOne(context.Background()); err != nil || !progress {
				t.Fatalf("new event outage: %t %v", progress, err)
			}
			reopen()
			offline = false
			if progress, err := service.processOne(context.Background()); err != nil || !progress {
				t.Fatalf("new event recovery: %t %v", progress, err)
			}
			if err := store.state.QueryRow("SELECT result_kind, resource_name FROM channel_events WHERE message_id = ?", newID).
				Scan(&kind, &name); err != nil || kind != "saved" {
				t.Fatalf("new event not recovered: kind=%s err=%v", kind, err)
			}
			if posts != 2 || memos[name]["content"] != text {
				t.Fatalf("new event was normalized: posts=%d content=%q", posts, memos[name]["content"])
			}
		})
	}
}

func TestFeishuLegacyRecordPreservesIdempotencyConflicts(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy", false: "verbatim"}[legacy], func(t *testing.T) {
			settings := feishuSettings(t)
			store := testStore(t, settings)
			client := fakeClient(settings, func(*http.Request) (int, any, error) {
				t.Error("conflicting request contacted Memos")
				return 500, nil, nil
			})
			reviews := NewReviewService(store, client)
			service := NewFeishuService(store, client, NewRecordService(store, client), reviews, NewNotificationService(store, reviews, nil))
			const id = "om_conflict"
			const text = "记录 backlog"
			content := "unrelated request"
			if !legacy {
				content = "backlog"
			}
			_, hash, err := prepareNote(CreateNote{Content: content})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.reserveNote("feishu:"+hashBytes([]byte(id)), hash); err != nil {
				t.Fatal(err)
			}
			if legacy {
				seedLegacyFeishuRecord(t, service, id, text, "pending")
			} else {
				body := testFeishuEvent(settings, id, "ou_owner", text)
				if _, _, err := service.Receive(httptest.NewRequest("POST", "/hooks/feishu", bytes.NewReader(body))); err != nil {
					t.Fatal(err)
				}
			}
			if progress, err := service.processOne(context.Background()); err != nil || !progress {
				t.Fatalf("conflict processing: %t %v", progress, err)
			}
			var kind, code, savedHash string
			if err := store.state.QueryRow("SELECT result_kind, resource_name FROM channel_events").Scan(&kind, &code); err != nil ||
				kind != "error" || code != "IDEMPOTENCY_CONFLICT" {
				t.Fatalf("conflict bypassed: kind=%s code=%s err=%v", kind, code, err)
			}
			if err := store.state.QueryRow("SELECT request_hash FROM note_writes").Scan(&savedHash); err != nil || savedHash != hash {
				t.Fatalf("persisted request hash changed: %v", err)
			}
		})
	}
}

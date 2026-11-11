package moerise

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type feishuAIPlatform struct {
	unknownFeishuReply
	mode          string
	texts         []string
	uuids         []string
	messages      map[string]FeishuSentMessage
	fixture       AIResult
	fixtureStatus string
	fixtureWrites *int
}

func (p *feishuAIPlatform) Reply(_ context.Context, parent, uuid, text string) (string, error) {
	p.calls++
	p.uuids = append(p.uuids, uuid)
	if p.mode == "rejected" {
		return "", upstreamError("CHANNEL_REJECTED", "rate limited")
	}
	if p.mode == "crash" {
		panic("send interrupted")
	}
	p.texts = append(p.texts, text)
	receipt := fmt.Sprintf("om_part_%d", p.calls)
	p.messages[receipt] = FeishuSentMessage{ID: receipt, ParentID: parent, Text: text,
		SenderType: "app", SenderIDType: "app_id", SenderID: "cli_test_app"}
	if p.mode == "unknown" {
		return "", upstreamError("SEND_OUTCOME_UNKNOWN", "response lost")
	}
	return receipt, nil
}

func (p *feishuAIPlatform) GetMessage(_ context.Context, receipt string) (FeishuSentMessage, error) {
	return p.messages[receipt], nil
}

func feishuAIFixture(t *testing.T, result AIResult) (*FeishuService, *feishuAIPlatform, map[string]map[string]any, *int) {
	t.Helper()
	writes := new(int)
	memos := map[string]map[string]any{"memos/seed": rawMemo("seed", "fictional basil")}
	memos["memos/seed"]["tags"] = []string{"topic"}
	evidence := evidenceFixture(t, memos)
	result.References = []Evidence{legacyEvidence(t, evidence.Search, "memos/seed", "E1")}
	result.UsedSources = []string{"memos/seed"}
	store := evidence.Search.Store
	store.Settings.Channel = feishuSettings(t).Channel
	reviews := NewReviewService(store, evidence.Search.Source)
	notifications := NewNotificationService(store, reviews, nil)
	service := NewFeishuService(store, evidence.Search.Source, NewRecordService(store, evidence.Search.Source), reviews, notifications)
	service.Search = evidence.Search
	platform := &feishuAIPlatform{messages: map[string]FeishuSentMessage{}, fixture: result, fixtureWrites: writes}
	service.Client = platform
	notifications.Transport = NewFeishuDelivery(store, reviews, service)
	return service, platform, memos, writes
}

func receiveFeishuAI(t *testing.T, service *FeishuService, text string) feishuMessage {
	t.Helper()
	body := testFeishuEvent(service.Store.Settings, "om_ai", "ou_owner", text)
	request := httptest.NewRequest("POST", "/hooks/feishu", bytes.NewReader(body))
	if _, _, err := service.Receive(request); err != nil {
		t.Fatal(err)
	}
	return feishuMessage{ID: "om_ai", Type: "text", Content: string(canonicalJSON(map[string]string{"text": text}))}
}

func processLegacyFeishuAI(t *testing.T, service *FeishuService) {
	t.Helper()
	// Seed a generation from the former entrypoint; new incoming text only records.
	var count int
	if err := service.Store.state.QueryRow("SELECT COUNT(*) FROM feishu_ai_generations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		if _, _, err := service.seedLegacyAI(context.Background(), "om_ai"); err != nil {
			t.Fatal(err)
		}
	}
	if progress, err := service.processOne(context.Background()); err != nil || !progress {
		t.Fatalf("processing: %v %v", progress, err)
	}
}

func deliverFeishuAI(t *testing.T, service *FeishuService) {
	t.Helper()
	if progress, err := service.Notifications.Deliver(context.Background()); err != nil || !progress {
		t.Fatalf("delivery: %v %v", progress, err)
	}
}

func restartFeishuAI(t *testing.T, service *FeishuService) {
	t.Helper()
	if err := service.prepare(); err != nil {
		t.Fatalf("restart recovery: %v", err)
	}
}

func legacyInsightResult() AIResult {
	return AIResult{Kind: "single", Status: "answered", Completed: true,
		Statements: []AIStatement{
			{Kind: "fact", Text: "The fictional note records basil.", Citations: []string{"E1"}},
			{Kind: "interpretation", Text: "A possible gardening topic.", Citations: []string{"E1"}},
			{Kind: "question", Text: "What would be useful to record next?", Citations: []string{}},
		}, Model: "historical-fixture", Omitted: []string{}, Missing: []string{}, Timeline: []AITimelinePeriod{},
	}
}

func longFeishuAIOutput() AIResult {
	result := legacyInsightResult()
	result.Status, result.Missing = "insufficient", []string{"缺少后续记录。"}
	result.Statements = nil
	for i := range 12 {
		result.Statements = append(result.Statements, AIStatement{
			Kind: []string{"fact", "interpretation", "question"}[i%3], Text: strings.Repeat("虚构的长正文。", 90), Citations: []string{"E1"},
		})
	}
	return result
}

func TestFeishuAITimelineRendering(t *testing.T) {
	result := AIResult{
		Statements: []AIStatement{{Kind: "interpretation", Text: "跨段解释只出现一次。", Citations: []string{"E1"}}},
		Timeline: []AITimelinePeriod{
			{ID: "P1", From: "2020-01-01T00:00:00Z", Until: "2021-01-01T00:00:00Z", RepresentativeNames: []string{"memos/seed"}, StatementIndexes: []int{0}},
			{ID: "P2", From: "2021-01-01T00:00:00Z", Until: "2022-01-01T00:00:00Z", Empty: true, MissingReason: "NO_MATCHES"},
			{ID: "P3", From: "2022-01-01T00:00:00Z", Until: "2023-01-01T00:00:00Z", Empty: true, MissingReason: "CONTEXT_BUDGET"},
		},
	}
	text := renderFeishuAI("timeline", result)
	if strings.Count(text, "跨段解释只出现一次。") != 1 || strings.Count(text, "缺少证据：") != 2 ||
		!strings.Contains(text, "2020-01-01T00:00:00Z 至 2021-01-01T00:00:00Z") ||
		!strings.Contains(text, "上下文预算不足") || !strings.Contains(text, "结束时间不含") {
		t.Fatalf("timeline reply lost period dates, gaps or duplicated interpretations: %s", text)
	}
}

func TestLegacyRepliesDoNotSuggestRetiredCommands(t *testing.T) {
	for _, text := range []string{feishuAIFailed, feishuAIInterrupted, feishuAISourceFailed, renderReview(ReviewBatch{})} {
		for _, hint := range []string{"重新发送", "换一组", "打开/略过"} {
			if strings.Contains(text, hint) {
				t.Fatalf("legacy reply still suggests a retired command: %s", text)
			}
		}
	}
}

func TestFeishuAIResultPrecedesEventCompletionAndSurvivesRedelivery(t *testing.T) {
	service, platform, _, fixtures := feishuAIFixture(t, legacyInsightResult())
	message := receiveFeishuAI(t, service, "洞察 memos/seed")
	receiveFeishuAI(t, service, "洞察 memos/seed")
	if kind, _, err := service.seedLegacyAI(context.Background(), message.ID); err != nil || kind != "ai" {
		t.Fatal(err)
	}
	var status string
	if err := service.Store.state.QueryRow("SELECT status FROM channel_events").Scan(&status); err != nil || status != "pending" {
		t.Fatal("test did not leave the crash gap between result and event completion")
	}
	restartFeishuAI(t, service)
	processLegacyFeishuAI(t, service)
	for _, column := range []struct{ table, field string }{
		{"feishu_ai_generations", "request"}, {"feishu_ai_generations", "result"}, {"feishu_ai_parts", "payload"},
	} {
		var sealed []byte
		if err := service.Store.state.QueryRow("SELECT " + column.field + " FROM " + column.table).Scan(&sealed); err != nil ||
			len(sealed) == 0 || bytes.Contains(sealed, []byte("memos/seed")) || bytes.Contains(sealed, []byte("fictional")) {
			t.Fatalf("AI private data was not encrypted in %s: %v", column.field, err)
		}
	}
	// Completed results remain deliverable without an active generation service.
	deliverFeishuAI(t, service)
	restartFeishuAI(t, service)
	receiveFeishuAI(t, service, "洞察 memos/seed")
	if progress, err := service.processOne(context.Background()); progress || err != nil || *fixtures != 1 || platform.calls != 1 {
		t.Fatalf("redelivery reconstructed a historical fixture or confirmed send: %v %v %d %d", progress, err, *fixtures, platform.calls)
	}
	if !strings.Contains(platform.texts[0], "[事实]") || !strings.Contains(platform.texts[0], "[解释]") ||
		!strings.Contains(platform.texts[0], "[追问]") || !strings.Contains(platform.texts[0], "memos/seed 第 1-1 行") {
		t.Fatal("AI reply lost labels, source names or line numbers")
	}
}

func TestFeishuAILegacyInterruptionAndFailureAreTerminal(t *testing.T) {
	for _, interrupted := range []bool{true, false} {
		t.Run(fmt.Sprintf("interrupted=%t", interrupted), func(t *testing.T) {
			service, platform, _, writes := feishuAIFixture(t, legacyInsightResult())
			platform.fixtureStatus = "failed"
			if interrupted {
				platform.fixtureStatus = "running"
			}
			receiveFeishuAI(t, service, "洞察 memos/seed")
			if _, _, err := service.seedLegacyAI(t.Context(), "om_ai"); err != nil {
				t.Fatal(err)
			}
			restartFeishuAI(t, service)
			processLegacyFeishuAI(t, service)
			platform.mode = "rejected"
			deliverFeishuAI(t, service)
			if _, err := service.Store.state.Exec("UPDATE notifications SET available_at = 0"); err != nil {
				t.Fatal(err)
			}
			platform.mode = ""
			deliverFeishuAI(t, service)
			expected := feishuAIFailed
			if interrupted {
				expected = feishuAIInterrupted
			}
			if *writes != 1 || len(platform.texts) != 1 || !strings.Contains(platform.texts[0], expected) {
				t.Fatalf("legacy terminal state was reconstructed or lost: fixtures=%d texts=%v", *writes, platform.texts)
			}
		})
	}
}

func TestFeishuAILongPartsRetryAndUnknownResolution(t *testing.T) {
	service, platform, _, fixtures := feishuAIFixture(t, longFeishuAIOutput())
	receiveFeishuAI(t, service, "洞察 memos/seed")
	processLegacyFeishuAI(t, service)
	deliverFeishuAI(t, service)
	var eventStatus string
	if err := service.Store.state.QueryRow("SELECT status FROM channel_events").Scan(&eventStatus); err != nil || eventStatus != "processed" {
		t.Fatal("first part marked the whole event as completed")
	}
	platform.mode = "rejected"
	deliverFeishuAI(t, service)
	var second string
	if err := service.Store.state.QueryRow("SELECT notification_id FROM feishu_ai_parts WHERE ordinal = 1").Scan(&second); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.state.Exec("UPDATE notifications SET available_at = 0 WHERE id = ?", second); err != nil {
		t.Fatal(err)
	}
	restartFeishuAI(t, service)
	platform.mode = "unknown"
	deliverFeishuAI(t, service)
	if progress, err := service.Notifications.Deliver(context.Background()); progress || err != nil {
		t.Fatal("later parts bypassed an unknown predecessor")
	}
	if _, err := service.Notifications.Resolve(context.Background(), second, "sent", "om_part_1", "operator"); errorCode(err) != "NOTIFICATION_CONFLICT" {
		t.Fatal("another part's receipt confirmed an unknown part")
	}
	if _, err := service.Notifications.Resolve(context.Background(), second, "sent", "om_part_3", "operator"); err != nil {
		t.Fatal(err)
	}
	platform.mode = ""
	for range 20 {
		progress, err := service.Notifications.Deliver(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !progress {
			break
		}
	}
	if *fixtures != 1 || platform.uuids[0] == platform.uuids[1] || platform.uuids[1] != platform.uuids[2] {
		t.Fatal("part UUIDs were reused across parts or changed on a known retry")
	}
	for _, text := range platform.texts {
		if len(text) > feishuAIPartBytes || !utf8.ValidString(text) {
			t.Fatal("long reply exceeded its byte limit or split a UTF-8 code point")
		}
	}
	full := strings.Join(platform.texts, "")
	for _, required := range []string{"[事实]", "[解释]", "[追问]", "引用 E1", "memos/seed", "第 1-1 行", "http", "缺少后续记录。"} {
		if !strings.Contains(full, required) {
			t.Fatalf("long reply lost %q", required)
		}
	}
	if err := service.Store.state.QueryRow("SELECT status FROM channel_events").Scan(&eventStatus); err != nil || eventStatus != "succeeded" {
		t.Fatalf("last part did not complete the event: %s %v", eventStatus, err)
	}
}

func TestFeishuAISourceChangesStopCachedParts(t *testing.T) {
	for _, mode := range []string{"edited", "deleted", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			service, platform, memos, fixtures := feishuAIFixture(t, longFeishuAIOutput())
			receiveFeishuAI(t, service, "洞察 memos/seed")
			processLegacyFeishuAI(t, service)
			deliverFeishuAI(t, service)
			switch mode {
			case "edited":
				memos["memos/seed"]["content"] = "changed source"
			case "deleted":
				delete(memos, "memos/seed")
			case "foreign":
				memos["memos/seed"]["creator"] = "users/other"
			}
			deliverFeishuAI(t, service)
			if platform.calls != 2 || !strings.Contains(platform.texts[1], feishuAISourceFailed) ||
				strings.Contains(platform.texts[1], "虚构") || strings.Contains(platform.texts[1], "memos/seed") ||
				*fixtures != 1 {
				t.Fatalf("cached private output survived a source change: %v", platform.texts)
			}
			if progress, err := service.Notifications.Deliver(context.Background()); progress || err != nil {
				t.Fatal("obsolete later parts remained sendable")
			}
		})
	}
}

func TestFeishuAIResolvedPartsCannotSendOldContent(t *testing.T) {
	service, platform, memos, fixtures := feishuAIFixture(t, longFeishuAIOutput())
	receiveFeishuAI(t, service, "洞察 memos/seed")
	processLegacyFeishuAI(t, service)
	if _, err := service.Store.state.Exec("UPDATE notifications SET status = 'unknown', last_error = 'STATE_RESTORED'"); err != nil {
		t.Fatal(err)
	}
	rows, err := service.Store.state.Query("SELECT notification_id FROM feishu_ai_parts ORDER BY ordinal")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) < 3 {
		t.Fatalf("missing long reply parts: %v %v", ids, err)
	}
	// A later, never-attempted part can already have an operator resolution.
	// It must stay blocked and retain that audit record when sources change.
	if _, err := service.Notifications.Resolve(context.Background(), ids[2], "not_sent", "", "operator"); err != nil {
		t.Fatal(err)
	}
	if progress, err := service.Notifications.Deliver(context.Background()); progress || err != nil {
		t.Fatal("an operator resolution bypassed an unknown predecessor")
	}
	memos["memos/seed"]["content"] = "changed"
	for i, id := range ids {
		if i != 2 {
			if _, err := service.Notifications.Resolve(context.Background(), id, "not_sent", "", "operator"); err != nil {
				t.Fatal(err)
			}
		}
		deliverFeishuAI(t, service)
	}
	for _, text := range platform.texts {
		if !strings.Contains(text, feishuAISourceFailed) || strings.Contains(text, "虚构") {
			t.Fatalf("resolved parts sent invalidated output: %s", text)
		}
	}
	if *fixtures != 1 {
		t.Fatal("resolving a part reconstructed a historical fixture")
	}
}

func TestFeishuAIRestoredPendingGenerationDoesNotRun(t *testing.T) {
	service, platform, _, fixtures := feishuAIFixture(t, legacyInsightResult())
	receiveFeishuAI(t, service, "洞察 memos/seed")
	request, err := service.seal("ai-request:om_ai", canonicalJSON(legacyAIRequest{Names: []string{"memos/seed"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.state.Exec(`INSERT INTO feishu_ai_generations
		(message_id, operation, request, status, created_at, updated_at)
		VALUES ('om_ai', 'insight', ?, 'running', ?, ?)`, request, nowText(), nowText()); err != nil {
		t.Fatal(err)
	}
	settings := service.Store.Settings
	snapshot := filepath.Join(filepath.Dir(settings.StateDir), "feishu-ai-snapshot")
	if _, err := Backup(settings, snapshot); err != nil {
		t.Fatal(err)
	}
	service.Store.Close()
	if err := Restore(settings, snapshot, filepath.Join(filepath.Dir(settings.StateDir), "before-ai-restore")); err != nil {
		t.Fatal(err)
	}
	store := testStore(t, settings)
	service.Store, service.Notifications.Store = store, store
	delivery := service.Notifications.Transport.(*FeishuDelivery)
	delivery.Store = store
	restartFeishuAI(t, service)
	processLegacyFeishuAI(t, service)
	unknown, err := service.Notifications.List("unknown")
	if err != nil || len(unknown) != 1 || *fixtures != 0 || platform.calls != 0 {
		t.Fatalf("restoring an older snapshot invoked the model: %+v %v", unknown, err)
	}
	if _, err := service.Notifications.Resolve(context.Background(), unknown[0].ID, "not_sent", "", "operator"); err != nil {
		t.Fatal(err)
	}
	deliverFeishuAI(t, service)
	if !strings.Contains(platform.texts[0], feishuAIInterrupted) || *fixtures != 0 {
		t.Fatal("restored event did not return a terminal interruption notice")
	}
}

type legacyAIRequest struct {
	Names []string `json:"names"`
}

// seedLegacyAI writes a fixed historical result; it never invokes a model.
func (s *FeishuService) seedLegacyAI(ctx context.Context, messageID string) (string, string, error) {
	request := legacyAIRequest{Names: []string{"memos/seed"}}
	sealed, err := s.seal("ai-request:"+messageID, canonicalJSON(request))
	if err != nil {
		return "", "", err
	}
	platform := s.Client.(*feishuAIPlatform)
	status, code := "completed", ""
	if platform.fixtureStatus != "" {
		status = platform.fixtureStatus
	}
	var restored bool
	config := s.Store.Settings.Channel
	if err := s.Store.state.QueryRowContext(ctx, `SELECT COALESCE(last_error = 'STATE_RESTORED', 0)
        FROM channel_events WHERE message_id = ? AND app_id = ? AND sender_open_id = ?`,
		messageID, configured(config.AppID), configured(config.UserOpenID)).Scan(&restored); err != nil {
		return "", "", err
	}
	if restored {
		status, code = "interrupted", "STATE_RESTORED"
	}
	var saved []byte
	if status == "completed" {
		saved, err = s.seal("ai-result:"+messageID, canonicalJSON(platform.fixture))
		if err != nil {
			return "", "", err
		}
	}
	stamp := nowText()
	inserted, err := s.Store.state.ExecContext(ctx, `INSERT OR IGNORE INTO feishu_ai_generations
        (message_id,operation,request,result,status,error_code,created_at,updated_at)
        VALUES (?, 'insight', ?, ?, ?, ?, ?, ?)`, messageID, sealed, saved, status, nullable(code), stamp, stamp)
	if err != nil {
		return "", "", err
	}
	count, err := inserted.RowsAffected()
	if err != nil {
		return "", "", err
	}
	if count > 0 {
		*platform.fixtureWrites++
	}
	return "ai", messageID, nil
}

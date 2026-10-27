package moerise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type feishuAIPlatform struct {
	unknownFeishuReply
	mode     string
	texts    []string
	uuids    []string
	messages map[string]FeishuSentMessage
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

func feishuAIFixture(t *testing.T, output string) (*FeishuService, *feishuAIPlatform, map[string]map[string]any, *int) {
	t.Helper()
	calls := new(int)
	memos := map[string]map[string]any{"memos/seed": rawMemo("seed", "fictional basil")}
	memos["memos/seed"]["tags"] = []string{"topic"}
	ai := insightFixture(t, memos, nil)
	ai.Provider = aiLLMFunc(func(_ context.Context, _ []LLMMessage, emit func(string) error) (LLMResult, error) {
		*calls++
		var status string
		if err := ai.Search.Store.state.QueryRow("SELECT status FROM feishu_ai_generations").Scan(&status); err != nil || status != "running" {
			t.Fatalf("model started before its running state was durable: %q %v", status, err)
		}
		if err := emit(output); err != nil {
			return LLMResult{}, err
		}
		return LLMResult{Content: output, Model: "fictional-model", FinishReason: "stop"}, nil
	})
	store := ai.Search.Store
	store.Settings.Channel = feishuSettings(t).Channel
	reviews := NewReviewService(store, ai.Search.Source)
	notifications := NewNotificationService(store, reviews, nil)
	service := NewFeishuService(store, ai.Search.Source, NewRecordService(store, ai.Search.Source), reviews, notifications)
	service.Search, service.AI = ai.Search, ai
	platform := &feishuAIPlatform{messages: map[string]FeishuSentMessage{}}
	service.Client = platform
	notifications.Transport = NewFeishuDelivery(store, reviews, service)
	return service, platform, memos, calls
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

func processFeishuAI(t *testing.T, service *FeishuService) {
	t.Helper()
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("restart recovery: %v", err)
	}
}

func longFeishuAIOutput() string {
	var output strings.Builder
	for i := range 12 {
		kind := []string{"fact", "interpretation", "question"}[i%3]
		body, _ := json.Marshal(map[string]any{"type": kind, "text": strings.Repeat("虚构的长正文。", 90), "citations": []string{"E1"}})
		fmt.Fprintln(&output, string(body))
	}
	fmt.Fprintln(&output, `{"type":"result","status":"insufficient","missing":["缺少后续记录。"]}`)
	return output.String()
}

func TestFeishuAICommandsAndRecordIsolation(t *testing.T) {
	for _, test := range []struct {
		text, operation, question string
		names                     int
		timeline                  bool
	}{
		{"洞察 memos/a, memos/b", "insight", "", 2, false},
		{"问答 basil 是什么 #topic", "answer", "basil 是什么", 0, false},
		{"历程 basil 的变化 #topic", "timeline", "basil 的变化", 0, true},
	} {
		operation, request, err := parseFeishuAI(test.text)
		if err != nil || operation != test.operation || request.Question != test.question ||
			len(request.Names) != test.names || request.Timeline != test.timeline {
			t.Fatalf("wrong command routing: %s %+v %v", operation, request, err)
		}
		if test.names == 0 && (len(request.Tags) != 1 || request.Tags[0] != "topic") {
			t.Fatal("tag was passed as part of the question")
		}
	}
	for _, text := range []string{"洞察", "洞察 memos/a,memos/a", "问答 #topic", "历程"} {
		if _, _, err := parseFeishuAI(text); err == nil {
			t.Fatalf("invalid AI command accepted: %q", text)
		}
	}
	service, _, _, calls := feishuAIFixture(t, insightOutput)
	kind, _, err := service.execute(context.Background(), feishuMessage{ID: "om_chat", Type: "text",
		Content: `{"text":"聊聊 basil"}`})
	if err != nil || kind != "help" || *calls != 0 {
		t.Fatal("ordinary text triggered AI")
	}
	var created map[string]any
	source := fakeClient(service.Store.Settings, func(request *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(request); ok {
			return status, body, nil
		}
		if request.Method == "POST" {
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			created = rawMemo(request.URL.Query().Get("memoId"), body["content"])
		}
		if created == nil {
			return 404, nil, nil
		}
		return 200, created, nil
	})
	service.Records = NewRecordService(service.Store, source)
	kind, _, err = service.execute(context.Background(), feishuMessage{ID: "om_record", Type: "text",
		Content: `{"text":"记录 问答 basil"}`})
	if err != nil || kind != "saved" || created["content"] != "问答 basil" || *calls != 0 {
		t.Fatalf("record command triggered AI or lost content: %s %v %v", kind, created, err)
	}
	receiveFeishuAI(t, service, "问答 basil #topic")
	processFeishuAI(t, service)
	deliverFeishuAI(t, service)
	if *calls != 1 {
		t.Fatal("explicit answer command did not invoke the shared service")
	}
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

func TestFeishuAIResultPrecedesEventCompletionAndSurvivesRedelivery(t *testing.T) {
	service, platform, _, calls := feishuAIFixture(t, insightOutput)
	message := receiveFeishuAI(t, service, "洞察 memos/seed")
	receiveFeishuAI(t, service, "洞察 memos/seed")
	if kind, _, err := service.execute(context.Background(), message); err != nil || kind != "ai" {
		t.Fatal(err)
	}
	var status string
	if err := service.Store.state.QueryRow("SELECT status FROM channel_events").Scan(&status); err != nil || status != "pending" {
		t.Fatal("test did not leave the crash gap between result and event completion")
	}
	restartFeishuAI(t, service)
	processFeishuAI(t, service)
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
	service.AI = nil
	deliverFeishuAI(t, service)
	restartFeishuAI(t, service)
	receiveFeishuAI(t, service, "洞察 memos/seed")
	if progress, err := service.processOne(context.Background()); progress || err != nil || *calls != 1 || platform.calls != 1 {
		t.Fatalf("redelivery repeated a paid generation or confirmed send: %v %v %d %d", progress, err, *calls, platform.calls)
	}
	if !strings.Contains(platform.texts[0], "[事实]") || !strings.Contains(platform.texts[0], "[解释]") ||
		!strings.Contains(platform.texts[0], "[追问]") || !strings.Contains(platform.texts[0], "memos/seed 第 1-1 行") {
		t.Fatal("AI reply lost labels, source names or line numbers")
	}
}

func TestFeishuAIGenerationInterruptionAndFailureAreTerminal(t *testing.T) {
	for _, interrupted := range []bool{true, false} {
		t.Run(fmt.Sprintf("interrupted=%t", interrupted), func(t *testing.T) {
			service, platform, _, calls := feishuAIFixture(t, insightOutput)
			service.AI.Provider = aiLLMFunc(func(context.Context, []LLMMessage, func(string) error) (LLMResult, error) {
				*calls++
				if interrupted {
					panic("generation interrupted")
				}
				return LLMResult{}, upstreamError("LLM_UNAVAILABLE", "private vendor error details")
			})
			receiveFeishuAI(t, service, "洞察 memos/seed")
			func() {
				defer func() { _ = recover() }()
				processFeishuAI(t, service)
			}()
			restartFeishuAI(t, service)
			if interrupted {
				processFeishuAI(t, service)
			}
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
			if *calls != 1 || len(platform.texts) != 1 || !strings.Contains(platform.texts[0], expected) ||
				strings.Contains(platform.texts[0], "private") {
				t.Fatalf("failure was replayed or exposed internal output: %d %v", *calls, platform.texts)
			}
		})
	}
}

func TestFeishuAILongPartsRetryAndUnknownResolution(t *testing.T) {
	service, platform, _, calls := feishuAIFixture(t, longFeishuAIOutput())
	receiveFeishuAI(t, service, "洞察 memos/seed")
	processFeishuAI(t, service)
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
	if *calls != 1 || platform.uuids[0] == platform.uuids[1] || platform.uuids[1] != platform.uuids[2] {
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
			service, platform, memos, calls := feishuAIFixture(t, longFeishuAIOutput())
			receiveFeishuAI(t, service, "洞察 memos/seed")
			processFeishuAI(t, service)
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
				*calls != 1 {
				t.Fatalf("cached private output survived a source change: %v", platform.texts)
			}
			if progress, err := service.Notifications.Deliver(context.Background()); progress || err != nil {
				t.Fatal("obsolete later parts remained sendable")
			}
		})
	}
}

func TestFeishuAIResolvedPartsCannotSendOldContent(t *testing.T) {
	service, platform, memos, calls := feishuAIFixture(t, longFeishuAIOutput())
	receiveFeishuAI(t, service, "洞察 memos/seed")
	processFeishuAI(t, service)
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
	if *calls != 1 {
		t.Fatal("resolving a part repeated AI generation")
	}
}

func TestFeishuAIRestoredPendingGenerationDoesNotRun(t *testing.T) {
	service, platform, _, calls := feishuAIFixture(t, insightOutput)
	receiveFeishuAI(t, service, "洞察 memos/seed")
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
	processFeishuAI(t, service)
	unknown, err := service.Notifications.List("unknown")
	if err != nil || len(unknown) != 1 || *calls != 0 || platform.calls != 0 {
		t.Fatalf("restoring an older snapshot invoked the model: %+v %v", unknown, err)
	}
	if _, err := service.Notifications.Resolve(context.Background(), unknown[0].ID, "not_sent", "", "operator"); err != nil {
		t.Fatal(err)
	}
	deliverFeishuAI(t, service)
	if !strings.Contains(platform.texts[0], feishuAIInterrupted) || *calls != 0 {
		t.Fatal("restored event did not return a terminal interruption notice")
	}
}

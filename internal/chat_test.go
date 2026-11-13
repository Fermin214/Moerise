package moerise

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func chatFixture(t *testing.T, memos map[string]map[string]any, provider LLMProvider) *ChatService {
	t.Helper()
	store, source := sourceFixture(t, memos)
	store.Settings.LLM.Enabled = true
	model := "chat-test-model"
	store.Settings.LLM.Model = &model
	notes := NewNoteService(store.Settings, store, source)
	chat := NewChatService(store, notes, NewRecordService(store, source), provider)
	t.Cleanup(chat.Close)
	return chat
}

func createTestChat(t *testing.T, service *ChatService, names ...string) ChatSession {
	t.Helper()
	chat, created, err := service.Create(context.Background(), ChatCreate{newID(), "材料讨论", names})
	if err != nil || !created {
		t.Fatalf("create: %+v %v %v", chat, created, err)
	}
	return chat
}

func textChatProvider(text string) LLMProvider {
	return aiLLMFunc(func(_ context.Context, _ []LLMMessage, emit func(string) error) (LLMResult, error) {
		if err := emit(text); err != nil {
			return LLMResult{}, err
		}
		return LLMResult{Content: text, Model: "chat-test-model", FinishReason: "stop"}, nil
	})
}

func TestChatFullMaterialsArchivedHistoryAndDurableEvents(t *testing.T) {
	ctx := context.Background()
	memos := map[string]map[string]any{
		"memos/one":        rawMemo("one", "  叶片长出。\n\n#中文标签 😀 "),
		"memos/two":        rawMemo("two", "归档：也许是光照影响？"),
		"memos/unselected": rawMemo("unselected", "NEVER_INCLUDE_RELATED_HISTORY"),
	}
	memos["memos/two"]["state"] = "ARCHIVED"
	service := chatFixture(t, memos, nil)
	chat := createTestChat(t, service, "memos/one", "memos/two")
	calls := 0
	reply := "## 把窗台当作一座微型城市\n\n可以把新叶类比成冒出的想法，让不同窗位扮演不同的信息环境。\n\n一个大胆的小尝试：轮流给每盆写一封信，看看观察植物会不会改变自己的注意力。这是由笔记引出的新玩法。 #联想\n"
	service.Provider = aiLLMFunc(func(_ context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
		calls++
		var input []struct {
			Name, Content string
		}
		materialJSON := strings.TrimPrefix(messages[1].Content, "本轮明确选定的全部笔记材料：\n")
		if messages[0].Role != "system" || json.Unmarshal([]byte(materialJSON), &input) != nil || len(input) != 2 ||
			input[0].Name != "memos/one" || input[0].Content != memos["memos/one"]["content"] ||
			input[1].Name != "memos/two" || input[1].Content != memos["memos/two"]["content"] {
			t.Fatalf("model did not receive exact full selected materials: %+v", messages)
		}
		if strings.Contains(string(canonicalJSON(messages)), "NEVER_INCLUDE_RELATED_HISTORY") {
			t.Fatal("added unselected history")
		}
		if calls == 1 && (len(messages) != 3 || messages[2].Content != service.Store.Settings.Insights.DefaultPrompt) {
			t.Fatal("insight did not use saved default Prompt")
		}
		if calls == 2 && (len(messages) != 5 || messages[2].Content != service.Store.Settings.Insights.DefaultPrompt ||
			messages[3].Content != reply || messages[3].Role != "assistant" || messages[4].Content != "还有其他解释吗？") {
			t.Fatalf("followup lost full conversation: %+v", messages)
		}
		for _, text := range []string{reply[:6], reply[6:]} {
			// 第一段以 UTF-8 字符边界结束。
			if err := emit(text); err != nil {
				return LLMResult{}, err
			}
		}
		return LLMResult{Content: reply, Model: "chat-test-model", FinishReason: "stop"}, nil
	})
	var events []string
	var delivered strings.Builder
	result, err := service.Generate(ctx, chat.ID, ChatGenerate{RequestID: "first", Version: chat.Version, Kind: "insight", Content: ""}, func(event ChatEvent) error {
		events = append(events, event.Type)
		switch event.Type {
		case "message":
			if event.Turn == nil || event.Turn.Assistant.Status != "running" {
				t.Fatal("missing running turn")
			}
		case "delta":
			delivered.WriteString(event.Text)
			durable, err := service.Store.chatTurn(chat.ID, "first")
			if err != nil || durable.Assistant.Content != delivered.String() {
				t.Fatal("stream text published before persistence")
			}
		case "complete":
			durable, err := service.Store.chatTurn(chat.ID, "first")
			if err != nil || durable.Assistant.Status != "completed" || !reflect.DeepEqual(*event.Turn, durable) {
				t.Fatal("completion preceded final persistence")
			}
		}
		return nil
	})
	if err != nil || result.Assistant.Content != reply || result.Assistant.Status != "completed" ||
		delivered.String() != reply || events[0] != "message" || events[len(events)-1] != "complete" || calls != 1 {
		t.Fatalf("chat: %+v events=%v error=%v", result, events, err)
	}
	if _, err := service.Generate(ctx, chat.ID, ChatGenerate{RequestID: "follow", Version: 1, Kind: "question", Content: "还有其他解释吗？"}, nil); err != nil || calls != 2 {
		t.Fatalf("followup: %v calls=%d", err, calls)
	}
	delete(memos, "memos/one")
	replay, err := service.Generate(ctx, chat.ID, ChatGenerate{RequestID: "first", Version: 1, Kind: "insight", Content: ""}, nil)
	if err != nil || replay.ID != result.ID || calls != 2 {
		t.Fatalf("replay rechecked or regenerated old response: %v", err)
	}
}

func TestChatMaterialLimitExcludesConversationAndDoesNotTruncate(t *testing.T) {
	for _, mode := range []string{"characters", "combined", "unicode-bytes", "history", "messages", "question", "exact-limit"} {
		t.Run(mode, func(t *testing.T) {
			text, question := strings.Repeat("字", ChatCharacterLimit), "问题"
			if mode == "characters" || mode == "combined" {
				text += "字"
			} else if mode == "unicode-bytes" {
				text = strings.Repeat("😀", ChatCharacterLimit)
			} else if mode == "question" {
				question = strings.Repeat("😀", ChatCharacterLimit+1)
			}
			memos := map[string]map[string]any{"memos/one": rawMemo("one", text)}
			names := []string{"memos/one"}
			if mode == "combined" {
				text = strings.Repeat("字", 10000)
				memos["memos/one"] = rawMemo("one", text)
				memos["memos/two"] = rawMemo("two", text+"字")
				names = append(names, "memos/two")
			}
			service := chatFixture(t, memos, nil)
			chat := createTestChat(t, service, names...)
			if mode == "history" || mode == "messages" {
				n, content := 2, strings.Repeat("旧回复", 50000)
				if mode == "messages" {
					n, content = 127, "短回复"
				}
				for range n {
					turn := testChatTurn(t, service.Store, chat, newID())
					if err := service.Store.appendChatText(chat.ID, turn.ID, content); err != nil {
						t.Fatal(err)
					}
					if err := service.Store.finishChatTurn(chat.ID, turn.ID, "completed", "", LLMUsage{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := service.Store.chat(chat.ID)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			service.Provider = aiLLMFunc(func(_ context.Context, messages []LLMMessage, emit func(string) error) (LLMResult, error) {
				calls++
				if !strings.Contains(messages[1].Content, text) || len(messages) != 3+2*len(before.Turns) ||
					messages[len(messages)-1].Content != question {
					t.Fatal("material, question or history was truncated")
				}
				for i, turn := range before.Turns {
					if messages[2+2*i].Content != turn.User.Content || messages[3+2*i].Content != turn.Assistant.Content {
						t.Fatal("conversation history changed")
					}
				}
				if err := emit("回复"); err != nil {
					return LLMResult{}, err
				}
				return LLMResult{Content: "回复", FinishReason: "stop"}, nil
			})
			expected := ""
			if mode == "characters" || mode == "combined" {
				expected = "CHAT_MATERIAL_LIMIT"
			}
			_, err = service.Generate(context.Background(), chat.ID, ChatGenerate{RequestID: "budget", Version: 1, Kind: "question", Content: question}, nil)
			if expected == "" {
				if err != nil || calls != 1 {
					t.Fatalf("valid material or conversation rejected: %v", err)
				}
			} else if errorCode(err) != expected || calls != 0 {
				t.Fatalf("budget %s: code=%s calls=%d", mode, errorCode(err), calls)
			}
			persisted, _ := service.Store.chat(chat.ID)
			if expected != "" && slices.ContainsFunc(persisted.Turns, func(turn ChatTurn) bool { return turn.RequestID == "budget" }) {
				t.Fatal("budget failure inserted a new turn")
			}
			if !reflect.DeepEqual(persisted.Turns[:len(before.Turns)], before.Turns) {
				t.Fatal("generation changed existing history")
			}
		})
	}
}

func TestChatSourceChangesAndFailuresNeverComplete(t *testing.T) {
	for _, mode := range []string{"changed-before", "deleted-before", "foreign-before", "change-first-delta",
		"change-after-delta", "provider-failure", "protocol", "consumer", "empty", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			memos := map[string]map[string]any{"memos/one": rawMemo("one", "原文")}
			service := chatFixture(t, memos, nil)
			chat := createTestChat(t, service, "memos/one")
			calls, completes := 0, 0
			service.Provider = aiLLMFunc(func(_ context.Context, _ []LLMMessage, emit func(string) error) (LLMResult, error) {
				calls++
				if mode == "change-first-delta" {
					memos["memos/one"]["content"] = " changed"
				}
				if mode == "empty" {
					return LLMResult{FinishReason: "stop"}, nil
				}
				if err := emit("部分回复\n"); err != nil {
					return LLMResult{}, err
				}
				if mode == "change-after-delta" {
					memos["memos/one"]["content"] = "changed"
				}
				if mode == "provider-failure" {
					return LLMResult{}, failure("LLM_UNAVAILABLE", "PRIVATE_PROVIDER_BODY", 503, true)
				}
				if mode == "protocol" {
					return LLMResult{Content: "different", FinishReason: "stop"}, nil
				}
				return LLMResult{Content: "部分回复\n", FinishReason: "stop"}, nil
			})
			expected := map[string]string{"changed-before": "SOURCE_CHANGED", "deleted-before": "SOURCE_NOT_FOUND",
				"foreign-before": "SOURCE_NOT_FOUND", "change-first-delta": "SOURCE_CHANGED",
				"change-after-delta": "SOURCE_CHANGED", "provider-failure": "LLM_UNAVAILABLE",
				"protocol": "LLM_PROTOCOL", "consumer": "CHAT_INTERRUPTED", "empty": "LLM_PROTOCOL", "disabled": "LLM_DISABLED"}[mode]
			switch mode {
			case "changed-before":
				memos["memos/one"]["content"] = "changed"
			case "deleted-before":
				delete(memos, "memos/one")
			case "foreign-before":
				memos["memos/one"]["creator"] = "users/other"
			case "disabled":
				service.Store.Settings.LLM.Enabled = false
			}
			input := ChatGenerate{RequestID: "first", Version: 1, Kind: "question", Content: "问题"}
			turn, err := service.Generate(context.Background(), chat.ID, input, func(event ChatEvent) error {
				if event.Type == "complete" {
					completes++
				}
				if event.Type == "delta" && mode == "consumer" {
					return errors.New("consumer closed")
				}
				return nil
			})
			if errorCode(err) != expected || completes != 0 || strings.Contains(err.Error(), "PRIVATE_PROVIDER_BODY") {
				t.Fatalf("failure %s: turn=%+v error=%v complete=%d", mode, turn, err, completes)
			}
			if strings.HasSuffix(mode, "-before") || mode == "disabled" {
				if calls != 0 || turn.ID != "" {
					t.Fatal("invalid source invoked the model")
				}
				loaded, readErr := service.Store.chat(chat.ID)
				if readErr != nil || len(loaded.Materials) != 1 {
					t.Fatal("source failure deleted chat")
				}
				return
			}
			expectedState := "failed"
			if mode == "consumer" {
				expectedState = "interrupted"
			}
			if turn.Assistant.Status != expectedState {
				t.Fatalf("incorrect terminal state: %+v", turn)
			}
			if _, err := service.Generate(context.Background(), chat.ID, input, nil); err == nil || calls != 1 {
				t.Fatal("retry regenerated failed message")
			}
		})
	}
}

func TestChatCancelDisconnectAndConcurrentReplay(t *testing.T) {
	for _, mode := range []string{"cancel", "disconnect", "close"} {
		t.Run(mode, func(t *testing.T) {
			ctx, disconnect := context.WithCancel(context.Background())
			defer disconnect()
			started := make(chan struct{})
			service := chatFixture(t, map[string]map[string]any{"memos/one": rawMemo("one", "原文")}, nil)
			chat := createTestChat(t, service, "memos/one")
			calls := 0
			service.Provider = aiLLMFunc(func(ctx context.Context, _ []LLMMessage, emit func(string) error) (LLMResult, error) {
				calls++
				if err := emit("已持久部分"); err != nil {
					return LLMResult{}, err
				}
				close(started)
				<-ctx.Done()
				return LLMResult{}, ctx.Err()
			})
			input := ChatGenerate{RequestID: "first", Version: 1, Kind: "question", Content: "问题"}
			type outcome struct {
				turn ChatTurn
				err  error
			}
			done := make(chan outcome, 1)
			go func() {
				turn, err := service.Generate(ctx, chat.ID, input, nil)
				done <- outcome{turn, err}
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("generation did not start")
			}
			if replay, err := service.Generate(context.Background(), chat.ID, input, nil); errorCode(err) != "CHAT_BUSY" || replay.Assistant.Content != "已持久部分" {
				t.Fatalf("concurrent replay: %+v %v", replay, err)
			}
			if err := service.Delete(context.Background(), chat.ID, 1); errorCode(err) != "CHAT_BUSY" {
				t.Fatalf("deleted running chat: %v", err)
			}
			if _, err := service.Update(context.Background(), chat.ID, ChatUpdate{1, "changed", []string{"memos/one"}}); errorCode(err) != "CHAT_BUSY" {
				t.Fatalf("updated running chat: %v", err)
			}
			switch mode {
			case "cancel":
				if _, err := service.Cancel(chat.ID, "first"); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				disconnect()
			case "close":
				service.Close()
			}
			select {
			case result := <-done:
				expected := "interrupted"
				if mode == "cancel" {
					expected = "canceled"
				}
				if result.err == nil || result.turn.Assistant.Status != expected || result.turn.Assistant.Content != "已持久部分" || calls != 1 {
					t.Fatalf("cancel result: %+v %v calls=%d", result.turn, result.err, calls)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancel did not reach Provider")
			}
		})
	}
}

func TestChatSaveIsVerbatimIdempotentAndDeletionDrainsIt(t *testing.T) {
	service := chatFixture(t, map[string]map[string]any{"memos/one": rawMemo("one", "原文")}, textChatProvider("  ## 标题\n\n回复 #原生标签\n "))
	chat := createTestChat(t, service, "memos/one")
	turn, err := service.Generate(context.Background(), chat.ID, ChatGenerate{RequestID: "first", Version: 1, Kind: "question", Content: "问题"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var created map[string]any
	posts := 0
	source := fakeClient(service.Store.Settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		if r.Method == "POST" {
			posts++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return 0, nil, err
			}
			if payload["content"] != turn.Assistant.Content {
				t.Fatal("save rewrote reply Markdown or tags")
			}
			once.Do(func() { close(started) })
			<-release
			created = rawMemo(r.URL.Query().Get("memoId"), payload["content"].(string))
			return 200, created, nil
		}
		if created == nil {
			return 404, nil, nil
		}
		return 200, created, nil
	})
	service.Records = NewRecordService(service.Store, source)
	saved := make(chan error, 1)
	go func() {
		_, status, err := service.Save(context.Background(), chat.ID, "first", turn.Assistant.Version)
		if status != 201 && err == nil {
			err = errors.New("first save did not create")
		}
		saved <- err
	}()
	<-started
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := service.Delete(waitCtx, chat.ID, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delete did not wait for in-flight save: %v", err)
	}
	close(release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	note, status, err := service.Save(context.Background(), chat.ID, "first", turn.Assistant.Version)
	if err != nil || status != 200 || posts != 1 || note.Content != turn.Assistant.Content {
		t.Fatalf("save replay: %+v status=%d posts=%d error=%v", note, status, posts, err)
	}
	if err := service.Delete(context.Background(), chat.ID, 1); err != nil {
		t.Fatal(err)
	}
	if created == nil || created["content"] != turn.Assistant.Content {
		t.Fatal("deleting chat removed saved Memos note")
	}
	var writes int
	if err := service.Store.state.QueryRow("SELECT COUNT(*) FROM note_writes").Scan(&writes); err != nil || writes != 1 {
		t.Fatalf("deleting chat removed write recovery: %d %v", writes, err)
	}
}

func TestChatCreationReplayAndExplicitMaterialRefresh(t *testing.T) {
	memos := map[string]map[string]any{"memos/one": rawMemo("one", "原文")}
	service := chatFixture(t, memos, textChatProvider("回复"))
	input := ChatCreate{"same", "title", []string{"memos/one"}}
	chat, _, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	memos["memos/one"]["content"] = "更新正文 😀"
	replay, created, err := service.Create(context.Background(), input)
	if err != nil || created || !reflect.DeepEqual(replay.Materials, chat.Materials) {
		t.Fatalf("creation replay changed material version: %+v %v", replay, err)
	}
	material, err := service.Materials(context.Background(), input.Names, nil)
	if err != nil || material.Characters != utf8.RuneCountInString("更新正文 😀") {
		t.Fatalf("Unicode material count: %+v %v", material, err)
	}
	updated, err := service.Update(context.Background(), chat.ID, ChatUpdate{1, chat.Title, input.Names})
	if err != nil || updated.Version != 2 || reflect.DeepEqual(updated.Materials, chat.Materials) {
		t.Fatalf("explicit refresh failed: %+v %v", updated, err)
	}
	if _, err := service.Generate(context.Background(), chat.ID, ChatGenerate{RequestID: "new", Version: 2, Kind: "question", Content: "继续"}, nil); err != nil {
		t.Fatalf("refreshed material could not generate: %v", err)
	}
}

func TestAppChatSharesProvider(t *testing.T) {
	settings, store := settingsFixture(t)
	source := fakeClient(settings, func(*http.Request) (int, any, error) {
		t.Fatal("retired generation accessed source")
		return 500, nil, nil
	})
	app := NewApp(settings, store, source)
	defer app.Close()
	if app.Chat.Notes != app.Notes || app.Chat.Records != app.Records || app.Chat.Store != app.Store {
		t.Fatal("chat does not share Notes and Records")
	}
	update := settingsChange(1, `{"llm":{"enabled":true,"base_url":"https://llm.invalid/v1","model":"new-model"}}`)
	key := "private-test-key"
	update.Secrets = map[string]SecretUpdate{"llm_api_key": {Action: "replace", Value: &key}}
	view, err := app.UpdateSettings(context.Background(), update)
	if err != nil || app.Chat.Provider == nil || app.Chat.Provider != app.LLM || view.Version != 2 {
		t.Fatalf("runtime chat provider not shared: %v", err)
	}
}

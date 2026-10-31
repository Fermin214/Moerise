package moerise

import (
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func testChat(t *testing.T, store *Store, key string) ChatSession {
	t.Helper()
	materials := []ChatMaterial{{"memos/normal", strings.Repeat("a", 64)}, {"memos/archived", strings.Repeat("b", 64)}}
	chat, created, err := store.createChat(key, "笔记讨论", materials)
	if err != nil || !created {
		t.Fatalf("create chat: created=%v error=%v", created, err)
	}
	return chat
}

func testChatTurn(t *testing.T, store *Store, chat ChatSession, key string) ChatTurn {
	t.Helper()
	turn, created, err := store.reserveChatTurn(chat.ID,
		ChatGenerate{RequestID: key, Version: chat.Version, Kind: "question", Content: "  哪些想法可能有关？\n"},
		7, "区分笔记原意与新想法", "test-model")
	if err != nil || !created {
		t.Fatalf("reserve chat turn: created=%v error=%v", created, err)
	}
	return turn
}

func TestChatStoreLifecycleAndFrozenTurn(t *testing.T) {
	store := testStore(t, testSettings(t))
	chat := testChat(t, store, "create")
	if chat.Version != 1 || chat.MaterialCount != 2 || len(chat.Turns) != 0 || chat.CreatedAt == "" {
		t.Fatalf("invalid chat: %+v", chat)
	}
	replay, created, err := store.createChat("create", chat.Title, chat.Materials)
	if err != nil || created || replay.ID != chat.ID {
		t.Fatalf("creation replay: %+v %v %v", replay, created, err)
	}
	changedRefs := append([]ChatMaterial{}, chat.Materials...)
	changedRefs[0].SourceHash = strings.Repeat("c", 64)
	replay, created, err = store.createChat("create", chat.Title, changedRefs)
	if err != nil || created || !reflect.DeepEqual(replay.Materials, chat.Materials) {
		t.Fatal("creation retry replaced frozen materials")
	}
	if _, _, err := store.createChat("create", "不同请求", chat.Materials); errorCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("creation conflict: %v", err)
	}
	turn := testChatTurn(t, store, chat, "first")
	if turn.Assistant.Status != "running" || turn.User.Content != "  哪些想法可能有关？\n" ||
		turn.User.Role != "user" || turn.Assistant.Role != "assistant" || turn.User.ID == turn.Assistant.ID ||
		!reflect.DeepEqual(turn.Materials, chat.Materials) || turn.SettingsVersion != 7 {
		t.Fatalf("invalid frozen turn: %+v", turn)
	}
	if _, err := store.updateChat(chat.ID, chat.Version, "修改", changedRefs); errorCode(err) != "CHAT_BUSY" {
		t.Fatalf("changed materials while generating: %v", err)
	}
	if err := store.deleteChat(chat.ID, chat.Version); errorCode(err) != "CHAT_BUSY" {
		t.Fatalf("deleted running chat: %v", err)
	}
	content := "  ## 新的联想\n\n原意与假设并不相同。 #原生标签\n "
	for _, part := range []string{"  ## 新的联想\n", "\n原意与假设并不相同。 #原生标签\n "} {
		if err := store.appendChatText(chat.ID, turn.ID, part); err != nil {
			t.Fatal(err)
		}
	}
	usage := LLMUsage{InputTokens: new(int64(12)), OutputTokens: new(int64(7))}
	if err := store.finishChatTurn(chat.ID, turn.ID, "completed", "", usage); err != nil {
		t.Fatal(err)
	}
	final, err := store.chatTurn(chat.ID, "first")
	if err != nil || final.Assistant.Content != content || final.Assistant.Status != "completed" ||
		final.Assistant.Version != 4 || *final.Usage.InputTokens != 12 || final.Usage.TotalTokens != nil {
		t.Fatalf("final turn: %+v %v", final, err)
	}
	if err := store.appendChatText(chat.ID, turn.ID, "late"); errorCode(err) != "CHAT_STREAM_CLOSED" {
		t.Fatalf("late delta: %v", err)
	}
	if err := store.finishChatTurn(chat.ID, turn.ID, "failed", "LATE", LLMUsage{}); errorCode(err) != "CHAT_STREAM_CLOSED" {
		t.Fatalf("late finish: %v", err)
	}
	updated, err := store.updateChat(chat.ID, chat.Version, "新的材料", changedRefs)
	if err != nil || updated.Version != 2 || updated.Turns[0].Prompt != turn.Prompt ||
		!reflect.DeepEqual(updated.Turns[0].Materials, chat.Materials) || !reflect.DeepEqual(updated.Materials, changedRefs) {
		t.Fatalf("update changed historical snapshot: %+v %v", updated, err)
	}
	if _, err := store.updateChat(chat.ID, 1, "stale", chat.Materials); errorCode(err) != "CHAT_CONFLICT" {
		t.Fatalf("stale update: %v", err)
	}
	if err := store.deleteChat(chat.ID, 1); errorCode(err) != "CHAT_CONFLICT" {
		t.Fatalf("stale deletion: %v", err)
	}
	insight, created, err := store.reserveChatTurn(chat.ID, ChatGenerate{"insight", 2, "insight", ""}, 8, "新的默认 Prompt\n", "other-model")
	if err != nil || !created || insight.User.Content != "新的默认 Prompt\n" || insight.Model != "other-model" {
		t.Fatalf("insight prompt not persisted: %+v %v", insight, err)
	}
}

func TestChatStoreConcurrentRequestsAreAtomic(t *testing.T) {
	store := testStore(t, testSettings(t))
	chat := testChat(t, store, "concurrent")
	input := ChatGenerate{"same", 1, "question", "问题"}
	type outcome struct {
		turn    ChatTurn
		created bool
		err     error
	}
	var group sync.WaitGroup
	results := make(chan outcome, 12)
	for range 12 {
		group.Go(func() {
			turn, created, err := store.reserveChatTurn(chat.ID, input, 3, "Prompt", "model")
			results <- outcome{turn, created, err}
		})
	}
	group.Wait()
	close(results)
	first, id := 0, ""
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			first++
		}
		if id != "" && id != result.turn.ID {
			t.Fatal("same request reserved multiple turns")
		}
		id = result.turn.ID
	}
	if first != 1 {
		t.Fatalf("new turns: %d", first)
	}
	input.Content = "不同问题"
	if _, _, err := store.reserveChatTurn(chat.ID, input, 4, "different", "model"); errorCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("conflict: %v", err)
	}
	input.RequestID = "another"
	if _, _, err := store.reserveChatTurn(chat.ID, input, 4, "different", "model"); errorCode(err) != "CHAT_BUSY" {
		t.Fatalf("parallel different turn: %v", err)
	}
	if err := store.finishChatTurn(chat.ID, id, "failed", "LLM_PROTOCOL", LLMUsage{}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.updateChat(chat.ID, 1, chat.Title, chat.Materials)
	if err != nil {
		t.Fatal(err)
	}
	replayed, created, err := store.reserveChatTurn(chat.ID, ChatGenerate{"same", 1, "question", "问题"}, 4, "new", "new")
	if err != nil || created || replayed.ID != id || replayed.Assistant.Status != "failed" || replayed.SettingsVersion != 3 {
		t.Fatalf("retry restarted failed turn or lost original settings: %+v %v", replayed, err)
	}
	input.Version = updated.Version
	if _, created, err := store.reserveChatTurn(chat.ID, input, 4, "new", "new"); err != nil || !created {
		t.Fatalf("explicit new request failed: %v", err)
	}
}

func TestChatStoreRestartRebuildBackupAndDelete(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	chat := testChat(t, store, "durable")
	completed := testChatTurn(t, store, chat, "complete")
	if err := store.appendChatText(chat.ID, completed.ID, "## 已完成\n\n保留原文"); err != nil {
		t.Fatal(err)
	}
	if err := store.finishChatTurn(chat.ID, completed.ID, "completed", "", LLMUsage{}); err != nil {
		t.Fatal(err)
	}
	final, _ := store.chatTurn(chat.ID, "complete")
	save, content, err := store.reserveChatSave(chat.ID, "complete", final.Assistant.Version)
	if err != nil || content != final.Assistant.Content || save.Name != "" {
		t.Fatalf("save reservation: %+v %q %v", save, content, err)
	}
	if err := store.completeChatSave(chat.ID, "complete", save.Key, "memos/saved"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"failed", "canceled", "interrupted"} {
		turn := testChatTurn(t, store, chat, status)
		if err := store.appendChatText(chat.ID, turn.ID, "部分回复 "+status); err != nil {
			t.Fatal(err)
		}
		if err := store.finishChatTurn(chat.ID, turn.ID, status, "TEST_"+status, LLMUsage{}); err != nil {
			t.Fatal(err)
		}
	}
	running := testChatTurn(t, store, chat, "running")
	if err := store.appendChatText(chat.ID, running.ID, "重启前的增量\n"); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if _, err := Backup(settings, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildIndex(settings); err != nil {
		t.Fatal(err)
	}
	store = testStore(t, settings)
	loaded, err := store.chat(chat.ID)
	if err != nil || len(loaded.Turns) != 5 || loaded.Turns[0].Assistant.SavedName != "memos/saved" ||
		loaded.Turns[4].Assistant.Status != "interrupted" || loaded.Turns[4].Assistant.ErrorCode != "CHAT_INTERRUPTED" ||
		loaded.Turns[4].Assistant.Content != "重启前的增量\n" {
		t.Fatalf("restart/rebuild lost chat state: %+v %v", loaded, err)
	}
	for i, status := range []string{"completed", "failed", "canceled", "interrupted"} {
		if loaded.Turns[i].Assistant.Status != status {
			t.Fatalf("restart changed terminal state: %+v", loaded.Turns[i])
		}
	}
	retry, retryContent, err := store.reserveChatSave(chat.ID, "complete", final.Assistant.Version)
	if err != nil || retry.Key != save.Key || retry.Name != "memos/saved" || retryContent != content {
		t.Fatalf("save association not durable: %+v %v", retry, err)
	}
	if err := store.deleteChat(chat.ID, loaded.Version); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"chat_sessions", "chat_materials", "chat_turns", "chat_saves"} {
		var count int
		if err := store.state.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("delete did not cascade %s: %d %v", table, count, err)
		}
	}
	if _, err := store.chat(chat.ID); errorCode(err) != "CHAT_NOT_FOUND" {
		t.Fatalf("deleted chat readable: %v", err)
	}
	if err := store.appendChatText(chat.ID, running.ID, "late"); errorCode(err) != "CHAT_STREAM_CLOSED" {
		t.Fatalf("deleted chat resurrected: %v", err)
	}
	if err := store.completeChatSave(chat.ID, "complete", save.Key, "memos/saved"); errorCode(err) != "CHAT_NOT_FOUND" {
		t.Fatalf("deleted save association resurrected: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(settings, snapshot, filepath.Join(t.TempDir(), "safety")); err != nil {
		t.Fatal(err)
	}
	store = testStore(t, settings)
	restored, err := store.chat(chat.ID)
	if err != nil || restored.Turns[0].Assistant.SavedName != "memos/saved" || restored.Turns[4].Assistant.Status != "interrupted" {
		t.Fatalf("restore lost chat: %+v %v", restored, err)
	}
}

func TestChatStoreScopeValidationAndSaveBoundaries(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	chat := testChat(t, store, "scope")
	turn := testChatTurn(t, store, chat, "scope")
	for _, change := range []string{"instance", "user"} {
		otherSettings := settings
		if change == "instance" {
			otherSettings.Memos.InstanceID = "other-instance"
		} else {
			otherSettings.Memos.User = "users/other"
		}
		other := &Store{Settings: otherSettings, state: store.state}
		if _, err := other.chat(chat.ID); errorCode(err) != "CHAT_NOT_FOUND" {
			t.Fatalf("%s leaked chat: %v", change, err)
		}
		if list, err := other.listChats("", "", 20); err != nil || len(list) != 0 {
			t.Fatalf("%s leaked list: %+v %v", change, list, err)
		}
		if _, err := other.updateChat(chat.ID, 1, chat.Title, chat.Materials); errorCode(err) != "CHAT_NOT_FOUND" {
			t.Fatalf("%s updated chat: %v", change, err)
		}
		if err := other.deleteChat(chat.ID, 1); errorCode(err) != "CHAT_NOT_FOUND" {
			t.Fatalf("%s deleted chat: %v", change, err)
		}
		if _, _, err := other.reserveChatTurn(chat.ID, ChatGenerate{"other", 1, "question", "other"}, 1, "prompt", "model"); errorCode(err) != "CHAT_NOT_FOUND" {
			t.Fatalf("%s generated chat: %v", change, err)
		}
		if err := other.appendChatText(chat.ID, turn.ID, "foreign"); errorCode(err) != "CHAT_STREAM_CLOSED" {
			t.Fatalf("%s appended chat: %v", change, err)
		}
		if err := other.finishChatTurn(chat.ID, turn.ID, "failed", "FOREIGN", LLMUsage{}); errorCode(err) != "CHAT_STREAM_CLOSED" {
			t.Fatalf("%s finalized chat: %v", change, err)
		}
		if _, _, err := other.reserveChatSave(chat.ID, "scope", 1); errorCode(err) != "CHAT_MESSAGE_NOT_FOUND" {
			t.Fatalf("%s reserved save: %v", change, err)
		}
		if err := other.recoverChats(); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := store.chatTurn(chat.ID, "scope")
	if current.Assistant.Status != "running" {
		t.Fatal("foreign restart recovery changed chat")
	}
	if _, _, err := store.reserveChatSave(chat.ID, "scope", 1); errorCode(err) != "CHAT_MESSAGE_INCOMPLETE" {
		t.Fatalf("saved running reply: %v", err)
	}
	if err := store.appendChatText(chat.ID, turn.ID, "completed reply"); err != nil {
		t.Fatal(err)
	}
	if err := store.finishChatTurn(chat.ID, turn.ID, "completed", "", LLMUsage{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.reserveChatSave(chat.ID, "scope", 1); errorCode(err) != "CHAT_CONFLICT" {
		t.Fatalf("saved stale message version: %v", err)
	}
	for _, materials := range [][]ChatMaterial{nil, {{"memos/one", "bad"}}, {chat.Materials[0], chat.Materials[0]}} {
		if _, _, err := store.createChat("invalid", "title", materials); errorCode(err) != "INVALID_ARGUMENT" {
			t.Fatalf("accepted invalid materials: %+v %v", materials, err)
		}
	}
	if _, _, err := store.createChat("invalid", strings.Repeat("字", 201), chat.Materials); errorCode(err) != "INVALID_ARGUMENT" {
		t.Fatalf("accepted oversized title: %v", err)
	}
	if _, _, err := store.reserveChatTurn(chat.ID, ChatGenerate{"bad", 1, "insight", "not empty"}, 1, "prompt", "model"); errorCode(err) != "INVALID_ARGUMENT" {
		t.Fatalf("accepted contradictory insight input: %v", err)
	}
}

func TestChatStoreMigrationRollbackAndPagination(t *testing.T) {
	store := testStore(t, testSettings(t))
	if _, err := store.state.Exec(`CREATE TRIGGER fail_second_material BEFORE INSERT ON chat_materials
		WHEN NEW.position = 1 BEGIN SELECT RAISE(ABORT, 'failed material'); END`); err != nil {
		t.Fatal(err)
	}
	materials := []ChatMaterial{{"memos/one", strings.Repeat("a", 64)}, {"memos/two", strings.Repeat("b", 64)}}
	if _, _, err := store.createChat("rollback", "title", materials); err == nil {
		t.Fatal("expected material insert failure")
	}
	for _, table := range []string{"chat_sessions", "chat_materials"} {
		var count int
		if err := store.state.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial chat transaction: %s %d %v", table, count, err)
		}
	}
	if _, err := store.state.Exec("DROP TRIGGER fail_second_material"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"one", "two", "three"} {
		testChat(t, store, key)
	}
	// 同时间仍按资源 ID 分页，后页不重复。
	if _, err := store.state.Exec("UPDATE chat_sessions SET updated_at = '2026-10-31T00:00:00.000000Z'"); err != nil {
		t.Fatal(err)
	}
	first, err := store.listChats("", "", 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := store.listChats(first[1].UpdatedAt, first[1].ID, 2)
	if err != nil || len(second) != 1 || first[0].ID <= first[1].ID || first[1].ID <= second[0].ID {
		t.Fatalf("unstable pagination: %+v %+v %v", first, second, err)
	}
	if err := migrate(store.state, "state"); err != nil {
		t.Fatalf("reapplying migrations: %v", err)
	}
}

func TestChatStoreOutputLimitDoesNotTruncate(t *testing.T) {
	store := testStore(t, testSettings(t))
	chat := testChat(t, store, "output-limit")
	turn := testChatTurn(t, store, chat, "output-limit")
	text := strings.Repeat("字", llmOutputBytes/3) + "!"
	if len(text) != llmOutputBytes {
		t.Fatal("invalid boundary fixture")
	}
	if err := store.appendChatText(chat.ID, turn.ID, text); err != nil {
		t.Fatal(err)
	}
	if err := store.appendChatText(chat.ID, turn.ID, "多"); errorCode(err) != "LLM_OUTPUT_LIMIT" {
		t.Fatalf("output overflow did not report limit: %v", err)
	}
	if err := store.appendChatText(chat.ID, turn.ID, strings.Repeat("x", llmOutputBytes+1)); errorCode(err) != "LLM_OUTPUT_LIMIT" {
		t.Fatalf("oversized delta accepted: %v", err)
	}
	if err := store.appendChatText(chat.ID, turn.ID, "\xff"); errorCode(err) != "LLM_PROTOCOL" {
		t.Fatalf("invalid UTF-8 accepted: %v", err)
	}
	loaded, err := store.chatTurn(chat.ID, "output-limit")
	if err != nil || loaded.Assistant.Content != text || loaded.Assistant.Version != 2 {
		t.Fatalf("overflow changed persisted output: %v", err)
	}
}

func TestChatStoreUpgradePreservesExistingProductState(t *testing.T) {
	legacy, batch := legacyNotificationFixture(t, "018")
	before, err := appliedMigrations(legacy.Store.state)
	if err != nil {
		t.Fatal(err)
	}
	upgradeNotificationFixture(t, legacy)
	store := legacy.Store
	var count int
	if err := store.state.QueryRow("SELECT COUNT(*) FROM review_items WHERE batch_id = ?", batch.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("chat migration lost old review: count=%d error=%v", count, err)
	}
	after, err := appliedMigrations(store.state)
	if err != nil || len(after) != len(before)+1 || after["019"] == "" {
		t.Fatalf("chat migration not recorded: %+v %v", after, err)
	}
	for version, hash := range before {
		if after[version] != hash {
			t.Fatalf("changed applied migration %s", version)
		}
	}
	chat := testChat(t, store, "upgraded")
	if len(chat.Materials) != 2 {
		t.Fatal("upgraded chat storage not usable")
	}
}

//go:build integration

package acceptance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	moerise "moerise/internal"
)

const liveReceiptWindow = 2 * time.Minute

func privateReceiptDeadline(lastSlot, started time.Time) time.Time {
	if started.After(lastSlot) {
		return started.Add(liveReceiptWindow)
	}
	return lastSlot.Add(liveReceiptWindow)
}

func TestPrivateReceiptDeadline(t *testing.T) {
	lastSlot := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for _, offsets := range []struct{ start, deadline time.Duration }{
		{-time.Hour, 2 * time.Minute},
		{2 * time.Minute, 4 * time.Minute},
		{4 * time.Minute, 6 * time.Minute},
	} {
		got := privateReceiptDeadline(lastSlot, lastSlot.Add(offsets.start))
		if want := lastSlot.Add(offsets.deadline); !got.Equal(want) {
			t.Fatalf("start offset %v: deadline %v, want %v", offsets.start, got, want)
		}
	}
}

func TestFourLiveBotDestinations(t *testing.T) {
	path := os.Getenv("MOERISE_BOTS_LIVE_CONFIG")
	if path == "" {
		t.Skip("MOERISE_BOTS_LIVE_CONFIG is not set")
	}
	var live struct {
		Channel             moerise.ChannelConfig `json:"channel"`
		Timezone            string                `json:"timezone"`
		First               string                `json:"first"`
		QQPeer              string                `json:"qq_peer"`
		TelegramPrivateChat string                `json:"telegram_private_chat_id"`
		QuietDayQQ          bool                  `json:"quiet_day_qq"`
		CursorSecret        string                `json:"cursor_secret"`
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&live); err != nil {
		t.Fatal("invalid live configuration")
	}
	for _, value := range []*string{live.Channel.QQAppID, live.Channel.QQAppSecret, live.Channel.BotToken,
		live.Channel.AppID, live.Channel.AppSecret, live.Channel.WebhookURL} {
		if value == nil || strings.TrimSpace(*value) == "" {
			t.Fatal("configure the three private bots and the group webhook")
		}
	}
	if live.TelegramPrivateChat != "" {
		id, err := strconv.ParseInt(live.TelegramPrivateChat, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != live.TelegramPrivateChat {
			t.Fatal("telegram_private_chat_id must identify the owner's private chat")
		}
	}
	if live.QQPeer == "." || live.QQPeer == ".." || strings.ContainsAny(live.QQPeer, "/\\?# \t\r\n") {
		t.Fatal("qq_peer must be the owner's private OpenID for this QQ app")
	}
	location, err := time.LoadLocation(live.Timezone)
	if err != nil || live.Timezone == "" {
		t.Fatal("set timezone to an instance timezone")
	}
	first := time.Now().In(location).Truncate(time.Minute).Add(8 * time.Minute)
	telegramDelay := 3 * time.Minute
	if live.QQPeer != "" {
		first = first.Add(-2 * time.Minute)
		telegramDelay = 20 * time.Minute
	}
	lastDelay := telegramDelay + 6*time.Minute
	if first.Day() != first.Add(lastDelay).Day() {
		first = first.Add(lastDelay + time.Minute)
	}
	if live.First != "" {
		first, err = time.Parse(time.RFC3339, live.First)
		first = first.In(location)
	}
	if err != nil || first.Second() != 0 || first.Nanosecond() != 0 || time.Until(first) < 5*time.Minute {
		t.Fatal("first must be a future whole minute with at least five minutes for private messages")
	}
	telegramSlot := first.Add(telegramDelay)
	slots := []time.Time{first, telegramSlot, telegramSlot.Add(3 * time.Minute), telegramSlot.Add(6 * time.Minute)}
	if live.QuietDayQQ {
		next := first.AddDate(0, 0, 1)
		if next.Format("15:04") != first.Format("15:04") {
			t.Fatal("choose a QQ time outside a clock change")
		}
		slots = append(slots, next)
	}
	if first.Format(time.DateOnly) != slots[3].Format(time.DateOnly) {
		t.Fatal("choose four consecutive slots on the same local day, outside a clock change")
	}
	privateKinds := []string{"qq", "telegram", "feishu-app"}
	lastCheck := slots[3].Add(time.Duration(len(privateKinds)) * liveReceiptWindow)
	if lastSlot := slots[len(slots)-1]; lastSlot.After(lastCheck) {
		lastCheck = lastSlot
	}
	ctx, cancel := context.WithDeadline(t.Context(), lastCheck.Add(liveReceiptWindow+time.Minute))
	defer cancel()
	h := newHarness(t)
	h.stop()
	settings := h.settings
	if live.CursorSecret != "" {
		settings.CursorSecret = live.CursorSecret
	}
	settings.StateDir, settings.IndexDir = filepath.Join(h.root, "live-state"), filepath.Join(h.root, "live-index")
	settings.Web = moerise.WebConfig{Enabled: true, StateEncryptionKey: randomStateKey(), PublicOrigin: h.base}
	settings.Channel, settings.Timezone = live.Channel, live.Timezone
	settings.Channel.Kind, settings.Channel.FeishuMode = "qq", "websocket"
	settings.Review = moerise.ReviewConfig{Count: 1}
	settings.Notifications = moerise.NotificationConfig{}
	for _, slot := range slots[:4] {
		settings.Notifications.Times = append(settings.Notifications.Times, slot.Format("15:04"))
	}
	app, err := moerise.OpenApp(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	h.settings = settings
	db := h.database("state")
	defer db.Close()
	seedLiveBotPeers(t, db, settings, live.QQPeer, live.TelegramPrivateChat)
	server := httptest.NewServer(moerise.NewClientAPI(app))
	defer server.Close()
	h.base = server.URL
	client := &clientHarness{h: h}
	client.call("POST", "/api/app/v1/session", map[string]string{"username": "acceptance-owner", "password": h.password}, 200)
	for index := range slots {
		_, _, err := app.Records.Create(ctx, fmt.Sprintf("live-example-%d", index), moerise.CreateNote{
			Content: fmt.Sprintf("定时回顾 %d\n#验收", index+1)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.Worker.Sync(ctx, moerise.Job{Mode: "full"}); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	errs := make(chan error, 3)
	defer func() { cancel(); workers.Wait() }()
	for _, run := range []func(context.Context) error{app.RunPrivateBots, app.RunFeishu, app.Notifications.Run} {
		workers.Go(func() { errs <- run(ctx) })
	}
	await := func(t *testing.T, until time.Time, description string, ready func() bool) {
		t.Helper()
		for !ready() {
			left := time.Until(until)
			if left <= 0 {
				t.Fatalf("timed out waiting for %s", description)
			}
			select {
			case err := <-errs:
				t.Fatalf("worker stopped during %s: %v", description, err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(min(left, 2*time.Second)):
			}
		}
	}
	await(t, first, "three connected bots", func() bool {
		view := client.call("GET", "/api/app/v1/settings", nil, 200)["channel_status"].(map[string]any)
		for _, kind := range privateKinds {
			if view[kind].(map[string]any)["state"] != "connected" {
				return false
			}
		}
		if live.TelegramPrivateChat != "" {
			return true
		}
		link, ok := view["telegram"].(map[string]any)["link"].(string)
		if !ok || link == "" {
			t.Fatal("missing Telegram start link")
		}
		startFile, err := os.CreateTemp(filepath.Dir(path), "telegram-start-*.txt")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(startFile.Name()) })
		_, writeErr := fmt.Fprintln(startFile, link)
		closeErr := startFile.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot save Telegram start link")
		}
		t.Logf("打开 %s 中的 Telegram 链接，点「开始」。", startFile.Name())
		return true
	})
	if live.QQPeer != "" {
		t.Logf("QQ 将在 %s 先发一条定时回顾，收到后开始私聊验收。", first.Format(time.RFC3339))
	}
	t.Log("请在三个私聊各发一句带 #验收 的笔记，再发 /回顾 或 /review。")
	checkPrivate := func(t *testing.T, kind string) {
		await(t, privateReceiptDeadline(slots[3], time.Now()), kind+" private note and review", func() bool {
			var noteName, batchID string
			var stamp time.Time
			var err error
			if kind == "feishu-app" {
				var created string
				err = db.QueryRow(`SELECT n.resource_name,r.resource_name,r.created_at FROM channel_events n,channel_events r
					WHERE n.result_kind='saved' AND r.result_kind='review' AND n.sender_open_id=r.sender_open_id
					AND n.reply_status='sent' AND r.reply_status='sent' AND n.receipt IS NOT NULL AND r.receipt IS NOT NULL
					ORDER BY n.created_at DESC,r.created_at DESC LIMIT 1`).Scan(&noteName, &batchID, &created)
				if err == nil {
					stamp, err = time.Parse(time.RFC3339Nano, created)
				}
			} else {
				var millis int64
				err = db.QueryRow(`SELECT w.memo_name,r.batch_id,r.message_at FROM bot_inbox n JOIN note_writes w ON w.idempotency_key=n.write_key
					JOIN bot_inbox r USING(instance_id,user_name,kind,bot_id,peer)
					WHERE n.kind=? AND w.status='succeeded' AND n.operation='text' AND r.operation='review'
					AND n.phase='sent' AND r.phase='sent' AND n.receipt IS NOT NULL AND r.receipt IS NOT NULL
					ORDER BY n.received_at DESC,r.received_at DESC LIMIT 1`, kind).Scan(&noteName, &batchID, &millis)
				stamp = time.UnixMilli(millis)
			}
			if err == sql.ErrNoRows {
				return false
			}
			if err != nil {
				t.Fatal(err)
			}
			note, err := app.Source.Get(ctx, noteName)
			if err != nil || note == nil {
				t.Fatalf("%s source note missing: %v", kind, err)
			}
			if note.Visibility != "PRIVATE" || note.Creator != settings.Memos.User {
				t.Fatalf("%s source note: private=%t expected_author=%t", kind, note.Visibility == "PRIVATE", note.Creator == settings.Memos.User)
			}
			if !strings.Contains(note.Content, "#验收") || !slices.Contains(note.Tags, "验收") {
				t.Fatalf("%s source note: marker=%t indexed_tag=%t", kind, strings.Contains(note.Content, "#验收"), slices.Contains(note.Tags, "验收"))
			}
			plan, err := app.Reviews.Daily(ctx, stamp)
			if err != nil {
				t.Fatalf("%s request-day plan: %v", kind, err)
			}
			expectedDay := stamp.In(location).Format(time.DateOnly)
			if plan.ID != batchID || plan.Date != expectedDay || len(plan.Items) == 0 {
				t.Fatalf("%s request-day plan: expected_batch=%t expected_day=%t items=%d", kind, plan.ID == batchID, plan.Date == expectedDay, len(plan.Items))
			}
			return true
		})
	}
	kinds := []string{"qq", "telegram", "feishu-app", "feishu-webhook", "qq"}
	for index, slot := range slots {
		kind := kinds[index]
		t.Run("scheduled/"+kind+"/"+slot.Format(time.DateOnly), func(t *testing.T) {
			view := client.call("GET", "/api/app/v1/settings", nil, 200)
			client.call("PATCH", "/api/app/v1/settings", map[string]any{"version": view["version"],
				"settings": map[string]any{"channel": map[string]string{"kind": kind}, "notifications": map[string]bool{"enabled": true}}}, 200)
			t.Logf("%s 定时目的地 %s", slot.Format(time.RFC3339), kind)
			await(t, slot.Add(liveReceiptWindow), kind+" scheduled review", func() bool {
				if time.Now().Before(slot) {
					return false
				}
				date, clock := slot.Format(time.DateOnly), slot.Format("15:04")
				var total, confirmed, failed int
				err := db.QueryRow(`SELECT count(*),count(*) FILTER (WHERE n.status='sent' AND n.receipt IS NOT NULL AND n.receipt NOT LIKE 'empty:%'),
				count(*) FILTER (WHERE n.status IN ('unknown','skipped','failed') OR n.channel_kind<>?)
				FROM notifications n JOIN review_plan_slots s ON s.id=n.slot_id AND s.batch_id=n.batch_id
				WHERE n.kind='review' AND n.local_date=? AND s.clock=? AND s.item_count>0`, kind, date, clock).
					Scan(&total, &confirmed, &failed)
				if err != nil || failed > 0 {
					t.Fatalf("%s delivery %s %s: failed=%d err=%v", kind, date, clock, failed, err)
				}
				return total > 0 && total == confirmed
			})
			if index == 4 {
				midnight := time.Date(slot.Year(), slot.Month(), slot.Day(), 0, 0, 0, 0, location)
				var recent int
				if err := db.QueryRow(`SELECT count(*) FROM bot_inbox WHERE kind='qq' AND message_at BETWEEN ? AND ?`,
					min(midnight.UnixMilli(), slot.Add(-time.Hour).UnixMilli()), slot.UnixMilli()).Scan(&recent); err != nil || recent != 0 {
					t.Fatalf("second QQ slot requires a quiet local day and an expired reply window: messages=%d error=%v", recent, err)
				}
			}
			plan, err := app.Reviews.Daily(ctx, slot)
			if err != nil || plan.Date != slot.Format(time.DateOnly) || !slices.ContainsFunc(plan.Items, func(item moerise.ReviewItem) bool {
				return item.Note != nil && strings.Contains(item.Note.Content, "#验收")
			}) {
				t.Fatalf("scheduled review differs from source plan: %v", err)
			}
		})
		if index == 3 {
			for _, privateKind := range privateKinds {
				t.Run("private/"+privateKind, func(t *testing.T) { checkPrivate(t, privateKind) })
			}
		}
	}
}

func seedLiveBotPeers(t *testing.T, db *sql.DB, settings moerise.Settings, qqPeer, telegramPeer string) {
	t.Helper()
	if qqPeer == "" && telegramPeer == "" {
		return
	}
	telegramID, _, _ := strings.Cut(*settings.Channel.BotToken, ":")
	for _, binding := range []struct{ kind, bot, peer string }{
		{"qq", *settings.Channel.QQAppID, qqPeer}, {"telegram", telegramID, telegramPeer},
	} {
		if binding.peer != "" {
			if _, err := db.Exec(`INSERT INTO bot_bindings (instance_id,user_name,kind,bot_id,peer,bound_at) VALUES (?,?,?,?,?,?)`,
				settings.Memos.InstanceID, settings.Memos.User, binding.kind, binding.bot, binding.peer, time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

package moerise

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type botReplyProbe struct {
	peer, message, text string
}

func (probe *botReplyProbe) Send(_ context.Context, peer, message, text string) (string, error) {
	probe.peer, probe.message, probe.text = peer, message, text
	return "receipt", nil
}

func TestBotReviewKeepsRequestDayAndRechecksSource(t *testing.T) {
	reviews, notes := reviewFixture(t, 4)
	store := reviews.Store
	store.Settings.Channel = ChannelConfig{Kind: "none", BotToken: new("147:" + strings.Repeat("s", 30))}
	app := NewApp(store.Settings, store, reviews.Source)
	defer app.Close()
	location, err := time.LoadLocation(store.Settings.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(location)
	requested := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location).Add(-time.Second)
	receiver := botReceiverStore{store}
	for _, msg := range []botMessage{
		{ID: "1", Peer: "35", Kind: "start", Bind: true, At: requested},
		{ID: "2", Peer: "35", Kind: "text", Text: "/回顾", At: requested},
	} {
		if err := receiver.Accept(t.Context(), "telegram", "147", botCursor{}, &msg); err != nil {
			t.Fatal(err)
		}
	}
	probe := &botReplyProbe{}
	step := func() {
		t.Helper()
		if progress, err := app.processBotMessage(t.Context(), "telegram", "147", probe); !progress || err != nil {
			t.Fatalf("private command: progress=%v err=%v", progress, err)
		}
	}
	step()
	plan, err := reviews.Daily(t.Context(), requested)
	if err != nil || len(plan.Items) < 2 {
		t.Fatalf("request day plan: %v", err)
	}
	var batch string
	if err := store.state.QueryRow("SELECT batch_id FROM bot_inbox WHERE message_id='2'").Scan(&batch); err != nil || batch != plan.ID {
		t.Fatalf("command did not reuse the request day's plan: %v", err)
	}
	deleted := plan.Items[0]
	delete(notes, deleted.Name)
	step()
	if probe.peer != "35" || !strings.Contains(probe.text, requested.Format(time.DateOnly)) ||
		strings.Contains(probe.text, deleted.Note.Content) || !strings.Contains(probe.text, plan.Items[1].Note.Content) {
		t.Fatalf("reply after midnight: %q", probe.text)
	}
}

func TestQQRestoreAndReplacementRetirePendingWork(t *testing.T) {
	reviews, _ := reviewFixture(t, 3)
	store := reviews.Store
	store.Settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))
	store.Settings.Channel = ChannelConfig{Kind: "qq", QQAppID: new("9101"), QQAppSecret: new("qq-test-secret")}
	store.Settings.Notifications.Enabled = true
	if err := store.initializeProductSettings(); err != nil {
		t.Fatal(err)
	}
	settings := store.Settings
	app := NewApp(settings, store, reviews.Source)
	defer app.Close()
	msg := botMessage{ID: "first", Peer: "owner", Bind: true, Kind: "text", Text: "/review", At: time.Now().Add(-time.Second)}
	if err := (botReceiverStore{store}).Accept(t.Context(), "qq", "9101", botCursor{"gateway", 20}, &msg); err != nil {
		t.Fatal(err)
	}
	tomorrow := time.Now().Add(24 * time.Hour)
	location, _ := time.LoadLocation(settings.Timezone)
	day := tomorrow.In(location)
	at := time.Date(day.Year(), day.Month(), day.Day(), 23, 0, 0, 0, location)
	if count, err := app.Notifications.Schedule(t.Context(), at); err != nil || count != 1 {
		t.Fatalf("bound QQ slot: count=%d err=%v", count, err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if _, err := Backup(settings, snapshot); err != nil {
		t.Fatal(err)
	}
	change := settingsChange(store.productVersion, `{"channel":{"qq_app_id":"9102"}}`)
	change.Secrets = map[string]SecretUpdate{"qq_app_secret": {Action: "replace", Value: new("replacement-secret")}}
	if _, err := app.UpdateSettings(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	persisted, err := loadPersistedProduct(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, configuration := range []Settings{store.Settings, persisted} {
		if len(configuration.notificationChannels) != 0 || configured(configuration.Channel.QQAppSecret) != "replacement-secret" {
			t.Fatal("retired QQ credential remained in product settings")
		}
	}
	requireEmpty := func(db *sql.DB, query string) {
		t.Helper()
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("obsolete work: %s: %d %v", query, count, err)
		}
	}
	requireEmpty(store.state, `SELECT COUNT(*) FROM bot_bindings WHERE kind='qq'`)
	requireEmpty(store.state, `SELECT COUNT(*) FROM bot_cursors WHERE kind='qq'`)
	requireEmpty(store.state, `SELECT COUNT(*) FROM bot_inbox WHERE phase!='retired' OR body IS NOT NULL`)
	requireEmpty(store.state, `SELECT COUNT(*) FROM notifications WHERE status!='skipped'`)
	msg.ID, msg.Peer = "new", "new-owner"
	if err := (botReceiverStore{store}).Accept(t.Context(), "qq", "9102", botCursor{}, &msg); err != nil {
		t.Fatal(err)
	}
	if peer, _, err := store.botPeer("qq", "9102"); err != nil || peer != msg.Peer {
		t.Fatalf("replacement binding: %s %v", peer, err)
	}
	app.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(settings, snapshot, filepath.Join(t.TempDir(), "safety")); err != nil {
		t.Fatal(err)
	}
	restored := testStore(t, settings)
	if peer, _, err := restored.botPeer("qq", "9101"); err != nil || peer != "owner" || !restored.Settings.Notifications.Enabled {
		t.Fatalf("restored QQ destination: %s %v", peer, err)
	}
	requireEmpty(restored.state, `SELECT COUNT(*) FROM bot_cursors WHERE kind='qq'`)
	requireEmpty(restored.state, `SELECT COUNT(*) FROM bot_inbox WHERE phase!='restored' OR body IS NOT NULL`)
	requireEmpty(restored.state, `SELECT COUNT(*) FROM notifications WHERE status IN ('pending','running')`)
	var restoreMillis int64
	if err := restored.state.QueryRow(`SELECT restored_at FROM bot_restore WHERE instance_id=?`, settings.Memos.InstanceID).Scan(&restoreMillis); err != nil ||
		time.Since(time.UnixMilli(restoreMillis)) > time.Minute {
		t.Fatalf("unexpected restore timestamp: %d %v", restoreMillis, err)
	}
	for _, offset := range []int64{-1, 0, 1} {
		msg.ID, msg.Peer, msg.At = fmt.Sprint(offset), "owner", time.UnixMilli(restoreMillis+offset)
		if err := (botReceiverStore{restored}).Accept(t.Context(), "qq", "9101", botCursor{}, &msg); err != nil {
			t.Fatal(err)
		}
		var accepted bool
		if err := restored.state.QueryRow(`SELECT EXISTS(SELECT 1 FROM bot_inbox WHERE message_id=?)`, msg.ID).Scan(&accepted); err != nil || accepted != (offset > 0) {
			t.Fatalf("restore cutoff +%dms: accepted=%v %v", offset, accepted, err)
		}
	}
	queue := NewNotificationService(restored, NewReviewService(restored, reviews.Source), nil)
	for _, test := range []struct {
		at   time.Time
		want bool
	}{{time.Now().Add(-time.Minute), false}, {at, true}} {
		if eligible, err := queue.slotAfterBinding(test.at); err != nil || eligible != test.want {
			t.Fatalf("restored slot at %v: %v %v", test.at, eligible, err)
		}
	}
}

func TestTelegramLegacyDestinationDoesNotBindAuthor(t *testing.T) {
	settings := testSettings(t)
	settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
	for _, destination := range []string{"641", "-10091"} {
		settings.StateDir, settings.IndexDir = t.TempDir(), t.TempDir()
		settings.Channel = ChannelConfig{Kind: "telegram", BotToken: new("410:" + strings.Repeat("t", 30)), Destination: &destination}
		if err := settings.Validate(); err != nil {
			t.Fatal(err)
		}
		store := testStore(t, settings)
		if got := configured(store.deliveryChannel().Destination); got != destination || !store.settingsView().ChannelCapabilities.Images {
			t.Fatalf("legacy notification destination: got %q, want %q", got, destination)
		}
		if peer, _, err := store.botPeer("telegram", "410"); err != nil || peer != "" {
			t.Fatalf("legacy notification recipient became an author: %s %v", peer, err)
		}
		receiver := botReceiverStore{store}
		msg := botMessage{ID: "1", Peer: destination, At: time.Now(), Kind: "text", Text: "/review"}
		if err := receiver.Accept(t.Context(), "telegram", "410", botCursor{}, &msg); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := store.state.QueryRow(`SELECT COUNT(*) FROM bot_inbox`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unbound recipient created inbox work: %d %v", count, err)
		}
		msg.ID, msg.Peer, msg.Kind, msg.Bind = "2", "714", "start", true
		if err := receiver.Accept(t.Context(), "telegram", "410", botCursor{}, &msg); err != nil {
			t.Fatal(err)
		}
		if peer, _, err := store.botPeer("telegram", "410"); err != nil || peer != "714" || configured(store.deliveryChannel().Destination) != destination {
			t.Fatalf("new author changed the notification destination: %s %v", peer, err)
		}
	}
}

func TestPrivatePushCapabilitiesFollowBinding(t *testing.T) {
	settings := feishuSettings(t)
	settings.Channel.UserOpenID = nil
	settings.Channel.BotToken = new(telegramTestToken)
	settings.Channel.QQAppID, settings.Channel.QQAppSecret = new("901"), new("qq-secret")
	store := testStore(t, settings)
	for _, kind := range []string{"qq", "telegram", "feishu-app"} {
		store.Settings.Channel.Kind = kind
		before := store.settingsView().ChannelCapabilities
		if before != (ChannelCapabilities{Record: true}) {
			t.Fatalf("%s without destination: %+v", kind, before)
		}
		err := transaction(store.state, func(tx *sql.Tx) error {
			_, err := store.bindBotPeer(tx, kind, settings.Channel.botID(kind), "123", time.Now())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		after := store.settingsView().ChannelCapabilities
		if after != (ChannelCapabilities{Text: true, Images: kind != "qq", Record: true}) {
			t.Fatalf("%s after binding: %+v", kind, after)
		}
	}
}

func TestInactiveLegacyChannelMigration(t *testing.T) {
	for _, kind := range []string{"none", "telegram", "feishu-webhook"} {
		settings := testSettings(t)
		settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
		settings.Channel = ChannelConfig{Kind: kind, AppID: new("cli_upload"), AppSecret: new(strings.Repeat("s", 32)),
			WebhookURL: new("https://example.test/hook"), BotToken: new(telegramTestToken), Destination: new("123")}
		if kind != "telegram" {
			settings.Channel.BotToken, settings.Channel.Destination = new("draft"), new("@draft")
		}
		body, err := encryptProduct(settings, 1)
		if err != nil {
			t.Fatal(err)
		}
		settings, err = decryptProduct(settings, 1, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := settings.Validate(); err != nil {
			t.Fatalf("legacy %s: %v", kind, err)
		}
		channel := settings.Channel
		if channel.botEnabled("feishu-app") || !channel.WebhookImages || configured(channel.WebhookAppID) != "cli_upload" ||
			configured(channel.WebhookAppSecret) != strings.Repeat("s", 32) || channel.botEnabled("telegram") != (kind == "telegram") {
			t.Fatalf("legacy %s channel capabilities changed", kind)
		}
		settings.Channel.BotToken = new("draft")
		if settings.Validate() == nil {
			t.Fatal("accepted an invalid new Telegram setting after migration")
		}
	}
}

func TestBotUnreadableMessageDoesNotBlockInbox(t *testing.T) {
	settings := testSettings(t)
	settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	settings.Channel = ChannelConfig{Kind: "none", BotToken: new(telegramTestToken)}
	store := testStore(t, settings)
	app := NewApp(settings, store, nil)
	defer app.Close()
	botID := settings.Channel.botID("telegram")
	receiver := botReceiverStore{store}
	for _, id := range []string{"short", "damaged", "next"} {
		msg := botMessage{ID: id, Peer: "123", Bind: true, At: time.Now(), Kind: "text", Text: "original"}
		if id == "next" {
			msg.Kind = "attachment"
		}
		if err := receiver.Accept(t.Context(), "telegram", botID, botCursor{}, &msg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.state.Exec(`UPDATE bot_inbox SET body=zeroblob(CASE WHEN message_id='short' THEN 1 ELSE 40 END) WHERE message_id!='next'`); err != nil {
		t.Fatal(err)
	}
	probe := &botReplyProbe{}
	for range 4 {
		if progress, err := app.processBotMessage(t.Context(), "telegram", botID, probe); !progress || err != nil {
			t.Fatalf("inbox progress: %v %v", progress, err)
		}
	}
	var count int
	if err := store.state.QueryRow(`SELECT COUNT(*) FROM bot_inbox WHERE phase='failed' AND error_code='INVALID_CIPHERTEXT' AND body IS NULL`).Scan(&count); err != nil || count != 2 || probe.message != "next" {
		t.Fatalf("damaged messages=%d last reply=%q err=%v", count, probe.message, err)
	}
}

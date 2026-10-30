package moerise

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func feishuQueueMigrationKey() string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{31}, 32))
}

func feishuQueueMigrationFixture(t *testing.T) (*FeishuService, *feishuAIPlatform, *int) {
	t.Helper()
	service, platform, _, calls := feishuAIFixture(t, insightOutput)
	receiveFeishuAI(t, service, "洞察 memos/seed")
	processLegacyFeishuAI(t, service)
	platform.mode = "unknown"
	deliverFeishuAI(t, service)
	items, err := service.Notifications.List("unknown")
	if err != nil || len(items) != 1 {
		t.Fatalf("missing uncertain reply: %+v %v", items, err)
	}
	if _, err := service.Notifications.Resolve(context.Background(), items[0].ID, "not_sent", "", "operator"); err != nil {
		t.Fatal(err)
	}
	deliverFeishuAI(t, service)
	for _, status := range []string{"pending", "processing", "processed", "succeeded"} {
		seedLegacyFeishuRecord(t, service, "om_"+status, "legacy queue text", status)
	}
	for _, kind := range []string{"search", "walk"} {
		id := "om_" + kind
		seedLegacyFeishuRecord(t, service, id, "legacy "+kind, "processed")
		body := canonicalJSON(SearchRequest{Query: "fictional", Limit: 5})
		if kind == "walk" {
			body = canonicalJSON(WalkRequest{Name: "memos/seed", Mode: "continue"})
		}
		sealed, err := service.seal(kind+":"+id, body)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Store.state.Exec(`UPDATE channel_events
			SET result_kind = ?, resource_name = ?, payload = NULL WHERE message_id = ?`,
			kind, id+"."+base64.RawURLEncoding.EncodeToString(sealed), id); err != nil {
			t.Fatal(err)
		}
	}
	return service, platform, calls
}

func feishuQueueSnapshot(t *testing.T, store *Store, ciphertext bool) []byte {
	t.Helper()
	tables := []string{"channel_events", "feishu_ai_generations", "feishu_ai_parts",
		"notifications", "notification_attempts", "notification_resolutions"}
	snapshot := map[string][][]any{}
	for _, table := range tables {
		rows, err := store.state.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, column := range columns {
				if !ciphertext && (column == "payload" || column == "request" || column == "result" || column == "resource_name") {
					values[i] = nil
				}
			}
			snapshot[table] = append(snapshot[table], values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return canonicalJSON(snapshot)
}

func TestFeishuQueueMigrationRotation(t *testing.T) {
	service, platform, calls := feishuQueueMigrationFixture(t)
	store := service.Store
	metadata := feishuQueueSnapshot(t, store, false)
	store.Settings.Web.StateEncryptionKey = feishuQueueMigrationKey()
	if err := store.migrateFeishuQueue(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(metadata, feishuQueueSnapshot(t, store, false)) {
		t.Fatal("migration changed event, generation, delivery or resolution state")
	}
	stable := feishuQueueSnapshot(t, store, true)
	store.Settings.Channel.AppSecret = stringPointer("replacement-app-secret")
	if err := store.migrateFeishuQueue(); err != nil {
		t.Fatal("repeated migration required the old application secret:", err)
	}
	if !bytes.Equal(stable, feishuQueueSnapshot(t, store, true)) {
		t.Fatal("repeated migration changed ciphertext or task state")
	}
	for _, status := range []string{"pending", "processing", "processed", "succeeded"} {
		id := "om_" + status
		var payload []byte
		if err := store.state.QueryRow("SELECT payload FROM channel_events WHERE message_id = ?", id).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		body, err := service.unseal(id, payload)
		if err != nil || !bytes.Contains(body, []byte("legacy queue text")) || !bytes.HasPrefix(payload, []byte(feishuQueuePrefix)) {
			t.Fatalf("%s event did not survive rotation: %s %v", status, body, err)
		}
	}
	for _, kind := range []string{"search", "walk"} {
		id := "om_" + kind
		var resource string
		if err := store.state.QueryRow("SELECT resource_name FROM channel_events WHERE message_id = ?", id).Scan(&resource); err != nil {
			t.Fatal(err)
		}
		_, encoded, _ := strings.Cut(resource, ".")
		payload, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if body, err := service.unseal(kind+":"+id, payload); err != nil || !bytes.Contains(body, []byte(`"`)) {
			t.Fatalf("%s result did not survive rotation: %s %v", kind, body, err)
		}
	}
	var request []byte
	if err := store.state.QueryRow("SELECT request FROM feishu_ai_generations WHERE message_id = 'om_ai'").Scan(&request); err != nil {
		t.Fatal(err)
	}
	if body, err := service.unseal("ai-request:om_ai", request); err != nil || !bytes.Contains(body, []byte("memos/seed")) {
		t.Fatalf("AI request did not survive rotation: %s %v", body, err)
	}
	generation, err := service.aiGeneration("om_ai")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := service.aiResult("om_ai", generation); err != nil || !result.Completed {
		t.Fatalf("AI result did not survive rotation: %+v %v", result, err)
	}
	unknown, err := service.Notifications.List("unknown")
	if err != nil || len(unknown) != 1 {
		t.Fatalf("uncertain outcome was lost: %+v %v", unknown, err)
	}
	if text, _, err := service.aiPart(unknown[0]); err != nil || !strings.Contains(text, "memos/seed") {
		t.Fatalf("AI reply part did not survive rotation: %s %v", text, err)
	}
	platform.mode = ""
	if _, err := service.Notifications.Resolve(context.Background(), unknown[0].ID, "not_sent", "", "operator"); err != nil {
		t.Fatal(err)
	}
	deliverFeishuAI(t, service)
	if *calls != 1 || platform.calls != 3 {
		t.Fatalf("rotation repeated generation or lost reply: model=%d reply=%d", *calls, platform.calls)
	}
}

func TestFeishuQueueMigrationAtomicFailure(t *testing.T) {
	for _, test := range []struct {
		name, query string
	}{
		{"event", "UPDATE channel_events SET payload = X'01' WHERE message_id = 'om_pending'"},
		{"search", "UPDATE channel_events SET resource_name = 'om_search.AQ' WHERE message_id = 'om_search'"},
		{"walk", "UPDATE channel_events SET resource_name = 'om_walk.AQ' WHERE message_id = 'om_walk'"},
		{"reference", "UPDATE channel_events SET resource_name = 'om_other.AQ' WHERE message_id = 'om_search'"},
		{"request", "UPDATE feishu_ai_generations SET request = X'01'"},
		{"result", "UPDATE feishu_ai_generations SET result = zeroblob(length(result))"},
		{"part", "UPDATE feishu_ai_parts SET payload = X'01'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, _ := feishuQueueMigrationFixture(t)
			store := service.Store
			if _, err := store.state.Exec(test.query); err != nil {
				t.Fatal(err)
			}
			before := feishuQueueSnapshot(t, store, true)
			store.Settings.Web.StateEncryptionKey = feishuQueueMigrationKey()
			if err := store.migrateFeishuQueue(); errorCode(err) != "CHANNEL_QUEUE_KEY" {
				t.Fatalf("corrupt ciphertext was not rejected: %v", err)
			}
			if !bytes.Equal(before, feishuQueueSnapshot(t, store, true)) {
				t.Fatal("failed migration committed partial ciphertext or changed task state")
			}
		})
	}
}

func TestFeishuQueueCipherFormatsAndBindings(t *testing.T) {
	settings := feishuSettings(t)
	service := &FeishuService{Store: &Store{Settings: settings}}
	const message = "om_cipher"
	body := []byte("legacy message")
	legacy, err := service.seal(message, body)
	if err != nil {
		t.Fatal(err)
	}
	service.Store.Settings.Web.StateEncryptionKey = feishuQueueMigrationKey()
	if plain, err := service.unseal(message, legacy); err != nil || !bytes.Equal(plain, body) {
		t.Fatalf("stable reader could not read legacy format: %s %v", plain, err)
	}
	stable, err := service.seal(message, body)
	if err != nil || !bytes.HasPrefix(stable, []byte(feishuQueuePrefix)) {
		t.Fatalf("missing versioned stable ciphertext: %v", err)
	}
	service.Store.Settings.Channel.AppSecret = stringPointer("new-app-secret")
	if plain, err := service.unseal(message, stable); err != nil || !bytes.Equal(plain, body) {
		t.Fatalf("stable ciphertext depended on application secret: %s %v", plain, err)
	}
	for _, binding := range []string{"instance", "user", "app", "sender", "message", "key", "version"} {
		t.Run(binding, func(t *testing.T) {
			other := &FeishuService{Store: &Store{Settings: service.Store.Settings}}
			id, ciphertext := message, bytes.Clone(stable)
			switch binding {
			case "instance":
				other.Store.Settings.Memos.InstanceID = "other-instance"
			case "user":
				other.Store.Settings.Memos.User = "users/other"
			case "app":
				other.Store.Settings.Channel.AppID = stringPointer("cli_other")
			case "sender":
				other.Store.Settings.Channel.UserOpenID = stringPointer("ou_other")
			case "message":
				id = "om_other"
			case "key":
				other.Store.Settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{32}, 32))
			case "version":
				ciphertext[0] ^= 1
			}
			if _, err := other.unseal(id, ciphertext); errorCode(err) != "CHANNEL_QUEUE_KEY" {
				t.Fatalf("ciphertext crossed %s binding: %v", binding, err)
			}
		})
	}
	service.Store.Settings = settings
	aead := service.queueCipher()
	nonce := []byte(feishuQueuePrefix)[:aead.NonceSize()]
	want := []byte(feishuQueuePrefix)[aead.NonceSize():]
	identity := []byte(settings.Memos.InstanceID + "\x00" + settings.Memos.User + "\x00" + message)
	stream := aead.Seal(nil, nonce, make([]byte, len(want)), identity)
	collisionBody := make([]byte, len(want))
	for i := range collisionBody {
		collisionBody[i] = stream[i] ^ want[i]
	}
	collision := aead.Seal(bytes.Clone(nonce), nonce, collisionBody, identity)
	if !bytes.HasPrefix(collision, []byte(feishuQueuePrefix)) {
		t.Fatal("fixture did not produce a legacy prefix collision")
	}
	service.Store.Settings.Web.StateEncryptionKey = feishuQueueMigrationKey()
	if plain, stable, err := service.openQueue(message, collision); err != nil || stable || !bytes.Equal(plain, collisionBody) {
		t.Fatalf("legacy prefix collision was misclassified: %x %t %v", plain, stable, err)
	}
}

func TestFeishuQueueMigrationWithoutStateKey(t *testing.T) {
	service, _, _ := feishuQueueMigrationFixture(t)
	before := feishuQueueSnapshot(t, service.Store, true)
	if err := service.Store.migrateFeishuQueue(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, feishuQueueSnapshot(t, service.Store, true)) {
		t.Fatal("non-Web compatibility migration changed queue data")
	}
}

func TestFeishuQueueMigrationLegacyDiscoveryResults(t *testing.T) {
	search, _, _ := discoveryFixture(t)
	store := search.Store
	store.Settings.Channel = feishuSettings(t).Channel
	service := &FeishuService{Store: store, Search: search}
	for _, kind := range []string{"search", "walk"} {
		id := "om_" + kind
		seedLegacyFeishuRecord(t, service, id, "legacy request", "processed")
		var request any = SearchRequest{Query: "seed", Limit: 5}
		if kind == "walk" {
			request = WalkRequest{Name: "memos/a", Mode: "continue"}
		}
		payload, err := service.seal(kind+":"+id, canonicalJSON(request))
		if err != nil {
			t.Fatal(err)
		}
		resource := id + "." + base64.RawURLEncoding.EncodeToString(payload)
		if _, err := store.state.Exec(`UPDATE channel_events SET result_kind = ?, resource_name = ?
			WHERE message_id = ?`, kind, resource, id); err != nil {
			t.Fatal(err)
		}
	}
	store.Settings.Web.StateEncryptionKey = feishuQueueMigrationKey()
	if err := store.migrateFeishuQueue(); err != nil {
		t.Fatal(err)
	}
	store.Settings.Channel.AppSecret = stringPointer("new-secret")
	for _, kind := range []string{"search", "walk"} {
		var resource string
		if err := store.state.QueryRow("SELECT resource_name FROM channel_events WHERE message_id = ?", "om_"+kind).Scan(&resource); err != nil {
			t.Fatal(err)
		}
		if text, err := service.render(context.Background(), kind, resource); err != nil || !strings.Contains(text, "memos/") {
			t.Fatalf("legacy %s rendering failed after rotation: %s %v", kind, text, err)
		}
	}
}

func TestFeishuQueueMigrationMixedFormatsAndNullResult(t *testing.T) {
	service, _, _ := feishuQueueMigrationFixture(t)
	store := service.Store
	store.Settings.Web.StateEncryptionKey = feishuQueueMigrationKey()
	stable := seedLegacyFeishuRecord(t, service, "om_stable", "stable event", "pending")
	if _, err := store.state.Exec(`UPDATE feishu_ai_generations SET result = NULL,
		status = 'interrupted', error_code = 'GENERATION_INTERRUPTED'`); err != nil {
		t.Fatal(err)
	}
	before := feishuQueueSnapshot(t, store, false)
	if err := store.migrateFeishuQueue(); err != nil {
		t.Fatal(err)
	}
	var saved []byte
	if err := store.state.QueryRow("SELECT payload FROM channel_events WHERE message_id = 'om_stable'").Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stable, saved) || !bytes.Equal(before, feishuQueueSnapshot(t, store, false)) {
		t.Fatal("mixed migration rewrote a stable event or changed terminal AI state")
	}
	var result []byte
	if err := store.state.QueryRow("SELECT result FROM feishu_ai_generations").Scan(&result); err != nil || result != nil {
		t.Fatalf("null AI result was changed: %x %v", result, err)
	}
	var damaged []byte
	if err := store.state.QueryRow("SELECT payload FROM feishu_ai_parts LIMIT 1").Scan(&damaged); err != nil {
		t.Fatal(err)
	}
	damaged[len(damaged)-1] ^= 1
	if _, err := store.state.Exec("UPDATE feishu_ai_parts SET payload = ?", damaged); err != nil {
		t.Fatal(err)
	}
	before = feishuQueueSnapshot(t, store, true)
	if err := store.migrateFeishuQueue(); errorCode(err) != "CHANNEL_QUEUE_KEY" {
		t.Fatalf("corrupt versioned ciphertext was not rejected: %v", err)
	}
	if !bytes.Equal(before, feishuQueueSnapshot(t, store, true)) {
		t.Fatal("versioned ciphertext failure changed queue data")
	}
}

func TestFeishuQueueMigrationStoredChannelBinding(t *testing.T) {
	settings := feishuSettings(t)
	store := testStore(t, settings)
	service := &FeishuService{Store: store}
	seedLegacyFeishuRecord(t, service, "om_other_binding", "other channel event", "pending")
	store.Settings.Channel.AppID = stringPointer("cli_replacement")
	store.Settings.Channel.UserOpenID = stringPointer("ou_replacement")
	store.Settings.Web.StateEncryptionKey = feishuQueueMigrationKey()
	if err := store.migrateFeishuQueue(); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := store.state.QueryRow("SELECT payload FROM channel_events").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if _, err := service.unseal("om_other_binding", payload); errorCode(err) != "CHANNEL_QUEUE_KEY" {
		t.Fatal("old queue was rebound to a different channel")
	}
	store.Settings.Channel = settings.Channel
	store.Settings.Channel.AppSecret = stringPointer("rotated-secret")
	if body, err := service.unseal("om_other_binding", payload); err != nil || !bytes.Contains(body, []byte("other channel event")) {
		t.Fatalf("original channel could not recover its migrated event: %s %v", body, err)
	}
}

package moerise

import (
	"context"
	"testing"
)

func (s *NotificationService) enqueue(kind, logical, date string, batch, event *string) (string, error) {
	settings := s.Store.Settings
	channel := s.channelKey()
	id := derivedID(settings.CursorSecret, "notification", settings.Memos.InstanceID, settings.Memos.User, channel, logical)
	stamp := nowText()
	var slot *string
	if batch != nil {
		slots, err := s.Reviews.slots(*batch)
		if err != nil {
			return "", err
		}
		if len(slots) > 0 {
			slot = &slots[0].ID
		}
	}
	_, err := s.Store.state.Exec(`INSERT OR IGNORE INTO delivery_logs
    (id,instance_id,user_name,channel_key,channel_kind,logical_key,kind,local_date,batch_id,source_event,slot_id,status,platform_uuid,available_at,created_at,updated_at,root_id)
    VALUES(?,?,?,?,?,?,?,?,?,?,?,'pending',?,?,?,?,?)`, id, settings.Memos.InstanceID, settings.Memos.User, channel, settings.Channel.Kind, logical, kind, date, batch, event, slot, derivedID(settings.CursorSecret, "notification-send", id), nowSeconds(), stamp, stamp, id)
	return id, err
}

func refreshReviewFixture(t *testing.T, store *Store, raws map[string]map[string]any) {
	t.Helper()
	names, err := store.Names()
	if err != nil {
		t.Fatal(err)
	}
	for name := range names {
		if raws[name] == nil {
			if err := store.Refresh(name, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, raw := range raws {
		memo, err := normalizeMemo(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Refresh(memo.Name, &memo); err != nil {
			t.Fatal(err)
		}
	}
}
func processFeishuMessage(ctx context.Context, service *FeishuService) (bool, error) {
	app := &App{Store: service.Store, Feishu: service, Reviews: service.Notifications.Reviews, Records: service.Records}
	return app.processBotMessage(ctx, "feishu-app", configured(service.Store.Settings.Channel.AppID), &feishuBot{events: service})
}

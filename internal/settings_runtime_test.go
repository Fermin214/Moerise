package moerise

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSettingsRuntimeDrainAndResourceReplacement(t *testing.T) {
	settings, store := settingsFixture(t)
	source := fakeClient(settings, func(r *http.Request) (int, any, error) {
		status, body, _ := identityResponse(r)
		return status, body, nil
	})
	app := NewApp(settings, store, source)
	defer app.Close()
	records, reviews, search := app.Records, app.Reviews, app.Search
	update := settingsChange(1, `{"review":{"count":9}}`)
	app.runtimeMu.RLock()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err := app.UpdateSettings(ctx, update)
	app.runtimeMu.RUnlock()
	if errorCode(err) != "SETTINGS_BUSY" || store.productVersion != 1 || store.Settings.Review.Count != 3 {
		t.Fatalf("busy application published settings: %v", err)
	}
	key := "runtime-private-model-key"
	update = settingsChange(1, `{"llm":{"enabled":true,"base_url":"http://127.0.0.1:1/v1","model":"runtime-model"}}`)
	update.Secrets = map[string]SecretUpdate{"llm_api_key": {Action: "replace", Value: &key}}
	view, err := app.UpdateSettings(t.Context(), update)
	if err != nil || view.Version != 2 || app.AI.Provider == nil {
		t.Fatalf("offline provider configuration did not publish: %v", err)
	}
	provider := app.AI.Provider
	view, err = app.UpdateSettings(t.Context(), settingsChange(2, `{"insights":{"default_prompt":"新的思考方向"}}`))
	if err != nil || view.Version != 3 || app.AI.Provider != provider {
		t.Fatalf("unrelated setting replaced provider: %v", err)
	}
	if app.Records != records || app.Reviews != reviews || app.Search != search ||
		app.Store != store || app.Source != source || app.Notes.Settings.Insights.DefaultPrompt != "新的思考方向" {
		t.Fatal("settings update rebuilt shared state or left a stale settings consumer")
	}
	if _, err := app.UpdateSettings(t.Context(), settingsChange(3, `{"llm":{"protocol":"invalid"}}`)); errorCode(err) != "INVALID_ARGUMENT" ||
		store.productVersion != 3 || app.AI.Provider != provider {
		t.Fatalf("invalid provider update changed runtime: %v", err)
	}
}

func TestSettingsRuntimeKeepsTodaysReviewAndTransitionsAfterMidnight(t *testing.T) {
	reviews, _ := reviewFixture(t, 12)
	store := reviews.Store
	store.Settings.Web.StateEncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	if err := store.initializeProductSettings(); err != nil {
		t.Fatal(err)
	}
	app := NewApp(store.Settings, store, reviews.Source)
	defer app.Close()
	now := time.Now()
	first, err := app.Reviews.Daily(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	update := settingsChange(1, `{"timezone":"America/Los_Angeles","review":{"count":7,"recent_days":0},"notifications":{"times":["10:00","18:00"]}}`)
	if _, err := app.UpdateSettings(t.Context(), update); err != nil {
		t.Fatal(err)
	}
	again, err := app.Reviews.Daily(t.Context(), now)
	if err != nil || again.ID != first.ID || len(again.Items) != 3 {
		t.Fatalf("timezone or review update replaced today's plan: %v", err)
	}
	if frozen := store.reviewSettings(now); frozen.Timezone != "Asia/Hong_Kong" || frozen.Review.Count != 3 ||
		strings.Join(frozen.Notifications.Clocks(), ",") != "09:00" {
		t.Fatal("same-day settings were not frozen")
	}
	loaded, err := loadPersistedProduct(app.Settings)
	if err != nil || loaded.daily == nil || loaded.daily.Timezone != first.Timezone {
		t.Fatalf("restart lost the day transition: %v", err)
	}
	after := store.Settings.daily.Until.Add(time.Second)
	if effective := store.reviewSettings(after); effective.Timezone != "America/Los_Angeles" || effective.Review.Count != 7 {
		t.Fatal("new day did not use saved settings")
	}
}

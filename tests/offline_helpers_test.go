//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"errors"

	moerise "moerise/internal"
)

// withOfflineApp holds the same writer lock as serve while exercising shared
// business services. Resources close before the subprocess starts again.
func (h *harness) withOfflineApp(call func(*moerise.App)) {
	h.t.Helper()
	h.stop()
	defer h.start()
	lock, err := moerise.AcquireWriter(h.settings.StateDir)
	if err != nil {
		h.t.Fatal(err)
	}
	defer lock.Close()
	store, err := moerise.NewStore(h.settings)
	if err != nil {
		h.t.Fatal(err)
	}
	defer store.Close()
	source := moerise.NewMemosClient(h.settings.Memos)
	defer source.Close()
	app := moerise.NewApp(h.settings, store, source)
	defer app.Close()
	call(app)
}

func businessResult(h *harness, value any, status int, err error, expected int) map[string]any {
	h.t.Helper()
	if err != nil {
		var service *moerise.ServiceError
		if !errors.As(err, &service) {
			h.t.Fatal(err)
		}
		status = service.Status
		value = map[string]any{"error": map[string]any{
			"code": service.Code, "message": service.Message, "retryable": service.Retryable,
		}}
	}
	if status != expected {
		h.t.Fatalf("business result: status=%d expected=%d value=%+v err=%v", status, expected, value, err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		h.t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h *harness) enqueueSync(mode string) moerise.Job {
	h.t.Helper()
	var job moerise.Job
	h.withOfflineApp(func(app *moerise.App) {
		var err error
		job, err = app.Store.Enqueue(mode, true)
		if err != nil {
			h.t.Fatal(err)
		}
	})
	return job
}

func (h *harness) recordInProcess(key string, input any, expected int) map[string]any {
	h.t.Helper()
	var result map[string]any
	h.withOfflineApp(func(app *moerise.App) {
		result = h.recordUsingApp(app, key, input, expected)
	})
	return result
}

func (h *harness) recordUsingApp(app *moerise.App, key string, input any, expected int) map[string]any {
	h.t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		h.t.Fatal(err)
	}
	var body moerise.CreateNote
	if err := json.Unmarshal(data, &body); err != nil {
		h.t.Fatal(err)
	}
	note, status, err := app.Records.Create(context.Background(), key, body)
	return businessResult(h, note, status, err, expected)
}

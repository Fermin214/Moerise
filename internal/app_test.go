package moerise

import (
	"context"
	"net"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"
)

func TestAppSharesServicesAndClosesOnce(t *testing.T) {
	settings := testSettings(t)
	appID := "app-test"
	settings.Channel.AppID = &appID
	store := testStore(t, settings)
	source := NewMemosClient(settings.Memos)
	defer source.Close()
	app := NewApp(settings, store, source)
	api, err := NewAPI(app)
	if err != nil {
		t.Fatal(err)
	}
	if api.App != app || app.Feishu == nil || app.Feishu.Search != app.Search ||
		app.Feishu.Records != app.Records || app.Feishu.Reviews != app.Reviews ||
		app.Feishu.Notifications != app.Notifications || app.Feishu.AI != app.AI {
		t.Fatal("entry points do not share the application services")
	}
	var order []int
	app.closers = []func(){func() { order = append(order, 1) }, func() { order = append(order, 2) }}
	app.Close()
	app.Close()
	if !slices.Equal(order, []int{2, 1}) {
		t.Fatalf("resource close order: %v", order)
	}
}

func TestAppListenerFailureStopsWorkers(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	source := fakeClient(settings, func(r *http.Request) (int, any, error) {
		if status, body, ok := identityResponse(r); ok {
			return status, body, nil
		}
		return 200, map[string]any{"memos": []any{}}, nil
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	app := NewApp(settings, store, source)
	defer app.Close()
	done := make(chan error, 1)
	go func() { done <- app.Run(context.Background(), &http.Server{Addr: listener.Addr().String()}) }()
	select {
	case err := <-done:
		if err == nil || app.Worker.alive.Load() {
			t.Fatalf("failed listener left a worker alive: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed listener did not stop the application")
	}
}

func TestAppConstructionFailureReleasesWriter(t *testing.T) {
	settings := testSettings(t)
	if err := os.WriteFile(settings.IndexDir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if app, err := OpenApp(settings); err == nil {
		app.Close()
		t.Fatal("database construction unexpectedly succeeded")
	}
	lock, err := AcquireWriter(settings.StateDir)
	if err != nil {
		t.Fatalf("failed construction retained writer lock: %v", err)
	}
	_ = lock.Close()
}

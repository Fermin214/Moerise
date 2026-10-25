package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	moerise "moerise/internal"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: moerise serve|check-config|backup|restore|rebuild-index|healthcheck|version")
	}
	if args[0] == "version" {
		fmt.Println(moerise.Version)
		return nil
	}
	options := flag.NewFlagSet(args[0], flag.ContinueOnError)
	config := options.String("config", "", "Configuration file (or MOERISE_CONFIG_FILE)")
	host := options.String("host", "0.0.0.0", "HTTP listen host")
	port := options.Int("port", 8787, "HTTP listen port")
	output := options.String("output", "", "New backup directory")
	source := options.String("source", "", "Backup directory")
	safety := options.String("safety-backup", "", "New directory for the replaced state")
	if err := options.Parse(args[1:]); err != nil {
		return err
	}
	if options.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *port < 1 || *port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if args[0] == "healthcheck" {
		if *host == "0.0.0.0" {
			*host = "127.0.0.1"
		}
		client := http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
		response, err := client.Get("http://" + net.JoinHostPort(*host, strconv.Itoa(*port)) + "/health/live")
		if err != nil {
			return errors.New("service is unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return errors.New("service is unavailable")
		}
		return nil
	}
	if *config == "" {
		*config = os.Getenv("MOERISE_CONFIG_FILE")
	}
	if *config == "" {
		*config = ".local/config.json"
	}
	settings, err := moerise.LoadSettings(*config)
	if err != nil {
		return err
	}
	encode := func(value any) error { return json.NewEncoder(os.Stdout).Encode(value) }
	switch args[0] {
	case "check-config":
		return encode(map[string]string{"status": "valid", "instance_id": settings.Memos.InstanceID, "user": settings.Memos.User, "timezone": settings.Timezone})
	case "serve":
		return serve(settings, net.JoinHostPort(*host, strconv.Itoa(*port)))
	case "backup":
		if *output == "" {
			return errors.New("backup requires --output")
		}
		manifest, err := moerise.Backup(settings, *output)
		if err != nil {
			return err
		}
		return encode(manifest)
	case "restore":
		if *source == "" || *safety == "" {
			return errors.New("restore requires --source and --safety-backup")
		}
		if err := moerise.Restore(settings, *source, *safety); err != nil {
			return err
		}
		fmt.Println("State restored; the next startup performs a full sync.")
		return nil
	case "rebuild-index":
		if err := moerise.RebuildIndex(settings); err != nil {
			return err
		}
		fmt.Println("Projection cleared; product state retained. Start the service to sync.")
		return nil
	default:
		return errors.New("unknown command")
	}
}

func serve(settings moerise.Settings, address string) error {
	lock, err := moerise.AcquireWriter(settings.StateDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	store, err := moerise.NewStore(settings)
	if err != nil {
		return err
	}
	defer store.Close()
	source := moerise.NewMemosClient(settings.Memos)
	defer source.Close()
	worker := moerise.NewSyncService(store, source)
	api, err := moerise.NewAPI(settings, store, source, worker)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: address, Handler: api, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 60 * time.Second,
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone, serverDone := make(chan error, 1), make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()
	go func() { serverDone <- server.ListenAndServe() }()
	slog.Info("service started", "address", address, "version", moerise.Version)
	var result error
	workerExited := false
	select {
	case <-signalCtx.Done():
	case err := <-workerDone:
		workerExited = true
		if err != nil && !errors.Is(err, context.Canceled) {
			result = errors.New("background worker stopped")
		}
	case err := <-serverDone:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		if result == nil {
			result = err
		}
	}
	if !workerExited {
		<-workerDone
	}
	return result
}

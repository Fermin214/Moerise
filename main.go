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
	"moerise/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: moerise serve|check-config|check-embedding|backup|restore|rollback|rebuild-index|healthcheck|version")
	}
	if args[0] == "version" {
		fmt.Println(moerise.Version)
		return nil
	}
	options := flag.NewFlagSet(args[0], flag.ContinueOnError)
	config := options.String("config", "", "Configuration file (or MOERISE_CONFIG_FILE)")
	host := options.String("host", "0.0.0.0", "HTTP listen host")
	port := options.Int("port", 8787, "HTTP listen port")
	webDir := options.String("web-dir", "", "Web build directory for development")
	output := options.String("output", "", "New backup directory")
	source := options.String("source", "", "Backup directory")
	safety := options.String("safety-backup", "", "New directory for the replaced state")
	backup := options.String("backup", "", "State SQLite snapshot file to roll back to")
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
	case "check-embedding":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		result, err := moerise.CheckEmbedding(ctx, settings.Embedding)
		if err != nil {
			var service *moerise.ServiceError
			if errors.As(err, &service) {
				return fmt.Errorf("%s: %w", service.Code, err)
			}
			return err
		}
		return encode(result)
	case "serve":
		return serve(settings, net.JoinHostPort(*host, strconv.Itoa(*port)), *webDir)
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
	case "rollback":
		result, err := moerise.Rollback(settings, *backup)
		if err != nil {
			return err
		}
		if result.CrossVersion {
			target := result.DowngradeVersion
			if target == "" {
				target = "the matching older release"
			}
			fmt.Printf("Rolled back state.sqlite3 from %s (replaced copy saved at %s); index.sqlite3 cleared for automatic rebuild.\nCross-version downgrade detected (snapshot schema %s < current %s): switch binary/image back to %s before starting Moerise so newer migrations are not re-applied.\n",
				result.BackupPath, result.SafetyPath, result.MaxSnapshotMigration, result.CurrentKnownMigration, target)
			return nil
		}
		fmt.Printf("Rolled back state.sqlite3 from %s (replaced copy saved at %s); index.sqlite3 cleared for automatic rebuild.\nSame-version snapshot restore completed; start Moerise to resume and rebuild the index.\n",
			result.BackupPath, result.SafetyPath)
		return nil
	default:
		return errors.New("unknown command")
	}
}

func serve(settings moerise.Settings, address, webDir string) error {
	resources := moerise.ConfigureRuntime()
	app, err := moerise.OpenApp(settings)
	if err != nil {
		return err
	}
	defer app.Close()
	api, err := moerise.NewHandler(app)
	if err != nil {
		return err
	}
	if settings.Web.Enabled {
		api = web.Handler(webDir, api)
	}
	server := &http.Server{Addr: address, Handler: api, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 60 * time.Second,
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("service started", "address", address, "version", moerise.Version,
		"cpus", resources.CPUs, "memory_budget", resources.MemoryBytes, "go_memory_limit", resources.GoMemoryBytes)
	return app.Run(ctx, server)
}

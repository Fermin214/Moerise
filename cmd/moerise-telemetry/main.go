package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"moerise/internal/telemetry"
)

func run() error {
	database := flag.String("db", "telemetry.sqlite3", "daily counters database")
	address := flag.String("listen", "127.0.0.1:8091", "HTTP listen address behind a private HTTPS proxy")
	flag.Parse()
	if flag.NArg() != 1 || (flag.Arg(0) != "serve" && flag.Arg(0) != "report") {
		return errors.New("usage: moerise-telemetry [-db file] [-listen address] serve|report")
	}
	receiver, err := telemetry.OpenReceiver(*database)
	if err != nil {
		return err
	}
	defer receiver.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if flag.Arg(0) == "report" {
		return receiver.WeeklyReport(ctx, os.Stdout)
	}
	server := &http.Server{Addr: *address, Handler: receiver, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 6 * time.Second,
		WriteTimeout: 6 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan error, 2)
	go func() { done <- receiver.RunExpiry(ctx) }()
	go func() { done <- server.ListenAndServe() }()
	select {
	case err = <-done:
	case <-ctx.Done():
	}
	stop()
	drain, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	closeErr := server.Shutdown(drain)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return closeErr
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

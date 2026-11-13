package main

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"datamesh/internal/modules/identity"
	"datamesh/internal/platform/database"
	"datamesh/internal/platform/httpserver"
)

func run() error {
	origin, err := url.Parse(os.Getenv("APP_ORIGIN"))
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.User != nil {
		return errors.New("APP_ORIGIN must be an HTTPS origin")
	}
	key, err := hex.DecodeString(os.Getenv("CSRF_KEY"))
	if err != nil || len(key) < 32 {
		return errors.New("CSRF_KEY must be at least 32 random bytes encoded in hex")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	db, err := database.Open(ctx, os.Getenv("APP_DSN"))
	cancel()
	if err != nil {
		return errors.New("database connection failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return errors.New("database unavailable")
	}
	defer sqlDB.Close()
	service, err := identity.NewService(identity.NewRepository(db), key)
	if err != nil {
		return err
	}
	stop, release := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer release()
	router, stopWorkers, err := httpserver.NewWithLifecycle(stop, db, service, origin.String())
	if err != nil {
		return err
	}
	defer stopWorkers()
	handler, err := httpserver.MountBasePath(os.Getenv("BASE_PATH"), router)
	if err != nil {
		return err
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8443"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return errors.New("invalid PORT")
	}
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServeTLS(os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY")) }()
	slog.Info("server started", "origin", origin.String())
	select {
	case err := <-done:
		return err
	case <-stop.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "error", err.Error())
		os.Exit(1)
	}
}

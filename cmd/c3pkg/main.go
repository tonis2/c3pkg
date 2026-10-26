package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tonis/c3pkg/internal/config"
	"github.com/tonis/c3pkg/internal/db"
	"github.com/tonis/c3pkg/internal/storage"
	"github.com/tonis/c3pkg/internal/store"
	"github.com/tonis/c3pkg/internal/web"
	"github.com/tonis/c3pkg/migrations"
)

func main() {
	if err := config.LoadDotEnv(".env"); err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cfg.UploadsDir(), 0o755); err != nil {
		log.Fatal(err)
	}
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
	case "admin":
		fatalIf(runAdmin(cfg, os.Args[2:]))
		return
	case "backup":
		fatalIf(runBackup(cfg, os.Args[2:]))
		return
	case "healthcheck":
		fatalIf(runHealthcheck(cfg))
		return
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	database, err := db.Open(cfg.DBPath(), migrations.FS)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	srv, err := web.New(cfg, &store.Store{DB: database}, storage.Local{Dir: cfg.UploadsDir()})
	if err != nil {
		log.Fatal(err)
	}
	srv.LogRequests = true
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // uploads are capped at a few MB, so this is generous
		WriteTimeout:      5 * time.Minute, // slow downloads
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.RunJobs(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
	}()

	log.Printf("listening on %s (data: %s)", cfg.Addr, cfg.DataDir)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func contextBackground() context.Context { return context.Background() }

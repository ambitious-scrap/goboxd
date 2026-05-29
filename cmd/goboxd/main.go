package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/ambitious-scrap/goboxd/internal/api"
	"github.com/ambitious-scrap/goboxd/internal/jail"
	"github.com/ambitious-scrap/goboxd/internal/config"
	"github.com/ambitious-scrap/goboxd/internal/registry"
	"github.com/ambitious-scrap/goboxd/internal/runner"
)

// Injected by -ldflags at build time.
var (
	version   = "dev"
	commit    = "unknown"
	goVersion = runtime.Version()
)

func main() {
	cfgPath := flag.String("config", "configs/languages.yaml", "path to config file")
	port := flag.Int("port", 0, "port override (default from config)")
	jailBase := flag.String("jail-base", "", "jail base dir override")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	if *port != 0 {
		cfg.Server.Port = *port
	}
	if *jailBase != "" {
		cfg.Server.JailBase = *jailBase
	}

	if err := os.MkdirAll(cfg.Server.JailBase, 0700); err != nil {
		slog.Error("create jail base", "err", err)
		os.Exit(1)
	}
	jail.SweepOrphans(cfg.Server.JailBase, 10*time.Minute)

	reg := registry.New(cfg.Languages)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("running language smoke probes...")
	smokes := reg.RunSmokes(ctx)
	for id, sr := range smokes {
		if sr.OK {
			slog.Info("language ready", "id", id, "version", sr.Version)
		} else {
			slog.Warn("language degraded", "id", id, "error", sr.Error)
		}
	}

	r := runner.New(cfg.Server.NsjailPath, cfg.Server.JailBase, cfg.Server.OutputCapBytes)

	srv := api.NewServer(cfg, reg, r, smokes, api.BuildInfo{
		Version:   version,
		Commit:    commit,
		GoVersion: goVersion,
	})

	httpSrv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      srv.Router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		slog.Info("listening", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
}

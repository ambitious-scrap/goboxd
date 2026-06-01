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

	// Sets GOMAXPROCS from the container's cgroup CPU quota at startup so the Go
	// scheduler doesn't oversubscribe when the host has more cores than the quota.
	_ "go.uber.org/automaxprocs"

	"github.com/thesouldev/goboxd/internal/api"
	"github.com/thesouldev/goboxd/internal/config"
	"github.com/thesouldev/goboxd/internal/jail"
	"github.com/thesouldev/goboxd/internal/registry"
	"github.com/thesouldev/goboxd/internal/runner"
	"github.com/thesouldev/goboxd/internal/sandbox"
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
	sandbox.SweepOrphanCgroups(10 * time.Minute)

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

	nsjailInfo := api.NsjailInfo{}
	if err := sandbox.Probe(ctx, cfg.Server.NsjailPath); err != nil {
		nsjailInfo.Error = err.Error()
		slog.Warn("nsjail probe failed", "err", err)
	} else {
		nsjailInfo.OK = true
		nsjailInfo.Version = sandbox.NsjailVersion()
		slog.Info("nsjail ready", "version", nsjailInfo.Version)
	}

	cgroupsEnabled := sandbox.MemoryAccountingAvailable()
	if cgroupsEnabled {
		slog.Info("cgroup v2 memory accounting active")
	} else {
		slog.Warn("cgroup v2 memory accounting unavailable; using rlimit_as fallback (no OOM detection)")
	}

	srv := api.NewServer(cfg, reg, r, smokes, api.BuildInfo{
		Version:   version,
		Commit:    commit,
		GoVersion: goVersion,
	}, nsjailInfo, cgroupsEnabled)

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

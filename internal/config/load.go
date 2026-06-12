package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultPort            = 8080
	defaultMaxBodyBytes    = 4194304 // 4 MiB: whole-request envelope (source + tests)
	defaultMaxSourceBytes  = 262144  // 256 KiB: source field cap (spec)
	defaultOutputCapBytes  = 65536   // 64 KiB
	defaultJailBase        = "/tmp/goboxd"
	defaultNsjailPath      = "/usr/local/bin/nsjail"
	defaultMaxTests        = 100
	defaultMetricsPort     = 9090
	defaultCacheDir        = "/tmp/goboxd-cache"
	defaultCacheMaxEntries = 512
	cgroupMemoryMaxPath    = "/sys/fs/cgroup/memory.max"                  // cgroup v2
	cgroupV1MemoryLimit    = "/sys/fs/cgroup/memory/memory.limit_in_bytes" // cgroup v1
)

// cgroupMemoryPaths is the ordered list of files probed for the container memory
// limit (v2 first, then v1). A package var so tests can point it at fixtures.
var cgroupMemoryPaths = []string{cgroupMemoryMaxPath, cgroupV1MemoryLimit}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = defaultPort
	}
	if cfg.Server.MaxConcurrency == 0 {
		cfg.Server.MaxConcurrency = runtime.GOMAXPROCS(0)
	}
	// Derived from MaxConcurrency, so resolve it first (above).
	if cfg.Server.MaxQueue == 0 {
		cfg.Server.MaxQueue = 2 * cfg.Server.MaxConcurrency
	}
	if cfg.Server.MaxBuildConcurrency == 0 {
		cfg.Server.MaxBuildConcurrency = max(1, cfg.Server.MaxConcurrency/2)
	}
	// Fast-lane reservation. Default to a quarter of the pool; clamp into
	// [0, MaxConcurrency-1] so heavy jobs always keep at least one slot and the
	// reservation can never deadlock a single-slot server.
	if cfg.Server.FastLaneReserved == 0 {
		cfg.Server.FastLaneReserved = max(1, cfg.Server.MaxConcurrency/4)
	}
	if cfg.Server.FastLaneReserved < 0 {
		cfg.Server.FastLaneReserved = 0
	}
	if cfg.Server.FastLaneReserved > cfg.Server.MaxConcurrency-1 {
		cfg.Server.FastLaneReserved = max(0, cfg.Server.MaxConcurrency-1)
	}

	if cfg.Server.SchedulerMemoryKB == 0 {
		cfg.Server.SchedulerMemoryKB = defaultSchedulerMemoryKB(cfg.Server.MaxConcurrency)
	}
	if cfg.Server.CacheEnabled == nil {
		enabled := true
		cfg.Server.CacheEnabled = &enabled
	}
	if cfg.Server.CacheDir == "" {
		cfg.Server.CacheDir = defaultCacheDir
	}
	if cfg.Server.CacheMaxEntries == 0 {
		cfg.Server.CacheMaxEntries = defaultCacheMaxEntries
	}
	if cfg.Server.MaxBodyBytes == 0 {
		cfg.Server.MaxBodyBytes = defaultMaxBodyBytes
	}
	if cfg.Server.MaxSourceBytes == 0 {
		cfg.Server.MaxSourceBytes = defaultMaxSourceBytes
	}
	if cfg.Server.OutputCapBytes == 0 {
		cfg.Server.OutputCapBytes = defaultOutputCapBytes
	}
	if cfg.Server.JailBase == "" {
		cfg.Server.JailBase = defaultJailBase
	}
	if cfg.Server.NsjailPath == "" {
		cfg.Server.NsjailPath = defaultNsjailPath
	}
	if cfg.Server.MaxTests == 0 {
		cfg.Server.MaxTests = defaultMaxTests
	}
	if cfg.Server.SeccompMode == "" {
		cfg.Server.SeccompMode = "off"
	}
	if cfg.Server.MetricsPort == 0 {
		cfg.Server.MetricsPort = defaultMetricsPort
	}
	for i := range cfg.Languages {
		applyLanguageDefaults(&cfg.Languages[i])
	}
}

func defaultSchedulerMemoryKB(maxConcurrency int) int {
	if bytes, ok := cgroupMemoryLimitBytes(); ok {
		return int((bytes / 1024) * 85 / 100)
	}
	// No finite cgroup limit visible (e.g. --cgroupns=host hides memory.max):
	// fall back to a per-slot reservation. Note this scales with concurrency, so
	// the token gate is effectively inert under the fallback — prefer a real
	// limit, or set scheduler_memory_kb explicitly.
	return max(1, maxConcurrency) * 524288
}

// cgroupMemoryLimitBytes returns the container memory limit in bytes from cgroup
// v2 (memory.max) or v1 (memory.limit_in_bytes). ok is false when neither yields a
// finite limit: the file is absent, says "max", or holds the v1 "unlimited"
// sentinel (PAGE_COUNTER_MAX, near int64 max).
func cgroupMemoryLimitBytes() (int64, bool) {
	for _, p := range cgroupMemoryPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		raw := strings.TrimSpace(string(data))
		if raw == "" || raw == "max" {
			continue
		}
		n, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil || n <= 0 {
			continue
		}
		if n >= 1<<62 { // v1 unlimited sentinel — treat as no limit
			continue
		}
		return n, true
	}
	return 0, false
}

func applyLanguageDefaults(lang *Language) {
	if lang.Build != nil {
		if lang.Build.Limits.WallTimeS == 0 {
			lang.Build.Limits.WallTimeS = 30
		}
		if lang.Build.Limits.MemoryKB == 0 {
			lang.Build.Limits.MemoryKB = 1048576
		}
		if lang.Build.Limits.MaxProcesses == 0 {
			lang.Build.Limits.MaxProcesses = 100
		}
	}
	if lang.Run.Limits.WallTimeS == 0 {
		lang.Run.Limits.WallTimeS = 5
	}
	if lang.Run.Limits.MemoryKB == 0 {
		lang.Run.Limits.MemoryKB = 262144
	}
	if lang.Run.Limits.MaxProcesses == 0 {
		lang.Run.Limits.MaxProcesses = 64
	}
}

func validate(cfg *Config) error {
	switch cfg.Server.SeccompMode {
	case "off", "audit", "enforce":
	default:
		return fmt.Errorf("server.seccomp_mode: invalid value %q (want off, audit, or enforce)", cfg.Server.SeccompMode)
	}
	seen := map[string]bool{}
	for _, lang := range cfg.Languages {
		if lang.ID == "" {
			return fmt.Errorf("language missing id")
		}
		if seen[lang.ID] {
			return fmt.Errorf("duplicate language id: %s", lang.ID)
		}
		seen[lang.ID] = true
		if lang.SourceFilename == "" {
			return fmt.Errorf("language %s: missing source_filename", lang.ID)
		}
		if lang.Run.Cmd == "" {
			return fmt.Errorf("language %s: missing run.cmd", lang.ID)
		}
		if lang.Smoke.Cmd == "" {
			return fmt.Errorf("language %s: missing smoke.cmd", lang.ID)
		}
		if lang.Build != nil && lang.Build.Cmd == "" {
			return fmt.Errorf("language %s: build block present but cmd is empty", lang.ID)
		}
	}
	return nil
}

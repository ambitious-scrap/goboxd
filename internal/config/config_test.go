package config_test

import (
	"os"
	"testing"

	"github.com/thesouldev/goboxd/internal/config"
)

const validYAML = `
languages:
  - id: py3
    name: Python 3
    source_filename: solution.py
    run:
      cmd: /usr/bin/python3
      args: ["{{source}}"]
    smoke:
      cmd: /usr/bin/python3
      args: ["--version"]
`

func TestLoad_Valid(t *testing.T) {
	f := writeTmp(t, validYAML)
	cfg, err := config.Load(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Languages) != 1 {
		t.Fatalf("expected 1 language, got %d", len(cfg.Languages))
	}
	if cfg.Languages[0].ID != "py3" {
		t.Errorf("expected id py3, got %s", cfg.Languages[0].ID)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}
	// C-1/C-2 defaults derive from MaxConcurrency (= NumCPU when unset).
	if cfg.Server.MaxQueue != 2*cfg.Server.MaxConcurrency {
		t.Errorf("MaxQueue = %d, want %d", cfg.Server.MaxQueue, 2*cfg.Server.MaxConcurrency)
	}
	wantBuild := cfg.Server.MaxConcurrency / 2
	if wantBuild < 1 {
		wantBuild = 1
	}
	if cfg.Server.MaxBuildConcurrency != wantBuild {
		t.Errorf("MaxBuildConcurrency = %d, want %d", cfg.Server.MaxBuildConcurrency, wantBuild)
	}
	if cfg.Server.CacheEnabled == nil || !*cfg.Server.CacheEnabled {
		t.Errorf("CacheEnabled = %v, want default true", cfg.Server.CacheEnabled)
	}
	if cfg.Server.CacheDir != "/tmp/goboxd-cache" {
		t.Errorf("CacheDir = %q, want /tmp/goboxd-cache", cfg.Server.CacheDir)
	}
	if cfg.Server.CacheMaxEntries != 512 {
		t.Errorf("CacheMaxEntries = %d, want 512", cfg.Server.CacheMaxEntries)
	}
}

// An explicit cache_enabled: false must survive defaulting (the *bool lets an
// explicit false be distinguished from "unset").
func TestLoad_CacheDisabledExplicit(t *testing.T) {
	yaml := validYAML + "server:\n  cache_enabled: false\n"
	f := writeTmp(t, yaml)
	cfg, err := config.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.CacheEnabled == nil || *cfg.Server.CacheEnabled {
		t.Errorf("CacheEnabled = %v, want explicit false", cfg.Server.CacheEnabled)
	}
}

func TestLoad_MissingID(t *testing.T) {
	yaml := `
languages:
  - name: Missing ID
    source_filename: foo.py
    run:
      cmd: /usr/bin/python3
    smoke:
      cmd: /usr/bin/python3
`
	f := writeTmp(t, yaml)
	if _, err := config.Load(f); err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestLoad_DuplicateID(t *testing.T) {
	yaml := `
languages:
  - id: py3
    name: Python 3
    source_filename: solution.py
    run:
      cmd: /usr/bin/python3
    smoke:
      cmd: /usr/bin/python3
  - id: py3
    name: Python 3 again
    source_filename: solution.py
    run:
      cmd: /usr/bin/python3
    smoke:
      cmd: /usr/bin/python3
`
	f := writeTmp(t, yaml)
	if _, err := config.Load(f); err == nil {
		t.Fatal("expected error for duplicate id")
	}
}

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

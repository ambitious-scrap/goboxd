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

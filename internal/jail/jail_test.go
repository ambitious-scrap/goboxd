package jail_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/thesouldev/goboxd/internal/jail"
)

var nameRe = regexp.MustCompile(`^\d+_\d+_[0-9a-f]{16}$`)

func TestCreateUniqueAndNamed(t *testing.T) {
	base := t.TempDir()
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		p, err := jail.Create(base)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		name := filepath.Base(p)
		if !nameRe.MatchString(name) {
			t.Errorf("name %q does not match expected pattern", name)
		}
		if seen[p] {
			t.Fatalf("duplicate jail path %q", p)
		}
		seen[p] = true
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			t.Errorf("jail dir not created: %v", err)
		}
	}
}

func TestCleanup(t *testing.T) {
	base := t.TempDir()
	p, err := jail.Create(base)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	jail.Cleanup(p)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("jail dir still exists after Cleanup: %v", err)
	}
}

func TestSweepOrphans(t *testing.T) {
	base := t.TempDir()

	oldDir := filepath.Join(base, "old")
	freshDir := filepath.Join(base, "fresh")
	for _, d := range []string{oldDir, freshDir} {
		if err := os.Mkdir(d, 0700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// Age the old dir well past the cutoff.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldDir, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	jail.SweepOrphans(base, 10*time.Minute)

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Errorf("old orphan dir should have been swept, err=%v", err)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Errorf("fresh dir should be kept, err=%v", err)
	}
}

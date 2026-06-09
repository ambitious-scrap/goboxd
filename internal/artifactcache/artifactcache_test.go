package artifactcache

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestKey_DeterministicAndSensitive(t *testing.T) {
	base := Key("cpp", "g++ 13.2", "int main(){}", []string{"-O2"}, "a.out")

	if got := Key("cpp", "g++ 13.2", "int main(){}", []string{"-O2"}, "a.out"); got != base {
		t.Errorf("key not deterministic: %s != %s", got, base)
	}

	cases := map[string]string{
		"source":     Key("cpp", "g++ 13.2", "int main(){return 1;}", []string{"-O2"}, "a.out"),
		"flags":      Key("cpp", "g++ 13.2", "int main(){}", []string{"-O3"}, "a.out"),
		"toolchain":  Key("cpp", "g++ 14.0", "int main(){}", []string{"-O2"}, "a.out"),
		"langID":     Key("c", "g++ 13.2", "int main(){}", []string{"-O2"}, "a.out"),
		"artifact":   Key("cpp", "g++ 13.2", "int main(){}", []string{"-O2"}, "prog"),
		"flag-order": Key("cpp", "g++ 13.2", "int main(){}", []string{"-O2", "-Wall"}, "a.out"),
		"no-flags":   Key("cpp", "g++ 13.2", "int main(){}", nil, "a.out"),
	}
	for name, k := range cases {
		if k == base {
			t.Errorf("%s change did not alter key", name)
		}
	}
}

// Key must not let a flag value bleed into the artifact filename field — the NUL
// separators guard against this collision.
func TestKey_NoFieldCollision(t *testing.T) {
	a := Key("cpp", "v1", "src", []string{"-O2"}, "a.out")
	b := Key("cpp", "v1", "src", []string{"-O2", "a.out"}, "")
	if a == b {
		t.Error("flag/artifact field boundary collided")
	}
}

func TestPutGet_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := New(filepath.Join(dir, "cache"), 16)
	if err != nil {
		t.Fatal(err)
	}

	// A jail workdir with a source file (excluded) and a build artifact (kept).
	jail := t.TempDir()
	mustWrite(t, filepath.Join(jail, "solution.cpp"), "int main(){}", 0644)
	mustWrite(t, filepath.Join(jail, "a.out"), "BINARY", 0755)
	// A nested artifact (e.g. Java inner class) must also be captured.
	mustWrite(t, filepath.Join(jail, "pkg", "Inner.class"), "CLASS", 0644)

	key := Key("cpp", "v1", "int main(){}", nil, "a.out")
	meta := Meta{BuildStdout: "out", BuildStderr: "warn", BuildDurationMs: 42}
	if err := c.Put(key, jail, "solution.cpp", meta); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Restore into a fresh empty jail.
	dest := t.TempDir()
	got, ok := c.Get(key, dest)
	if !ok {
		t.Fatal("expected hit")
	}
	if got != meta {
		t.Errorf("meta = %+v, want %+v", got, meta)
	}
	// Source must NOT have been cached.
	if _, err := os.Stat(filepath.Join(dest, "solution.cpp")); !os.IsNotExist(err) {
		t.Error("source file should not be cached")
	}
	// Artifact present with executable bit preserved.
	info, err := os.Stat(filepath.Join(dest, "a.out"))
	if err != nil {
		t.Fatalf("a.out not restored: %v", err)
	}
	if info.Mode().Perm()&0100 == 0 {
		t.Errorf("a.out lost exec bit: %v", info.Mode())
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "a.out")); string(b) != "BINARY" {
		t.Errorf("a.out content = %q", b)
	}
	// Nested artifact restored.
	if b, _ := os.ReadFile(filepath.Join(dest, "pkg", "Inner.class")); string(b) != "CLASS" {
		t.Errorf("nested class = %q", b)
	}
}

func TestGet_Miss(t *testing.T) {
	c, err := New(t.TempDir(), 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("nonexistent", t.TempDir()); ok {
		t.Error("expected miss for unknown key")
	}
}

func TestEviction_DropsOldest(t *testing.T) {
	c, err := New(t.TempDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	jail := t.TempDir()
	mustWrite(t, filepath.Join(jail, "a.out"), "x", 0755)

	// Insert 3 entries into a cache capped at 2; the first should be evicted.
	for _, k := range []string{"k1", "k2", "k3"} {
		if err := c.Put(k, jail, "", Meta{}); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
		// Distinct mtimes so "oldest" is well-defined.
		bump(t, filepath.Join(c.dir, k))
	}

	if _, ok := c.Get("k1", t.TempDir()); ok {
		t.Error("k1 should have been evicted")
	}
	if _, ok := c.Get("k3", t.TempDir()); !ok {
		t.Error("k3 should remain")
	}
}

// Lock serializes identical keys so a guarded build runs exactly once even with
// concurrent callers.
func TestLock_SingleFlight(t *testing.T) {
	c, err := New(t.TempDir(), 16)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	var (
		mu      sync.Mutex
		active  int
		maxSeen int
		wg      sync.WaitGroup
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			unlock := c.Lock("same-key")
			defer unlock()
			mu.Lock()
			active++
			if active > maxSeen {
				maxSeen = active
			}
			mu.Unlock()
			// Hold the critical section briefly.
			for j := 0; j < 1000; j++ {
				_ = j
			}
			mu.Lock()
			active--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if maxSeen != 1 {
		t.Errorf("max concurrent holders = %d, want 1", maxSeen)
	}
	// The lock map must be empty again (refcount dropped to 0).
	c.mu.Lock()
	n := len(c.locks)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("lock map leaked %d entries", n)
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile honors umask; force the mode explicitly so the exec-bit test is
	// deterministic.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// bumpClock hands out strictly increasing timestamps, anchored in the past so a
// freshly-inserted entry (real-now mtime) always sorts as the newest. This makes
// eviction's "oldest" ordering deterministic regardless of FS timestamp
// granularity.
var bumpClock = time.Now().Add(-time.Hour)

// bump sets a directory's mtime to a strictly increasing value so eviction's
// "oldest" ordering is deterministic.
func bump(t *testing.T, path string) {
	t.Helper()
	bumpClock = bumpClock.Add(time.Second)
	if err := os.Chtimes(path, bumpClock, bumpClock); err != nil {
		t.Fatal(err)
	}
}

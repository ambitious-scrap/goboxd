// Package artifactcache is a content-addressed cache for compiled build
// artifacts. It is verdict-neutral: it stores only the output of a successful
// build step (e.g. a.out, *.class, a vvp image), never run results. A cache hit
// lets the runner skip the compile and replay the original build output, while
// the run phase is still executed live in a fresh jail for every test.
//
// The cache key folds in the toolchain version, so a compiler bump never serves
// a stale binary. All disk/IO errors degrade gracefully to a miss — the cache
// never fails a run.
package artifactcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultTTL is the age past which a startup Sweep removes a cache entry.
const DefaultTTL = 24 * time.Hour

const metaFilename = "meta.json"

// Meta is the replayable build output stored alongside the cached artifacts so a
// hit can reconstruct a faithful build response block.
type Meta struct {
	BuildStdout     string `json:"build_stdout"`
	BuildStderr     string `json:"build_stderr"`
	BuildDurationMs int64  `json:"build_duration_ms"`
}

// Key derives the content-addressed cache key. It folds the language id, the
// toolchain version, the source, the build flags and the artifact filename into
// a single hex digest. NUL separators keep field boundaries from colliding
// (e.g. flags vs. filename). Callers must skip the cache entirely when the
// toolchain version is unknown (empty), never compute a key without it.
func Key(langID, toolchainVersion, source string, buildFlags []string, artifactFilename string) string {
	srcHash := sha256.Sum256([]byte(source))
	h := sha256.New()
	io.WriteString(h, langID)
	h.Write([]byte{0})
	io.WriteString(h, toolchainVersion)
	h.Write([]byte{0})
	io.WriteString(h, hex.EncodeToString(srcHash[:]))
	h.Write([]byte{0})
	io.WriteString(h, strings.Join(buildFlags, "\x00"))
	h.Write([]byte{0})
	io.WriteString(h, artifactFilename)
	return hex.EncodeToString(h.Sum(nil))
}

// Cache is a directory of content-addressed artifact entries plus an in-process
// keyed mutex for single-flight builds. The zero value is not usable; call New.
type Cache struct {
	dir        string
	maxEntries int

	mu    sync.Mutex // guards locks
	locks map[string]*keyLock

	// dirMu serializes cache-wide filesystem mutation (entry commit + eviction)
	// so concurrent Puts on *different* keys cannot race each other's
	// rename/RemoveAll. Held only briefly and off the run-hot path.
	dirMu sync.Mutex
}

// keyLock is a per-key single-flight gate. ch is a capacity-1 semaphore (rather
// than a sync.Mutex) so acquisition can be abandoned on context cancellation.
type keyLock struct {
	ch  chan struct{}
	ref int
}

// New creates a Cache rooted at dir, creating the directory if needed.
func New(dir string, maxEntries int) (*Cache, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("artifactcache dir: %w", err)
	}
	return &Cache{dir: dir, maxEntries: maxEntries, locks: make(map[string]*keyLock)}, nil
}

// Lock acquires the single-flight lock for key and returns its release function.
// The runner holds it across the Get -> build -> Put sequence so that identical
// concurrent submissions compile exactly once. Acquisition honors ctx: if the
// caller is cancelled while waiting for an in-flight build of the same key, Lock
// returns ctx.Err() and a nil unlock instead of blocking until that build ends.
func (c *Cache) Lock(ctx context.Context, key string) (unlock func(), err error) {
	c.mu.Lock()
	kl := c.locks[key]
	if kl == nil {
		kl = &keyLock{ch: make(chan struct{}, 1)}
		c.locks[key] = kl
	}
	kl.ref++
	c.mu.Unlock()

	release := func() {
		c.mu.Lock()
		kl.ref--
		if kl.ref == 0 {
			delete(c.locks, key)
		}
		c.mu.Unlock()
	}

	select {
	case kl.ch <- struct{}{}: // acquired the single-flight token
		return func() {
			<-kl.ch
			release()
		}, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// Get copies a cached entry's artifacts into destDir and returns its Meta. A
// missing entry or any IO error returns (Meta{}, false) so the caller treats it
// as a miss and builds normally. On a hit the entry's mtime is bumped so
// eviction approximates LRU.
func (c *Cache) Get(key, destDir string) (Meta, bool) {
	entry := filepath.Join(c.dir, key)
	info, err := os.Stat(entry)
	if err != nil || !info.IsDir() {
		return Meta{}, false
	}

	metaBytes, err := os.ReadFile(filepath.Join(entry, metaFilename))
	if err != nil {
		return Meta{}, false
	}
	var meta Meta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return Meta{}, false
	}

	if err := copyTree(entry, destDir, metaFilename); err != nil {
		return Meta{}, false
	}

	now := time.Now()
	_ = os.Chtimes(entry, now, now)
	return meta, true
}

// Put snapshots every file under srcDir (recursively, preserving mode bits)
// except excludeFile — the source — into the cache under key, along with meta.
// The entry is staged in a temp dir and atomically renamed into place. Errors
// are returned but are non-fatal to the caller (best-effort caching).
func (c *Cache) Put(key, srcDir, excludeFile string, meta Meta) error {
	staging, err := os.MkdirTemp(c.dir, ".staging-*")
	if err != nil {
		return fmt.Errorf("staging dir: %w", err)
	}
	// Clean up staging on any failure; on success it's been renamed away.
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(staging)
		}
	}()

	if err := copyTree(srcDir, staging, excludeFile); err != nil {
		return fmt.Errorf("snapshot artifacts: %w", err)
	}

	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(staging, metaFilename), metaBytes, 0644); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	// Commit + eviction are serialized cache-wide so two Puts on different keys
	// cannot race each other's rename/RemoveAll (or evict a just-committed entry).
	c.dirMu.Lock()
	defer c.dirMu.Unlock()

	entry := filepath.Join(c.dir, key)
	os.RemoveAll(entry) // replace any partial/previous entry
	if err := os.Rename(staging, entry); err != nil {
		return fmt.Errorf("commit entry: %w", err)
	}
	committed = true

	c.evictLocked()
	return nil
}

// evictLocked trims the cache to maxEntries, removing the oldest entries by
// mtime. Best-effort: IO errors are ignored. Caller must hold c.dirMu.
func (c *Cache) evictLocked() {
	if c.maxEntries <= 0 {
		return
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type aged struct {
		name string
		mod  time.Time
	}
	dirs := make([]aged, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".staging-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, aged{e.Name(), info.ModTime()})
	}
	if len(dirs) <= c.maxEntries {
		return
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mod.Before(dirs[j].mod) })
	for _, d := range dirs[:len(dirs)-c.maxEntries] {
		os.RemoveAll(filepath.Join(c.dir, d.name))
	}
}

// Sweep removes cache entries older than maxAge. Call once at startup to reclaim
// stale entries. Mirrors jail.SweepOrphans.
func Sweep(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// copyTree recursively copies regular files and subdirectories from src to dst,
// preserving mode bits, skipping any top-level entry named exclude.
func copyTree(src, dst, exclude string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == exclude {
			continue
		}
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyTree(srcPath, dstPath, ""); err != nil {
				return err
			}
			continue
		}
		if !e.Type().IsRegular() {
			continue // skip symlinks, sockets, devices
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

package jail

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

var counter int64

// Create makes a unique, isolated working directory under base.
// The caller must defer Cleanup on the returned path.
func Create(base string) (string, error) {
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", fmt.Errorf("jail base: %w", err)
	}
	suffix, err := randomHex(8)
	if err != nil {
		return "", fmt.Errorf("jail rand: %w", err)
	}
	n := atomic.AddInt64(&counter, 1)
	name := fmt.Sprintf("%d_%d_%s", n, os.Getpid(), suffix)
	path := filepath.Join(base, name)
	if err := os.Mkdir(path, 0700); err != nil {
		return "", fmt.Errorf("jail create: %w", err)
	}
	return path, nil
}

// Cleanup removes the jail directory and all contents.
func Cleanup(path string) {
	_ = os.RemoveAll(path)
}

// SweepOrphans removes subdirectories of base older than maxAge.
// Call once at server startup to reclaim dirs from a previous crashed run.
func SweepOrphans(base string, maxAge time.Duration) {
	entries, err := os.ReadDir(base)
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
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCgroupMemoryLimitBytes(t *testing.T) {
	tmpDir := t.TempDir()
	
	v2File := filepath.Join(tmpDir, "memory.max")
	v1File := filepath.Join(tmpDir, "memory.limit_in_bytes")
	
	// Save the original paths and restore on cleanup
	origPaths := cgroupMemoryPaths
	t.Cleanup(func() {
		cgroupMemoryPaths = origPaths
	})
	
	cgroupMemoryPaths = []string{v2File, v1File}
	
	// Test case 1: files absent
	if _, ok := cgroupMemoryLimitBytes(); ok {
		t.Error("expected ok=false when files are absent")
	}
	
	// Test case 2: v2 exists but has "max" (unlimited), v1 absent
	if err := os.WriteFile(v2File, []byte("max\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := cgroupMemoryLimitBytes(); ok {
		t.Error("expected ok=false when v2 holds max")
	}
	
	// Test case 3: v2 has "max", v1 has unlimited sentinel
	if err := os.WriteFile(v1File, []byte("9223372036854771712\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := cgroupMemoryLimitBytes(); ok {
		t.Error("expected ok=false when v1 holds unlimited sentinel")
	}
	
	// Test case 4: v2 has "max", v1 has a valid limit (1 GB)
	if err := os.WriteFile(v1File, []byte("1073741824\n"), 0644); err != nil {
		t.Fatal(err)
	}
	val, ok := cgroupMemoryLimitBytes()
	if !ok || val != 1073741824 {
		t.Errorf("expected 1073741824, got %d (ok=%t)", val, ok)
	}
	
	// Test case 5: v2 has a valid limit (2 GB)
	if err := os.WriteFile(v2File, []byte("2147483648\n"), 0644); err != nil {
		t.Fatal(err)
	}
	val, ok = cgroupMemoryLimitBytes()
	if !ok || val != 2147483648 {
		t.Errorf("expected 2147483648, got %d (ok=%t)", val, ok)
	}
}

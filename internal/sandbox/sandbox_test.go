package sandbox

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thesouldev/goboxd/internal/config"
)

// contains reports whether args contains a "--flag value" pair in order.
func hasPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func has(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

func TestBuildNsjailArgs_Common(t *testing.T) {
	cfg := RunConfig{
		LanguageID: "py3",
		WorkDir:    "/jail/abc",
		Cmd:        "/usr/bin/python3",
		Args:       []string{"solution.py"},
		Limits:     config.Limits{WallTimeS: 5, MemoryKB: 262144, MaxProcesses: 64},
	}
	args := buildNsjailArgs(cfg, &cgroup{ok: false})

	if !hasPair(args, "--chroot", "/jail/abc") {
		t.Error("missing --chroot WorkDir")
	}
	if !hasPair(args, "--cwd", "/") {
		t.Error("missing --cwd /")
	}
	if !hasPair(args, "--time_limit", "5") {
		t.Error("missing --time_limit 5")
	}
	if !hasPair(args, "--rlimit_nproc", "64") {
		t.Error("missing --rlimit_nproc 64")
	}
	// Command + args must come after the -- separator, in order.
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep == -1 {
		t.Fatal("missing -- separator")
	}
	if args[sep+1] != "/usr/bin/python3" || args[sep+2] != "solution.py" {
		t.Errorf("cmd/args after --: got %v", args[sep+1:])
	}
}

func TestBuildNsjailArgs_CgroupVsRlimit(t *testing.T) {
	cfg := RunConfig{
		LanguageID: "py3",
		WorkDir:    "/jail/abc",
		Cmd:        "/bin/true",
		Limits:     config.Limits{WallTimeS: 3, MemoryKB: 262144},
	}

	// cgroup available: use cgroup v2 memory.max, not rlimit_as.
	withCg := buildNsjailArgs(cfg, &cgroup{ok: true, path: "/sys/fs/cgroup/goboxd/abc"})
	if !has(withCg, "--use_cgroupv2") {
		t.Error("expected --use_cgroupv2 when cgroup ok")
	}
	if !hasPair(withCg, "--cgroupv2_mount", "/sys/fs/cgroup/goboxd/abc") {
		t.Error("expected --cgroupv2_mount with cgroup path")
	}
	if !hasPair(withCg, "--cgroup_mem_max", "268435456") { // 262144 KiB * 1024
		t.Error("expected --cgroup_mem_max in bytes")
	}
	if has(withCg, "--rlimit_as") {
		t.Error("should not set --rlimit_as when cgroup is available")
	}

	// cgroup unavailable: fall back to rlimit_as in MiB.
	noCg := buildNsjailArgs(cfg, &cgroup{ok: false})
	if !hasPair(noCg, "--rlimit_as", "256") { // 262144 KiB / 1024 = 256 MiB
		t.Error("expected --rlimit_as 256 fallback")
	}
	if has(noCg, "--use_cgroupv2") {
		t.Error("should not use cgroup when unavailable")
	}

	// cgroup unavailable, VM language: --rlimit_as must be omitted to let the VM start.
	cfgVM := cfg
	cfgVM.Cmd = "/usr/bin/node"
	noCgVM := buildNsjailArgs(cfgVM, &cgroup{ok: false})
	if has(noCgVM, "--rlimit_as") {
		t.Error("expected --rlimit_as to be omitted for VM runtimes")
	}
}

func TestBuildNsjailArgs_Seccomp(t *testing.T) {
	base := RunConfig{
		LanguageID: "py3",
		WorkDir:    "/jail/abc",
		Cmd:        "/bin/true",
		Limits:     config.Limits{WallTimeS: 3},
	}
	policy := "POLICY p { ALLOW { read, write } } USE p DEFAULT KILL"

	t.Run("off emits no seccomp flags", func(t *testing.T) {
		cfg := base
		cfg.SeccompMode = "off"
		cfg.SeccompPolicy = policy
		args := buildNsjailArgs(cfg, &cgroup{ok: false})
		if has(args, "--seccomp_string") || has(args, "--seccomp_log") {
			t.Errorf("off must not emit seccomp flags: %v", args)
		}
	})

	t.Run("mode set but empty policy emits nothing", func(t *testing.T) {
		cfg := base
		cfg.SeccompMode = "enforce"
		args := buildNsjailArgs(cfg, &cgroup{ok: false})
		if has(args, "--seccomp_string") {
			t.Errorf("empty policy must not emit --seccomp_string: %v", args)
		}
	})

	t.Run("enforce emits policy without log", func(t *testing.T) {
		cfg := base
		cfg.SeccompMode = "enforce"
		cfg.SeccompPolicy = policy
		args := buildNsjailArgs(cfg, &cgroup{ok: false})
		if !hasPair(args, "--seccomp_string", policy) {
			t.Errorf("missing --seccomp_string policy: %v", args)
		}
		if has(args, "--seccomp_log") {
			t.Error("enforce must not add --seccomp_log")
		}
	})

	t.Run("audit emits policy and log", func(t *testing.T) {
		cfg := base
		cfg.SeccompMode = "audit"
		cfg.SeccompPolicy = policy
		args := buildNsjailArgs(cfg, &cgroup{ok: false})
		if !hasPair(args, "--seccomp_string", policy) {
			t.Errorf("missing --seccomp_string policy: %v", args)
		}
		if !has(args, "--seccomp_log") {
			t.Error("audit must add --seccomp_log")
		}
	})

	t.Run("seccomp flags precede the -- separator", func(t *testing.T) {
		cfg := base
		cfg.SeccompMode = "enforce"
		cfg.SeccompPolicy = policy
		args := buildNsjailArgs(cfg, &cgroup{ok: false})
		sep, ss := -1, -1
		for i, a := range args {
			if a == "--" && sep == -1 {
				sep = i
			}
			if a == "--seccomp_string" {
				ss = i
			}
		}
		if ss == -1 || sep == -1 || ss > sep {
			t.Errorf("--seccomp_string (%d) must precede -- (%d)", ss, sep)
		}
	})
}

func TestLimitedWriter(t *testing.T) {
	t.Run("under cap writes all, no truncation", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &limitedWriter{w: &buf, remaining: 10}
		n, err := lw.Write([]byte("hello"))
		if err != nil || n != 5 {
			t.Fatalf("Write = %d, %v", n, err)
		}
		if lw.truncated {
			t.Error("should not be truncated")
		}
		if buf.String() != "hello" {
			t.Errorf("buf = %q", buf.String())
		}
	})

	t.Run("over cap truncates underlying but reports full len", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &limitedWriter{w: &buf, remaining: 3}
		n, err := lw.Write([]byte("abcdef"))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		// Reports full input length so the child process doesn't see a short write.
		if n != 6 {
			t.Errorf("n = %d, want 6", n)
		}
		if !lw.truncated {
			t.Error("should be truncated")
		}
		if buf.String() != "abc" {
			t.Errorf("buf = %q, want abc", buf.String())
		}
	})

	t.Run("write after exhausted drops everything", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &limitedWriter{w: &buf, remaining: 0}
		n, err := lw.Write([]byte("xyz"))
		if err != nil || n != 3 {
			t.Fatalf("Write = %d, %v", n, err)
		}
		if !lw.truncated {
			t.Error("should be truncated")
		}
		if buf.Len() != 0 {
			t.Errorf("buf should be empty, got %q", buf.String())
		}
	})
}

func TestFilterNsjailLog(t *testing.T) {
	in := "real output line\n[I][1234] nsjail info\n[W][5] warning\nmore output"
	got := filterNsjailLog(in)
	if strings.Contains(got, "nsjail info") || strings.Contains(got, "warning") {
		t.Errorf("nsjail log lines not stripped: %q", got)
	}
	if !strings.Contains(got, "real output line") || !strings.Contains(got, "more output") {
		t.Errorf("real output dropped: %q", got)
	}
}

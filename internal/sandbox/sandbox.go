package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thesouldev/goboxd/internal/config"
)

const truncationMarker = "\n...[truncated]"

// Probe verifies that the nsjail binary is present and executable. nsjail has no
// --version flag and embeds no version string, so the only thing we can check at
// runtime is that it runs and prints its usage banner. `nsjail --help` exits 0
// and writes a "Usage:" line; anything else (missing binary, non-zero exit, no
// usage output) is reported as an error.
//
// The version itself is not discoverable from the binary; it is pinned by the
// build (the nsjail submodule is checked out at tag 3.4) and surfaced separately
// via NsjailVersion.
func Probe(ctx context.Context, nsjailPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nsjailPath, "--help")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nsjail --help: %w", err)
	}
	if !strings.Contains(buf.String(), "Usage:") {
		return fmt.Errorf("nsjail --help: unexpected output (binary may be wrong)")
	}
	return nil
}

// NsjailVersion returns the pinned nsjail version. nsjail cannot report its own
// version, so the build injects it via the GOBOXD_NSJAIL_VERSION env var (set in
// the Dockerfile from the submodule tag). Falls back to "unknown" outside the
// image.
func NsjailVersion() string {
	if v := strings.TrimSpace(os.Getenv("GOBOXD_NSJAIL_VERSION")); v != "" {
		return v
	}
	return "unknown"
}

// RunConfig holds everything needed to execute one command in nsjail.
type RunConfig struct {
	NsjailPath string
	WorkDir    string
	Cmd        string
	Args       []string
	Stdin      string
	Limits     config.Limits
	OutputCap  int
	// SeccompMode is "off", "audit", or "enforce"; SeccompPolicy is the kafel
	// program for this language. A filter is applied only when mode != "off" and
	// the policy is non-empty. In "audit" mode --seccomp_log is added so denials
	// are logged rather than (only) killed.
	SeccompMode   string
	SeccompPolicy string
}

// Result holds the outcome of a single sandboxed execution.
type Result struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	Truncated bool
	WallMs    int64
	MemPeakKB int64
	OOMKilled bool
	TimedOut  bool
}

// Run executes cmd inside nsjail and returns the result.
func Run(ctx context.Context, cfg RunConfig) (*Result, error) {
	if cfg.OutputCap == 0 {
		cfg.OutputCap = 65536
	}

	// Dedicated cgroup for this run, named after the (unique) workdir, giving
	// per-request memory accounting. Best-effort: if unavailable, cg.ok is false
	// and we fall back to rlimit-only enforcement.
	cg := setupCgroup(filepath.Base(cfg.WorkDir), cfg.Limits.MemoryKB, cfg.Limits.MaxProcesses, cfg.Limits.CPUMaxPercent)
	defer cg.cleanup()

	argv := buildNsjailArgs(cfg, cg)
	cmd := exec.CommandContext(ctx, cfg.NsjailPath, argv...)

	if cfg.Stdin != "" {
		cmd.Stdin = strings.NewReader(cfg.Stdin)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutLimited := &limitedWriter{w: &stdoutBuf, remaining: int64(cfg.OutputCap)}
	stderrLimited := &limitedWriter{w: &stderrBuf, remaining: int64(cfg.OutputCap)}
	cmd.Stdout = stdoutLimited
	cmd.Stderr = stderrLimited

	start := time.Now()
	err := cmd.Run()
	wallMs := time.Since(start).Milliseconds()

	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()
	truncated := stdoutLimited.truncated || stderrLimited.truncated
	if stdoutLimited.truncated {
		stdout += truncationMarker
	}

	res := &Result{
		WallMs:    wallMs,
		Stdout:    stdout,
		Stderr:    stderr,
		Truncated: truncated,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("exec nsjail: %w", err)
		}
	}

	// Per-request memory accounting from this run's dedicated cgroup.
	res.OOMKilled = cg.oomKilled()
	res.MemPeakKB = cg.memPeakKB()

	// Distinguish time_exceeded from memory_exceeded and runtime_error.
	// A time-limit kill and an OOM kill are both delivered as SIGKILL (exit 137),
	// so OOM must be checked first (above). If it wasn't an OOM, treat a SIGKILL
	// exit or a process that ran up to the wall-time limit as a timeout.
	if !res.OOMKilled && res.ExitCode != 0 {
		killedBySignal := res.ExitCode == 137 // 128 + SIGKILL(9)
		ranToLimit := cfg.Limits.WallTimeS > 0 &&
			wallMs >= int64(cfg.Limits.WallTimeS)*1000-100
		if killedBySignal || ranToLimit {
			res.TimedOut = true
		}
	}

	// Stderr from nsjail itself contains log lines; strip them.
	res.Stderr = filterNsjailLog(stderr)

	return res, nil
}

// systemBindMounts are read-only bind mounts providing toolchains to the sandbox.
// Each is included only if it exists on the host: nsjail aborts the entire jail
// if a bind-mount source is missing, and some paths are architecture-dependent
// (e.g. /lib64 exists on amd64 but not on arm64). /opt is included because
// PowerShell installs its runtime under /opt/microsoft/powershell; without it
// pwsh is unreachable inside the jail.
var systemBindMounts = []string{"/bin", "/usr", "/lib", "/lib64", "/opt", "/dev", "/etc", "/tmp", "/var"}

func buildNsjailArgs(cfg RunConfig, cg *cgroup) []string {
	memMiB := int64(cfg.Limits.MemoryKB) / 1024
	if memMiB < 1 {
		memMiB = 1
	}
	args := []string{
		"--log", "/dev/null",
		"--mode", "o",
		"--chroot", cfg.WorkDir,
		"--cwd", "/",
		"--time_limit", strconv.Itoa(cfg.Limits.WallTimeS),
		"--rlimit_fsize", "100", // 100 MiB output cap
		"--rlimit_nofile", "1000",
		"--env", "TMP=/",
		"--env", "TMPDIR=/",
		// PATH is required: the C/C++ compiler driver locates the linker (ld) via
		// PATH, and Node looks up helpers via PATH. Without it, g++ fails with
		// "collect2: cannot find 'ld'" and node hangs.
		"--env", "PATH=/usr/local/bin:/usr/bin:/bin",
		"--rw",
	}
	if cfg.Limits.MaxProcesses > 0 {
		args = append(args, "--rlimit_nproc", strconv.Itoa(cfg.Limits.MaxProcesses))
	}
	// Put the sandboxed process in this run's dedicated cgroup v2 so memory is
	// capped and accounted per request. nsjail creates a child cgroup under the
	// mount we hand it; the cap and OOM events are then visible at cg.path.
	if cg.ok {
		// cgroup v2 memory.max enforces a resident-memory (RSS) cap, which is the
		// spec-correct memory limit and what produces a clean OOM kill.
		args = append(args, "--use_cgroupv2", "--cgroupv2_mount", cg.path)
		if cfg.Limits.MemoryKB > 0 {
			args = append(args, "--cgroup_mem_max",
				strconv.FormatInt(int64(cfg.Limits.MemoryKB)*1024, 10)) // bytes
		}
	} else {
		// Fallback only: --rlimit_as caps virtual address space, not RSS. VM-based
		// runtimes (Node/V8, the JVM) reserve far more virtual memory than they ever
		// make resident, so an rlimit_as set to the RSS budget makes them fail to
		// start. Use it only when no cgroup is available to do the accounting.
		args = append(args, "--rlimit_as", strconv.FormatInt(memMiB, 10)) // MiB
	}
	for _, dir := range systemBindMounts {
		// Skip missing sources; nsjail aborts the jail if a bind source doesn't exist.
		if _, err := os.Stat(dir); err == nil {
			args = append(args, "-B", dir)
		}
	}
	// seccomp-bpf filter (kafel). Applied only when enabled and a policy exists,
	// so the default "off" path is byte-for-byte unchanged. In "audit" mode
	// --seccomp_log records denied syscalls (pair with a permissive policy
	// default to observe without killing).
	if cfg.SeccompMode != "off" && cfg.SeccompMode != "" && cfg.SeccompPolicy != "" {
		args = append(args, "--seccomp_string", cfg.SeccompPolicy)
		if cfg.SeccompMode == "audit" {
			args = append(args, "--seccomp_log")
		}
	}
	args = append(args, "--")
	args = append(args, cfg.Cmd)
	args = append(args, cfg.Args...)
	return args
}

func filterNsjailLog(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "[I][") || strings.Contains(line, "[W][") ||
			strings.Contains(line, "[E][") || strings.Contains(line, "[F][") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

type limitedWriter struct {
	w         io.Writer
	remaining int64
	truncated bool
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	// Always report the full input length back to the caller. The child's stdout
	// is copied here by os/exec's io.Copy; returning n < len(p) with a nil error
	// makes io.Copy fail with io.ErrShortWrite, which would surface as a sandbox
	// exec error (HTTP 500) instead of a cleanly truncated 200. We swallow the
	// overflow and record truncation instead.
	if lw.remaining <= 0 {
		lw.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > lw.remaining {
		lw.truncated = true
		if _, err := lw.w.Write(p[:lw.remaining]); err != nil {
			return 0, err
		}
		lw.remaining = 0
		return len(p), nil
	}
	n, err := lw.w.Write(p)
	lw.remaining -= int64(n)
	return n, err
}

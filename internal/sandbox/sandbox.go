package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ambitious-scrap/goboxd/internal/config"
)

const truncationMarker = "\n...[truncated]"

// RunConfig holds everything needed to execute one command in nsjail.
type RunConfig struct {
	NsjailPath string
	WorkDir    string
	Cmd        string
	Args       []string
	Stdin      string
	Limits     config.Limits
	OutputCap  int
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

	argv := buildNsjailArgs(cfg)
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

	// Check for OOM via cgroup v2 memory.events if available.
	res.OOMKilled = checkOOMKill(cfg.WorkDir)
	res.MemPeakKB = readMemPeak(cfg.WorkDir)

	// nsjail exits with code 1 and logs "prctl(PR_SET_CHILD_SUBREAPER" or similar
	// when timing out; we detect via wall time vs limit.
	if wallMs >= int64(cfg.Limits.WallTimeS)*1000-100 && res.ExitCode != 0 {
		res.TimedOut = true
	}

	// Stderr from nsjail itself contains log lines; strip them.
	res.Stderr = filterNsjailLog(stderr)

	return res, nil
}

func buildNsjailArgs(cfg RunConfig) []string {
	memBytes := int64(cfg.Limits.MemoryKB) * 1024
	args := []string{
		"--log", "/dev/null",
		"--mode", "o",
		"--time_limit", strconv.Itoa(cfg.Limits.WallTimeS),
		"--max_cpus", "1",
		"--rlimit_as", strconv.FormatInt(memBytes/1024/1024, 10), // MB
		"--rlimit_fsize", "32", // 32 MB file size limit
		"--bindmount", cfg.WorkDir + ":/workdir",
		"--cwd", "/workdir",
	}
	if cfg.Limits.MaxProcesses > 0 {
		args = append(args, "--max_pids", strconv.Itoa(cfg.Limits.MaxProcesses))
	}
	args = append(args, "--")
	args = append(args, cfg.Cmd)
	args = append(args, cfg.Args...)
	return args
}

// checkOOMKill reads cgroup v2 memory.events from a best-effort path.
func checkOOMKill(workDir string) bool {
	data, err := os.ReadFile("/sys/fs/cgroup/memory.events")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "oom_kill ") {
			parts := strings.Fields(line)
			if len(parts) == 2 {
				n, _ := strconv.ParseInt(parts[1], 10, 64)
				return n > 0
			}
		}
	}
	return false
}

func readMemPeak(workDir string) int64 {
	data, err := os.ReadFile("/sys/fs/cgroup/memory.peak")
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n / 1024
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
	if lw.remaining <= 0 {
		lw.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > lw.remaining {
		lw.truncated = true
		p = p[:lw.remaining]
	}
	n, err := lw.w.Write(p)
	lw.remaining -= int64(n)
	return n, err
}

// Sandbox containment probe runner.
//
// Usage:
//
//	go run ./tests/sandbox [--url http://localhost:8080] [--out results.md]
//
// Requires the goboxd service to be running (make run in another terminal).
// Runs all probes sequentially and prints a Markdown report. Each probe submits
// a deliberately dangerous program and checks whether the sandbox contained it.
// Exit code: 0 = all contained, 1 = unexpected status, 2 = security breach.
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed programs
var programFS embed.FS

func prog(name string) string {
	b, err := programFS.ReadFile("programs/" + name)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "missing program %q: %v\n", name, err)
		os.Exit(1)
	}
	return string(b)
}

// ── API types ─────────────────────────────────────────────────────────────────

type limDef struct {
	WallTimeS int `json:"wall_time_s,omitempty"`
	MemoryKB  int `json:"memory_kb,omitempty"`
}

type runPhase struct {
	Limits limDef `json:"limits,omitempty"`
}

type runReq struct {
	Language string    `json:"language"`
	Source   string    `json:"source"`
	Run      *runPhase `json:"run,omitempty"`
	Tests    []tc      `json:"tests"`
}

type tc struct {
	Stdin          string `json:"stdin"`
	ExpectedStdout string `json:"expected_stdout"`
}

type runResp struct {
	Status string `json:"status"`
}

// ── Probe definitions ─────────────────────────────────────────────────────────

type probe struct {
	Name     string   // display name
	Category string   // sandbox property being tested
	Lang     string   // language id
	File     string   // filename inside programs/
	WallTime int      // run.limits.wall_time_s (0 = language default)
	MemoryKB int      // run.limits.memory_kb (0 = language default)
	Want     []string // any of these statuses = sandbox contained it
	Breach   []string // these statuses = security failure
}

var probes = []probe{
	// ── CPU limits / Timeout enforcement ──────────────────────────────────────
	{
		Name: "CPU Stress", Category: "CPU limits / Timeout",
		Lang: "py3", File: "cpu_stress.py", WallTime: 1,
		Want: []string{"time_exceeded"},
	},
	// ── Memory limits ─────────────────────────────────────────────────────────
	{
		Name: "Memory Allocation (512 MiB)", Category: "Memory limits",
		Lang: "py3", File: "mem_alloc.py", WallTime: 5, MemoryKB: 32768,
		Want: []string{"memory_exceeded", "runtime_error"},
	},
	// ── Disk quotas ───────────────────────────────────────────────────────────
	{
		Name: "Large File Write", Category: "Disk quotas (rlimit_fsize 100 MB)",
		Lang: "py3", File: "file_write_large.py", WallTime: 10,
		Want: []string{"runtime_error", "time_exceeded"},
	},
	{
		Name: "Temp File Flood", Category: "Disk quotas",
		Lang: "py3", File: "tempfile_flood.py", WallTime: 2,
		Want: []string{"time_exceeded", "runtime_error"},
	},
	// ── Process limits ────────────────────────────────────────────────────────
	{
		Name: "Fork Bomb", Category: "Process limits (cgroup_pids_max)",
		Lang: "py3", File: "fork_bomb.py", WallTime: 3,
		Want: []string{"runtime_error", "time_exceeded"},
	},
	{
		Name: "Process Spawn", Category: "Process limits",
		Lang: "py3", File: "proc_spawn.py", WallTime: 3,
		Want: []string{"runtime_error", "time_exceeded"},
	},
	{
		Name: "Thread Explosion", Category: "Process limits (threads = processes)",
		Lang: "py3", File: "thread_flood.py", WallTime: 3,
		Want: []string{"runtime_error", "time_exceeded"},
	},
	{
		Name: "C Fork Bomb", Category: "Process limits (native binary)",
		Lang: "c", File: "fork_bomb.c", WallTime: 3,
		Want: []string{"runtime_error", "time_exceeded"},
	},
	// ── Deep recursion / stack ────────────────────────────────────────────────
	{
		Name: "Deep Recursion", Category: "Stack limits",
		Lang: "py3", File: "deep_recursion.py", WallTime: 5,
		Want: []string{"runtime_error"},
	},
	// ── Output cap ────────────────────────────────────────────────────────────
	{
		Name: "Output Bomb", Category: "Output cap (io.LimitReader)",
		Lang: "py3", File: "output_bomb.py", WallTime: 1,
		Want: []string{"time_exceeded"},
	},
	// ── Network isolation ─────────────────────────────────────────────────────
	{
		Name: "Network Connect", Category: "Network isolation (new netns)",
		Lang: "py3", File: "network_connect.py", WallTime: 3,
		Want:   []string{"runtime_error"},
		Breach: []string{"accepted"},
	},
	// ── Filesystem isolation / permissions ────────────────────────────────────
	{
		Name: "Read /etc/passwd", Category: "Filesystem isolation (chroot)",
		Lang: "py3", File: "fs_read_passwd.py", WallTime: 5,
		Want:   []string{"runtime_error"},
		Breach: []string{"accepted", "wrong_output"},
	},
	{
		Name: "Write /etc/hostname", Category: "Filesystem permissions",
		Lang: "py3", File: "fs_write_etc.py", WallTime: 5,
		Want:   []string{"runtime_error"},
		Breach: []string{"accepted"},
	},
	// ── Container escape resistance ───────────────────────────────────────────
	{
		Name: "Chroot Escape (os.chroot)", Category: "Container escape (no CAP_SYS_CHROOT)",
		Lang: "py3", File: "escape_chroot.py", WallTime: 5,
		Want:   []string{"runtime_error"},
		Breach: []string{"accepted"},
	},
	{
		Name: "Ptrace Syscall", Category: "API restrictions (seccomp KILL_PROCESS)",
		Lang: "py3", File: "escape_ptrace.py", WallTime: 5,
		Want:   []string{"runtime_error"},
		Breach: []string{"accepted"},
	},
}

// ── HTTP helper ───────────────────────────────────────────────────────────────

func postRun(baseURL string, req runReq) (string, error) {
	body, _ := json.Marshal(req)
	resp, err := http.Post(baseURL+"/run", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var r runResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	return r.Status, nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	baseURL := flag.String("url", "http://localhost:8080", "goboxd service base URL")
	outFile := flag.String("out", "", "write Markdown report to this file (default: stdout)")
	flag.Parse()

	// Health check.
	resp, err := http.Get(*baseURL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		_, _ = fmt.Fprintf(os.Stderr, "service unreachable at %s — run 'make run' first\n", *baseURL)
		os.Exit(1)
	}

	var w io.Writer = os.Stdout
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "create %s: %v\n", *outFile, err)
			os.Exit(1)
		}
		defer func() { _ = f.Close() }()
		w = f
	}

	_, _ = fmt.Fprintf(w, "# Sandbox Containment Report\n\n")
	_, _ = fmt.Fprintf(w, "**Date:** %s  \n", time.Now().Format("2006-01-02 15:04:05"))
	_, _ = fmt.Fprintf(w, "**Service:** %s  \n\n", *baseURL)
	_, _ = fmt.Fprintf(w, "| # | Test | Category | Lang | Expected | Actual | Result |\n")
	_, _ = fmt.Fprintf(w, "|---|------|----------|------|----------|--------|--------|\n")

	pass, fail, breach := 0, 0, 0

	for i, p := range probes {
		req := runReq{
			Language: p.Lang,
			Source:   prog(p.File),
			Tests:    []tc{{Stdin: "", ExpectedStdout: ""}},
		}
		if p.WallTime > 0 || p.MemoryKB > 0 {
			req.Run = &runPhase{Limits: limDef{WallTimeS: p.WallTime, MemoryKB: p.MemoryKB}}
		}

		status, err := postRun(*baseURL, req)

		var result string
		switch {
		case err != nil:
			status = "ERROR"
			result = "FAIL"
			fail++
		case contains(p.Breach, status):
			result = "**BREACH**"
			breach++
		case contains(p.Want, status):
			result = "PASS"
			pass++
		default:
			result = "FAIL"
			fail++
		}

		want := strings.Join(p.Want, " \\| ")
		_, _ = fmt.Fprintf(w, "| %d | %s | %s | %s | %s | %s | %s |\n",
			i+1, p.Name, p.Category, p.Lang, want, status, result)
	}

	total := len(probes)
	_, _ = fmt.Fprintf(w, "\n## Summary\n\n")
	_, _ = fmt.Fprintf(w, "**%d / %d contained** — PASS: %d, FAIL: %d, BREACH: %d\n\n",
		pass, total, pass, fail, breach)

	if breach > 0 {
		_, _ = fmt.Fprintf(w, "> **BREACH** means the program ran to completion when it should have been blocked.\n")
		_, _ = fmt.Fprintf(w, "> This indicates a sandbox misconfiguration, not a test error.\n")
	}

	// Also print summary to stderr when writing to a file.
	if *outFile != "" {
		_, _ = fmt.Fprintf(os.Stderr, "Results written to %s\n", *outFile)
		_, _ = fmt.Fprintf(os.Stderr, "PASS %d/%d | FAIL %d | BREACH %d\n", pass, total, fail, breach)
	}

	if breach > 0 {
		os.Exit(2)
	}
	if fail > 0 {
		os.Exit(1)
	}
}

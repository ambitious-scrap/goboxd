package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ambitious-scrap/goboxd/internal/config"
	"github.com/ambitious-scrap/goboxd/internal/jail"
	"github.com/ambitious-scrap/goboxd/internal/registry"
	"github.com/ambitious-scrap/goboxd/internal/sandbox"
	"github.com/ambitious-scrap/goboxd/internal/status"
)

// SandboxRunner is the interface the runner uses to execute commands.
// The real implementation delegates to sandbox.Run; tests inject a fake.
type SandboxRunner interface {
	Run(ctx context.Context, cfg sandbox.RunConfig) (*sandbox.Result, error)
}

type defaultSandbox struct{ nsjailPath string }

func (d *defaultSandbox) Run(ctx context.Context, cfg sandbox.RunConfig) (*sandbox.Result, error) {
	cfg.NsjailPath = d.nsjailPath
	return sandbox.Run(ctx, cfg)
}

// TestCase is a single input/expected-output pair.
type TestCase struct {
	Stdin    string
	Expected string
}

// TestResult is the outcome of one test case execution.
type TestResult struct {
	Status    string
	Stdout    string
	Stderr    string
	DurationMs int64
	MemPeakKB int64
	Truncated bool
}

// RunRequest bundles everything needed to execute a submission.
type RunRequest struct {
	Language   *config.Language
	Source     string
	Flags      []string
	Tests      []TestCase
	JailBase   string
	NsjailPath string
	OutputCap  int
}

// RunResult is the fully computed outcome of a submission.
type RunResult struct {
	BuildStatus   string
	BuildDurationMs int64
	BuildStdout   string
	BuildStderr   string
	Tests         []TestResult
	TopStatus     string
}

// Runner orchestrates build + test execution.
type Runner struct {
	sb       SandboxRunner
	jailBase string
	outputCap int
}

// New creates a Runner using the real nsjail sandbox.
func New(nsjailPath, jailBase string, outputCap int) *Runner {
	return &Runner{
		sb:        &defaultSandbox{nsjailPath: nsjailPath},
		jailBase:  jailBase,
		outputCap: outputCap,
	}
}

// NewWithSandbox creates a Runner with a custom sandbox (for testing).
func NewWithSandbox(sb SandboxRunner, jailBase string, outputCap int) *Runner {
	return &Runner{sb: sb, jailBase: jailBase, outputCap: outputCap}
}

// Execute runs a full submission: optional build, then each test case.
func (r *Runner) Execute(ctx context.Context, req RunRequest) (*RunResult, error) {
	jailPath, err := jail.Create(r.jailBase)
	if err != nil {
		return nil, fmt.Errorf("create jail: %w", err)
	}
	defer jail.Cleanup(jailPath)

	sourcePath := filepath.Join(jailPath, req.Language.SourceFilename)
	if err := os.WriteFile(sourcePath, []byte(req.Source), 0644); err != nil {
		return nil, fmt.Errorf("write source: %w", err)
	}

	res := &RunResult{}

	vars := placeholderVars(req, jailPath)

	// Build phase (compiled languages only).
	if req.Language.Build != nil {
		args := registry.Resolve(req.Language.Build.Args, vars)
		args = registry.ExpandFlags(args, req.Flags)

		start := time.Now()
		br, err := r.sb.Run(ctx, sandbox.RunConfig{
			WorkDir:   jailPath,
			Cmd:       req.Language.Build.Cmd,
			Args:      args,
			Limits:    req.Language.Build.Limits,
			OutputCap: r.outputCap,
		})
		res.BuildDurationMs = time.Since(start).Milliseconds()

		if err != nil {
			return nil, fmt.Errorf("build exec: %w", err)
		}
		res.BuildStdout = br.Stdout
		res.BuildStderr = br.Stderr

		if br.ExitCode != 0 {
			res.BuildStatus = status.BuildFailed
			res.Tests = make([]TestResult, len(req.Tests))
			for i := range res.Tests {
				res.Tests[i].Status = status.NotExecuted
			}
			res.TopStatus = status.BuildFailed
			return res, nil
		}
		res.BuildStatus = status.BuildOK
	}

	// Run phase — one sandbox call per test case.
	res.Tests = make([]TestResult, len(req.Tests))
	testStatuses := make([]string, len(req.Tests))

	runArgs := registry.Resolve(req.Language.Run.Args, vars)

	for i, tc := range req.Tests {
		start := time.Now()
		rr, err := r.sb.Run(ctx, sandbox.RunConfig{
			WorkDir:   jailPath,
			Cmd:       req.Language.Run.Cmd,
			Args:      runArgs,
			Stdin:     tc.Stdin,
			Limits:    req.Language.Run.Limits,
			OutputCap: r.outputCap,
		})
		durationMs := time.Since(start).Milliseconds()

		if err != nil {
			return nil, fmt.Errorf("run test %d: %w", i, err)
		}

		tr := TestResult{
			Stdout:     rr.Stdout,
			Stderr:     rr.Stderr,
			DurationMs: durationMs,
			MemPeakKB:  rr.MemPeakKB,
			Truncated:  rr.Truncated,
		}

		switch {
		case rr.OOMKilled:
			tr.Status = status.MemoryExceeded
		case rr.TimedOut:
			tr.Status = status.TimeExceeded
		case rr.ExitCode != 0:
			tr.Status = status.RuntimeError
		default:
			tr.Status = status.CompareOutput(rr.Stdout, tc.Expected)
		}

		res.Tests[i] = tr
		testStatuses[i] = tr.Status
	}

	res.TopStatus = status.TopLevel(res.BuildStatus, testStatuses)
	return res, nil
}

func placeholderVars(req RunRequest, jailPath string) map[string]string {
	vars := map[string]string{
		"source":  req.Language.SourceFilename,
		"workdir": jailPath,
	}
	if req.Language.Artifact != "" {
		vars["artifact"] = req.Language.Artifact
	}
	return vars
}

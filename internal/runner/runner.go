package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/thesouldev/goboxd/internal/artifactcache"
	"github.com/thesouldev/goboxd/internal/config"
	"github.com/thesouldev/goboxd/internal/jail"
	"github.com/thesouldev/goboxd/internal/registry"
	"github.com/thesouldev/goboxd/internal/sandbox"
	"github.com/thesouldev/goboxd/internal/status"
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
	Status     string
	Stdout     string
	Stderr     string
	DurationMs int64
	MemPeakKB  int64
	Truncated  bool
}

// RunRequest bundles everything needed to execute a submission.
type RunRequest struct {
	Language         *config.Language
	Source           string
	SourceFilename   string // overrides lang.SourceFilename when strategy=from_request
	ArtifactFilename string // overrides lang.Artifact when strategy=from_request
	BuildFlags       []string
	RunFlags         []string
	BuildLimits      config.Limits // effective build limits (defaults merged with request override)
	RunLimits        config.Limits // effective run limits (defaults merged with request override)
	Tests            []TestCase
	JailBase         string
	NsjailPath       string
	OutputCap        int
	SeccompMode      string // server-wide: off|audit|enforce
	// ToolchainVersion is the language's smoke-probe version string. It is part
	// of the artifact-cache key so a toolchain bump never serves a stale binary.
	// When empty, the cache is skipped entirely for this request.
	ToolchainVersion string
}

// RunResult is the fully computed outcome of a submission.
type RunResult struct {
	BuildStatus     string
	BuildDurationMs int64
	BuildStdout     string
	BuildStderr     string
	Tests           []TestResult
	TopStatus       string
	// CacheStatus reports the artifact-cache outcome for the build phase:
	// "hit", "miss", or "" when the cache was not consulted (interpreted
	// language, cache disabled, or unknown toolchain version).
	CacheStatus string
	// BuildWaitMs is the time the build step spent waiting for a build-lane
	// token, in milliseconds. 0 when there is no build lane or no build step.
	BuildWaitMs int64
}

// Runner orchestrates build + test execution.
type Runner struct {
	sb        SandboxRunner
	jailBase  string
	outputCap int
	// buildSem caps concurrent build steps (the C-1 build lane). nil disables
	// the lane (no extra cap beyond the handler's run-slot semaphore).
	buildSem chan struct{}
	// cache is the content-addressed artifact cache (C-2). nil disables caching.
	cache *artifactcache.Cache
}

// New creates a Runner using the real nsjail sandbox. maxBuildConcurrency caps
// concurrent build steps (<=0 disables the build lane); cache may be nil to
// disable artifact caching.
func New(nsjailPath, jailBase string, outputCap, maxBuildConcurrency int, cache *artifactcache.Cache) *Runner {
	return &Runner{
		sb:        &defaultSandbox{nsjailPath: nsjailPath},
		jailBase:  jailBase,
		outputCap: outputCap,
		buildSem:  newBuildSem(maxBuildConcurrency),
		cache:     cache,
	}
}

// NewWithSandbox creates a Runner with a custom sandbox (for testing).
func NewWithSandbox(sb SandboxRunner, jailBase string, outputCap, maxBuildConcurrency int, cache *artifactcache.Cache) *Runner {
	return &Runner{
		sb:        sb,
		jailBase:  jailBase,
		outputCap: outputCap,
		buildSem:  newBuildSem(maxBuildConcurrency),
		cache:     cache,
	}
}

func newBuildSem(n int) chan struct{} {
	if n <= 0 {
		return nil
	}
	return make(chan struct{}, n)
}

// Execute runs a full submission: optional build, then each test case.
func (r *Runner) Execute(ctx context.Context, req RunRequest) (*RunResult, error) {
	jailPath, err := jail.Create(r.jailBase)
	if err != nil {
		return nil, fmt.Errorf("create jail: %w", err)
	}
	defer jail.Cleanup(jailPath)

	srcFilename := req.Language.SourceFilename
	if req.SourceFilename != "" {
		srcFilename = req.SourceFilename
	}
	sourcePath := filepath.Join(jailPath, srcFilename)
	if err := os.WriteFile(sourcePath, []byte(req.Source), 0644); err != nil {
		return nil, fmt.Errorf("write source: %w", err)
	}

	res := &RunResult{}

	vars := placeholderVars(req, jailPath)

	// Effective limits. The API handler always supplies merged limits, but direct
	// callers (and integration tests) may leave them zero — fall back to the
	// language defaults so a step is never run with an empty limit set.
	buildLimits := req.BuildLimits
	if req.Language.Build != nil && buildLimits == (config.Limits{}) {
		buildLimits = req.Language.Build.Limits
	}
	runLimits := req.RunLimits
	if runLimits == (config.Limits{}) {
		runLimits = req.Language.Run.Limits
	}

	// Build phase (compiled languages only).
	if req.Language.Build != nil {
		buildFailed, err := r.buildPhase(ctx, req, jailPath, vars, buildLimits, srcFilename, res)
		if err != nil {
			return nil, err
		}
		if buildFailed {
			res.Tests = make([]TestResult, len(req.Tests))
			for i := range res.Tests {
				res.Tests[i].Status = status.NotExecuted
			}
			res.TopStatus = status.TopBuildFailed // top-level = "build_failed"
			return res, nil
		}
	}

	// Run phase — one sandbox call per test case.
	res.Tests = make([]TestResult, len(req.Tests))
	testStatuses := make([]string, len(req.Tests))

	runArgs := registry.Resolve(req.Language.Run.Args, vars)
	runArgs = registry.ExpandFlags(runArgs, req.RunFlags)
	runCmd := registry.ResolveOne(req.Language.Run.Cmd, vars)

	for i, tc := range req.Tests {
		start := time.Now()
		rr, err := r.sb.Run(ctx, sandbox.RunConfig{
			WorkDir:       jailPath,
			Cmd:           runCmd,
			Args:          runArgs,
			Stdin:         tc.Stdin,
			Limits:        runLimits,
			OutputCap:     r.outputCap,
			SeccompMode:   req.SeccompMode,
			SeccompPolicy: req.Language.SeccompPolicy,
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

// buildPhase resolves the compile step against the artifact cache: a hit
// replays the stored build output and skips compilation; a miss compiles live
// (gated by the build lane) and caches a successful result. It returns whether
// the build failed (so Execute can mark tests not_executed) and any internal
// error. The single-flight key lock is scoped to this method — released before
// the run phase — so identical concurrent submissions compile once but still run
// their tests in parallel. It mutates res in place with the build fields.
func (r *Runner) buildPhase(ctx context.Context, req RunRequest, jailPath string, vars map[string]string, buildLimits config.Limits, srcFilename string, res *RunResult) (buildFailed bool, err error) {
	artifactFilename := req.Language.Artifact
	if req.ArtifactFilename != "" {
		artifactFilename = req.ArtifactFilename
	}

	// Artifact cache: serve a previously compiled binary when source + flags +
	// toolchain match. The single-flight lock spans the get -> build -> put
	// sequence so identical concurrent submissions compile exactly once. An
	// unknown toolchain version skips the cache.
	cacheable := r.cache != nil && req.ToolchainVersion != ""
	var key string
	if cacheable {
		key = artifactcache.Key(req.Language.ID, req.ToolchainVersion, req.Source, req.BuildFlags, artifactFilename)
		unlock := r.cache.Lock(key)
		defer unlock()
		if meta, ok := r.cache.Get(key, jailPath); ok {
			res.CacheStatus = "hit"
			res.BuildStatus = status.BuildOK
			res.BuildStdout = meta.BuildStdout
			res.BuildStderr = meta.BuildStderr
			res.BuildDurationMs = meta.BuildDurationMs
			return false, nil
		}
		res.CacheStatus = "miss"
	}

	// Miss (or uncached language): compile live, gated by the build lane.
	br, waitMs, durMs, err := r.build(ctx, req, jailPath, vars, buildLimits)
	res.BuildWaitMs = waitMs
	res.BuildDurationMs = durMs
	if err != nil {
		return false, fmt.Errorf("build exec: %w", err)
	}
	res.BuildStdout = br.Stdout
	res.BuildStderr = br.Stderr

	if br.ExitCode != 0 {
		res.BuildStatus = status.BuildFailed // build.status = "failed"
		return true, nil
	}
	res.BuildStatus = status.BuildOK

	// Populate the cache from this successful build (best-effort).
	if cacheable {
		_ = r.cache.Put(key, jailPath, srcFilename, artifactcache.Meta{
			BuildStdout:     br.Stdout,
			BuildStderr:     br.Stderr,
			BuildDurationMs: durMs,
		})
	}
	return false, nil
}

// build executes the compile step, gated by the build-lane semaphore. The build
// token is acquired only for the duration of the compile and released on return
// (never held across the run phase), preserving the lock-ordering invariant:
// the run-slot semaphore (handler) is always acquired before the build token.
// It returns the sandbox result, the build-lane wait time, and the compile wall
// time, both in milliseconds.
func (r *Runner) build(ctx context.Context, req RunRequest, jailPath string, vars map[string]string, buildLimits config.Limits) (*sandbox.Result, int64, int64, error) {
	var waitMs int64
	if r.buildSem != nil {
		waitStart := time.Now()
		select {
		case r.buildSem <- struct{}{}:
			defer func() { <-r.buildSem }()
		case <-ctx.Done():
			return nil, 0, 0, ctx.Err()
		}
		waitMs = time.Since(waitStart).Milliseconds()
	}

	args := registry.Resolve(req.Language.Build.Args, vars)
	args = registry.ExpandFlags(args, req.BuildFlags)

	start := time.Now()
	br, err := r.sb.Run(ctx, sandbox.RunConfig{
		WorkDir:       jailPath,
		Cmd:           registry.ResolveOne(req.Language.Build.Cmd, vars),
		Args:          args,
		Limits:        buildLimits,
		OutputCap:     r.outputCap,
		SeccompMode:   req.SeccompMode,
		SeccompPolicy: req.Language.SeccompPolicy,
	})
	durMs := time.Since(start).Milliseconds()
	return br, waitMs, durMs, err
}

func placeholderVars(req RunRequest, jailPath string) map[string]string {
	src := req.Language.SourceFilename
	if req.SourceFilename != "" {
		src = req.SourceFilename
	}
	vars := map[string]string{
		"source":  src,
		"workdir": jailPath,
	}
	artifact := req.Language.Artifact
	if req.ArtifactFilename != "" {
		artifact = req.ArtifactFilename
	}
	if artifact != "" {
		vars["artifact"] = artifact
	}
	return vars
}

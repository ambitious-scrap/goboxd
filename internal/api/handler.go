package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/thesouldev/goboxd/internal/config"
	"github.com/thesouldev/goboxd/internal/flags"
	"github.com/thesouldev/goboxd/internal/limits"
	"github.com/thesouldev/goboxd/internal/obs"
	"github.com/thesouldev/goboxd/internal/registry"
	"github.com/thesouldev/goboxd/internal/runner"
	"github.com/thesouldev/goboxd/internal/status"
)

// Server holds all dependencies for the HTTP layer.
type Server struct {
	cfg       *config.Config
	reg       *registry.Registry
	runner    *runner.Runner
	sem       chan struct{}
	smokes    map[string]registry.SmokeResult
	buildInfo BuildInfo
	nsjail    NsjailInfo
	// cgroupsEnabled reports whether per-run cgroup v2 memory accounting is
	// active; false means the sandbox is on the rlimit_as fallback.
	cgroupsEnabled bool
}

// BuildInfo is injected at link time via -ldflags.
type BuildInfo struct {
	Version   string
	Commit    string
	GoVersion string
}

// NsjailInfo is the result of probing nsjail at startup.
type NsjailInfo struct {
	OK      bool
	Version string
	Error   string
}

func NewServer(cfg *config.Config, reg *registry.Registry, r *runner.Runner, smokes map[string]registry.SmokeResult, bi BuildInfo, nsjail NsjailInfo, cgroupsEnabled bool) *Server {
	return &Server{
		cfg:            cfg,
		reg:            reg,
		runner:         r,
		sem:            make(chan struct{}, cfg.Server.MaxConcurrency),
		smokes:         smokes,
		buildInfo:      bi,
		nsjail:         nsjail,
		cgroupsEnabled: cgroupsEnabled,
	}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", s.healthz)
	r.Get("/readyz", s.readyz)
	r.Get("/info", s.info)
	r.Post("/run", s.run)
	return r
}

// --- /run ---

type runRequest struct {
	Language         string          `json:"language"`
	Source           string          `json:"source"`
	SourceFilename   string          `json:"source_filename"`
	ArtifactFilename string          `json:"artifact_filename"`
	Build            *stepOptions    `json:"build"`
	Run              *stepOptions    `json:"run"`
	Tests            []testCaseInput `json:"tests"`
}

// stepOptions is the per-step (build/run) request block: an optional partial
// limits override plus an optional flags list filtered against the language's
// per-step allow-list.
type stepOptions struct {
	Limits *limitsInput `json:"limits"`
	Flags  []string     `json:"flags"`
}

// limitsInput mirrors config.Limits but uses pointers so an absent field can be
// distinguished from an explicit zero and falls back to the language default.
type limitsInput struct {
	WallTimeS    *int `json:"wall_time_s"`
	MemoryKB     *int `json:"memory_kb"`
	MaxProcesses *int `json:"max_processes"`
}

func (l *limitsInput) override() *limits.RequestOverride {
	if l == nil {
		return nil
	}
	return &limits.RequestOverride{
		WallTimeS:    l.WallTimeS,
		MemoryKB:     l.MemoryKB,
		MaxProcesses: l.MaxProcesses,
	}
}

type testCaseInput struct {
	Stdin          string `json:"stdin"`
	ExpectedOutput string `json:"expected_stdout"`
}

type runResponse struct {
	Status string     `json:"status"`
	Build  *buildInfo `json:"build,omitempty"`
	Tests  []testOut  `json:"tests"`
}

type buildInfo struct {
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
}

type testOut struct {
	Status     string `json:"status"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"duration_ms"`
	MemPeakKB  int64  `json:"memory_peak_kb"`
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	ctx := obs.WithRequestID(r.Context(), middleware.GetReqID(r.Context()))

	r.Body = http.MaxBytesReader(w, r.Body, int64(s.cfg.Server.MaxBodyBytes))

	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	if req.Language == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "language is required")
		return
	}

	lang, ok := s.reg.Get(req.Language)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown_language", "language "+req.Language+" is not supported")
		return
	}

	if len(req.Source) > s.cfg.Server.MaxBodyBytes {
		writeError(w, http.StatusBadRequest, "source_too_large", "source exceeds maximum allowed size")
		return
	}

	if len(req.Tests) == 0 {
		writeError(w, http.StatusBadRequest, "missing_field", "at least one test case is required")
		return
	}
	if len(req.Tests) > s.cfg.Server.MaxTests {
		writeError(w, http.StatusBadRequest, "too_many_tests",
			fmt.Sprintf("at most %d test cases are allowed", s.cfg.Server.MaxTests))
		return
	}

	// Resolve filenames — Java-style languages take them from the request.
	sourceFilename := lang.SourceFilename
	artifactFilename := lang.Artifact
	if lang.SourceFilenameStrategy == "from_request" {
		if req.SourceFilename == "" {
			writeError(w, http.StatusBadRequest, "missing_field", "source_filename is required for "+lang.ID)
			return
		}
		sourceFilename = req.SourceFilename
	}
	if lang.ArtifactFilenameStrategy == "from_request" {
		if req.ArtifactFilename == "" {
			writeError(w, http.StatusBadRequest, "missing_field", "artifact_filename is required for "+lang.ID)
			return
		}
		artifactFilename = req.ArtifactFilename
	}
	if err := validateFilename(sourceFilename); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filename", err.Error())
		return
	}
	if artifactFilename != "" && lang.ArtifactFilenameStrategy == "from_request" {
		if err := validateFilename(artifactFilename); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_filename", "artifact_filename: "+err.Error())
			return
		}
	}

	// Per-step flags and limit overrides (nested build/run objects, per spec).
	var buildFlags, runFlags []string
	var buildOverride, runOverride *limits.RequestOverride
	if req.Build != nil {
		buildFlags = req.Build.Flags
		buildOverride = req.Build.Limits.override()
	}
	if req.Run != nil {
		runFlags = req.Run.Flags
		runOverride = req.Run.Limits.override()
	}

	if len(buildFlags) > 0 {
		if lang.Build == nil {
			writeError(w, http.StatusBadRequest, "invalid_flag", "build flags are not applicable to "+lang.ID)
			return
		}
		if err := flags.Validate(buildFlags, lang.Build.FlagAllowlist); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_flag", err.Error())
			return
		}
	}
	if len(runFlags) > 0 {
		if err := flags.Validate(runFlags, lang.Run.FlagAllowlist); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_flag", err.Error())
			return
		}
	}

	// Effective limits: language defaults merged with the request's partial override.
	var buildLimits config.Limits
	if lang.Build != nil {
		buildLimits = limits.Merge(lang.Build.Limits, buildOverride)
	}
	runLimits := limits.Merge(lang.Run.Limits, runOverride)

	// Acquire concurrency slot (block until available or context cancelled).
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		writeError(w, http.StatusServiceUnavailable, "server_busy", "request cancelled while waiting for slot")
		return
	}

	obs.InFlight.Add(1)
	obs.TotalRequests.Add(1)
	defer obs.InFlight.Add(-1)

	tcs := make([]runner.TestCase, len(req.Tests))
	for i, tc := range req.Tests {
		tcs[i] = runner.TestCase{Stdin: tc.Stdin, Expected: tc.ExpectedOutput}
	}

	result, err := s.runner.Execute(ctx, runner.RunRequest{
		Language:         lang,
		Source:           req.Source,
		SourceFilename:   sourceFilename,
		ArtifactFilename: artifactFilename,
		BuildFlags:       buildFlags,
		RunFlags:         runFlags,
		BuildLimits:      buildLimits,
		RunLimits:        runLimits,
		Tests:            tcs,
		JailBase:         s.cfg.Server.JailBase,
		NsjailPath:       s.cfg.Server.NsjailPath,
		OutputCap:        s.cfg.Server.OutputCapBytes,
	})
	if err != nil {
		obs.TotalErrors.Add(1)
		obs.MarkInternalError()
		obs.Error(ctx, "runner failed", "err", err.Error())
		writeError(w, http.StatusInternalServerError, "internal_error", "execution engine error")
		return
	}

	obs.Log(ctx, "run complete",
		"language", req.Language,
		"status", result.TopStatus,
		"in_flight", obs.InFlight.Load(),
	)

	resp := runResponse{
		Status: result.TopStatus,
		Tests:  make([]testOut, len(result.Tests)),
	}

	if lang.Build != nil {
		bs := result.BuildStatus
		if bs == "" {
			bs = status.BuildOK
		}
		resp.Build = &buildInfo{
			Status:     bs,
			DurationMs: result.BuildDurationMs,
			Stdout:     result.BuildStdout,
			Stderr:     result.BuildStderr,
		}
	}

	for i, tr := range result.Tests {
		resp.Tests[i] = testOut{
			Status:     tr.Status,
			Stdout:     tr.Stdout,
			Stderr:     tr.Stderr,
			DurationMs: tr.DurationMs,
			MemPeakKB:  tr.MemPeakKB,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- /healthz ---

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- /readyz ---

type readyzResponse struct {
	Status    string                `json:"status"`
	Nsjail    nsjailReadyz          `json:"nsjail"`
	Languages map[string]langReadyz `json:"languages"`
}

type nsjailReadyz struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

type langReadyz struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	allOK := s.nsjail.OK
	langStatus := make(map[string]langReadyz, len(s.smokes))
	for id, sr := range s.smokes {
		langStatus[id] = langReadyz{OK: sr.OK, Version: sr.Version, Error: sr.Error}
		if !sr.OK {
			allOK = false
		}
	}
	code := http.StatusOK
	topStatus := "ok"
	if !allOK {
		code = http.StatusServiceUnavailable
		topStatus = "degraded"
	}
	writeJSON(w, code, readyzResponse{
		Status:    topStatus,
		Nsjail:    nsjailReadyz{OK: s.nsjail.OK, Version: s.nsjail.Version, Error: s.nsjail.Error},
		Languages: langStatus,
	})
}

// --- /info ---

type infoResponse struct {
	BuildInfo      map[string]string `json:"build_info"`
	Nsjail         map[string]string `json:"nsjail"`
	CgroupsEnabled bool              `json:"cgroups_enabled"`
	Languages      []langInfo        `json:"languages"`
	Limits         map[string]int    `json:"limits"`
	Stats          map[string]any    `json:"stats"`
}

type langInfo struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Version string        `json:"version,omitempty"`
	Limits  config.Limits `json:"default_run_limits"`
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	langs := make([]langInfo, 0, len(s.smokes))
	for _, l := range s.reg.All() {
		sr := s.smokes[l.ID]
		langs = append(langs, langInfo{
			ID:      l.ID,
			Name:    l.Name,
			Version: sr.Version,
			Limits:  l.Run.Limits,
		})
	}

	var stat syscall.Statfs_t
	diskFree := int64(0)
	if syscall.Statfs(s.cfg.Server.JailBase, &stat) == nil {
		diskFree = int64(stat.Bavail) * int64(stat.Bsize)
	}

	var lastInternalErr any // null unless an internal error has occurred
	if t, ok := obs.LastInternalError(); ok {
		lastInternalErr = t.UTC().Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, infoResponse{
		BuildInfo: map[string]string{
			"version":    s.buildInfo.Version,
			"commit":     s.buildInfo.Commit,
			"go_version": s.buildInfo.GoVersion,
		},
		Nsjail: map[string]string{
			"path":    s.cfg.Server.NsjailPath,
			"version": s.nsjail.Version,
		},
		CgroupsEnabled: s.cgroupsEnabled,
		Languages:      langs,
		Limits: map[string]int{
			"max_source_bytes":    s.cfg.Server.MaxBodyBytes,
			"max_tests":           s.cfg.Server.MaxTests,
			"max_concurrent_jobs": s.cfg.Server.MaxConcurrency,
		},
		Stats: map[string]any{
			"jobs_total":               obs.TotalRequests.Load(),
			"in_flight_jobs":           obs.InFlight.Load(),
			"jobs_failed_internal":     obs.TotalErrors.Load(),
			"last_internal_error_at":   lastInternalErr,
			"disk_free_bytes_jail_dir": diskFree,
			"uptime_s":                 int64(time.Since(startTime).Seconds()),
		},
	})
}

var startTime = time.Now()

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, errCode, message string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{
			"code":    errCode,
			"message": message,
		},
	})
}

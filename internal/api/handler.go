package api

import (
	"encoding/json"
	"net/http"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ambitious-scrap/goboxd/internal/config"
	"github.com/ambitious-scrap/goboxd/internal/flags"
	"github.com/ambitious-scrap/goboxd/internal/limits"
	"github.com/ambitious-scrap/goboxd/internal/obs"
	"github.com/ambitious-scrap/goboxd/internal/registry"
	"github.com/ambitious-scrap/goboxd/internal/runner"
	"github.com/ambitious-scrap/goboxd/internal/status"
)

// Server holds all dependencies for the HTTP layer.
type Server struct {
	cfg      *config.Config
	reg      *registry.Registry
	runner   *runner.Runner
	sem      chan struct{}
	smokes   map[string]registry.SmokeResult
	buildInfo BuildInfo
}

// BuildInfo is injected at link time via -ldflags.
type BuildInfo struct {
	Version   string
	Commit    string
	GoVersion string
}

func NewServer(cfg *config.Config, reg *registry.Registry, r *runner.Runner, smokes map[string]registry.SmokeResult, bi BuildInfo) *Server {
	return &Server{
		cfg:       cfg,
		reg:       reg,
		runner:    r,
		sem:       make(chan struct{}, cfg.Server.MaxConcurrency),
		smokes:    smokes,
		buildInfo: bi,
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
	Language string          `json:"language"`
	Source   string          `json:"source"`
	Flags    []string        `json:"flags"`
	Tests    []testCaseInput `json:"tests"`
}

type testCaseInput struct {
	Stdin          string `json:"stdin"`
	ExpectedOutput string `json:"expected_output"`
}

type runResponse struct {
	Status string      `json:"status"`
	Build  *buildInfo  `json:"build,omitempty"`
	Tests  []testOut   `json:"tests"`
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

	if err := validateFilename(lang.SourceFilename); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filename", err.Error())
		return
	}

	if lang.Build != nil && len(req.Flags) > 0 {
		if err := flags.Validate(req.Flags, lang.Build.FlagAllowlist); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_flag", err.Error())
			return
		}
	}

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

	limitOverride := limits.RequestOverride{} // extend later for per-request limit API
	_ = limitOverride

	result, err := s.runner.Execute(ctx, runner.RunRequest{
		Language:   lang,
		Source:     req.Source,
		Flags:      req.Flags,
		Tests:      tcs,
		JailBase:   s.cfg.Server.JailBase,
		NsjailPath: s.cfg.Server.NsjailPath,
		OutputCap:  s.cfg.Server.OutputCapBytes,
	})
	if err != nil {
		obs.TotalErrors.Add(1)
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

	if lang.Build != nil || result.BuildStatus != "" {
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
	Status    string                      `json:"status"`
	Languages map[string]langReadyz       `json:"languages"`
}

type langReadyz struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	allOK := true
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
	writeJSON(w, code, readyzResponse{Status: topStatus, Languages: langStatus})
}

// --- /info ---

type infoResponse struct {
	BuildInfo  map[string]string          `json:"build_info"`
	Nsjail     map[string]string          `json:"nsjail"`
	Languages  []langInfo                 `json:"languages"`
	Stats      map[string]any             `json:"stats"`
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

	writeJSON(w, http.StatusOK, infoResponse{
		BuildInfo: map[string]string{
			"version":    s.buildInfo.Version,
			"commit":     s.buildInfo.Commit,
			"go_version": s.buildInfo.GoVersion,
		},
		Nsjail: map[string]string{
			"path": s.cfg.Server.NsjailPath,
		},
		Languages: langs,
		Stats: map[string]any{
			"total_requests":       obs.TotalRequests.Load(),
			"in_flight":            obs.InFlight.Load(),
			"total_errors":         obs.TotalErrors.Load(),
			"disk_free_bytes_jail": diskFree,
			"uptime_s":             int64(time.Since(startTime).Seconds()),
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

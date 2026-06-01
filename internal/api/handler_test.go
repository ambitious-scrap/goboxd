package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thesouldev/goboxd/internal/api"
	"github.com/thesouldev/goboxd/internal/config"
	"github.com/thesouldev/goboxd/internal/registry"
	"github.com/thesouldev/goboxd/internal/runner"
	"github.com/thesouldev/goboxd/internal/sandbox"
)

// fakeSandbox returns scripted results, one per Run call.
type fakeSandbox struct {
	results []*sandbox.Result
	idx     int
}

func (f *fakeSandbox) Run(_ context.Context, _ sandbox.RunConfig) (*sandbox.Result, error) {
	if f.idx >= len(f.results) {
		return &sandbox.Result{ExitCode: 0}, nil
	}
	r := f.results[f.idx]
	f.idx++
	return r, nil
}

func testLangs() []config.Language {
	return []config.Language{
		{
			ID:             "py3",
			Name:           "Python 3",
			SourceFilename: "solution.py",
			Run: config.RunStep{
				Cmd:    "/usr/bin/python3",
				Args:   []string{"solution.py"},
				Limits: config.Limits{WallTimeS: 5, MemoryKB: 262144, MaxProcesses: 64},
			},
		},
		{
			ID:             "cpp",
			Name:           "C++",
			SourceFilename: "solution.cpp",
			Artifact:       "solution",
			Build: &config.BuildStep{
				Cmd:           "/usr/bin/g++",
				Args:          []string{"{{flags}}", "-o", "{{artifact}}", "{{source}}"},
				Limits:        config.Limits{WallTimeS: 10, MemoryKB: 1048576, MaxProcesses: 100},
				FlagAllowlist: []string{"-O2"},
			},
			Run: config.RunStep{
				Cmd:    "./{{artifact}}",
				Limits: config.Limits{WallTimeS: 5, MemoryKB: 262144, MaxProcesses: 64},
			},
		},
		{
			ID:                       "jlang",
			Name:                     "FromRequest",
			SourceFilenameStrategy:   "from_request",
			ArtifactFilenameStrategy: "from_request",
			Build: &config.BuildStep{
				Cmd:    "/usr/bin/javac",
				Args:   []string{"{{source}}"},
				Limits: config.Limits{WallTimeS: 10, MemoryKB: 524288, MaxProcesses: 100},
			},
			Run: config.RunStep{
				Cmd:    "/usr/bin/java",
				Args:   []string{"{{artifact}}"},
				Limits: config.Limits{WallTimeS: 10, MemoryKB: 524288, MaxProcesses: 300},
			},
		},
	}
}

func newTestServer(t *testing.T, results ...*sandbox.Result) http.Handler {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxConcurrency: 4,
			MaxBodyBytes:   1 << 20,
			MaxTests:       100,
			JailBase:       t.TempDir(),
			NsjailPath:     "/unused",
			OutputCapBytes: 65536,
		},
	}
	reg := registry.New(testLangs())
	r := runner.NewWithSandbox(&fakeSandbox{results: results}, cfg.Server.JailBase, cfg.Server.OutputCapBytes)
	smokes := map[string]registry.SmokeResult{"py3": {OK: true}}
	nsjail := api.NsjailInfo{OK: true, Version: "nsjail test"}
	srv := api.NewServer(cfg, reg, r, smokes, api.BuildInfo{Version: "test"}, nsjail)
	return srv.Router()
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("healthz body = %q", rec.Body.String())
	}
}

func TestRun_BadRequests(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"invalid json", `{not json`, "invalid_json"},
		{"missing language", `{"tests":[{"stdin":"","expected_stdout":"x"}]}`, "missing_field"},
		{"unknown language", `{"language":"cobol","tests":[{"stdin":"","expected_stdout":"x"}]}`, "unknown_language"},
		{"missing tests", `{"language":"py3","source":"print(1)"}`, "missing_field"},
		{"bad filename", `{"language":"jlang","source":"x","source_filename":"../evil","artifact_filename":"A","tests":[{"stdin":"","expected_stdout":"x"}]}`, "invalid_filename"},
		{"disallowed flag", `{"language":"cpp","source":"int main(){}","build":{"flags":["-fevil"]},"tests":[{"stdin":"","expected_stdout":"x"}]}`, "invalid_flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestServer(t)
			rec := post(t, h, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("error body not shaped {error:{code,message}}: %v", err)
			}
			if resp.Error.Code != tc.wantCode {
				t.Errorf("error code = %q, want %q", resp.Error.Code, tc.wantCode)
			}
			if resp.Error.Message == "" {
				t.Error("error message is empty")
			}
		})
	}
}

func TestRun_AcceptedEchoesMetrics(t *testing.T) {
	// One test case → one Run call returning matching stdout.
	h := newTestServer(t, &sandbox.Result{ExitCode: 0, Stdout: "hello\n"})
	rec := post(t, h, `{"language":"py3","source":"print(\"hello\")","tests":[{"stdin":"","expected_stdout":"hello\n"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	var resp struct {
		Status string `json:"status"`
		Tests  []struct {
			Status     string `json:"status"`
			DurationMs int64  `json:"duration_ms"`
			MemPeakKB  int64  `json:"memory_peak_kb"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "accepted" {
		t.Errorf("status = %q, want accepted", resp.Status)
	}
	// Fields must always be present in the JSON, even at zero.
	if !strings.Contains(body, "duration_ms") || !strings.Contains(body, "memory_peak_kb") {
		t.Errorf("response missing duration_ms/memory_peak_kb keys: %s", body)
	}
	if len(resp.Tests) != 1 || resp.Tests[0].Status != "accepted" {
		t.Errorf("tests = %+v", resp.Tests)
	}
}

func TestRun_BuildFailed(t *testing.T) {
	// cpp build step returns non-zero → build_failed, test not_executed.
	h := newTestServer(t, &sandbox.Result{ExitCode: 1, Stderr: "compile error"})
	rec := post(t, h, `{"language":"cpp","source":"bad","tests":[{"stdin":"","expected_stdout":"x"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d (build failure is a 200, not 5xx); body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status string `json:"status"`
		Build  struct {
			Status string `json:"status"`
		} `json:"build"`
		Tests []struct {
			Status string `json:"status"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "build_failed" {
		t.Errorf("top status = %q, want build_failed", resp.Status)
	}
	if resp.Tests[0].Status != "not_executed" {
		t.Errorf("test status = %q, want not_executed", resp.Tests[0].Status)
	}
}

func TestRun_NestedBuildFlagsAccepted(t *testing.T) {
	// Allow-listed flag in the nested build object passes validation.
	// Two Run calls: build (exit 0) then the single test.
	h := newTestServer(t,
		&sandbox.Result{ExitCode: 0},
		&sandbox.Result{ExitCode: 0, Stdout: "ok\n"},
	)
	rec := post(t, h, `{"language":"cpp","source":"int main(){}","build":{"flags":["-O2"],"limits":{"wall_time_s":7}},"run":{"limits":{"memory_kb":131072}},"tests":[{"stdin":"","expected_stdout":"ok\n"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"accepted"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestRun_TooManyTests(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"language":"py3","source":"print(1)","tests":[`)
	for i := 0; i < 101; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"stdin":"","expected_stdout":"x"}`)
	}
	b.WriteString(`]}`)
	h := newTestServer(t)
	rec := post(t, h, b.String())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "too_many_tests") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestReadyz_IncludesNsjail(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz code = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status string `json:"status"`
		Nsjail struct {
			OK      bool   `json:"ok"`
			Version string `json:"version"`
		} `json:"nsjail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Nsjail.OK || resp.Nsjail.Version == "" {
		t.Errorf("nsjail block = %+v", resp.Nsjail)
	}
}

func TestInfo_LimitsAndNsjailVersion(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/info", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("info code = %d", rec.Code)
	}
	var resp struct {
		Nsjail map[string]string `json:"nsjail"`
		Limits map[string]int    `json:"limits"`
		Stats  map[string]any    `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Nsjail["version"] == "" {
		t.Error("info nsjail.version is empty")
	}
	for _, k := range []string{"max_source_bytes", "max_tests", "max_concurrent_jobs"} {
		if _, ok := resp.Limits[k]; !ok {
			t.Errorf("info limits missing %q: %+v", k, resp.Limits)
		}
	}
	if _, ok := resp.Stats["last_internal_error_at"]; !ok {
		t.Error("info stats missing last_internal_error_at")
	}
}

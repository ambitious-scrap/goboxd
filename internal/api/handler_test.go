package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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
			MaxSourceBytes: 1 << 18,
			MaxTests:       100,
			JailBase:       t.TempDir(),
			NsjailPath:     "/unused",
			OutputCapBytes: 65536,
		},
	}
	reg := registry.New(testLangs())
	r := runner.NewWithSandbox(&fakeSandbox{results: results}, cfg.Server.JailBase, cfg.Server.OutputCapBytes, 0, nil)
	smokes := map[string]registry.SmokeResult{"py3": {OK: true}}
	nsjail := api.NsjailInfo{OK: true, Version: "nsjail test"}
	srv := api.NewServer(cfg, reg, r, smokes, api.BuildInfo{Version: "test"}, nsjail, true)
	return srv.Router()
}

// blockingSandbox blocks every Run until release is closed, so requests can be
// pinned in-system to saturate the admission queue deterministically.
type blockingSandbox struct{ release chan struct{} }

func (b *blockingSandbox) Run(_ context.Context, _ sandbox.RunConfig) (*sandbox.Result, error) {
	<-b.release
	return &sandbox.Result{ExitCode: 0, Stdout: "hi\n"}, nil
}

// newServerWith builds a server with explicit concurrency/queue limits and a
// caller-supplied sandbox, for admission-control tests.
func newServerWith(t *testing.T, sb runner.SandboxRunner, maxConc, maxQueue int) *api.Server {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxConcurrency: maxConc,
			MaxQueue:       maxQueue,
			MaxBodyBytes:   1 << 20,
			MaxSourceBytes: 1 << 18,
			MaxTests:       100,
			JailBase:       t.TempDir(),
			NsjailPath:     "/unused",
			OutputCapBytes: 65536,
		},
	}
	reg := registry.New(testLangs())
	r := runner.NewWithSandbox(sb, cfg.Server.JailBase, cfg.Server.OutputCapBytes, 0, nil)
	smokes := map[string]registry.SmokeResult{"py3": {OK: true}}
	nsjail := api.NsjailInfo{OK: true, Version: "nsjail test"}
	return api.NewServer(cfg, reg, r, smokes, api.BuildInfo{Version: "test"}, nsjail, true)
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// gateSandbox signals each Run entry on entered, then blocks until release is
// closed — letting a test observe exactly which requests cleared admission.
type gateSandbox struct {
	entered chan struct{}
	release chan struct{}
}

func (g *gateSandbox) Run(_ context.Context, _ sandbox.RunConfig) (*sandbox.Result, error) {
	g.entered <- struct{}{}
	<-g.release
	return &sandbox.Result{ExitCode: 0, Stdout: "hi\n"}, nil
}

// newServerWithFastLane is newServerWith plus an explicit fast-lane reservation.
func newServerWithFastLane(t *testing.T, sb runner.SandboxRunner, maxConc, maxQueue, reserved int) *api.Server {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxConcurrency:   maxConc,
			MaxQueue:         maxQueue,
			FastLaneReserved: reserved,
			MaxBodyBytes:     1 << 20,
			MaxSourceBytes:   1 << 18,
			MaxTests:         100,
			JailBase:         t.TempDir(),
			NsjailPath:       "/unused",
			OutputCapBytes:   65536,
		},
	}
	reg := registry.New(testLangs())
	r := runner.NewWithSandbox(sb, cfg.Server.JailBase, cfg.Server.OutputCapBytes, 0, nil)
	smokes := map[string]registry.SmokeResult{"py3": {OK: true}}
	nsjail := api.NsjailInfo{OK: true, Version: "nsjail test"}
	return api.NewServer(cfg, reg, r, smokes, api.BuildInfo{Version: "test"}, nsjail, true)
}

// waitEntries blocks until n values arrive on ch or the deadline elapses.
func waitEntries(ch <-chan struct{}, n int, d time.Duration) bool {
	deadline := time.After(d)
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-deadline:
			return false
		}
	}
	return true
}

// Heavy (compiled) jobs must not starve light (interpreted) jobs of admission:
// the fast-lane reservation keeps slots open for light requests even when the
// heavy lane is saturated.
func TestRun_FastLaneAdmitsLightUnderHeavySaturation(t *testing.T) {
	gate := &gateSandbox{entered: make(chan struct{}, 8), release: make(chan struct{})}
	defer close(gate.release)

	// maxConc=2, reserved=1 => heavy lane capped at 1; one slot stays open for light.
	srv := newServerWithFastLane(t, gate, 2, 16, 1)
	h := srv.Router()

	cpp := `{"language":"cpp","source":"int main(){}","tests":[{"stdin":"","expected_stdout":"hi\n"}]}`
	py := `{"language":"py3","source":"print(1)","tests":[{"stdin":"","expected_stdout":"hi\n"}]}`
	fire := func(body string) {
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}

	// Two heavy jobs; the heavy lane (cap 1) admits exactly one into the sandbox.
	fire(cpp)
	fire(cpp)
	if !waitEntries(gate.entered, 1, 2*time.Second) {
		t.Fatal("no heavy job entered the sandbox")
	}

	// A light job must still clear admission via the reserved slot.
	fire(py)
	if !waitEntries(gate.entered, 1, 2*time.Second) {
		t.Fatal("light job blocked behind saturated heavy lane; fast-lane reservation not working")
	}
}

// A reservation larger than the pool must clamp the heavy lane to >=1 so
// compiled jobs still run instead of deadlocking on a zero-capacity lane.
func TestRun_FastLaneOverReservationNoDeadlock(t *testing.T) {
	sb := &fakeSandbox{results: []*sandbox.Result{{ExitCode: 0}, {ExitCode: 0, Stdout: "ok\n"}}}
	srv := newServerWithFastLane(t, sb, 1, 4, 5) // reserved > maxConc
	rec := post(t, srv.Router(), `{"language":"cpp","source":"int main(){}","tests":[{"stdin":"","expected_stdout":"ok\n"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"accepted"`) {
		t.Errorf("body = %s", rec.Body.String())
	}
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

// source_too_large fires when the source field exceeds MaxSourceBytes but the
// whole body still fits under MaxBodyBytes — i.e. it is reachable and distinct
// from the body-reader cap. Regression guard for the two limits being equal.
func TestRun_SourceTooLarge(t *testing.T) {
	h := newTestServer(t)
	// MaxSourceBytes is 1<<18; MaxBodyBytes is 1<<20. A source just over the
	// source cap keeps the body well under the body cap.
	src := strings.Repeat("a", (1<<18)+1)
	body := `{"language":"py3","source":"` + src + `","tests":[{"stdin":"","expected_stdout":"x"}]}`
	rec := post(t, h, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	var resp struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Code != "source_too_large" {
		t.Errorf("code = %q, want source_too_large", resp.Error.Code)
	}
}

// request_too_large fires when the whole body exceeds MaxBodyBytes; the
// MaxBytesReader trips during decode and must map to request_too_large, not
// invalid_json.
func TestRun_RequestTooLarge(t *testing.T) {
	h := newTestServer(t)
	src := strings.Repeat("a", (1<<20)+1) // exceeds MaxBodyBytes 1<<20
	body := `{"language":"py3","source":"` + src + `","tests":[{"stdin":"","expected_stdout":"x"}]}`
	rec := post(t, h, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	var resp struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Code != "request_too_large" {
		t.Errorf("code = %q, want request_too_large", resp.Error.Code)
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

// Under saturation the excess requests must be shed with 503 + Retry-After and
// counted in rejected_total, while the admitted set stays bounded. The queue
// depth gauge must reflect the pinned in-system requests.
func TestRun_QueueShedsWhenSaturated(t *testing.T) {
	const (
		maxConc  = 1
		maxQueue = 1
		fired    = 6
	)
	// Admission cap is maxConc+maxQueue = 2; the other 4 must be rejected.
	const wantRejected = fired - (maxConc + maxQueue)

	sb := &blockingSandbox{release: make(chan struct{})}
	srv := newServerWith(t, sb, maxConc, maxQueue)
	h := srv.Router()
	body := `{"language":"py3","source":"print(1)","tests":[{"stdin":"","expected_stdout":"hi\n"}]}`

	codes := make(chan int, fired)
	for i := 0; i < fired; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			codes <- rec.Code
			if rec.Code == http.StatusServiceUnavailable {
				if rec.Header().Get("Retry-After") == "" {
					t.Errorf("503 response missing Retry-After header")
				}
			}
		}()
	}

	// The rejected requests return immediately; the admitted ones block on the
	// sandbox until released. Collect exactly the rejections first.
	for i := 0; i < wantRejected; i++ {
		if code := <-codes; code != http.StatusServiceUnavailable {
			t.Errorf("rejection %d: code = %d, want 503", i, code)
		}
	}

	// While the admitted requests are pinned, the queue-depth gauge reflects
	// them and rejected_total counts the shed load.
	scrape := scrapeMetrics(t, srv)
	if got := metricValue(scrape, "goboxd_rejected_total"); got < wantRejected {
		t.Errorf("goboxd_rejected_total = %v, want >= %d", got, wantRejected)
	}
	if got := metricValue(scrape, "goboxd_queue_depth"); got < float64(maxConc+maxQueue) {
		t.Errorf("goboxd_queue_depth = %v, want >= %d", got, maxConc+maxQueue)
	}

	// Release the admitted requests so they finish (200).
	close(sb.release)
	for i := 0; i < maxConc+maxQueue; i++ {
		if code := <-codes; code != http.StatusOK {
			t.Errorf("admitted request: code = %d, want 200", code)
		}
	}
}

func scrapeMetrics(t *testing.T, srv *api.Server) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.MetricsHandler().ServeHTTP(rec, req)
	return rec.Body.String()
}

// metricValue returns the value of a single (unlabelled) metric sample, or -1 if
// absent.
func metricValue(scrape, name string) float64 {
	for _, line := range strings.Split(scrape, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == name {
			if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
				return v
			}
		}
	}
	return -1
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
		Nsjail         map[string]string `json:"nsjail"`
		CgroupsEnabled bool              `json:"cgroups_enabled"`
		Limits         map[string]int    `json:"limits"`
		Stats          map[string]any    `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Nsjail["version"] == "" {
		t.Error("info nsjail.version is empty")
	}
	if !resp.CgroupsEnabled {
		// newTestServer wires cgroupsEnabled=true; assert the field round-trips.
		t.Error("info cgroups_enabled = false, want true")
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

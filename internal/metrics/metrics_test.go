package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scrape returns the /metrics body served by m.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func TestNew_RegistersAndServes(t *testing.T) {
	m := New()
	body := scrape(t, m)
	// Non-vec collectors emit a zero sample (and HELP) before any observation.
	// Labelled vecs (runs_total, run_duration, queue_wait) have no children until
	// first use, so they only appear after an observation — covered below.
	for _, name := range []string{
		"goboxd_inflight",
		"goboxd_requests_total",
		"goboxd_internal_errors_total",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("scrape missing %q", name)
		}
	}
	// Standard Go/process collectors registered.
	if !strings.Contains(body, "go_goroutines") {
		t.Error("Go collector not registered")
	}
	// queue_wait is labelled by lane; it materializes after the first observation.
	m.QueueWait.WithLabelValues("light").Observe(0.01)
	if body := scrape(t, m); !strings.Contains(body, `goboxd_queue_wait_seconds_count{lane="light"} 1`) {
		t.Errorf("queue_wait not recorded by lane:\n%s", body)
	}
}

func TestObserveRun_Compiled(t *testing.T) {
	m := New()
	m.ObserveRun("cpp", "accepted", 1.5, []float64{0.2, 0.3})
	body := scrape(t, m)

	if !strings.Contains(body, `goboxd_runs_total{language="cpp",verdict="accepted"} 1`) {
		t.Errorf("runs_total counter not incremented:\n%s", body)
	}
	// Build phase observed (count 1) and run phase observed (count 2).
	if !strings.Contains(body, `goboxd_run_duration_seconds_count{language="cpp",phase="build"} 1`) {
		t.Errorf("build phase not observed:\n%s", body)
	}
	if !strings.Contains(body, `goboxd_run_duration_seconds_count{language="cpp",phase="run"} 2`) {
		t.Errorf("run phase not observed twice:\n%s", body)
	}
}

func TestObserveRun_InterpretedNoBuild(t *testing.T) {
	m := New()
	// buildSeconds < 0 => no build phase recorded.
	m.ObserveRun("py3", "wrong_output", -1, []float64{0.05})
	body := scrape(t, m)

	if strings.Contains(body, `phase="build"`) {
		t.Errorf("interpreted run must not record a build phase:\n%s", body)
	}
	if !strings.Contains(body, `goboxd_run_duration_seconds_count{language="py3",phase="run"} 1`) {
		t.Errorf("run phase not observed:\n%s", body)
	}
	if !strings.Contains(body, `goboxd_runs_total{language="py3",verdict="wrong_output"} 1`) {
		t.Errorf("verdict counter wrong:\n%s", body)
	}
}

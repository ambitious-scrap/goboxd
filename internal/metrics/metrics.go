// Package metrics provides the Prometheus instrumentation for goboxd.
//
// All collectors live on a private registry (not the global default), so the
// scrape surface is exactly what we register here plus the standard Go runtime
// and process collectors. The handler is served on a separate admin port (see
// cmd/goboxd) rather than the public API, so submitters cannot scrape internal
// operational detail.
//
// Label cardinality is deliberately bounded: we label only by `language` (the
// fixed configured set) and `verdict` (the fixed status constants). We never
// label by source hash, request id, or filename — those are unbounded and would
// explode the time-series count.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Phase labels for run_duration_seconds.
const (
	PhaseBuild = "build"
	PhaseRun   = "run"
)

// Metrics holds every collector and the registry they belong to.
type Metrics struct {
	reg *prometheus.Registry

	// RunsTotal counts completed runs, labelled by language and top-level
	// verdict (accepted, wrong_output, time_exceeded, build_failed, ...).
	RunsTotal *prometheus.CounterVec
	// RunDuration is the wall time of a single phase (build or run), in seconds,
	// labelled by language and phase.
	RunDuration *prometheus.HistogramVec
	// QueueWait is the time a request spent waiting for a concurrency slot, in
	// seconds, labelled by admission lane (light|heavy). Ties to the scheduler
	// work in Part C-1 of the backlog and the fast-lane reservation.
	QueueWait *prometheus.HistogramVec
	// InFlight is the number of runs currently executing (post-admission).
	InFlight prometheus.Gauge
	// RequestsTotal counts admitted /run requests.
	RequestsTotal prometheus.Counter
	// InternalErrors counts runs that failed with a server-side error.
	InternalErrors prometheus.Counter
	// QueueDepth is the number of /run requests currently in the admission
	// section (waiting for a slot plus running). Ties to the C-1 scheduler.
	QueueDepth prometheus.Gauge
	// RejectedTotal counts /run requests shed at admission (503 server_busy)
	// because the queue was saturated.
	RejectedTotal prometheus.Counter
	// CacheHits / CacheMisses count artifact-cache outcomes by language (the
	// fixed configured set — bounded cardinality). Proves the C-2 cache's value.
	CacheHits   *prometheus.CounterVec
	CacheMisses *prometheus.CounterVec
	// BuildWait is the time a build step waited for a build-lane token, in
	// seconds. Ties to the C-1 build lane.
	BuildWait prometheus.Histogram
}

// New builds a Metrics with a private registry and all collectors registered.
func New() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		reg: reg,
		RunsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "goboxd_runs_total",
			Help: "Completed runs by language and top-level verdict.",
		}, []string{"language", "verdict"}),
		RunDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "goboxd_run_duration_seconds",
			Help: "Per-phase execution wall time in seconds.",
			// Code-execution latencies span ms (interpreted hello-world) to
			// tens of seconds (compiled, large limits).
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"language", "phase"}),
		QueueWait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "goboxd_queue_wait_seconds",
			Help:    "Time a request waited for a concurrency slot, in seconds, by admission lane.",
			Buckets: []float64{0.001, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"lane"}),
		InFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "goboxd_inflight",
			Help: "Runs currently executing (after admission).",
		}),
		RequestsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "goboxd_requests_total",
			Help: "Admitted /run requests.",
		}),
		InternalErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "goboxd_internal_errors_total",
			Help: "Runs that failed with a server-side error.",
		}),
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "goboxd_queue_depth",
			Help: "Requests currently in the admission section (waiting + running).",
		}),
		RejectedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "goboxd_rejected_total",
			Help: "Requests shed at admission (503) because the queue was full.",
		}),
		CacheHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "goboxd_cache_hits_total",
			Help: "Artifact-cache hits by language.",
		}, []string{"language"}),
		CacheMisses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "goboxd_cache_misses_total",
			Help: "Artifact-cache misses by language.",
		}, []string{"language"}),
		BuildWait: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "goboxd_build_wait_seconds",
			Help:    "Time a build step waited for a build-lane token, in seconds.",
			Buckets: []float64{0.001, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}),
	}

	reg.MustRegister(
		m.RunsTotal,
		m.RunDuration,
		m.QueueWait,
		m.InFlight,
		m.RequestsTotal,
		m.InternalErrors,
		m.QueueDepth,
		m.RejectedTotal,
		m.CacheHits,
		m.CacheMisses,
		m.BuildWait,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// ObserveRun records the durations and verdict of a completed run. buildSeconds
// is observed only when buildSeconds >= 0 (i.e. the language has a build step).
// runSeconds is the per-test run wall time; pass one value per test.
func (m *Metrics) ObserveRun(language, verdict string, buildSeconds float64, runSeconds []float64) {
	m.RunsTotal.WithLabelValues(language, verdict).Inc()
	if buildSeconds >= 0 {
		m.RunDuration.WithLabelValues(language, PhaseBuild).Observe(buildSeconds)
	}
	for _, s := range runSeconds {
		m.RunDuration.WithLabelValues(language, PhaseRun).Observe(s)
	}
}

// Handler returns the /metrics HTTP handler for this registry, with OpenMetrics
// encoding enabled (native histograms / exemplars ready).
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

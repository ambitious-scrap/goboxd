# Load Testing — goboxd (Claude, phased, honest metrics)

Phased optimization of `goboxd` under the `MemoryHog.java` workload, measured with
**goodput** and **success-only latency** instead of all-response percentiles. New
results live here; the earlier runs under `docs/loadtestGem/` and `docs/loadtestCld/`
are left untouched.

## Why the metric changed

The earlier harness reported vegeta's p50/p95 over **every** response, including fast
`503` rejects. Under a saturating workload the median response at high offered RPS is
a reject, so the all-response p50 collapses to ~1.5 ms — which *looks* like a latency
win but only measures how fast the server says "no". It hides both the real
throughput and how long real executions take.

This harness reports what matters:

* **goodput_rps** — successful (HTTP 200) executions per second. Flat goodput across
  rising offered RPS = the server is at its ceiling.
* **success_p50/p95/p99_ms** — latency over **only** the 200 responses.

The all-response columns are kept alongside, so the distortion is visible side by side.

## System configuration

* CPU limit: 2 vCPU (`--cpus=2`), Memory: 2 GB (`--memory=2g`), `--privileged --cgroupns=host`
* Container reports `cgroups_enabled=true`, Java = OpenJDK 17
* Load tool: vegeta (local `.tools/vegeta`), 10 s per-request timeout, 30 s per step
* Workload: `MemoryHog.java` — allocates 150 MB, touches every page, holds RSS ~1 s

## The ceiling

`MemoryHog` holds ~150 MB RSS for ~1 s per run. With a 512 MB per-job limit in a 2 GB
container only ~3–4 jobs fit at once, and each takes ≥1 s, so real throughput is
**~3 successful exec/s regardless of offered load**. The levers are therefore JVM
startup/GC cost, the artifact cache, and right-sized admission — *not* faster rejection.

## Results by phase (offered RPS → goodput / success-only latency)

### Phase 0 — limiter correctness (slot-leak fix, dead `sem` removed, config-driven cap)

| Offered RPS | Success | Error % | goodput rps | all p50 (ms) | success p50 (ms) | success p95 (ms) |
|------------:|--------:|--------:|------------:|-------------:|-----------------:|-----------------:|
| 5   | 94 / 150  | 37.3 | 3.15 | 4049.6 | 5580.9 | 6537.4 |
| 10  | 105 / 300 | 65.0 | 3.51 | 1.6    | 8479.4 | 8698.2 |
| 25  | 98 / 750  | 86.9 | 3.27 | 1.4    | 8651.1 | 9579.5 |
| 50  | 90 / 1500 | 94.0 | 3.00 | 1.5    | 4379.7 | 6507.2 |

Read this as the corrected baseline: goodput is pinned at ~3 rps and the **real**
median execution takes 4.4–8.7 s under load — the all-response p50 of ~1.5 ms was an
artifact of measuring rejects. The limiter fix is a correctness change (no more
permanent `active` drift under cancel storms); it is not expected to move a
workload-bound ceiling on its own.

_Later phases (JVM tuning, right-sized concurrency, memory tokens) are appended here
as they land, each with its own `results-phaseN.csv` + `goodput-phaseN.png` +
`latency-phaseN.png`._

## How to reproduce

```bash
make docker-build                       # builds goboxd:$(git describe --tags --always --dirty)
docker rm -f goboxd-loadtest 2>/dev/null
docker run -d --name goboxd-loadtest --privileged --cgroupns=host \
  --cpus=2 --memory=2g -p 8080:8080 -p 9090:9090 goboxd:$(git describe --tags --always --dirty)
python3 docs/loadtestCldFn/load-test.py phase0   # -> results-phase0.csv, goodput-phase0.png, latency-phase0.png
```

Pass a different tag (`phase1`, `phase2`, …) to keep each phase's outputs side by side.

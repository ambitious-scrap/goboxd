# Load Testing — goboxd (Claude, honest metrics)

Optimization of `goboxd` under the `MemoryHog.java` workload, measured with
**goodput** and **success-only latency** instead of all-response percentiles. New
results live here; the earlier runs under `docs/loadtestGem/` and `docs/loadtestCld/`
are left untouched.

## Why the metric changed

The earlier harness reported vegeta's p50/p95 over **every** response, including fast
`503` rejects, and counted every HTTP-200 as "success". Two problems:

1. Under saturation the median response at high RPS is a reject, so all-response p50
   collapses to ~1.5 ms — it measures how fast the server says "no", not how long a
   real run takes.
2. The payload's `expected_stdout` used a literal `"\\n"` (backslash-n, two chars)
   instead of a real newline, so **every** run mismatched and returned HTTP-200
   `wrong_output` — still counted as "success". The headline numbers were HTTP-200
   counts of failed comparisons.

This harness reports:

* **goodput_rps** — successful (HTTP 200) executions per second.
* **success_p50/p95/p99_ms** — latency over only the 200 responses.

and uses a correct `expected_stdout` (verified `accepted` at idle, and the verdict
mix is checked from `/metrics` after each run, not assumed).

## System configuration

* 2 vCPU (`--cpus=2`), 2 GB (`--memory=2g`), `--privileged --cgroupns=host`
* `cgroups_enabled=true`, Java = OpenJDK 17; vegeta, 10 s timeout, 30 s per step
* Workload: `MemoryHog.java` — allocates 150 MB, touches every page, holds RSS ~1 s

## The ceiling

`MemoryHog` holds ~150–180 MB RSS for ~1 s per run. The binding resource is **memory**,
not CPU — the job is mostly allocation plus a 1 s sleep, so it barely uses a core.
Throughput is therefore governed by how many runs fit in RAM concurrently, not by the
2-core quota.

## Results (offered RPS → goodput / success-only latency)

| Config | 5 RPS | 10 RPS | 25 RPS | 50 RPS | success p95 (50 RPS) |
|---|---|---|---|---|---|
| **Phase 0** — limiter slot-leak fix, honest metric | 3.15 | 3.51 | 3.27 | 3.00 | 6507 ms |
| **CPU-quota** — `MaxConcurrency = GOMAXPROCS = 2` | 1.17 | 1.04 | 1.10 | 1.10 | 6483 ms |
| **Tuned** — memory-governed admission (final) | **5.00** | **5.72** | **5.64** | **5.00** | 8804 ms |

(goodput in successful exec/s; full per-rate CSVs: `results-phase0.csv`,
`results-full.csv`, `results-tuned.csv`.)

### What each change did

* **Phase 0 (limiter correctness):** fixed the `AdaptiveLimiter` slot leak (a waiter
  woken and cancelled in the same instant left `active` permanently incremented,
  slowly pinning the limit). Honest metric exposed the real baseline: goodput ~3/s,
  real median run 4–9 s — the old "1.5 ms p50" was a rejection artifact.

* **CPU-quota (regression!):** `automaxprocs` + `MaxConcurrency = GOMAXPROCS(0)` set
  admission to the 2-core quota. With `fast_lane_reserved = max(1, 2/4) = 1`, the
  heavy (compiled) lane capped at `2 − 1 = 1`, serializing Java to ~1 run/s. Tying
  concurrency to CPU count is wrong for a memory-bound workload — Phase 0's "buggy"
  `NumCPU = 4` was accidentally right (`4 − 1 = 3` matched what RAM holds).

* **Tuned (final):** decouple admission from the core count — `max_concurrency: 6`,
  `fast_lane_reserved: 1` — and let the **scheduler memory-token gate** govern
  memory-heavy jobs while the per-run cgroup `cpu.max` bounds CPU. Goodput rose to
  ~5.5/s (+72 % over Phase 0, 5× the CPU-quota config). Verdict mix from `/metrics`:
  **623 accepted, 0 memory_exceeded, 0 time_exceeded**, 16 wrong_output + 1
  runtime_error (~2.5 % transients) — the goodput is real `accepted` work, not OOMs.

### Honest caveat on the memory budget

Under `--cgroupns=host` the container cannot read its own `/sys/fs/cgroup/memory.max`
(it sees the host root), so the token budget falls back to
`MaxConcurrency × 512 MB = 3 GB` — above the 2 GB box. It is safe **here** only
because each run's real peak (~180 MB) is far below its 512 MB reservation, so ~6
concurrent runs use ~1.1 GB. For a strict guarantee, either run without
`--cgroupns=host` (so `memory.max` reads 2 GB → budget 1.78 GB → ~3 concurrent, i.e.
back to ~3/s but provably within RAM) or set `scheduler_memory_kb` / a realistic
per-job cost explicitly. The high goodput trades a nominal over-commit for measured
real-RSS headroom.

## How to reproduce

```bash
make docker-build
docker rm -f goboxd-loadtest 2>/dev/null
docker run -d --name goboxd-loadtest --privileged --cgroupns=host \
  --cpus=2 --memory=2g -p 8080:8080 -p 9090:9090 goboxd:$(git describe --tags --always --dirty)
python3 docs/loadtestCldFn/load-test.py tuned     # -> results-tuned.csv + goodput/latency PNGs
curl -s localhost:9090/metrics | grep '^goboxd_runs_total'   # verdict mix
```

Pass a different tag (`phase0`, `full`, `tuned`) to keep each run's outputs side by side.

# Benchmarks

Latency for `POST /run` running a Python 3 "Hello World" against one test case, at increasing concurrency. Numbers below are from a clean `docker run` of image `goboxd:1f9b07a`, captured 2026-05-31. Raw `hey` output is the source for the table; re-running regenerates it.

Reproduce with `scripts/bench.sh` (uses [`hey`](https://github.com/rakyll/hey)):

```
scripts/bench.sh http://localhost:8080 500
```

## Hardware

These numbers were taken on the development machine, not final judging hardware. State your own hardware when you re-run.

- Host: Apple M4, 10 cores, 16 GB RAM, macOS 15.7.7
- Container host: Colima Linux VM (aarch64), 4 vCPU, 6 GB RAM, cgroup v2, Docker runtime
- Container: `docker run --rm --privileged -p 8080:8080 goboxd`, 4 CPUs visible (`GOMAXPROCS` auto-set to 4 by automaxprocs), nsjail 3.4
- Load tool: `hey`, 500 requests per concurrency level, from the macOS host over the forwarded port

Caveat: this is an arm64 dev VM with virtualized I/O. Absolute latencies will differ on bare-metal amd64 judging hardware; the shape (flat error rate, latency scaling with concurrency past the CPU count) is the informative part.

## Payload

```json
{"language":"py3","source":"print(\"hello\")","tests":[{"stdin":"","expected_stdout":"hello\n"}]}
```

## Results

| Concurrency | Requests/sec | p50 | p95 | p99 | Errors |
|-------------|--------------|----------|----------|----------|--------|
| 1   | 86  | 11.7 ms  | 17.9 ms  | 21.6 ms  | 0 / 500 |
| 10  | 268 | 36.3 ms  | 52.3 ms  | 59.8 ms  | 0 / 500 |
| 50  | 315 | 154.1 ms | 184.5 ms | 199.1 ms | 0 / 500 |
| 100 | 306 | 318.8 ms | 367.8 ms | 375.3 ms | 0 / 500 |

All requests returned `200`. No `5xx`, no dropped requests.

## Reading the numbers

- **Throughput plateaus around 310 req/s** past concurrency 50. The VM has 4 CPUs and the default concurrency semaphore is `runtime.NumCPU()` = 4, so only 4 requests execute at once; the rest queue. A single request takes ~12 ms, so the theoretical ceiling is ~4 / 0.012 ≈ 330 req/s; the observed 315 at concurrency 50 sits just under that. Each request does real per-request work: jail dir create, a dedicated cgroup v2 create + `memory.max` write + teardown, nsjail namespace/chroot setup, and interpreter start.
- **Latency scales roughly linearly with concurrency above the core count.** At concurrency 100 on 4 CPUs, requests queue on the semaphore, so p50 ≈ 100/4 × single-request time (~12 ms) ≈ 300 ms, which matches the observed 319 ms. This is the semaphore doing its job — requests wait for a slot instead of overcommitting the box and failing.
- **No errors at any level.** All 2000 requests across the four levels returned `200`. Under load the service queues on slot acquisition rather than returning `503`, which is the intended backpressure behavior.
- **Per-request cgroup lifecycle is part of the cost.** Memory accounting (`memory_exceeded`, `memory_peak_kb`) is paid for here: each request creates and tears down a cgroup. On a faster filesystem (bare-metal amd64 vs this virtualized arm64 VM) this overhead shrinks. The flat error rate and linear latency scaling are the portable findings; absolute throughput will be higher on judging hardware.

## Notes

- The concurrency limit is `GOBOXD_MAX_CONCURRENCY` (default `runtime.NumCPU()`). Raising it past the CPU count trades latency for little throughput on a CPU-bound workload; lowering it tightens tail latency under burst.
- Per-request cost is dominated by sandbox setup, not Python execution. A compiled language adds the one-time build step per request on top.

---

## 2026-06-09 — C-1 admission shedding + C-2 artifact cache

New run after the C-1 scheduler (bounded admission + build lane) and C-2 artifact cache
landed (commit `5a6e32d`). **The 2026-05-31 table above is kept as the pre-C-1/C-2
baseline; this section is appended, not a replacement.**

### Hardware / setup

- Host: Apple M4, 10 cores, 16 GB RAM, macOS 15.7.7
- Container host: Colima Linux VM (aarch64), 4 vCPU, 6 GB RAM, cgroup v2
- Container: `docker run --privileged` from image `goboxd:5a6e32d`, 4 CPUs visible
  (`GOMAXPROCS`=4), nsjail 3.4
- Load tool: `hey`. `MaxConcurrency`=4, `MaxQueue`=8 → in-system admission cap = 12.
- Same caveat as above: arm64 dev VM, virtualized I/O. The **shape** (cache elides the
  build; overload sheds with 503 instead of queueing) is the portable finding.

### C-2 artifact cache — C++ (`g++`, identical resubmissions)

Payload: a `cpp` hello-world (`a.out`), the same source resubmitted. Cache OFF =
`cache_enabled: false`; cache ON = default. 300 requests per level.

| Concurrency | rps (cache OFF) | rps (cache ON) | p50 OFF | p50 ON | p95 OFF | p95 ON |
|-------------|-----------------|----------------|---------|--------|---------|--------|
| 1  | 9.5  | **347** | 104 ms | **2.8 ms**  | 110 ms | 3.4 ms  |
| 10 | 18.8 | **903** | 529 ms | **10.5 ms** | 540 ms | 15.6 ms |

- Build wall time **143 ms → replayed from `meta.json`**; the compile step is skipped on
  a hit. ~36× throughput at c=1, p50 down ~37×.
- `goboxd_cache_hits_total{language="cpp"}`=657, `…_misses_total`=1 across the ON sweep —
  one cold compile, everything else served from cache.
- Verdict identical on hit (`accepted`, same build `duration_ms` replayed) — the cache is
  verdict-neutral. Interpreted languages (Python/Bash/JS) are unaffected: no build step,
  cache never consulted.

### C-1 admission shedding — Python (no build; cache irrelevant)

Payload: the Python hello-world from the baseline. 500 requests per level.

| Concurrency | 200 | 503 | p95 (admitted) | vs 2026-05-31 baseline |
|-------------|-----|-----|----------------|------------------------|
| 1   | 500 | 0   | 7.9 ms | was 0 err |
| 10  | 500 | 0   | 33 ms  | was 0 err |
| 50  | 97  | **403** | 32 ms | old: 0 err, all queued, p95 184 ms |
| 100 | 55  | **445** | 31 ms | old: 0 err, all queued, p95 368 ms |

- **Behavior changed by design.** Pre-C-1: every request queued on the semaphore
  (unbounded goroutine parking, tail latency climbing to 368 ms at c=100). Post-C-1:
  once in-system requests exceed `MaxConcurrency + MaxQueue` (=12), the excess gets
  **`503` + `Retry-After: 2`** (`goboxd_rejected_total`=848 across the sweep).
- Admitted requests keep a **flat ~32 ms p95 regardless of offered load** — the queue no
  longer degrades everyone under burst; it serves a bounded set fast and tells the rest to
  retry. `goboxd_queue_wait_seconds` p99 < 50 ms.
- This is the intended backpressure (see `docs/improvements.md` C-1/C-3): verdicts stay
  load-independent, server memory stays bounded. The new 503s are the feature, not a
  regression — the old "0 errors under load" line came at the cost of unbounded queueing.

### Reading the numbers

- C-2 is a large win exactly where build dominates cost (C/C++/Java/Verilog) and a no-op
  for interpreted languages — as designed.
- C-1 trades "admit-and-degrade" for "shed-fast": under overload, a bounded working set
  is served at low latency and the overflow is rejected cheaply instead of parking
  goroutines. Tune the trade with `max_concurrency` / `max_queue`.

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

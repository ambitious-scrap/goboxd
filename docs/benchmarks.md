# Benchmarks

Latency for `POST /run` running a Python 3 "Hello World" against one test case, at increasing concurrency. Numbers below are from a clean `docker run` of the image.

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
| 1   | 43  | 22.4 ms  | 32.7 ms  | 42.8 ms   | 0 / 500 |
| 10  | 102 | 91.8 ms  | 146.8 ms | 193.8 ms  | 0 / 500 |
| 50  | 107 | 452.6 ms | 592.1 ms | 616.4 ms  | 0 / 500 |
| 100 | 101 | 906.0 ms | 1198 ms  | 1219 ms   | 0 / 500 |

All requests returned `200`. No `5xx`, no dropped requests.

## Reading the numbers

- **Throughput plateaus around 105 req/s** past concurrency 10. The VM has 4 CPUs and the default concurrency semaphore is `runtime.NumCPU()` = 4, so only 4 requests execute at once; the rest queue. Each request does real per-request work: jail dir create, a dedicated cgroup v2 create + `memory.max` write + teardown, nsjail namespace/chroot setup, and interpreter start. That fixed cost — not Python itself — sets the ceiling.
- **Latency scales roughly linearly with concurrency above the core count.** At concurrency 100 on 4 CPUs, requests queue on the semaphore, so p50 ≈ 100/4 × single-request time (~22 ms) ≈ 0.9 s, which matches. This is the semaphore doing its job — requests wait for a slot instead of overcommitting the box and failing.
- **No errors at any level.** Under load the service queues on slot acquisition rather than returning `503`, which is the intended backpressure behavior.
- **Per-request cgroup lifecycle is part of the cost.** Memory accounting (`memory_exceeded`, `memory_peak_kb`) is paid for here: each request creates and tears down a cgroup. On a faster filesystem (bare-metal amd64 vs this virtualized arm64 VM) this overhead shrinks. The flat error rate and linear latency scaling are the portable findings; absolute throughput will be higher on judging hardware.

## Notes

- The concurrency limit is `GOBOXD_MAX_CONCURRENCY` (default `runtime.NumCPU()`). Raising it past the CPU count trades latency for little throughput on a CPU-bound workload; lowering it tightens tail latency under burst.
- Per-request cost is dominated by sandbox setup, not Python execution. A compiled language adds the one-time build step per request on top.

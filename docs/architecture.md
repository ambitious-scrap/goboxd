# Architecture

## Overview

goboxd is a single Go binary that accepts HTTP requests, executes user code inside nsjail sandboxes, and returns structured results. The service is stateless — each request is fully self-contained.

## Package structure

```
cmd/goboxd/         entry point: config, wiring, graceful shutdown
internal/config/    YAML schema, loader, defaults, validation
internal/registry/  language lookup, placeholder resolution, smoke probes
internal/api/       HTTP handlers, request validation, response shaping
internal/runner/    execution orchestration (build → run tests → map status)
internal/sandbox/   nsjail process management, output capture, OOM/timeout detection
internal/jail/      jail directory lifecycle (create, cleanup, orphan sweep)
internal/flags/     per-language compiler flag allow-listing
internal/limits/    request-level limit override merging (partial override, default fallback)
internal/status/    output comparison and top-level status computation
internal/artifactcache/  content-addressed cache of compiled build artifacts (verdict-neutral)
internal/metrics/   Prometheus counters/histograms, served on a separate admin port
internal/obs/       structured JSON logging and atomic stats counters
```

## Request lifecycle

```
POST /run
  → validate (language known, source size, filename, flags)
  → admission: shed if in-system > MaxConcurrency+MaxQueue  → 503 server_busy + Retry-After
  → [if compiled] acquire heavy-lane token (light jobs skip)
  → acquire run slot (buffered channel semaphore)
  → runner.Execute
      → jail.Create        (atomic counter + PID + crypto/rand suffix)
      → defer jail.Cleanup
      → write source file
      → [if compiled] artifact-cache get (single-flight key lock)
          → hit:  replay stored build output, skip compile
          → miss: acquire build-lane token, sandbox.Run(build cmd) → build_failed if exit != 0
                  on success: cache put (best-effort)
      → for each test: sandbox.Run(run cmd, stdin)   ← always live, fresh per test
          → map sandbox.Result → test status
      → status.TopLevel
  → release slot(s)
  → write JSON response
```

## Language registry

Languages are defined entirely in `configs/languages.yaml`. The engine resolves `{{source}}`, `{{artifact}}`, `{{flags}}`, and `{{workdir}}` placeholders at runtime. There are no language-specific branches in Go code — "compiled" means the YAML has a `build` block; "interpreted" means it does not.

Adding a language requires:
1. A YAML block in `configs/languages.yaml`
2. An install script in `scripts/lang_install/<id>.sh`
3. A `docker build`

## Sandbox isolation

Each execution runs inside nsjail with:
- A unique read-write workdir as the chroot (`--chroot <workdir> --cwd /`), with the host toolchain dirs bind-mounted read-only
- Wall time limit (`--time_limit`)
- Memory limit via cgroup v2 `memory.max` (`--use_cgroupv2 --cgroup_mem_max`), which caps resident memory (RSS) and produces a clean OOM kill. `--rlimit_as` is used only as a fallback when no cgroup mount is available — and is skipped entirely for VM-based runtimes (`node`, `java`, `javac`), because `rlimit_as` caps virtual address space, which the JVM/V8 reserve far beyond their RSS, so an address-space cap set to the RSS budget would prevent them from starting
- Process count limit enforced two ways: `--rlimit_nproc` (per-UID) plus cgroup v2 `pids.max` (absolute, hierarchical) set from `max_processes` — the cgroup cap is the real fork-bomb guard because `rlimit_nproc` is shared across all concurrent runs under the sandbox UID
- Optional CPU bandwidth cap via cgroup v2 `cpu.max`, set from the per-language `cpu_max_percent` config field (server-side only, not a request limit). Disabled by default (`max`); a sub-core quota throttles the process and inflates wall-clock time
- File size limit (`--rlimit_fsize`, 100 MiB)
- seccomp-bpf syscall filter (`--seccomp_string`, kafel), **enforced by the shipped
  `configs/languages.yaml`** (`seccomp_mode: enforce`): a shared deny-list (`DEFAULT ALLOW`)
  that kills the kernel sandbox-escape surface (`ptrace`, `bpf`, `mount`, module loading,
  `kexec`, `process_vm_*`, namespace ops) while leaving threaded/JIT runtimes intact. The three
  modes are `off` / `audit` / `enforce`; the code fallback when the key is absent entirely is
  `off` (a no-regression default), but the product ships enforcing. See `docs/security.md` §8.

The cgroup `memory`, `cpu` and `pids` controllers are delegated into the parent cgroup once at startup; each is enabled independently so a host that cannot delegate `cpu`/`pids` still gets memory accounting. `pids.max` and `cpu.max` are best-effort — skipped silently when the controller is unavailable.

stdout and stderr are captured through a custom `limitedWriter` (`internal/sandbox/sandbox.go`) with a hard byte cap; excess output is discarded and a `...[truncated]` marker is appended.

Outcome detection has to disambiguate signals that look alike: both an OOM kill and a wall-time
kill arrive as `SIGKILL` (exit 137). So the order matters. OOM is checked **first** via the
cgroup v2 `memory.events` `oom_kill` counter (recursive, so it captures kills inside nsjail's
child cgroup) → `memory_exceeded`. If it was not an OOM and the exit is non-zero, the run is
treated as a timeout when it was either killed by signal (exit 137) or ran up to the wall-time
limit (`wall_ms ≥ limit·1000 − 100ms`, the slack absorbing scheduling jitter); the nsjail
`--time_limit` kill is backed by a Go `context` deadline as a belt-and-suspenders backstop →
`time_exceeded`. Any other non-zero exit maps to `runtime_error`.

## Concurrency model

A buffered channel of size `MaxConcurrency` (default `runtime.NumCPU()`) acts as the run-slot semaphore. Requests block on slot acquisition, bound by the request context.

**Bounded admission (load shedding).** Before waiting for a slot, each `/run` counts itself as in-system (`waiting` atomic + `goboxd_queue_depth` gauge). When the in-system count exceeds `MaxConcurrency + MaxQueue` (`MaxQueue` defaults to `2 × MaxConcurrency`), the request is shed immediately with `503 server_busy` and a `Retry-After: 2` header instead of parking an unbounded goroutine. This keeps memory bounded under a flood. It is pure traffic control: it never mutates per-run limits, so verdicts remain a load-independent function of `(source, tests, limits)`.

**Build lane.** Compilation is the CPU-heavy phase, so a second semaphore (`buildSem`, size `MaxBuildConcurrency`, default `max(1, MaxConcurrency/2)`) caps concurrent build steps below the run-slot count. A flood of `g++ -O2` jobs can no longer peg every core and starve light interpreted runs. The build token is held only for the compile, never across the run phase. **Lock ordering:** the run-slot semaphore (handler) is always acquired before the build token (runner) — build-token holders are a strict subset of run-slot holders, so no deadlock is possible. The lane is disabled when `MaxBuildConcurrency` is `≤ 0` or `≥ MaxConcurrency`.

**Fast-lane reservation.** The build lane stops compiles from pegging every core, but light interpreted jobs (no build step) still competed with heavy compiled jobs for the same run slots — so a burst of slow `java`/`cpp` runs could head-of-line-block a `py3` run. A second cap fixes this: heavy (build ≠ nil) jobs first take a token from a `heavy` semaphore sized `MaxConcurrency − FastLaneReserved` before acquiring a run slot; light jobs skip it. This guarantees `FastLaneReserved` run slots (default `max(1, MaxConcurrency/4)`) can never be held by heavy jobs, so light requests always have admission headroom. **Lock ordering:** heavy jobs acquire `heavy` then the run-slot semaphore; light jobs take the run slot only — light never holds `heavy`, so the order cannot deadlock. The heavy lane is clamped to `≥ 1` so an over-large reservation never zeroes it. `FastLaneReserved = 0` disables the reservation (every job competes for the full pool). Like the other limits, this is pure admission ordering — it never changes per-run limits, so verdicts stay load-independent. Admission wait is tracked per lane via `goboxd_queue_wait_seconds{lane}`.

The concurrency limits are the only global locks on the hot path; jail dir naming is lock-free (atomic counter).

## Artifact cache

Identical resubmissions (common on contestant retries) skip recompilation via a content-addressed cache of the compiled output (`internal/artifactcache`). The cache is **verdict-neutral**: it stores only the build artifacts (`a.out`, `*.class`, vvp images — every file in the jail workdir except the source), never run results. The run phase always executes live in a fresh jail, per test.

- **Key:** `sha256(langID, toolchainVersion, sha256(source), buildFlags, artifactFilename)`. The toolchain version comes from the per-language smoke probe, so a compiler bump never serves a stale binary. An unknown (empty) toolchain version skips the cache entirely.
- **Hit:** cached files are copied into the fresh jail (mode bits preserved, so `a.out` stays executable) and the stored build stdout/stderr/duration are replayed; the compile is skipped.
- **Single-flight:** an in-process keyed semaphore spans get → build → put, so identical concurrent submissions compile exactly once. The wait is context-cancellable — a disconnecting client unblocks immediately rather than parking until the in-flight compile finishes.
- **Eviction:** a count cap (`CacheMaxEntries`, default 512) evicts the oldest entry by mtime on insert; a startup TTL sweep removes stale entries. Commit + eviction are serialized under a cache-wide lock, so concurrent `Put`s on different keys cannot race each other's rename/`RemoveAll`. Only successful builds are cached. All disk/IO errors degrade gracefully to a miss — the cache never fails a run.

## Performance

Per-request overhead is dominated by nsjail's namespace and filesystem setup. The jail directory is a minimal writable tmpfs workdir; the language toolchain lives in the image and is shared read-only across all requests. This keeps per-request setup to one `mkdir` + one file write.

## Status computation

Output comparison is a pure function:
1. Exact byte match → `accepted`
2. Equal after trimming leading/trailing whitespace from the whole output → `output_whitespace_mismatch` (internal whitespace differences are not normalized; matches the reference implementation)
3. Otherwise → `wrong_output`

Top-level status is the first non-`accepted` test status in order, or `build_failed` if the build step failed. All test statuses are `not_executed` when build fails.

## Health and observability

- `/healthz` — liveness only, no dependencies
- `/readyz` — executes smoke probes for every language at startup; caches results; returns 503 if any language is degraded
- `/info` — build metadata, language versions, atomic request stats, jail dir disk usage, and `cgroups_enabled` (whether per-run cgroup memory accounting is live)
- `/metrics` — Prometheus counters/histograms (`goboxd_runs_total{language,verdict}`, `goboxd_run_duration_seconds{language,phase}`, `goboxd_queue_wait_seconds{lane}`, `goboxd_build_wait_seconds`, `goboxd_inflight`, `goboxd_queue_depth`, `goboxd_rejected_total`, `goboxd_cache_hits_total{language}` / `_misses_total`), served by a **separate `http.Server` on the admin port** (`MetricsPort`, default 9090; `≤ 0` disables). It is never mounted on the public API router, so a submitter on `:8080` cannot scrape internal telemetry. Label cardinality is bounded to fixed `language`/`verdict` sets — never source hashes or request ids.

One structured JSON log line is emitted per request (request-id propagated via chi middleware).

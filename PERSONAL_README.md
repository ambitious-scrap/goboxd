# goboxd — Personal README (version `5a6e32d`, 2026-06-09)

A deep, feature-by-feature walkthrough of **goboxd**: a sandboxed code-execution and
grading service. It accepts source code + test cases over HTTP, compiles/runs the code
inside hardened [nsjail](https://github.com/google/nsjail) sandboxes, compares output
against expected results, and returns a per-test verdict.

This document describes the codebase **as of commit `5a6e32d`** — the point at which the
full event-prep hardening backlog (seccomp, cgroup CPU/PID controls, Prometheus metrics,
the C-1 scheduler, and the C-2 artifact cache) is shipped. It is the long-form companion to
the terse top-level `README.md`.

> **Design holy-grail:** *verdicts must be a pure function of `(source, tests, limits)` —
> load-independent.* No feature in this service is allowed to change a verdict based on how
> busy the server is. Every throughput/safety feature below is built to respect that.

---

## 1. What it is, in one paragraph

`goboxd` is the execution engine an online judge would sit on top of. A client `POST`s a
language id, source, optional per-step limit/flag overrides, and a list of
`{stdin, expected_stdout}` test cases. The server writes the source into a fresh isolated
working directory, optionally compiles it (compiled languages), runs the program once per
test case under strict resource limits, classifies each test's outcome, and returns a JSON
verdict. Untrusted user code never escapes the sandbox, never affects another request's
verdict, and cannot exhaust the host.

---

## 2. Request lifecycle (the whole pipeline)

```
client ── POST /run ──▶ chi router (request-id, panic recovery)
                          │
                          ▼
                  [handler] decode + validate
                    · body/source size caps
                    · language lookup
                    · filename validation
                    · per-step flag allowlist check
                    · merge limits (defaults ⊕ override)
                          │
                          ▼
                  [C-1] bounded admission
                    · waiting.Add(1); queue_depth gauge
                    · if in-system > MaxConcurrency+MaxQueue → 503 + Retry-After
                    · else acquire run-slot semaphore (ctx-cancellable)
                          │
                          ▼
                  [runner] Execute
                    · jail.Create() → unique 0700 workdir
                    · write source file
                    · BUILD phase (compiled langs only):
                        - [C-2] cache key = sha256(lang,toolchain,src,flags,artifact)
                        - cache HIT  → copy artifacts into jail, replay build meta
                        - cache MISS → [C-1 build lane] acquire buildSem
                                        → sandbox.Run(compile) → cache.Put()
                    · RUN phase: one sandbox.Run() per test case
                    · classify each test (compare / timeout / OOM / crash)
                          │
                          ▼
                  [sandbox] nsjail exec
                    · namespaces + chroot + rlimits
                    · per-run cgroup v2: memory.max, cpu.max, pids.max
                    · optional seccomp-bpf filter
                    · capture stdout/stderr (capped), exit cause, mem peak
                          │
                          ▼
                  [status] grade → top-level verdict
                          │
                          ▼
                  [metrics] observe; JSON 200 response
```

Key packages:

| Package | Responsibility |
|---|---|
| `internal/api` | HTTP handlers, request schema, admission control (C-1) |
| `internal/runner` | Build+run orchestration, build lane (C-1), cache wiring (C-2) |
| `internal/artifactcache` | Content-addressed compiled-artifact cache (C-2) |
| `internal/sandbox` | nsjail invocation, cgroup v2, seccomp, output capture |
| `internal/jail` | Unique workdir create/cleanup + orphan sweep |
| `internal/registry` | Language registry, placeholder expansion, smoke probes |
| `internal/config` | YAML config load, defaults, validation |
| `internal/limits` | Limit merge (defaults ⊕ partial request override) |
| `internal/flags` | Per-step flag allowlist validation |
| `internal/status` | Output comparison + verdict classification |
| `internal/metrics` | Prometheus collectors (admin port) |
| `internal/obs` | Structured logging + in-process counters |

---

## 3. HTTP API

Public API on `:8080`. Metrics on a **separate** admin port `:9090`.

### `POST /run`
Execute a submission. Nested per-step request schema:

```json
{
  "language": "cpp",
  "source": "#include <iostream>\nint main(){std::cout<<\"hi\\n\";}",
  "source_filename": "solution.cpp",
  "artifact_filename": "a.out",
  "build": { "limits": {"wall_time_s": 30, "memory_kb": 1048576, "max_processes": 100},
             "flags": ["-O2"] },
  "run":   { "limits": {"wall_time_s": 5,  "memory_kb": 262144,  "max_processes": 64},
             "flags": [] },
  "tests": [ {"stdin": "hello", "expected_stdout": "hi\n"} ]
}
```

- `source_filename` / `artifact_filename` are **required only** for languages whose strategy
  is `from_request` (e.g. Java, where the class name must match the file). Validated as a
  single, safe path component.
- `build` is ignored for interpreted languages.
- `tests`: at least one, at most `max_tests` (default 100).

### Response (`200`)
```json
{
  "status": "accepted",
  "build": {"status": "ok", "duration_ms": 143, "stdout": "", "stderr": ""},
  "tests": [
    {"status": "accepted", "stdout": "hi\n", "stderr": "", "duration_ms": 3, "memory_peak_kb": 8192}
  ]
}
```
`build` appears only for compiled languages.

### Verdict values
Top-level status = the **first** non-accepted test status, in order:

| Value | Meaning |
|---|---|
| `accepted` | Build ok (if any) and all tests accepted |
| `build_failed` | Compilation failed; every test is `not_executed` |
| `wrong_output` | First test whose stdout differs |
| `output_whitespace_mismatch` | First test that matches only after whitespace normalization |
| `time_exceeded` | First test that hit the wall-time limit |
| `memory_exceeded` | First test OOM-killed |
| `runtime_error` | First test with a non-zero exit (not OOM/timeout) |
| `not_executed` | (per-test only) build failed, so the test never ran |

**User code crashing is never a 5xx** — a crash is `200` + `runtime_error`.

### Error responses
`{"error": {"code": "...", "message": "..."}}`

| HTTP | Code | Cause |
|---|---|---|
| 400 | `invalid_json` | malformed body |
| 400 | `missing_field` | `language` / `tests` absent |
| 400 | `unknown_language` | id not registered |
| 400 | `source_too_large` | `source` > `max_source_bytes` |
| 400 | `request_too_large` | body > `max_body_bytes` |
| 400 | `invalid_filename` | bad source/artifact filename |
| 400 | `invalid_flag` | flag not in the per-step allowlist |
| 400 | `too_many_tests` | > `max_tests` |
| **503** | **`server_busy`** | **C-1 admission shed (with `Retry-After`)** |
| 500 | `internal_error` | server-side failure (nsjail missing, disk full…) |

### `GET /healthz`
Liveness only. Always `200 {"status":"ok"}`.

### `GET /readyz`
Readiness. Returns cached startup probe results: nsjail status + every language's smoke
probe. `200` only if nsjail OK **and** all languages passed; otherwise `503 degraded`.

### `GET /info`
Build metadata, `cgroups_enabled` flag, language list with versions + default run limits,
configured caps, and live stats (`jobs_total`, `in_flight_jobs`, `jobs_failed_internal`,
`last_internal_error_at`, `disk_free_bytes_jail_dir`, `uptime_s`).

### `GET /metrics` (admin port `:9090`)
Prometheus/OpenMetrics. See §9.

---

## 4. Languages (data-driven)

Languages are defined entirely in `configs/languages.yaml` — no Go code per language. Each
block declares source filename, optional build step (cmd/args/limits/flag-allowlist), run
step, a smoke probe (used for `/readyz` + the cache toolchain version), and an optional
seccomp policy. Placeholders (`{{source}}`, `{{artifact}}`, `{{workdir}}`, `{{flags}}`) are
expanded at run time.

Configured set in this version: **`py3`, `c`, `cpp`, `java`, `bash`, `javascript`,
`verilog`**. Compiled languages (`c`, `cpp`, `java`, `verilog`) have a build step and are the
ones that benefit from the C-2 cache and the C-1 build lane. Live per-language status is at
`/readyz`; versions at `/info`.

**Adding a language:** add a YAML block + an install script under
`scripts/lang_install/<id>.sh`, rebuild the image, check `/readyz`. No recompile of Go.

---

## 5. Sandbox isolation (`internal/sandbox` + nsjail)

Every build step and every test run executes inside nsjail (built from source, pinned
submodule `external/nsjail` @ tag **3.4**):

- **Namespaces + chroot:** mount/user/PID/net isolation; the program sees only its jail
  workdir. Network is all-deny.
- **rlimits:** wall-time (`--time_limit` + a Go `context` deadline as belt-and-suspenders),
  address space (`rlimit_as` fallback when cgroups are unavailable), file size
  (`rlimit_fsize`), process count (`rlimit_nproc`).
- **Output capping:** stdout/stderr are captured up to `output_cap_bytes` (default 64 KiB)
  and flagged `truncated` beyond that — a program can't flood the host with output.

### cgroup v2 resource control (per run)
A dedicated cgroup is created per run, written, and torn down:

- **`memory.max`** — hard memory ceiling; over-limit ⇒ OOM-kill ⇒ `memory_exceeded`.
  `memory.peak` is read back into `memory_peak_kb` (commit baseline).
- **`cpu.max`** *(commit `9b6f431`)* — optional CPU-bandwidth cap, per language
  (`cpu_max_percent`; 100 = one core). **Server-side only — never part of the request limit
  schema**, so a submission can't change it. Defaults to unlimited because a sub-core quota
  inflates wall-clock time and could trip `time_exceeded` (documented trade-off).
- **`pids.max`** *(commit `9b6f431`)* — deterministic fork-bomb kill at the cgroup level,
  bounding the whole run tree (stronger than per-uid `rlimit_nproc`).

`cgroups_enabled` (in `/info`) reports whether cgroup v2 accounting is active; if not, the
sandbox falls back to `rlimit_as` (limits still enforced, but OOM/peak not reported).

### seccomp-bpf syscall filtering *(commit `dbc446a`)*
Optional, server-wide `seccomp_mode`:

- **`off`** (default) — no syscall filter; no behavior change vs the pre-seccomp build.
- **`audit`** — load the per-language kafel policy **and** `--seccomp_log` so violations are
  logged but not killed (author policies with a permissive default action to observe first).
- **`enforce`** — apply the policy as written (e.g. `DEFAULT KILL`).

Policies are **per-language** (`seccomp_policy` in the YAML), because JIT/interpreted
runtimes (JVM, Node/V8) legitimately need `mprotect(PROT_EXEC)`, `futex`, `clone`, etc. that
a static C++ binary does not. A language with no policy is never filtered, regardless of
mode.

---

## 6. Resource limits & override semantics

Limits are `{wall_time_s, memory_kb, max_processes}` per step, plus the server-side
`cpu_max_percent`. The request may supply a **partial override** per step
(`build.limits` / `run.limits`):

- **Partial replace, no clamping, no ceiling.** Each present field replaces the language
  default for that step; absent fields keep the default. The server never silently lowers a
  limit under load (that would make verdicts load-dependent — forbidden, see C-3 in
  `docs/improvements.md`).

Code defaults (`internal/config/load.go`): run = 5 s / 256 MiB / 64 procs; build = 30 s /
1 GiB / 100 procs.

---

## 7. C-1 — Scheduler: bounded admission + build lane *(commit `5a6e32d`)*

Replaces the old bare semaphore (which parked unbounded goroutines under flood).

### Bounded admission (`internal/api/handler.go`)
- An atomic `waiting` counter tracks requests in-system (waiting for a run slot + running).
- Capacity = `MaxConcurrency + MaxQueue`. When exceeded, the request is **shed at the door**
  with **`503 server_busy` + `Retry-After: 2`** instead of queueing.
- Pure traffic control: it never touches per-run limits → verdicts stay load-independent.
- `goboxd_queue_depth` gauge + `goboxd_rejected_total` counter expose the behavior.

### Build lane (`internal/runner/runner.go`)
- A separate `buildSem` of size `MaxBuildConcurrency` caps **concurrent compiles** below the
  total run-slot count. A flood of heavy `g++ -O2` builds can no longer starve light
  interpreted runs.
- **Lock ordering invariant (deadlock-free):** the run-slot semaphore (handler) is *always*
  acquired before the build token (runner), never the reverse — build-token holders are a
  strict subset of run-slot holders.
- Auto-disables when `MaxBuildConcurrency >= MaxConcurrency` (no point capping). The
  build-lane wait time is exposed as `goboxd_build_wait_seconds`.

**Benchmarked:** under c=100 overload on a 4-CPU box, admitted requests hold a flat ~32 ms
p95 while the overflow gets 503s — vs the pre-C-1 behavior where everyone queued and p95
climbed to 368 ms. See `docs/benchmarks.md` (2026-06-09).

---

## 8. C-2 — Artifact cache *(commit `5a6e32d`)*

`internal/artifactcache`: a content-addressed cache of **compiled output only**. Verdict-
neutral by construction — it reuses the *binary*, never a run result, and always re-executes
in a fresh jail per test.

- **Key:** `sha256(langID ⊕ toolchainVersion ⊕ sha256(source) ⊕ joined(buildFlags) ⊕
  artifactFilename)`. The toolchain version comes from the language's smoke probe — **a
  compiler bump invalidates the cache**, so a stale binary is never served. If the toolchain
  version is unknown (empty), caching is skipped entirely.
- **Store:** on a successful build, every file in the jail workdir **except the source** is
  snapshotted into `CacheDir/<key>/` (captures `a.out`, all `*.class` incl. inner classes,
  vvp images — language-agnostic). A `meta.json` records build stdout/stderr/duration so a
  hit replays a faithful `build` response block.
- **Hit:** copy cached artifacts into the fresh jail, set `build.status = ok`, replay the
  stored build output + duration, **skip** the compile. The run phase still runs live per
  test case.
- **Single-flight:** an in-process keyed mutex spans `get → build → put`, so N identical
  concurrent submissions compile exactly once.
- **Eviction:** count cap `CacheMaxEntries` (default 512), oldest-by-mtime evicted on
  insert; a startup TTL sweep clears stale entries. **All IO errors degrade gracefully to a
  miss** (build normally).
- **Scope:** only languages with a build step ever consult the cache; toggle with
  `cache_enabled`.

**Benchmarked:** identical C++ resubmissions — ~36× throughput at c=1 (build 143 ms →
replayed; p50 104 ms → 2.8 ms), 657 hits / 1 miss across a sweep, identical verdicts. See
`docs/benchmarks.md`. Known best-effort limitations (non-cancellable single-flight wait;
cross-key eviction race) are logged in `docs/improvements.md` → *Follow-up notes*.

---

## 9. Observability

### Prometheus metrics *(commit `0561987`)*
Served on a **separate admin port** (`:9090`) so submitters can't scrape internal detail.
Private registry, bounded label cardinality (`language`, `verdict` only — never source-hash,
request-id, or filename):

| Metric | Type | Notes |
|---|---|---|
| `goboxd_runs_total{language,verdict}` | counter | completed runs by verdict |
| `goboxd_run_duration_seconds{language,phase}` | histogram | build vs run wall time |
| `goboxd_queue_wait_seconds` | histogram | time waiting for a run slot |
| `goboxd_inflight` | gauge | runs currently executing |
| `goboxd_requests_total` | counter | admitted /run requests |
| `goboxd_internal_errors_total` | counter | server-side failures |
| `goboxd_queue_depth` | gauge | requests in the admission section (C-1) |
| `goboxd_rejected_total` | counter | 503 admission sheds (C-1) |
| `goboxd_cache_hits_total{language}` / `…_misses_total{language}` | counter | artifact cache (C-2) |
| `goboxd_build_wait_seconds` | histogram | build-lane wait (C-1) |

Plus standard Go runtime + process collectors.

### Logs & stats
Structured logs (`log/slog`) carry a request id (chi middleware) through the run. In-process
counters surface in `/info.stats` (jobs total/in-flight/failed, last internal error, disk
free, uptime).

---

## 10. Configuration reference (`server` block)

| Key | Default | Purpose |
|---|---|---|
| `port` | 8080 | public API port |
| `metrics_port` | 9090 | admin metrics port (≤0 disables) |
| `max_concurrency` | `runtime.NumCPU()` | concurrent run slots |
| `max_queue` | `2 × max_concurrency` | extra waiters before 503 (C-1) |
| `max_build_concurrency` | `max(1, max_concurrency/2)` | build-lane cap (C-1); 0 or ≥ max_concurrency disables |
| `cache_enabled` | `true` | artifact cache on/off (C-2) |
| `cache_dir` | `/tmp/goboxd-cache` | cache storage dir |
| `cache_max_entries` | 512 | cache count cap (LRU-by-mtime evict) |
| `max_body_bytes` | 4 MiB | whole-request envelope cap |
| `max_source_bytes` | 256 KiB | `source` field cap |
| `output_cap_bytes` | 64 KiB | stdout/stderr capture cap |
| `max_tests` | 100 | max test cases per request |
| `jail_base` | `/tmp/goboxd` | jail workdir root |
| `nsjail_path` | `/usr/local/bin/nsjail` | nsjail binary |
| `seccomp_mode` | `off` | `off` / `audit` / `enforce` |

Per-language: `cpu_max_percent` (server-side CPU cap), `seccomp_policy` (kafel program),
build/run `limits` + `flag_allowlist`.

---

## 11. Build, run, deploy

```bash
make build         # compile binary
make run           # build + run locally (requires nsjail on PATH)
make test          # unit tests (73 as of this version)
make lint          # go vet + staticcheck
make verify-nsjail # assert the nsjail submodule is at tag 3.4
make docker-build  # build image (compiles nsjail + all language toolchains)
make docker-run    # docker run --privileged -p 8080:8080
make load          # hey load test against localhost:8080
```

- Go 1.22+. `go.uber.org/automaxprocs` sets `GOMAXPROCS` from the cgroup CPU quota so the
  scheduler doesn't oversubscribe when the host has more cores than the container quota.
- Docker entrypoint bakes the config at `/etc/goboxd/languages.yaml`. Run requires
  `--privileged` (nsjail needs namespace/cgroup access).
- Graceful shutdown: SIGINT/SIGTERM → `http.Server.Shutdown` for both API and metrics
  servers.

---

## 12. Security model (summary)

Defense in depth: nsjail namespaces + chroot + all-deny network → cgroup v2 memory/CPU/PID
caps → optional seccomp-bpf syscall filter → rlimits → output caps → bounded admission. Each
run gets a fresh 0700 jail dir (unique by atomic counter + PID + random hex); orphans from a
crashed run are swept at startup. Untrusted code cannot reach the network, exhaust host
memory/CPU/PIDs, escape the chroot, or influence another request. Full detail in
`docs/security.md`.

---

## 13. Related docs

- `README.md` — terse quick-start.
- `docs/api.md` — full request/response schema.
- `docs/architecture.md` — component diagram + data flow.
- `docs/security.md` — isolation layers, seccomp, cgroups, load-shedding notes.
- `docs/benchmarks.md` — perf over time (incl. the 2026-06-09 C-1/C-2 results).
- `docs/improvements.md` — backlog, Antigravity-memo verdicts, post-merge follow-up notes.
- `docs/roadmap.md` — forward-looking gaps + shipped addendum.

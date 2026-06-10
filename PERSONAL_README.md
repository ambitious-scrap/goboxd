# goboxd — Personal README (version `5a6e32d` + fast-lane, Grafana, seccomp-enforce & differential conformance, 2026-06-10)

A deep, feature-by-feature walkthrough of **goboxd**: a sandboxed code-execution and
grading service. It accepts source code + test cases over HTTP, compiles/runs the code
inside hardened [nsjail](https://github.com/google/nsjail) sandboxes, compares output
against expected results, and returns a per-test verdict.

This document describes the codebase **as of branch `c1-scheduler-c2-cache`** (base commit
`5a6e32d`) — the point at which the full event-prep hardening backlog (seccomp, cgroup
CPU/PID controls, Prometheus metrics, the C-1 scheduler, the C-2 artifact cache, the C-3
fast-lane fairness reservation, and a `docker compose` Prometheus + Grafana stack) is
shipped. It is the long-form companion to the terse top-level `README.md`.

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

**Terms & techniques**
- **Sandbox / isolation:** running untrusted code in a confined environment so it can't touch the host or other jobs.
- **Verdict:** the graded outcome of a submission (accepted, wrong output, timeout, etc.).
- **Resource limits:** hard ceilings on time, memory, and processes a run may consume.
- **Pure function of `(source, tests, limits)`:** same inputs always yield the same verdict, regardless of server load — the system's core correctness guarantee.
- **Interpreted vs compiled language:** interpreted (py3, bash, js) runs source directly; compiled (c, cpp, java, verilog) first builds an artifact, then runs it.

> **ELI5:** It's a robot teacher that runs your homework code in a locked, padded room
> so it can't break anything, checks your answers against the answer key, and tells you
> which ones are right.

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

**Terms & techniques**
- **chi router:** lightweight Go HTTP router; supplies request-id and panic-recovery middleware.
- **Request-id / panic recovery:** each request gets a trace id; a panic in a handler is caught and turned into a clean 500, not a crash.
- **Admission control (C-1):** deciding at the door whether to accept, queue, or reject a request based on in-flight load.
- **Semaphore:** a counter that caps how many things run at once; "run-slot semaphore" bounds concurrent executions.
- **`ctx`-cancellable:** an operation that aborts early if the request's Go `context` is cancelled (e.g. client disconnect).
- **Backpressure / load shedding:** returning `503 + Retry-After` instead of queueing unboundedly when overloaded.
- **Content-addressed cache key (C-2):** a `sha256` over build inputs; identical inputs map to the same cached artifact.
- **cgroup v2:** Linux kernel resource-accounting groups; here enforce per-run `memory.max`, `cpu.max`, `pids.max`.
- **seccomp-bpf:** kernel syscall filter that blocks dangerous syscalls.

> **ELI5:** When your code arrives, it goes down an assembly line: a guard checks it's
> not too big or sneaky, a bouncer turns it away if the place is too crowded, a worker
> builds it and runs each test in a locked room, and a grader writes down the score.

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

**Terms & techniques**
- **Readiness vs liveness:** `/readyz` reports whether the service can *serve* (nsjail + all language smoke probes OK); a startup probe runs each language once and caches the result.
- **Smoke probe:** a minimal known-good run per language proving the toolchain works.
- **`/info` metadata:** static build info + live counters (in-flight, failures, disk free, uptime).
- **Prometheus/OpenMetrics:** text exposition format scraped by Prometheus, served on the admin port.
- **HTTP status semantics:** `400` = malformed request, `503` = overloaded/degraded, `200` = a normal verdict even when *your* code crashes.

> **ELI5:** You mail in a box with your code and a list of questions-and-right-answers.
> The service mails back a report card saying which answers were right, wrong, too slow,
> or crashed. If you fill the form out wrong it says "400 bad form"; if it's too busy it
> says "503 come back later" — but a crash in *your* code is still a normal report card.

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

**Terms & techniques**
- **Data-driven registry:** languages defined in `languages.yaml`, loaded at startup — adding a language is a config edit, not a code change.
- **Build / run steps:** each language declares an optional `build` command and a `run` command as templates.
- **Placeholder templating (`{{source}}`, `{{artifact}}`, `{{workdir}}`, `{{flags}}`):** the runner substitutes concrete paths/flags into the command templates per job.
- **Flag allowlist:** only whitelisted compiler/runtime flags are accepted, blocking injection of arbitrary toolchain options.
- **Toolchain version pinning:** the registry records each compiler/interpreter version, feeding cache keys and `/info`.

> **ELI5:** Each programming language is described in a recipe file, not baked into the
> program. Want a new language? Write a new recipe, install its tools, done — no need to
> rebuild the whole machine.

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

### seccomp-bpf syscall filtering *(mechanism `dbc446a`; enforced by default 2026-06-10)*
Server-wide `seccomp_mode`, **`enforce` by default**:

- **`off`** — no syscall filter (byte-for-byte the pre-seccomp path).
- **`audit`** — load the kafel policy **and** `--seccomp_log`, so violations are logged.
- **`enforce`** *(default)* — apply the policy; a denied syscall delivers `SIGSYS` →
  `runtime_error`.

**Policy: a shared deny-list with `DEFAULT ALLOW`** (`&deny_seccomp` YAML anchor, aliased by
every language). Rather than enumerate every syscall a JVM/V8/CPython needs (an allow-list is
brittle across runtimes + arch and is the documented way to break JIT), it `KILL`s the
kernel sandbox-escape surface and allows the rest:

```
ptrace, mount, pivot_root, chroot, setns, unshare, keyctl, add_key, request_key,
bpf, perf_event_open, init_module, finit_module, delete_module, kexec_load, reboot,
swapon, swapoff, process_vm_readv, process_vm_writev
```

Same shape as Docker's default profile: threads (`clone`/`clone3`), `mmap`/`mprotect`,
`futex`, file + signal I/O stay available, so all seven languages run unmodified while the
escape surface is hard-blocked. **Verified:** the differential conformance suite passes under
enforce across all languages, and a submission calling `ptrace` is killed (`runtime_error`)
rather than succeeding. Two kafel footguns learned the hard way: an unknown identifier
(`umount2`, `kexec_file_load` aren't in this build) or a trailing comma after the rule block
fails the **whole** policy compilation, which silently disables the filter — covered now by a
CI smoke + ptrace-block gate.

**Terms & techniques**
- **nsjail:** process-isolation tool that launches each run inside fresh Linux namespaces.
- **Namespaces (mount/pid/net/user/ipc/uts):** kernel feature giving the run its own private view of the filesystem, processes, network, etc.
- **chroot / read-only rootfs:** the run sees a minimal root filesystem it cannot write to or escape.
- **rlimits:** per-process kernel limits (`rlimit_as` address space, `rlimit_fsize` file size, `rlimit_nproc`).
- **cgroup v2 (`memory.max`, `cpu.max`, `pids.max`):** kernel-enforced ceilings on the whole run tree; `pids.max` is a deterministic fork-bomb kill.
- **seccomp-bpf deny-list:** blocks dangerous syscalls (ptrace, mount, kexec, reboot, module loading) while DEFAULT-ALLOWing the rest to preserve JIT runtimes.
- **OOM detection:** cgroup memory accounting reports when a run was killed for exceeding `memory.max`.
- **No network:** network namespace with no interfaces — code cannot phone home.

> **ELI5:** Your code runs in a padded cell with no windows and no phone: it can't see
> other programs, can't use the internet, and gets only so much memory, CPU, and time
> before it's stopped. We also took away the kernel's "escape" buttons (like `ptrace`)
> while leaving normal buttons, so any code that tries to break out is instantly killed.

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

**Terms & techniques**
- **Per-step limits:** `{wall_time_s, memory_kb, max_processes}` applied separately to build and run phases.
- **`cpu_max_percent`:** server-side CPU quota mapped onto cgroup `cpu.max`.
- **Wall-time vs CPU time:** wall = real elapsed seconds (triggers `time_exceeded`); CPU = scheduler quota.
- **Partial override:** a request may tighten *some* limits; unspecified fields keep defaults.
- **No clamping / no ceiling raise:** overrides may only lower below the run defaults, never raise above them — prevents abuse.

> **ELI5:** Every job gets an allowance of time, memory, and number of helpers. You can
> ask for a different allowance, but the server never *secretly* shrinks it when busy —
> because then the same code could pass on a quiet day and fail on a busy one, which is unfair.

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

### Fast-lane fairness (C-3, `internal/api/handler.go`)
- The build lane throttles *compiles*, but light interpreted jobs still competed with heavy
  compiled jobs for the same run slots — a burst of slow `java`/`cpp` runs could head-of-line
  block a `py3` run. Fixed with a second admission cap.
- Heavy jobs (`lang.Build != nil`) take a token from a `heavy` semaphore of size
  `MaxConcurrency - FastLaneReserved` **before** acquiring a run slot; light jobs skip it. So
  `FastLaneReserved` run slots (default `max(1, MaxConcurrency/4)`) can never be held by heavy
  jobs — light requests always have admission headroom.
- **Lock ordering (deadlock-free):** heavy = `heavy` then run-slot; light = run-slot only. Light
  never holds `heavy`. The heavy lane is clamped to `>= 1`; `FastLaneReserved = 0` disables it.
- Pure admission ordering — never mutates per-run limits, so verdicts stay load-independent.
  Admission wait is exposed per lane as `goboxd_queue_wait_seconds{lane}`.

**Benchmarked:** under c=100 overload on a 4-CPU box, admitted requests hold a flat ~32 ms
p95 while the overflow gets 503s — vs the pre-C-1 behavior where everyone queued and p95
climbed to 368 ms. See `docs/benchmarks.md` (2026-06-09).

**Terms & techniques**
- **Admission lanes (fast / heavy):** dual-lane scheduling — light interpreted jobs reserve a `FastLaneReserved` slice so they aren't starved by heavy `java`/`cpp` compiles.
- **`heavy` semaphore:** sized `MaxConcurrency - FastLaneReserved`; heavy jobs must take a token from it.
- **Build lane (`buildSem`):** separate semaphore throttling *compiles*; build-token holders are a strict subset of run-slot holders.
- **Auto-disable:** fast lane turns off when `MaxBuildConcurrency >= MaxConcurrency` (no benefit).
- **Bounded queue + `MaxQueue`:** waiting room with a hard cap; overflow is shed as `503`.
- **`goboxd_queue_wait_seconds{lane}` / `goboxd_build_wait_seconds`:** metrics exposing wait time dimensioned per lane.

> **ELI5:** Like a restaurant with a fixed number of tables. If too many people show up,
> we politely turn extras away at the door ("come back in 2 min") instead of letting the
> line grow forever. Slow dishes that need a lot of cooking get their own limited burners,
> and we always keep a few tables free for quick orders so a fast eater never waits behind
> a giant feast.

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
- **Single-flight:** an in-process keyed semaphore spans `get → build → put`, so N identical
  concurrent submissions compile exactly once. The wait is **context-cancellable** — a client
  that disconnects mid-wait unblocks immediately instead of parking until the in-flight
  compile finishes.
- **Eviction:** count cap `CacheMaxEntries` (default 512), oldest-by-mtime evicted on
  insert; a startup TTL sweep clears stale entries. Commit + eviction are serialized under a
  cache-wide lock so concurrent `Put`s on different keys can't race each other's
  rename/`RemoveAll`. **All IO errors degrade gracefully to a miss** (build normally).
- **Scope:** only languages with a build step ever consult the cache; toggle with
  `cache_enabled`.

**Benchmarked:** identical C++ resubmissions — ~36× throughput at c=1 (build 143 ms →
replayed; p50 104 ms → 2.8 ms), 657 hits / 1 miss across a sweep, identical verdicts. See
`docs/benchmarks.md`. The two previously-logged best-effort limitations (non-cancellable
single-flight wait; cross-key eviction race) are now **fixed** and covered by a `-race` test.

**Terms & techniques**
- **Content-addressed cache:** key = `sha256(langID ⊕ toolchainVersion ⊕ sha256(source) ⊕ buildFlags ⊕ artifactFilename)`; identical inputs → same key.
- **Cache HIT/MISS:** HIT copies stored artifacts into the jail and replays build metadata; MISS compiles then `cache.Put()`.
- **Single-flight:** concurrent identical builds collapse into one compile; the rest wait on the result.
- **Cancellable single-flight:** a waiter that disconnects releases its wait via `context` cancellation instead of blocking.
- **Race-free eviction:** eviction serialized under a cache-wide `dirMu` so concurrent puts/evicts can't corrupt the dir.
- **LRU / `cache_max_entries`:** bounded entry count; least-recently-used artifacts evicted past the cap.

> **ELI5:** If the same code gets sent twice, we don't recompile it — we keep the finished
> program in a cupboard and reuse it, which is ~36× faster. We only ever reuse the *built
> program*, never an old score, and we still run every test fresh, so the grade is always
> honest. If the compiler gets upgraded, the cupboard is cleared so nothing stale is used.

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
| `goboxd_queue_wait_seconds{lane}` | histogram | time waiting for a run slot, by admission lane (light\|heavy) |
| `goboxd_inflight` | gauge | runs currently executing |
| `goboxd_requests_total` | counter | admitted /run requests |
| `goboxd_internal_errors_total` | counter | server-side failures |
| `goboxd_queue_depth` | gauge | requests in the admission section (C-1) |
| `goboxd_rejected_total` | counter | 503 admission sheds (C-1) |
| `goboxd_cache_hits_total{language}` / `…_misses_total{language}` | counter | artifact cache (C-2) |
| `goboxd_build_wait_seconds` | histogram | build-lane wait (C-1) |

A `docker compose up` brings up a full stack: goboxd (API `:8080`, metrics `:9090`), Prometheus
(scrapes `goboxd:9090`, UI `:9091`), and Grafana (`:3000`, anonymous admin) with an
auto-provisioned dashboard. Scrape config + provisioning live under `deploy/`. The dashboard
charts throughput by verdict, latency p95, queue/in-flight, 503 rate, cache hit ratio, and
admission wait by lane.

Plus standard Go runtime + process collectors.

### Logs & stats
Structured logs (`log/slog`) carry a request id (chi middleware) through the run. In-process
counters surface in `/info.stats` (jobs total/in-flight/failed, last internal error, disk
free, uptime).

**Terms & techniques**
- **Prometheus metrics:** counters/histograms scraped on the **separate admin port** `:9090` so submitters can't reach them.
- **Counter vs histogram:** `_total` metrics only increase; `_seconds` metrics are latency distributions (buckets).
- **Labels/dimensions (`{language,verdict}`, `{lane}`, `{phase}`):** tag values splitting a metric for slice-and-dice.
- **`goboxd_inflight` / `goboxd_queue_depth`:** live gauges of work in progress and queued.
- **`goboxd_rejected_total` / `goboxd_internal_errors_total`:** load-shed rejections vs server-side faults.
- **Grafana:** dashboard tool that visualizes the scraped Prometheus series.

> **ELI5:** The service keeps a scoreboard of how it's doing — how many jobs, how fast,
> how many were turned away, how often the cupboard trick worked — on a private back door
> so strangers can't peek. There's even a pretty live dashboard (Grafana) to watch it all.

---

## 10. Configuration reference (`server` block)

| Key | Default | Purpose |
|---|---|---|
| `port` | 8080 | public API port |
| `metrics_port` | 9090 | admin metrics port (≤0 disables) |
| `max_concurrency` | `runtime.NumCPU()` | concurrent run slots |
| `max_queue` | `2 × max_concurrency` | extra waiters before 503 (C-1) |
| `fast_lane_reserved` | `max(1, max_concurrency/4)` | run slots reserved for light jobs (C-3); `0` disables |
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

**Terms & techniques**
- **Defaults:** every key has a sensible fallback so a near-empty config still boots.
- **`runtime.NumCPU()` default:** concurrency auto-sizes to the box's core count.
- **Derived defaults:** `max_queue`, `fast_lane_reserved`, `max_build_concurrency` computed from `max_concurrency`.
- **Envelope vs field caps:** `max_body_bytes` (whole request) vs `max_source_bytes` / `output_cap_bytes` (sub-parts).
- **Disable sentinels:** `metrics_port ≤ 0`, `fast_lane_reserved = 0`, etc. turn a feature off.
- **`seccomp_mode` off/audit/enforce:** dry-run (audit logs would-be kills) vs hard kill (enforce).
- **kafel:** policy language compiled into the seccomp-bpf program per language.

> **ELI5:** All the knobs you can turn — ports, how many jobs at once, cache size, size
> limits — live in one settings list with sensible defaults, so you tune the machine
> without touching the code.

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

**Terms & techniques**
- **Makefile targets:** named one-word recipes (`make build`, `make test`…) wrapping the real commands.
- **`go vet` / `staticcheck`:** static analyzers catching suspicious code and lint issues pre-runtime.
- **nsjail submodule pinned to tag 3.4:** reproducible build of the isolation tool; `verify-nsjail` asserts the pin.
- **`go.uber.org/automaxprocs`:** sets `GOMAXPROCS` from the cgroup CPU quota so Go's scheduler doesn't oversubscribe in a capped container.
- **`--privileged`:** Docker flag granting nsjail the namespace/cgroup access it needs.
- **Graceful shutdown:** SIGINT/SIGTERM → `http.Server.Shutdown` drains in-flight jobs before exit.
- **`hey`:** HTTP load generator used by `make load`.

> **ELI5:** Simple one-word commands to build it, test it, and pack it into a shippable
> box (Docker). When you ask it to stop, it finishes the jobs it's holding before shutting
> the doors instead of slamming them.

---

## 12. Security model (summary)

Defense in depth: nsjail namespaces + chroot + all-deny network → cgroup v2 memory/CPU/PID
caps → **enforced seccomp-bpf deny-list** (blocks ptrace/bpf/mount/module-load/kexec/
process_vm_*…) → rlimits → output caps → bounded admission. Each
run gets a fresh 0700 jail dir (unique by atomic counter + PID + random hex); orphans from a
crashed run are swept at startup. Untrusted code cannot reach the network, exhaust host
memory/CPU/PIDs, escape the chroot, or influence another request. Full detail in
`docs/security.md`.

**Terms & techniques**
- **Defense in depth:** independent stacked layers so one breached layer isn't a full escape.
- **`process_vm_*` / ptrace/bpf/kexec/module-load:** syscalls the seccomp deny-list blocks (cross-process memory, debugging, kernel control).
- **0700 jail dir:** owner-only-permission scratch dir, one per run.
- **Unique naming (atomic counter + PID + random hex):** collision-free jail dir names, no predictability.
- **Orphan sweep at startup:** leftover jail dirs from crashed runs are reclaimed on boot.
- **Non-influence guarantee:** isolation ensures no run can observe or affect another request.

> **ELI5:** Many locked doors, one behind another. Even if bad code picks one lock, the
> next one stops it. Every job gets its own clean room that's thrown away after, leftover
> messes from crashes are swept up, and no job can spy on or mess with another.

---

## 12a. Testing & conformance

- **Unit + property tests** across 15 packages (`go test ./...`). Native `go test -fuzz`
  targets on the user-input surface: the placeholder resolver and flag expander
  (`internal/registry`), the verdict classifier (`internal/status`), and the nsjail argv
  builder (`internal/sandbox`) — the last asserts user tokens can never escape the `--`
  separator into the nsjail flag region (flag-injection guard).
- **Differential conformance** (`tests/conformance`, build tag `integration`): drives the live
  service with the reference implementation's own recorded fixtures
  (`pyjail/src/tests/testcases/<lang>/<case>/{request,reply}.txt`, proto-text) across all eight
  reference languages and asserts our verdict matches the reference. Runs under **seccomp
  enforce**. It even pins one place where **we are more correct than the reference**:
  `java/error_runtime` (divide-by-zero) — the reference records `OK`; goboxd correctly returns
  `runtime_error`, asserted as a known-reference-bug correction. `pyjail/` is kept local
  (gitignored), so the suite skips when the fixtures aren't present.
- **CI** (`.github/workflows/ci.yml`): `gofmt` + `vet` + `build` + `go test ./...`, plus a
  `smoke` job that builds the image, boots it `--privileged --cgroupns=host`, checks `/readyz`,
  runs `scripts/smoke_languages.sh` (all seven languages → `accepted`) under seccomp enforce,
  and asserts a `ptrace` submission is killed — the regression gate for the seccomp policy.

**Terms & techniques**
- **Property tests:** assert invariants hold over many generated inputs, not fixed examples.
- **`go test -fuzz`:** native fuzzer that mutates inputs to crash parsers/builders.
- **Flag-injection guard:** asserts user tokens can never cross the nsjail `--` separator into the flag region.
- **Differential / conformance testing:** run our service on the reference's own fixtures and assert verdicts match.
- **Proto-text fixtures:** recorded `request`/`reply` pairs in protobuf text format.
- **Known-reference-bug correction:** a pinned case (`java/error_runtime`) where goboxd is *more* correct than the reference.
- **CI regression gate:** the smoke job fails the build if any language breaks or a `ptrace` escape isn't killed.

> **ELI5:** We grade our own grader. We throw weird and random inputs at it to find bugs,
> and we re-run the official answer key's own example questions to prove we give the same
> grades (we even caught one spot where the official key was wrong and we're right). A robot
> checks all this on every change, including that escape attempts still get blocked.

---

## 12b. Demo UI — intentionally NOT bundled (`demo/`)

`demo/index.html` is an optional Monaco-editor page for manual exploration. It is
**deliberately decoupled** from the service and excluded from the deployable artifact.

**Why not bundled:**
- goboxd is a *headless* judge; its only trust boundary is the API + sandbox
  (nsjail/seccomp/cgroups). A web UI enforces nothing.
- A real submitter (or attacker) hits the API with `curl` and never touches the page,
  so any client-side check is bypassed trivially — client-side never reduces server
  security.
- Bundling would only enlarge the attack surface (XSS, CDN deps, a second port) for
  zero security gain and zero rubric points.

So the page is a standalone static file that calls the public API like any other
client. Browsers block cross-origin `fetch`, so the demo needs a CORS allowance —
`Server.demoCORSOrigin()` (env `GOBOXD_DEMO_CORS_ORIGIN` > config `demo_cors_origin`),
mounted in `Router()` only when non-empty. **Off by default, exact origin only, never
`*`, never production.** CORS is a browser convenience, not a security control (`curl`
ignores it). All server-returned text is rendered via `textContent` to prevent XSS.

**Terms & techniques**
- **Headless service:** no UI; usable only over the API.
- **Same-Origin Policy / CORS:** browser rule blocking cross-origin `fetch` unless the
  server opts in with `Access-Control-Allow-Origin`; only browsers enforce it.
- **Env-gated feature:** disabled unless `GOBOXD_DEMO_CORS_ORIGIN` is set — prod-safe default.
- **CDN-loaded Monaco:** editor pulled at runtime, no build step, kept out of the Go binary.

> **ELI5:** The fancy code-typing webpage is a toy kept in a separate box. The real
> robot grader doesn't need it and is safer without it, because bad guys skip the
> webpage and talk to the robot directly anyway.

## 13. Related docs

- `README.md` — terse quick-start.
- `docs/api.md` — full request/response schema.
- `docs/architecture.md` — component diagram + data flow.
- `docs/security.md` — isolation layers, seccomp, cgroups, load-shedding notes.
- `docs/benchmarks.md` — perf over time (incl. the 2026-06-09 C-1/C-2 results).
- `docs/improvements.md` — backlog, Antigravity-memo verdicts, post-merge follow-up notes.
- `docs/roadmap.md` — forward-looking gaps + shipped addendum.

**Terms & techniques**
- **Quick-start vs reference docs:** `README.md` is terse onboarding; `docs/api.md` is the full schema contract.
- **ADR (Architecture Decision Record):** a logged design choice + rationale (chi router, nsjail-from-source, artifact-not-result cache, 503-not-clamp).
- **Benchmarks doc:** time-series perf (incl. the 2026-06-09 C-1/C-2 results).
- **Roadmap / backlog:** forward-looking gaps and a shipped addendum tracking completed work.

> **ELI5:** A list of other instruction sheets — how to talk to it, how it's built inside,
> how it stays safe, how fast it is, and what's planned next — so you can dig deeper on
> whichever part you care about.

# goboxd — improvement backlog

Forward-looking engineering notes. Three sources, kept separate:

- **Part A** — gaps found by reviewing competing Stage-1 submissions (things rivals
  did that we did not).
- **Part B** — improvements beyond hackathon scope (production hardening).
- **Part C** — review of an external (Antigravity) improvement memo, each item
  validated against the current code and improved where the original was wrong,
  unsafe, or spec-violating.

Verified facts about the current code (so the gaps below are grounded):

- Concurrency gate = a single buffered channel `sem chan struct{}` sized to
  `MaxConcurrency` (`internal/api/handler.go:28,56`). Plain FIFO admission, no queue,
  no priority, no per-class isolation.
- cgroup v2 writes **only** `memory.max` and reads `memory.peak`
  (`internal/sandbox/cgroup.go:57,168`). **No `cpu.max`, no `pids.max`** at the cgroup
  level (process count is limited only by nsjail `--rlimit_nproc`).
- No seccomp / kafel policy anywhere in `internal/` or `configs/`.
- No `/metrics` endpoint; counters are exposed as plain JSON in `/info`.
- Every `/run` rebuilds from source — no compiler/artifact cache.
- Wall time enforced via nsjail `--time_limit` plus a Go `context` timeout
  (`internal/sandbox/sandbox.go:169`). No CPU-bandwidth cap.

---

## Part A — Gaps vs competing submissions

Each verified absent from our tree.

1. **seccomp-bpf syscall filtering** (seen in pr_46) — an explicit syscall allowlist
   layered on top of nsjail namespaces + rlimits. We rely on namespaces/rlimits only.
   Real defense-in-depth we skipped. (Expanded in Part C-4.)
2. **Property / fuzz tests** (pr_46) — argv builder, output capture and status
   classifier exercised with generated inputs. Ours are example-based only. Their
   output classifier is also a separate unit, more testable than our inline status
   mapping.
3. **Prometheus `/metrics` endpoint** (pr_23) — a scrapeable telemetry surface with
   histograms. We expose flat counters inside `/info` JSON only. (Expanded in Part C-6.)
4. **Modular worker-pool job queue** (pr_28 / pr_36 / pr_40 / pr_18 / pr_27) — an
   explicit queue with backpressure and queue-depth visibility, vs our bare channel
   semaphore (no queue metric, no fairness). (Expanded in Part C-1.)
5. **Deeper sandbox layering** (pr_28) — isolation concerns split across more files,
   easier to audit one mechanism at a time.
6. **Broader language coverage actually wired** (pr_36 ~10, pr_27 ~11) — more than our
   effective spec set. Breadth ≠ correctness, but coverage is real.

What we still hold over the field: only honest CI pipeline; nsjail pinned-submodule
built-from-source (reproducible vs assuming host nsjail); cgroup v2 memory accounting;
strict nested `build`/`run` request schema; AI development journals.

---

## Part B — Beyond-scope production hardening

### Isolation / security
- seccomp-bpf per-language allowlist (deny `ptrace`/`mount`/`keyctl`/`clone3`/`bpf`);
  set `no_new_privs`; drop all capabilities explicitly.
- Stronger sandbox tier option: gVisor or Firecracker microVM for untrusted code
  instead of namespaces alone.
- Read-only rootfs + size-capped tmpfs workdir; overlayfs upper-dir quota (beyond
  `rlimit_fsize`).
- Optional egress allowlist for build-time package fetch in an isolated network
  namespace (currently all-deny).

### Scale / architecture
- Async API: submit → job id → poll / SSE-stream results; decouple accept from execute.
- Persistent job bus (NATS / Redis) + stateless worker fleet; autoscale on queue depth;
  per-tenant fairness and quotas.
- Warm jail pools per language (compile cache, prewarmed base) to remove cold-start.
- Result cache keyed by `(lang, sha256(source), sha256(tests), toolchain_version)` →
  skip re-execution of identical submissions.

### Observability / ops
- Prometheus `/metrics` (latency histograms, per-language exec time, OOM/timeout/
  build-fail counters, queue depth) + OpenTelemetry spans build→run→grade.
- Structured per-run audit log (resource peaks, exit cause) for forensics.
- Add a startup probe and per-language smoke-staleness TTL on `/readyz`.

### Correctness / testing
- Property / fuzz tests for argv expansion, whitespace normalization, status
  classification.
- Differential test vs the reference `code_runner.py` to guarantee byte-identical
  grading.
- Golden-file conformance suite from spec examples; CI matrix running real nsjail in a
  privileged runner; `-race` + k6 load test + chaos test (kill jail mid-run → assert
  reaper correctness).

### Extensibility / product
- Toolchain version pinning + checksums per language manifest; reproducible builds +
  SBOM; cosign image signing + SLSA attestation.
- Multi-file projects, stdin fixtures, interactive/grader tests, per-test limit
  overrides, partial-credit grading.
- OpenAPI spec + generated SDKs; versioned `/v1`; API-key / mTLS auth + per-key rate
  limiting; web playground + run replay.

---

## Part C — Review of the Antigravity improvement memo

Each item: the original proposal, then **our verdict + improvement**. The memo is
directionally good; three items need correction (one is unsafe as written, one is
spec-violating, one under-specifies its cache key).

### C-1. Shortest-Job-First scheduling with starvation aging
**Memo:** replace the FIFO semaphore with a min-heap priority queue; cost =
`wall_time × (1 + mem_GB) × test_count`; subtract `wait_seconds × multiplier` for aging.

**Verdict: right problem (head-of-line blocking is real — confirmed, plain FIFO
channel), flawed mechanism.**

Improvements:
- **SJF needs the job size, which we don't have.** `wall_time_limit` is an *upper bound*,
  not expected runtime — a 10 s-limit job usually finishes in 50 ms. Ranking by the
  limit mis-estimates badly. Estimate instead from an **EMA of actual past runtime per
  `(language, source-hash)`**, falling back to a per-language median for cold entries.
- **A min-heap with aging is O(n) re-scoring** (every waiting job's priority drifts with
  time, forcing re-heapify) and adds lock contention on the hot admission path. Prefer a
  **multi-level feedback queue (MLFQ)** or, simplest and 90 % of the benefit, **two
  classes with separate semaphores**: a *fast lane* (interpreted / no build / small
  limits) and a *slow lane* (compiled / large limits). A flood of C++ compiles can no
  longer starve Python runs because they draw from a different pool. This is a ~30-line
  change vs a whole scheduler.
- **Bound the heavy class explicitly:** cap concurrent *build* steps (`MaxBuildConcurrency`)
  below total `MaxConcurrency`, since compilation is the CPU-heavy phase. This caps the
  blast radius without any priority math.
- For multi-tenant fairness later, layer **weighted fair queueing per API key** on top
  of the class split.

### C-2. Compiler & artifact caching
**Memo:** hash source + flags, store compiled artifact under `/tmp/goboxd/cache/`, skip
build on hash hit.

**Verdict: correct and high-value** (we rebuild every run — confirmed). Tighten the key
and the safety model:
- **Key must include the toolchain version**, not just source + flags:
  `sha256(source) + flags + compiler_id + compiler_version + target_arch`. Without the
  compiler version a toolchain bump serves a stale binary.
- **Cache the build artifact only; never short-circuit the run.** Always execute in a
  fresh jail even on a cache hit — caching the *binary* is safe (identical source ⇒
  identical binary), caching *run results* is not unless test inputs + limits are also
  in the key.
- **Only populate on a verified-successful build;** store content-addressed, read-only,
  and `exec` from a copy (never hand the canonical cached file to the sandbox).
- **Negative-cache build failures:** identical source that failed to compile returns
  `build_failed` instantly without re-invoking the compiler.
- Add **LRU eviction + total-size cap + single-flight** (so N concurrent identical
  submissions compile once, not N times — this alone is a big win under contention).

### C-3. Load-adaptive resource clamping
**Memo:** under high request rate, dynamically lower the allowed `wall_time_s` ceiling
for overrides and report it in a `warnings` field.

**Verdict: reject as written — it breaks grading determinism and violates the spec.**

The spec defines limit overrides as *"partial override, replaces the default, no
clamping/ceiling."* Silently lowering `wall_time_s` under load means the **same
submission gets `accepted` off-peak and `time_exceeded` at peak** — a judge must never
return load-dependent verdicts. Improve by separating *admission* from *limits*:
- **Do not mutate per-run limits.** Keep verdicts a pure function of `(source, tests,
  limits)`.
- **Shed load at the door instead:** when the queue is saturated, return `503` with a
  `Retry-After` header (and/or enqueue with backpressure), rather than degrading a run
  that was admitted.
- The memo's **`warnings` field is worth keeping** — but for *admission/queue* signals
  ("queued 4.2 s", "running degraded CPU share"), never for silent verdict-affecting
  changes.
- If we must throttle admitted work, throttle **CPU share** (slows wall clock) only for
  steps graded on **cpu-time**, not wall-time — otherwise throttling itself causes false
  timeouts (see C-5).

### C-4. seccomp syscall filtering (kafel)
**Memo:** use nsjail `--kafel_file`; allow `read`/`write`/`exit`, deny `mount`/`ptrace`/
`syslog`/`reboot`.

**Verdict: correct, keep — matches Part A-1.** Make it production-safe:
- **One policy per language class, not one global allowlist.** A static C++ binary needs
  a tiny set; interpreters and JITs (JVM, Node, PyPy) legitimately need `mmap` +
  `mprotect(PROT_EXEC)`, `futex`, `clone`, `epoll_*`. A single strict list will break
  the JIT languages.
- **Deny the full escalation/escape set**, not just the memo's four:
  `ptrace`, `mount`, `umount2`, `keyctl`, `add_key`, `bpf`, `clone3`, `unshare`,
  `setns`, `pivot_root`, `init_module`, `finit_module`, `kexec_load`, `reboot`,
  `process_vm_*`, `perf_event_open`.
- Combine with `no_new_privs=1` and an explicit capability drop (nsjail defaults help,
  but assert them).
- **Ship in audit/log mode first** (`SECCOMP_RET_LOG`) to discover which syscalls each
  real language smoke needs, then flip to enforce. Add a CI smoke per language that
  fails if the policy blocks a legitimate run.

### C-5. cgroup v2 CPU quota
**Memo:** write `cpu.max` (e.g. `50000 100000` = 0.5 core) so fork-bomb loops can't
starve the host.

**Verdict: correct gap — we set `memory.max` but never `cpu.max` (confirmed).** Watch
the verdict interaction:
- **Capping to a fraction of a core inflates wall-clock time**, so a CPU-bound test
  graded on `wall_time_s` can flip to a spurious `time_exceeded`. Two clean fixes:
  (a) grade CPU-bound limits on **cpu-time** (cgroup `cpu.stat usage_usec` or
  `rlimit_cpu`) rather than wall, or (b) give full cores but cap **total CPU-seconds**
  per run. Pick one and document it; don't mix wall-grading with a sub-core quota.
- **Also set `pids.max` at the cgroup level** — this is the deterministic fork-bomb kill
  (the memo's example is literally `fork()` spam). nsjail `--rlimit_nproc` helps per-uid
  but cgroup `pids.max` bounds the whole run tree.
- Consider `io.max` on the jail device for disk-IO fairness under load.
- Set these on the **same per-run cgroup** we already create for `memory.max`, so
  teardown/accounting stays in one place.

### C-6. Prometheus observability
**Memo:** add a `/metrics` endpoint with per-language latency histograms, queue
saturation, resource distributions, and verdict-type breakdown.

**Verdict: correct, keep — matches Part A-3 and pr_23.** Make it not foot-gun:
- **Control label cardinality.** Label by `language` and `verdict` (bounded); **never**
  by `source-hash`, request id, or filename — that explodes the time-series count and
  OOMs the scrape target.
- Use **OpenMetrics histograms** for latency (per language, per phase build vs run) and
  for **queue wait time** (ties back to C-1: queue depth + wait p50/p99 are the signals
  that prove the scheduler change worked).
- Add a **build-cache hit-ratio** gauge (proves C-2's value) and counters per verdict
  (`accepted`/`wrong_output`/`time_exceeded`/`memory_exceeded`/`build_failed`/
  `runtime_error`).
- Serve `/metrics` on a **separate admin port / network**, not the public API, to avoid
  leaking internal operational detail to submitters. Layer OpenTelemetry trace spans
  (build→run→grade) for per-run drill-down.

### Antigravity memo — summary table

| # | Idea | Verdict | Key correction |
|---|------|---------|----------------|
| 1 | SJF + aging | Right problem, wrong mechanism | size unknown → estimate from EMA; use 2-lane semaphores / MLFQ, not min-heap+aging |
| 2 | Compiler cache | Keep | add toolchain version to key; cache artifact not verdict; single-flight + LRU; negative-cache failures |
| 3 | Load clamping | **Reject as written** | spec forbids clamping; verdicts must be load-independent → shed load (503/Retry-After), don't mutate limits |
| 4 | seccomp/kafel | Keep | per-language policies (JITs need more); wider deny set; audit-mode first |
| 5 | cgroup `cpu.max` | Keep (real gap) | sub-core quota inflates wall time → grade on cpu-time; also add cgroup `pids.max` |
| 6 | Prometheus | Keep | cap label cardinality; admin-only port; add cache-hit + queue-wait metrics |

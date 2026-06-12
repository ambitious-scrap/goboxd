# goboxd Roadmap: Future Improvements & Architectural Evolution

This document outlines the architectural gaps, security hardening suggestions, and scalability improvements for the `goboxd` sandbox service. It incorporates verified technical findings from peer submissions in the hackathon, critical evaluation of grading correctness, and suggestions for enterprise-grade scalability.

---

## Part 1: Competitor Gaps & Opportunities
These are verified technical features and patterns from competing submissions that are absent from our current architecture:

### 1. Seccomp-BPF System Call Filtering
*   **Context**: Competing codebase PR #46 (Glitchbox) featured custom Seccomp system call filtering.
*   **Gap**: Currently, our sandbox relies on `nsjail` mount/user namespace isolation and `rlimits`. A kernel vulnerability (e.g., exploitation of a system call like `keyctl` or `clone3`) could permit a sandbox escape.
*   **Solution**: Layer a strict Seccomp-BPF policy over namespaces. Configure a whitelist using `nsjail`'s kafel policy language (`--kafel_file`). Limit calls strictly to memory/file IO essentials (e.g., `read`, `write`, `exit`, `mmap`, `futex`), dropping all linux capabilities and blocking `mount`, `ptrace`, `syslog`, and other high-risk calls.

### 2. Property-Based & Fuzz Testing
*   **Context**: PR #46 implemented fuzzing and property tests.
*   **Gap**: Our testing is currently example-based (checking expected inputs against outputs). This leaves edge cases in argument expansion, output truncation, or flag parsing untested.
*   **Solution**: Write Go fuzz tests (`go test -fuzz`) for:
    *   The placeholder template engine (`internal/registry/placeholders.go`) to check for crashes on malformed brackets/tokens.
    *   The sandbox argument builder to verify that no user inputs can inject unintended flags.
    *   The status classifier, confirming that arbitrary outputs do not lead to panic states.

### 3. Native Scrapeable Observability (`/metrics`)
*   **Context**: PR #23 implemented a Prometheus exporter.
*   **Gap**: We expose metrics solely via a JSON structure inside the `/info` endpoint. This is non-standard for infrastructure monitoring.
*   **Solution**: Integrate the Prometheus Go client package. Expose a `/metrics` scrape endpoint. Track latency percentiles (p50/p95/p99) for compiles/runs, cgroup-measured memory peak distribution histograms, and counter statistics for timeouts, OOM kills, and queue saturation.

### 4. Modular Job Queue & Backpressure
*   **Context**: PRs #28, #36, and #40 implemented modular queue managers.
*   **Gap**: We restrict concurrency using a bare channel semaphore in the API handler. This works but lacks observability (e.g., tracking the size of the wait queue) and backpressure interfaces.
*   **Solution**: Separate the queue into a dedicated `internal/queue` package that manages a thread-safe list. This allows the server to expose queue length in `/info` and enforce maximum queue capacity limits.

### 5. Deeper Sandbox Layering
*   **Context**: PR #28 separated nsjail interactions into distinct helper files.
*   **Gap**: Our `internal/sandbox` package is highly integrated, containing cgroup management, exec orchestration, and nsjail parameter formatting.
*   **Solution**: Decouple isolation drivers (cgroup, namespaces, namespaces mounts) from runner execution, making it easier to swap `nsjail` for other runtime sandboxes (e.g. gVisor).

---

## Part 2: Critical Corrections & Refinements

### 1. Throttling via API Backpressure, NOT Limit Clamping
*   **Correction**: We reject the "load-adaptive limit clamping" pattern implemented in PR #40.
*   **Rationale**: Dynamically lowering a user request's `wall_time_s` or `memory_kb` budget during high server traffic **violates grading determinism**. A user's code could pass (`accepted`) under low load but fail (`time_exceeded`) during a traffic spike solely because the server silently shrank its limits. This violates the requirements of an online judge.
*   **Improved Implementation**: Enforce static, deterministic resource limits for every submission. When load spike boundaries are hit, manage traffic via **API backpressure**:
    *   Establish a maximum queue capacity (e.g., 500 pending runs).
    *   Once full, reject new incoming requests with a `503 Service Unavailable`.
    *   Return a precise `Retry-After` estimation header based on average run duration ($T_{avg}$) and queue backlog:
        $$\text{Estimated Wait} = \frac{\text{QueueSize} \times T_{avg}}{\text{MaxConcurrency}}$$

### 2. Shortest Job First (SJF) Heap Scheduling with Starvation Aging
*   **Current State**: FIFO queueing can lead to Head-of-Line blocking where a batch of heavy compilation jobs starves quick Python runs.
*   **Shipped (C-3, partial)**: A **fast-lane reservation** now addresses the head-of-line case without a full priority queue. Heavy (compiled) jobs are capped by a `heavy` semaphore of size `MaxConcurrency - FastLaneReserved`, guaranteeing reserved run slots for light interpreted jobs. Pure admission ordering — verdicts stay load-independent. See `docs/architecture.md` and `PERSONAL_README.md` §7. A full cost-scored heap (below) remains future work.
*   **Improvement**: Implement a Priority Queue (Min-Heap) scheduler.
    *   **Cost Score**: Estimate execution complexity at insertion:
        $$\text{Job Cost} = \text{Wall Time Limit} \times \left(1.0 + \frac{\text{Memory Limit in KB}}{1048576.0}\right) \times \text{Test Case Count}$$
    *   **Starvation Prevention**: As a job waits, dynamically decrease its cost priority score:
        $$\text{Priority Score} = \text{Job Cost} - (\text{Wait Time in Seconds} \times \text{Starvation Multiplier})$$
    *   Pop the item with the lowest priority score first. This prioritizes fast executions while ensuring slow compiles run eventually.

---

## Part 3: Beyond-the-Hackathon Roadmap

### 1. Isolation & Security Hardening
*   **MicroVM Sandbox Tier**: Introduce support for **gVisor (`runsc`)** or **Firecracker MicroVMs** for untrusted code runs. Namespace isolation shares the host Linux kernel; a virtualization-based sandbox prevents kernel-level escapes.
*   **Overlayfs Disk Quotas**: Combine nsjail `--rlimit_fsize` with a true Overlayfs upper-dir disk quota. This prevents malicious scripts from filling host disk blocks by writing to `/tmp` via rapid directory/file creation.
*   **Egress Allowlisting**: Support isolated egress proxying for compilation blocks (e.g. enabling `cargo fetch` or `npm install` for third-party libraries at build time) while strictly keeping execution test runtimes network-disabled.

### 2. Scalability & Operational Architecture
*   **Asynchronous Submission API**: Transition from synchronous POST requests to an asynchronous pattern:
    1. `POST /runs` $\rightarrow$ Returns a `201 Created` with a `Job ID`.
    2. Client polls `GET /runs/{id}` or listens to a Server-Sent Events (SSE) / WebSocket stream for live execution logs.
*   **Distributed Worker Fleet**: Decouple the HTTP API layer from the sandboxed runners using a distributed message broker (e.g., NATS, RabbitMQ, or Redis). The API puts jobs on a queue, and stateless worker instances scale up/down dynamically based on queue depth.
*   **Warm Process Pools**: Runtimes like Java (JVM) or Node.js (V8) suffer from high cold-start latency due to JVM/garbage-collection startup. Maintain a pool of pre-warmed, suspended sandbox containers, waking them up instantly when code is sent to stdin.

### 3. Concurrency & Performance Caching
*   **Submission Execution Caching**: Hash the target language config, source code, and expected tests. If a subsequent submission matches the hash, return the cached test verdicts directly, reducing database/CPU load during typical classroom submissions.

### 4. Developer Experience & API Capabilities
*   **Multi-file Project Support**: Accept a file map (zip/tar or JSON structures) instead of a single source file, allowing sandboxed compilation of modular, multi-file codebases.
*   **Grader-style Evaluations**: Support grader files. Instead of comparing strings via stdout, compile the user's code against a hidden local test runner binary that executes assertions and reports structured metrics.
*   **Typed SDK Generation**: Expose a versioned `/v1` API documented with OpenAPI specs, allowing automated generation of client SDKs for frontend online judges.

---

## Shipped (appended 2026-06-09)

The items above are kept verbatim as the original forward-looking record. Several have
since landed; this section is **appended, not edited in place**. Cross-references:
`docs/improvements.md` (Part C verdicts), `docs/benchmarks.md` (2026-06-09 results),
commit `5a6e32d`.

*   **Part 1 §3 — `/metrics`**: DONE. Prometheus exporter on a separate admin port
    (commit `0561987`); bounded label cardinality (`language`, `verdict`), latency
    histograms per phase, plus cache-hit and queue-wait series.
*   **Part 1 §4 — Job queue & backpressure**: DONE (as bounded admission, not a separate
    `internal/queue` package). `waiting` atomic counter + `goboxd_queue_depth` gauge;
    capacity = `MaxConcurrency + MaxQueue`; overflow shed with `503` + `Retry-After`.
*   **Part 2 §1 — Backpressure not clamping**: DONE and benchmarked. `503` + `Retry-After`
    at saturation; per-run limits never mutated, so verdicts stay load-independent.
    Measured: flat ~32 ms p95 for admitted requests under c=100 overload (see benchmarks).
*   **Part 2 §2 — SJF heap scheduling**: PARTIAL / deliberately deferred. The head-of-line
    problem is addressed by a **build lane** (`buildSem` of `MaxBuildConcurrency`) that caps
    concurrent compiles below total run slots, so a flood of C++ builds can't starve light
    runs. The min-heap + starvation-aging design is **not** shipped: ranking by the
    wall-time *limit* mis-estimates real job size (see `docs/improvements.md` C-1). Revisit
    only with an EMA-of-actual-runtime signal.
*   **Part 3 §3 — Execution caching**: DONE as an **artifact** cache, NOT a full result
    cache. `internal/artifactcache` is content-addressed and toolchain-versioned, caches
    the compiled binary only, and always re-runs in a fresh jail (verdict-neutral).
    Caching test *verdicts* was rejected: nondeterministic programs would return stale
    results. ~36× C++ throughput on identical resubmissions (see benchmarks).
*   **Seccomp-BPF (Part 1 §1)**: DONE and **enforced by default** (2026-06-10). Mechanism
    landed in `dbc446a`; now a shared kafel deny-list (`DEFAULT ALLOW`, killing
    ptrace/bpf/mount/module-load/kexec/process_vm_*/namespace ops) is applied to every
    language with `seccomp_mode: enforce`. Verified end-to-end (all langs run; `ptrace`
    killed). cgroup `cpu.max` / `pids.max` shipped in `9b6f431`. See `docs/security.md` §8.
*   **Property/fuzz testing (Part 1 §2)**: DONE. Native `go test -fuzz` on the placeholder
    resolver/flag expander, status classifier, and nsjail argv builder (flag-injection guard).
    Plus a **differential conformance suite** (`tests/conformance`) that runs the reference
    implementation's own fixtures through the live service under enforce — which also pinned a
    bug in the reference (`java/error_runtime`) where goboxd is the more-correct one.

Still open from this doc: deeper sandbox layering (Part 1 §5), microVM tier, async API,
distributed workers, warm pools, multi-file / grader / SDK (Part 3 §1, §2, §4).

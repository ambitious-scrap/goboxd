# Architectural Decision Records

Short records for non-obvious design choices. "Non-obvious" means: a reasonable engineer might have made a different call, and the reason I didn't is worth capturing.

---

## ADR-001: Data-driven language registry (YAML + install scripts, zero Go changes per language)

**Date:** 2025-05-26

**Status:** Accepted

**Context:**
The scoring criteria rewards "plug-and-play" extensibility (25% weight) and gives a bonus for extra languages (10%). The question was whether to handle each language in Go code or make language config fully external.

**Options considered:**

1. **Hardcoded map in Go.** Simple to start. Every new language requires editing Go source, rebuilding the image, and touching the runner — risk of inadvertently adding language-specific branches.

2. **YAML config + placeholder templating.** Language behavior described entirely in config. The engine resolves `{{source}}`, `{{artifact}}`, `{{flags}}`, `{{workdir}}` but never checks language ID. "Has a build block" = compiled; absent = interpreted. New language = YAML block + shell script.

3. **Plugin system / dynamic loading.** Overkill for a hackathon. Adds complexity with no scoring benefit over option 2.

**Decision:** Option 2.

**Rationale:**
Option 2 hits the scoring criteria directly. The implementation cost of the templating engine is one small function (`registry/placeholders.go`). Once that's working, 7 languages cost about as much as 2. Option 1 can't score extensibility points no matter how clean the code is.

**What was rejected and why:**
Option 1 was rejected specifically because adding C++ would have required touching the same Go files as adding Python — there's no way to demonstrate zero-Go-change extensibility if Go files are involved.

---

## ADR-002: nsjail as git submodule, built from source at image build time

**Date:** 2025-05-26

**Status:** Accepted

**Context:**
The spec explicitly requires nsjail 3.4 built from source as a git submodule at `external/nsjail`. This isn't really a decision — it's a spec requirement. Recording here anyway because the implementation has meaningful implications.

**Options that were disqualified:**

1. `apt-get install nsjail` — spec says no, also gets a version I don't control.
2. Bundling a prebuilt binary — spec says no.
3. Downloading from GitHub releases at build time — not a submodule, spec says no.

**Decision:** git submodule at `external/nsjail`, pinned to tag 3.4, `make` in Dockerfile builder stage.

**Implications:**
- Cold `docker build` takes longer (compiling nsjail + protobuf deps)
- Dockerfile must install nsjail's build dependencies (bison, flex, libprotobuf-dev, etc.) in the builder stage
- Runtime image needs `libprotobuf23` because nsjail links against it dynamically (static linking didn't work cleanly on Ubuntu 22.04)
- Submodule must be initialized (`git submodule update --init`) before `docker build`

---

## ADR-003: SandboxRunner interface in runner package

**Date:** 2025-05-27

**Status:** Accepted

**Context:**
The runner package orchestrates build and test execution. The sandbox package is the only place nsjail is invoked. If the runner depends directly on the concrete sandbox type, unit tests for the runner require nsjail to be installed and the tests run real subprocesses — slow and environment-dependent.

**Options considered:**

1. **Direct dependency on `*sandbox.NsjailRunner`.** Simple. Tests need nsjail or heavy mocking at the `exec.Command` level.

2. **`SandboxRunner` interface.** Runner depends on an interface. Tests inject a fake that replays scripted results. Production code injects the real sandbox.

3. **Build tag to swap sandbox implementation.** More complex, same benefit as option 2 without the clarity.

**Decision:** Option 2. `SandboxRunner` interface in `internal/runner/runner.go`.

**Rationale:**
The fake sandbox in `tests/runner_e2e_test.go` covers all status paths (accepted, wrong_output, whitespace_mismatch, runtime_error, time_exceeded, memory_exceeded, build_failed, not_executed) without spawning a single process. Adding test cases is trivial — add a scripted result, assert on the status. This wouldn't be possible without the interface.

**What was rejected:**
Option 3. Build tags work but they're invisible — you have to know to look for them. An interface is explicit about the abstraction boundary.

---

## ADR-004: Buffered channel as concurrency semaphore

**Date:** 2025-05-26

**Status:** Accepted

**Context:**
nsjail sandbox setup is heavyweight. Allowing unlimited concurrent requests would exhaust file descriptors, cgroup limits, or memory before the requests could even start executing user code. Need to bound concurrency.

**Options considered:**

1. **`golang.org/x/sync/semaphore` package.** Weighted semaphore, well-tested. External dependency for a thing Go can do in 3 lines.

2. **Worker pool with goroutines pre-spawned.** Fixed goroutine count, requests distributed via channel. More complex, doesn't add anything useful here since the goroutines just call sandbox.Run anyway.

3. **Buffered channel as counting semaphore.** `sem := make(chan struct{}, n)`. Acquire: `sem <- struct{}{}`. Release: `<-sem`. Blocks on context cancellation naturally when wrapped with select.

**Decision:** Option 3.

**Rationale:**
No external dependency. Three lines. Callers block on slot acquisition bound by request context — if the client disconnects, the context cancels and the request is dropped cleanly. The default limit is `runtime.NumCPU()`, configurable via `GOBOXD_MAX_CONCURRENCY`.

**What was rejected:**
Option 1 mainly because it's an external dependency for something this simple. Option 2 because worker pools add a queue that complicates backpressure.

---

## ADR-005: Chroot + explicit system bind mounts over per-request bind mounts

**Date:** 2025-05-28

**Status:** Accepted

**Context:**
Each sandbox request needs access to the language toolchain (Python interpreter, g++, node, etc.) but must be isolated from the host filesystem and from other requests. Two approaches:

**Options considered:**

1. **`--bindmount workdir:/workdir --cwd /workdir`.** Mount the request workdir at a fixed inner path. Language toolchain is accessible at its normal host paths via nsjail's default passthrough. Less isolation but simpler setup.

2. **`--chroot workdir -B /bin -B /usr -B /lib -B /lib64 -B /dev -B /etc -B /tmp`.** Workdir becomes the filesystem root. System directories bind-mounted read-only. Complete filesystem isolation per request.

**Decision:** Option 2.

**Rationale:**
Option 2 matches the pyjail reference implementation, which is significant for spec conformance. It also provides better isolation: the sandbox has no access to host directories not explicitly provided. The workdir-as-root model means YAML config paths like `./solution` (run command) work naturally without needing to know the inner mount path.

**What was rejected:**
Option 1. Works, but the inner path `/workdir/solution.py` would need to be threaded through the YAML config or computed at runtime, complicating placeholder resolution. Also weaker isolation.

---

## ADR-006: Per-request cgroup v2 for memory limiting and accounting

**Date:** 2025-05-29

**Status:** Accepted (supersedes the original rlimit-only approach)

**Context:**
The spec requires `memory_exceeded` to be distinct from `runtime_error`, and `memory_peak_kb` to be reported per test. The first implementation capped memory with nsjail's `--rlimit_as` and read OOM state from the host-global `/sys/fs/cgroup/memory.events`. Both were wrong: an address-space rlimit makes allocation fail (the program crashes → `runtime_error`) instead of triggering an OOM kill, and a host-global counter can't attribute a kill to one request.

**Options considered:**

1. **Keep `--rlimit_as` only.** Simplest. But `memory_exceeded` is unreachable and `memory_peak_kb` is unavailable — fails the spec.

2. **rlimit + read host-global `memory.events`.** What was there. False positives from any OOM elsewhere on the host; peak is host-wide. Effectively broken.

3. **Per-request dedicated cgroup v2.** Create `/sys/fs/cgroup/goboxd/<jail-id>` per run, set `memory.max`, run the process inside it via nsjail's cgroup flags, read that cgroup's `memory.events` (recursive `oom_kill`) and `memory.peak`, then remove it.

**Decision:** Option 3, with `--rlimit_as` retained as a secondary layer.

**Rationale:**
Only owning the cgroup gives correct, per-request memory semantics. cgroup v2's `oom_kill` counter is recursive, so reading the parent cgroup (whose path we create and therefore know) captures the child cgroup nsjail spawns — no need to discover nsjail's child-cgroup name. `memory.peak` on that cgroup is the real per-run peak.

**What was rejected and why:**
Options 1 and 2 both fail the spec's memory semantics. Also rejected: locating nsjail's auto-created child cgroup by name (unnecessary given recursive accounting). All cgroup operations are best-effort — on a host without a writable cgroup v2 tree, accounting degrades silently and the rlimit still enforces the cap, so the service never fails a request just because cgroups are unavailable.

**Implementation:** `internal/sandbox/cgroup.go`, wired into `internal/sandbox/sandbox.go`; startup orphan sweep in `cmd/goboxd/main.go`.

---

## ADR-007: automaxprocs library over a hand-rolled GOMAXPROCS setter

**Date:** 2025-05-29

**Status:** Accepted

**Context:**
In a container with a CPU quota below the host core count, Go defaults `GOMAXPROCS` to the host count, oversubscribing OS threads and hurting tail latency — which the concurrency benchmarks measure directly.

**Options considered:**

1. **`go.uber.org/automaxprocs`** (blank import). Reads the cgroup CPU quota at startup, handles cgroup v1 and v2 and fractional quotas. Cost: one external dependency.

2. **Hand-rolled parser** of `/sys/fs/cgroup/cpu.max`. No dependency. Must handle the `max` (unlimited) sentinel, fractional quotas, and cgroup v1 vs v2 layout differences.

**Decision:** Option 1.

**Rationale:**
The decision was made on fit for the goal, not on a project rule to use it. The library covers cgroup v1/v2 and fractional quotas that a quick parser would get wrong, and the thing it affects — scheduler behavior under load — is exactly what the benchmarks grade. The dependency is small and widely used.

**What was rejected and why:**
The hand-rolled parser — fewer dependencies, but more edge cases to get right for no measurable benefit. If dependency count were a hard constraint it would win; here it isn't.

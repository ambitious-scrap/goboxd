# PROJECT_HANDOFF.md

Single-file continuation context for another coding agent. Read this top to bottom; you can resume development without further context.

---

## 1. Project Overview

**goboxd** — sandboxed code execution HTTP service in Go. Built for the SEEK/Paradox hackathon (IIT Madras). Runs arbitrary user code in isolated nsjail containers and judges stdout against expected test cases, returning structured per-test pass/fail.

- **Spec:** https://intern-iitm.github.io/goboxd-hackathon/spec.html
- **Solo project** (one-person team). Use first-person singular in all docs/commits.
- **Language:** Go 1.26.3. **Module:** `github.com/ambitious-scrap/goboxd` (dev path — see §11 for submission rename).
- **Branch:** `team/goboxd`. Git repo is real (the env banner saying "not a git repo" is wrong).

### Workflow constraints (hard rules)
- **Local-only.** Nothing is pushed anywhere without explicit user consent. All work stays on this laptop until the user says otherwise.
- At submission (and only then, with consent): rename module `ambitious-scrap` → `thesouldev`, push to `thesouldev/goboxd`.
- **No AI-sounding prose** in README/docs: no "elegant/robust/seamlessly/leverage", no emoji, no marketing. Write like a tired engineer documenting for a teammate. Spec penalizes AI prose.
- Small conventional commits. Commit history is itself scored (SDLC signal). No "implement everything" commits.

---

## 2. Goals (scoring-aligned)

| Goal | Why it matters |
|------|----------------|
| 100% data-driven language registry (YAML + install scripts, **zero Go changes per language**) | Wins 25% plug-and-play + 10% extra-languages simultaneously. The single highest-leverage decision. |
| Exact API contract conformance (status codes, error shapes) | Scoring-critical; see §6. |
| All 7 security holes closed + documented with `file:line` | Judging deliverable (`docs/security.md`). |
| Distinct `memory_exceeded` / `time_exceeded` / `runtime_error` | Spec requirement; needs real cgroup accounting. |
| Benchmarks p50/p95/p99 at 1/10/50/100 concurrency from clean `docker run` | Explicit deliverable (`docs/benchmarks.md`). |
| Structured per-request JSON logs | Explicit bonus criterion. |

---

## 3. Architecture

```
cmd/goboxd/main.go      flags, config load, server wiring, graceful shutdown, startup sweeps
internal/config/        YAML load + validate, placeholder parsing, defaults merge
internal/registry/      language registry: lookup, smoke probes, placeholder resolution
internal/api/           HTTP handlers, request decode, validation, error shaping
internal/runner/        execution engine: build -> run tests -> map status (pure orchestration)
internal/sandbox/       nsjail wrapper + cgroup v2 (ONLY package that knows nsjail/cgroups exist)
internal/limits/        limit merge (defaults (+) request override), clamping
internal/flags/         per-language compiler-flag allow-list matching
internal/jail/          jail dir lifecycle: unique create, cleanup, startup orphan sweep
internal/status/        status mapping + output comparison (incl. whitespace rule)
internal/obs/           structured logging, stats counters
configs/languages.yaml  the 7 language definitions
scripts/lang_install/   one install script per language id, invoked from Dockerfile
external/nsjail         git submodule, tag 3.4 (nested kafel submodule)
pyjail/                 organizer reference implementation (read-only; do not ship)
docs/, docs/ai/         deliverable docs + AI development log
tests/                  e2e tests via fake sandbox
```

**Key separation (do not violate):** `runner` is pure orchestration, unit-testable with a fake sandbox. `sandbox` is the only package touching nsjail/cgroups. Never let language-specific branches leak into `runner` or `sandbox` — push everything into YAML config. The only branch is "has a `build` block" = compiled; absent = interpreted.

### Data-driven registry mechanics
Placeholders `{{source}}`, `{{artifact}}`, `{{flags}}`, `{{workdir}}` are resolved by the engine; it never checks language id. Adding a language = YAML block + `scripts/lang_install/<id>.sh` + `docker build`. No Go change.

- `registry.Resolve([]string, vars)` — resolves a slice (args).
- `registry.ResolveOne(string, vars)` — resolves a single string (the run/build **cmd**). Added this session; see §10 Bug 1.
- `{{flags}}` is special: skipped by `Resolve`, then expanded element-by-element by `ExpandFlags` so multi-flag strings don't collapse into one argv element.

### Sandbox model
nsjail with `--chroot {workdir} --cwd /`, system dirs bind-mounted read-only, per-request cgroup v2 for memory. Build a read-only base rootfs once; per request create a small writable workdir. Bind mounts are stat-guarded (`/lib64` absent on arm64 — nsjail aborts the whole jail if a bind source is missing).

### Concurrency
Buffered-channel semaphore, default `runtime.NumCPU()`, env `GOBOXD_MAX_CONCURRENCY`. Requests queue on slot acquisition (bound by request context); never `503` under load unless genuinely OOM. `go.uber.org/automaxprocs` sets GOMAXPROCS from cgroup quota.

---

## 4. Implementation Progress

**Status: functionally complete and validated end-to-end. 7/7 languages execute correctly against a real built image.**

| Area | State |
|------|-------|
| Language registry + placeholders | Done |
| All 7 languages (py3, c, cpp, java, bash, javascript, verilog) | Done — 7/7 accepted on real image |
| Status mapping (all 6 distinctions) | Done + verified end-to-end |
| Per-request cgroup v2 memory accounting | Done — `memory_peak_kb` nonzero, `memory_exceeded` fires |
| nsjail submodule (tag 3.4 + kafel) | Done |
| Dockerfile (multi-stage, all 7 install scripts) | Done, builds clean on Colima arm64 |
| Unit + e2e tests (fake sandbox) | 18 pass |
| Benchmarks (valid numbers) | Done — re-run this session after fixing broken sandbox |
| Docs (api/architecture/security/languages/benchmarks) | Present |
| AI dev log (docs/ai/) | Present; postmortem.md still in progress |

---

## 5. Completed Work (this session)

1. **Per-request cgroup v2** (`internal/sandbox/cgroup.go`): dedicated `/sys/fs/cgroup/goboxd/<jail-id>` per run, `memory.max` + `memory.swap.max=0`, recursive `oom_kill` read, `memory.peak` read, cleanup, startup orphan sweep.
2. **Fixed 3 execution bugs** found only by running real code in the built image (see §10 — all resolved).
3. **rlimit_as → cgroup** as authoritative RSS cap. `--rlimit_as` now used only as fallback when no cgroup (it caps *virtual* address space, which breaks Node/V8 and the JVM).
4. **`sync.Once`** guarding cgroup controller setup (was a data race across request goroutines).
5. **automaxprocs** blank import in `main.go`; cgroup orphan sweep wired after jail sweep.
6. **Dockerfile**: builder `golang:1.26-bookworm` (matches go.mod), unpinned nsjail-builder apt versions (arch-portable), all 7 install scripts.
7. **Makefile**: `verify-nsjail` asserts submodule tag `3.4*`; `docker-build` depends on it.
8. **Valid benchmarks** re-run + `docs/benchmarks.md` rewritten (old numbers measured failed requests).
9. **docs/ai/issues.md** Issue 7 documents the 3 execution bugs.

**All session work is UNCOMMITTED** (local-only constraint). `git status` shows modified + untracked files. Commit when the user approves.

---

## 6. API Contract (get exact — scoring-critical)

- `POST /run` returns **200 even when user code crashes**. `5xx` = server failure only (nsjail missing, disk full, sandbox setup error). Never `5xx` for user-code outcomes.
- Top-level status = `accepted` only if `build.status == ok` AND every test `accepted`; else = **first non-accepted test status in order**.
- Build fails → top-level `build_failed`, every `tests[].status = not_executed`.
- `output_whitespace_mismatch` distinct from `wrong_output`: compare exact first; if unequal, normalize whitespace; if then equal → `output_whitespace_mismatch`, else `wrong_output`.
- `time_exceeded` / `memory_exceeded` / `runtime_error` distinguished: cgroup `memory.events` oom_kill → memory; nsjail time-limit kill or SIGKILL-at-wall-limit → time; other non-zero exit → runtime_error. **OOM checked before timeout** (both arrive as exit 137).
- `400` for: bad JSON, unknown language, oversize body, malformed filename, disallowed flag. Shape: `{"error":{"code","message"}}`.
- Always echo `build.duration_ms`, per-test `duration_ms` and `memory_peak_kb` (even when zero).
- Endpoints: `/healthz` (liveness), `/readyz` (nsjail + per-language smoke probes; 503 with breakdown if degraded), `/info` (build info, nsjail version, per-language versions+limits, stats).

### Request field names (exact — source of a past bug)
- Test case expected output field is **`expected_stdout`** (not `expected_output`).
- Java needs request fields **`source_filename`** (e.g. `Main.java`) and **`artifact_filename`** (e.g. `Main`) — its YAML uses `from_request` strategy.

---

## 7. Security (7 holes — all must stay closed; documented in docs/security.md)

1. Path traversal via filename — validate `^[A-Za-z0-9._-]+$`, assert `filepath.Base(name) == name`
2. Shell invocation — never `sh -c`, always `exec.Command` with explicit argv
3. Compiler-flag injection — per-language allow-list; reject unmatched
4. Request size limits — `http.MaxBytesReader` + `rlimit_fsize` in nsjail
5. UID/dir collisions — atomic counter + PID + `crypto/rand` suffix for jail dir names
6. Unbounded child output — `limitedWriter` hard cap + truncation marker
7. Stale jail dirs — `defer` cleanup per-request + startup orphan sweep (same for cgroups)

---

## 8. Technical Decisions (ADRs — full text in docs/ai/adrs.md)

- **ADR-001** Data-driven YAML registry over hardcoded Go (extensibility scoring).
- **ADR-002** nsjail as submodule built from source, tag 3.4 (spec requirement).
- **ADR-003** `SandboxRunner` interface so runner is unit-testable with a fake.
- **ADR-004** Buffered-channel semaphore for concurrency (no external dep).
- **ADR-005** chroot + explicit system bind mounts over per-request bindmount (isolation + matches pyjail reference).
- **ADR-006** Per-request cgroup v2 for memory limit + accounting (supersedes rlimit-only).
- **ADR-007** `automaxprocs` over hand-rolled GOMAXPROCS parser.

---

## 9. Setup & Commands

```bash
# Prereqs: Go 1.26.3, Docker. No native Docker on macOS dev box -> Colima:
#   colima start --arch aarch64 --cpu 4 --memory 6   (cgroup v2 VM)

# Submodules (REQUIRED before docker build — kafel is nested in nsjail):
git submodule update --init --recursive

# Build / test / lint
make build
go test ./...                 # 18 tests, fake sandbox, no Docker needed
make lint

# Single test
go test ./internal/status/... -run Test...

# Docker (memory accounting needs --privileged or a writable cgroup v2 tree)
docker build -t goboxd:bench .
docker run -d --name goboxd-bench --privileged -p 8080:8080 goboxd:bench

# Benchmark (hey not on host; installed locally this session at ./.tools/hey)
GOBIN=$(pwd)/.tools go install github.com/rakyll/hey@latest
scripts/bench.sh http://localhost:8080 500
```

### Quick 7-language smoke (after `docker run`)
Use `/tmp/validate2.py` (written this session) — POSTs a hello-world per language, prints top/test status + `memory_peak_kb`. Expect `7/7 accepted`. Java payload must include `source_filename:"Main.java"`, `artifact_filename:"Main"`.

---

## 10. Bugs / Issues

### Fixed this session (root-caused via real-image validation, NOT unit tests)
1. **Run/build `cmd` not placeholder-resolved.** Runner resolved args but passed `Run.Cmd`/`Build.Cmd` raw → C/C++ exec'd literal `./{{artifact}}` → exit 127 → `runtime_error`. Fixed with `registry.ResolveOne` in `internal/runner/runner.go`. (Verilog masked it — its artifact is in args.)
2. **cgroup memory controller never enabled in container** → all `memory_peak_kb=0`, rlimit_as fallback ran for everything, which strangled VM-runtime virtual reservations (javac `build_failed`, Node `Fatal OOM in CodeRange setup`). Root cause: cgroup v2 "no internal process" rule — the namespace-root cgroup held the service procs. Fixed: `enableMemoryController` evacuates root procs to `/sys/fs/cgroup/_svc` leaf, then enables `+memory`, verifies via `cgroup.controllers`.
3. **Absurd memory limits** (C/C++ build 10 MB, run 1 MB) killed `cc1` → segfault once enforced. Fixed in `configs/languages.yaml`: C/C++ build 1 GiB / run 256 MiB, Java 512 MiB, Node 256 MiB.

### Open / to verify
- **Limit override clamp may be off.** In a `memory_exceeded` test the request sent `limits.memory_kb: 65536` but `memory_peak_kb` reported ~102400. `memory_exceeded` still fired correctly, but the applied cap didn't match the override. Investigate `internal/limits/Merge` + how the API threads `limits` into the runner. Low severity, but verify before trusting per-request limit overrides.
- **cgroup is best-effort.** Without `--privileged` (or a delegated writable cgroup v2 tree) accounting silently degrades to rlimit-only — no OOM/peak. Consider a one-line log when `cg.ok` stays false (currently silent `_ =` writes hid bug #2 for a long time).
- **No per-language integration test.** All 3 fixed bugs were invisible to the fake-sandbox unit tests. A "build + run trivial program, assert stdout" test per language against the built image would have caught all three. Worth adding.

### No currently-open functional bugs. 7/7 languages + all 6 status distinctions verified.

---

## 11. Pending Tasks

1. **Spec conformance gap (per-step limits/flags).** Spec shows per-step `build`/`run` objects each carrying their own `limits` + `flags`; current API takes only a single top-level `flags`. Reconcile request schema + runner. **This is the most substantive open item.**
2. **Finish `docs/ai/postmortem.md`** (marked in progress).
3. **Add per-language integration tests** (see §10).
4. **Verify limit-override clamp** (see §10).
5. **Commit the session work** in small conventional commits once user approves (currently all uncommitted).
6. **At submission only, with explicit consent:** rename module `github.com/ambitious-scrap/goboxd` → `github.com/thesouldev/goboxd` across all Go files + `go.mod`, then push to `thesouldev/goboxd`. One-time global find/replace.

---

## 12. Blockers

None currently. Service is functional end-to-end. The only environmental dependency is a writable cgroup v2 tree for memory accounting (have it via `--privileged` on Colima; degrades gracefully otherwise).

---

## 13. Next Recommended Actions (in order)

1. Resolve the per-step `limits`/`flags` schema gap (§11.1) — highest scoring/conformance value.
2. Verify the limit-override clamp (§10) — small, de-risks per-request limits.
3. Add one per-language build+run integration test against the image (§10).
4. Finish postmortem.md.
5. Stage small conventional commits for the session work, get user approval.

---

## 14. Exact Continuation Context

- **CWD:** `/Users/dinesh/Documents/Projects/Golang/goboxd`
- **Branch:** `team/goboxd`. Last commit `e88ec84` (spec conformance). All work after that is uncommitted in the working tree.
- **Running container:** `goboxd-bench` from image `goboxd:bench` (rebuild after any Go/config change — Docker recompiles the Go layer; nsjail layer is cached).
- **Rebuild loop:** edit → `go test ./...` → `docker build -t goboxd:bench .` → `docker rm -f goboxd-bench` → `docker run -d --name goboxd-bench --privileged -p 8080:8080 goboxd:bench` → `python3 /tmp/validate2.py`.
- **Caveman mode** is active in the user's session (terse replies). Write code/commits/docs normally; keep chat terse.
- **Memory:** persistent notes live in `~/.claude/projects/-Users-dinesh-Documents-Projects-Golang-goboxd/memory/` (spec content, refs, submission workflow). Honor the local-only + solo-team constraints recorded there.
- **Validation truth:** the API returns 200 even for user-code failures, so HTTP status hides broken execution. **Always validate by decoding the JSON body's `status`/`tests[].status`, never by HTTP code.** This masked a fully-broken sandbox during an earlier benchmark run.
- **Files changed this session:** `internal/sandbox/sandbox.go`, `internal/sandbox/cgroup.go` (new), `internal/runner/runner.go`, `internal/registry/placeholders.go`, `configs/languages.yaml`, `cmd/goboxd/main.go`, `Dockerfile`, `Makefile`, `scripts/lang_install/{bash,verilog}.sh`, `scripts/bench.sh` (new), `docs/benchmarks.md`, `docs/languages.md`, `docs/ai/*`, `docs/api.md`.

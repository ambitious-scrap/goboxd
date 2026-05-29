# goboxd — Plan to Win

A tactical build + scoring plan for the SEEK / Paradox hackathon. Written against the published rubric and the three-stage structure. Assumes you're fluent in Go and Linux isolation, so this is strategy and prioritization, not tutorial.

---

## 0. The one insight that decides the outcome

The rubric is public, and it is lopsided. Spend your effort where the weights are.

| Criterion | Weight | What actually moves it |
|---|---|---|
| Plug-and-play language model | **25%** | A data-driven engine: zero Go change to add a language. Templated cmd/args, install scripts wired into the Docker build. |
| Concurrency | **20%** | Bounded worker pool + real queue, low per-request setup cost, honest p50/p95/p99 benchmarks, survives sustained load. |
| API contract conformance | 15% | Every field, every status value, every 400-vs-5xx case. Mechanical, fully winnable. |
| Security holes closed | 15% | Close all 7, not the required 5. Each extra = half a required one. |
| Code quality, tests | 10% | Clean packages, `staticcheck` clean, table-driven tests, honest commit history, human-written README. |
| Languages beyond the seven | 10% | **Free** if the registry is truly plug-and-play — each extra language is one YAML block + one install script. |
| Health endpoints | 5% | `/healthz`, `/readyz`, `/info` accurate and complete. Cheap, mechanical. |

**The compounding play:** the language registry is not 25% — it's effectively **35%**. A genuinely data-driven registry wins the 25% plug-and-play score *and* hands you the 10% "extra languages" bonus for almost no marginal work (Rust, Go, Kotlin, Ruby, Lua, etc. each become a YAML block + an apt/install line). Every hour spent making the registry truly code-free pays out twice.

So the priority order is: **registry engine → concurrency → API conformance → security → health → polish.** Build the registry as the spine of the whole service; everything else hangs off it.

A second, quieter insight: nothing about the staged briefs stops you from building the full published spec now. The briefs only gate *what's judged when*. Architect for all three stages from day one; let each stage be a checkpoint, not a redesign.

---

## 1. Reality check on the calendar

- **Today: May 29.** Stage 1 closes **June 1** — ~3 working days.
- Stage 2: **June 4–5** (24–36h), polyglot + YAML + `/readyz` + `/info` + the 30-minute add.
- Stage 3: **Paradox, in person, 24h** — security + sustained load, brief drops that morning.

The trap is treating Stage 1 as "just two languages." If you ship a quick two-language hack now and rebuild for Stage 2, you lose the architecture lead. Instead: **in the next 3 days, build the registry-driven engine and ship it configured for two languages.** Stage 2 then becomes five YAML blocks and five install scripts, not a rewrite. Stage 3 becomes hardening, which you've also already mostly done.

Your fork (`ambitious-scrap/goboxd`) is an empty scaffold — `cmd/goboxd`, `internal`, `docs`, `tests`, a Dockerfile, a Makefile. No Go code. You're building the spine from scratch, which is good: no legacy to fight.

---

## 2. Architecture — the spine

### 2.1 Package layout

```
cmd/goboxd/main.go              flags, config load, server wiring, graceful shutdown
internal/config/                YAML load + validate, placeholder parsing, defaults merge
internal/registry/             language registry: lookup, smoke probes, /info + /readyz data
internal/api/                   HTTP handlers, request decode, validation, error shaping
internal/runner/               the execution engine: build → run tests → map status
internal/sandbox/              nsjail wrapper: argv construction, exec, resource readout
internal/limits/               limit merge (defaults ⊕ request override), clamping
internal/flags/                per-language flag allow-list matching
internal/jail/                 jail dir lifecycle: unique create, cleanup, startup sweep
internal/status/               status mapping + comparison (incl. whitespace rule)
internal/obs/                  structured logging, stats counters
```

Keep `sandbox` and `runner` separate. `runner` is pure orchestration (build, then per-test run, then map results) and should be unit-testable with a fake sandbox. `sandbox` is the only thing that knows nsjail exists.

### 2.2 Framework choice

Use **`net/http` + `chi`** (or stdlib `http.ServeMux` on Go 1.22+; it now does method+path routing). Justify in two sentences in the README: you need three trivial routes and one POST, so a heavy framework buys nothing; chi gives you clean middleware (request-id, recovery, max-body) without runtime cost. Judges explicitly ask for a two-sentence justification — give exactly that, no marketing.

### 2.3 The execution loop (per `POST /run`)

```
acquire concurrency slot (block/queue if full)
  → create unique jail dir (atomic counter + PID + crypto-rand suffix)
  → defer cleanup (every exit path)
  → write source with validated filename
  → if language.build: run build in nsjail; on failure → build_failed, tests = not_executed
  → for each test: run in nsjail with stdin, capped output capture, measure wall+mem
  → map each test status; compute top-level status
  → release slot, cleanup fires
```

The defer for cleanup must be at the per-request scope, and the jail-dir create must happen *before* the defer is registered (Go idiom: create, check error, then `defer cleanup`). This is also security hole #7.

### 2.4 The performance lever judges won't expect

Per-request cost under load is dominated by filesystem + namespace setup, not by the user code. Build a **read-only base rootfs once at startup** (the toolchains, `/usr`, `/lib`, etc.), and per request only create a small writable work dir that nsjail bind-mounts over a tmpfs. nsjail mounts the shared base read-only and the per-request dir read-write. This collapses per-request setup to "make one dir + write one file," which is what lets you hold p95/p99 flat at 50–100 concurrent clients. Mention this in `docs/architecture.md` — it's the kind of decision that separates first place from fifth.

---

## 3. The language registry (25% + the 10% bonus)

This is where you win. The acceptance test on demo day is literal: *they hand you a language, you add it in under 30 minutes with no Go code change, while they watch.* Engineer for that exact moment.

### 3.1 Schema (extend the spec's recommended shape; document any change)

```yaml
languages:
  - id: cpp
    name: C++
    source_filename: solution.cpp     # fixed name, or strategy: from_request
    artifact: solution
    build:
      cmd: /usr/bin/g++
      args: ["{{flags}}", "-o", "{{artifact}}", "{{source}}"]
      limits: { wall_time_s: 3, memory_kb: 1048576, max_processes: 100 }
      flag_allowlist: ["-O0","-O1","-O2","-O3","-Wall","-Wextra","-std=*"]
    run:
      cmd: ./{{artifact}}
      limits: { wall_time_s: 3, memory_kb: 524288, max_processes: 64 }
    smoke: { cmd: /usr/bin/g++, args: ["--version"] }   # for /readyz
```

### 3.2 Design rules that earn the marks

- **Placeholders are the entire contract.** Support `{{source}}`, `{{artifact}}`, `{{flags}}`, and ideally `{{workdir}}`. Resolve them in the engine; the engine never special-cases a language id. If you find yourself writing `if lang == "java"`, you've lost a point — push it into config (`source_filename_strategy: from_request`, `artifact_filename_strategy: from_request`).
- **Interpreted vs compiled is just "has a `build` block or not."** No language-specific branches.
- **Startup validation that fails loudly.** On boot, parse every language, verify `cmd` paths exist and are executable, run each `smoke` probe, and refuse to serve (or serve `/readyz` = 503 with a per-language breakdown) if a language is broken. The spec explicitly rewards "startup-time validation that fails loudly with a useful error."
- **`/info` and `/readyz` are generated from the registry**, never hand-maintained. Adding a YAML block must automatically surface the language in both endpoints. Judges check this.
- **Install scripts live in `scripts/lang_install/<id>.sh`** and are invoked from the Dockerfile. The "add a language" workflow is then: (1) write a YAML block, (2) drop an install script, (3) `docker build`, (4) `/readyz` goes green. Rehearse this end to end until it's under 10 minutes, so 30 is comfortable with a strange language and nerves.

### 3.3 Pre-stage the bonus

Before Stage 3, have YAML + install scripts ready (even if commented/optional) for **Rust, Go, Kotlin, Ruby, Lua**. Each one that passes its `/readyz` smoke probe is a free point (10% bucket). Adding them is the same motion you've already automated. Don't bloat the image to the point that `docker build` exceeds the 10-minute fresh-clone bar — measure it.

### 3.4 Rehearse the demo-day add

Pick a language you did *not* pre-stage (say, **Zig** or **Crystal**), and time yourself adding it cold. The first time exposes every hidden assumption (toolchain path, default limits, artifact naming, a compiler that writes to `$HOME`). Fix the engine until any reasonable language is pure config. This dry run is worth more than any feature.

---

## 4. Concurrency (20%)

### 4.1 Design

- **Bounded global limit**, configurable via env (`GOBOXD_MAX_CONCURRENCY`) or YAML, default `runtime.NumCPU()`. Implement as a buffered-channel semaphore.
- **Queue, don't fail.** When the limit is hit, requests block on slot acquisition. Bound the *wait* with the request context, not by rejecting — the spec is explicit that requests should queue rather than fail. If you add an admission cap to avoid unbounded memory, return `503` only for genuine overload, never for user-code outcomes.
- **Per-request context** with the wall-time deadline; nsjail enforces its own `--time_limit`, and the Go context is defense-in-depth that guarantees the slot is released and the jail dir is cleaned even if nsjail wedges.
- **No unbounded goroutine fan-out**, no global locks on the hot path. Jail-dir naming is lock-free (atomic counter). Reuse read buffers via `sync.Pool` if profiling shows allocation pressure.
- Set `GOMAXPROCS` sensibly in-container (respect cgroup CPU quota — use `automaxprocs` or read the quota yourself).

### 4.2 Benchmarks (this is judged on evidence, not vibes)

- Ship a load script in-repo (`hey`, `k6`, or `vegeta`) wired to `make load`.
- `docs/benchmarks.md`: requests/sec and **p50/p95/p99** for the trivial "Hello World, py3" case at **1, 10, 50, 100** concurrent clients, measured from a **clean `docker run`**, on a box you name (CPU, RAM, cores). State the methodology so it's reproducible.
- The story the numbers should tell: throughput scales to NumCPU, then plateaus *flat* (queue absorbing excess) rather than collapsing. A flat p99 under sustained overload is the win condition; a blowing-up p99 is the lose condition.
- Capture per-request CPU + wall time in logs (ties into structured-logging bonus).

### 4.3 The judging-day sustained-load run

They run real traffic, not a micro-burst. Test this yourself before Stage 3: run 100 clients for several minutes and watch for jail-dir leaks (hole #7), fd leaks, memory growth, and zombie nsjail processes. A service that's fast for 10 seconds and degrades over 5 minutes loses here. Reap children, close pipes, and verify the startup sweep + per-request cleanup actually keep `/tmp` flat.

---

## 5. API conformance (15%) — make it mechanical

Build a conformance test suite that asserts every row of the status table. The subtle points where teams drop marks:

- **Top-level status rule:** `accepted` only if `build.status == ok` AND every test `accepted`; otherwise the **first non-accepted status in test order**. If build fails → top-level `build_failed` and every `tests[].status = not_executed`. Encode this as a single pure function with a table-driven test.
- **`output_whitespace_mismatch` is distinct from `wrong_output`.** Compare exact first; if unequal, compare after whitespace normalization (trim trailing whitespace/newlines, normalize line endings); if *then* equal → `output_whitespace_mismatch`, else `wrong_output`. Many teams collapse these and lose conformance points.
- **`time_exceeded` vs `memory_exceeded` vs `runtime_error`** must be distinguished correctly. Read cgroup v2 `memory.events` (`oom_kill > 0`) and `memory.peak` for memory; treat nsjail time-limit kills as `time_exceeded`; non-zero exit otherwise → `runtime_error`. Don't report every SIGKILL as the same thing.
- **400 vs 5xx discipline:** bad JSON, unknown language, oversize body, malformed filename, disallowed flag → `400` with `{"error":{"code","message"}}`. `5xx` is *only* for server failures (nsjail missing, sandbox setup error, disk full). **Never** return 5xx because user code crashed — user-code failure is a `200` with a non-accepted status. This rule is stated twice in the spec; they will test it.
- Echo `build.duration_ms`, per-test `duration_ms` and `memory_peak_kb`. Include them even when zero/empty so the shape always matches.
- Enforce field rules: `language` required (else 400), `source` UTF-8 ≤ max (default 256 KiB), `tests` ≥ 1, filenames a single path component.

Write a golden-file test harness: input JSON → expected output JSON, one pair per scenario. The reference repo ships sample request/reply pairs under `tests/` — use them as your conformance oracle.

---

## 6. Security (15%) — close all 7, document each

The spec says fix 5; each extra is worth half a required one. All 7 are cheap once the architecture is right, so **close all 7** and bank the bonus. In the PR description, list each with a `file:line` link to the fix — that's how they're scored.

1. **Path traversal via filename** → strict validation: single path component matching `^[A-Za-z0-9._-]+$`, no separators, no leading dot, length cap; assert `filepath.Base(name) == name`; never `filepath.Join` with an unvalidated value. (`internal/api` validation + `internal/jail`.)
2. **Shell-style directory commands** → never invoke a shell. `os.MkdirTemp` / `os.RemoveAll`; `exec.Command` with explicit argv, never `sh -c "..."`. Audit every path-handling line. (`internal/jail`, `internal/sandbox`.)
3. **Compiler-flag injection** → per-language allow-list with exact + pattern matching (`-std=*`). Explicitly reject `-fplugin=`, `-x`, `-B`, `--specs=`, `-Wl,`, `@responsefile`, and anything not on the list, with `400`. (`internal/flags`.)
4. **Request size limits** → `http.MaxBytesReader` on the body; cap source bytes, test count, per-test stdin/expected size; cap captured stdout/stderr; set `rlimit_fsize` in nsjail. Defense at both HTTP and sandbox layers. (`internal/api` + `internal/sandbox`.)
5. **UID collisions under load** → process-unique scheme: atomic counter + PID + `crypto/rand` suffix, or a tempdir API; never reuse a dir or UID. (`internal/jail`.)
6. **Unbounded child output** → `io.LimitReader` with a hard cap; truncate with an explicit marker (`...[truncated]`). (`internal/sandbox`.)
7. **Stale jail directories** → `defer` cleanup at per-request scope on every exit path, plus a startup sweep removing orphans older than N minutes. (`internal/jail` + `cmd/goboxd` boot.)

`docs/security.md` should explain the threat model and each fix in plain prose. This doc plus the PR `file:line` list is the deliverable judges actually read.

---

## 7. Health endpoints (5%)

- **`/healthz`** — liveness, no dependencies, `200 {"status":"ok"}`.
- **`/readyz`** — runs (or serves cached results of) the nsjail check + every language smoke probe. `200` only if all green; `503` `{"status":"degraded", ...}` with per-language `{ok, version|error}` breakdown. Generated from the registry.
- **`/info`** — always `200`. `build_info` (version/commit/go_version injected via `-ldflags`), nsjail path+version, languages with versions + default limits, server limits, and `stats` (in_flight via atomic, totals, last_internal_error_at, `disk_free_bytes_jail_dir` via `syscall.Statfs`). Wire stats counters from day one so this is real, not faked.

These are pure mechanics — don't leave the 5% on the table.

---

## 8. Repo, tooling, and code quality (10% + the doc bonus)

- **Dockerfile** must build nsjail **from source at image-build time**, pinned to tag **`3.4`**, added as a **git submodule** (`git submodule add https://github.com/google/nsjail external/nsjail`) — the spec says they *expect to see* this. Do not bundle a prebuilt binary, do not apt-install it. Install every toolchain. Fresh clone → `docker build` → `docker run` → `/healthz` 200 must work in **under 10 minutes**; measure it.
- **docker-compose.yml** — one command to bring up the dev server.
- **Makefile** — `build`, `run`, `test`, `integration`, `load`, `lint`. No bare `go run` in the README.
- **docs/** — `api.md`, `languages.md`, `security.md`, `benchmarks.md`, `architecture.md`. The architecture doc is a named bonus: write it so a new engineer could onboard from it on day one.
- **Tests** — table-driven unit tests for config load, filename validation, flag allow-listing, status mapping, output truncation (everything that doesn't need nsjail). At least one end-to-end test per in-scope language.
- **Lint** — `go vet` + `staticcheck` (or `golangci-lint`) clean. Add a CI workflow that runs them on the team branch; a green CI badge is cheap signal.
- **README** — short and human. The spec bans AI filler explicitly: no "elegant", "robust", "seamlessly", "leverage", no emoji, no marketing. Write like a tired engineer documenting for a teammate. **This is a real scoring signal** — they call out AI-sounding prose, so read every doc and strip it.
- **Commit history** — branch `team/<your-team-name>` off `master` in your fork. Small, conventional commits that tell a story. No single giant "implement everything" commit — Stage 1 is explicitly judged on "how you handle the basics of an SDLC: branches, tests, commit history."

---

## 9. Bonuses worth grabbing

- **Extra languages** (10% bucket): Rust, Go, Kotlin, C#, Ruby, Lua, OCaml, Swift, Zig — each passing `/readyz` = 1 point. Free given your registry.
- **Structured request logs**: one JSON line per request — request id, language, build/run durations, status. Cheap, explicitly rewarded, and doubles as your concurrency evidence.
- **Architecture doc**: covered above; named bonus.
- **Closing >5 security holes**: covered; close all 7.

---

## 10. Stage-by-stage execution

### Stage 1 — by June 1 (next 3 days) — *front-load the architecture*
- Registry engine + config loader with placeholder templating (build the real spine, not a hack).
- nsjail wrapper (build from source, submodule, tag 3.4) + Dockerfile that produces a working image.
- `POST /run` end to end for **py3** (interpreted) + **cpp** (compiled), full status mapping incl. the whitespace and top-level rules.
- `/healthz` 200. (Stub `/readyz`/`/info` now; they're Stage 2 but cheap to start.)
- Unit tests on validation/flags/status; one e2e test per language; human README; clean commit history on `team/<name>`.
- Open the PR before the deadline and mark ready-for-review.

### Stage 2 — June 4–5 — *config, not code*
- All 7 in-scope languages as YAML + install scripts (C, C++, Java, Python 3, Bash, Node, Verilog).
- `/readyz` + `/info` fully generated from the registry; startup validation that fails loudly.
- Per-request limit overrides (defaults ⊕ request) and flag allow-lists enforced.
- Rehearse the 30-minute cold add until it's boring. Pre-stage Rust/Go/Kotlin/Ruby/Lua for the bonus.

### Stage 3 — Paradox, 24h in person — *harden + load*
- All 7 security holes closed, documented in the PR with `file:line`.
- Bounded concurrency + queue finalized; `docs/benchmarks.md` at 1/10/50/100.
- Run your own multi-minute sustained-load test the night before; fix leaks (jail dirs, fds, zombies).
- Whatever the surprise brief is, you've already built the hard parts — keep capacity for it.

Because the full spec is public, do not actually wait for each brief to start its work. Build toward the whole spec now; the stages just check what's done.

---

## 11. Failure modes that lose (pre-mortem)

- A two-language Stage 1 hack that needs a rewrite for Stage 2 — you fall behind on the 25%.
- `if lang == "java"` anywhere in the engine — kills the plug-and-play score and slows the demo-day add.
- Returning 5xx when user code crashes — direct conformance loss, stated twice in the spec.
- Collapsing `output_whitespace_mismatch` into `wrong_output`, or mis-tagging OOM as runtime error — silent conformance losses.
- Benchmarks from a debugger / dirty host instead of a clean `docker run` — they'll notice and discount them.
- p99 that blows up under sustained load while looking great in a 10-second burst.
- nsjail apt-installed or prebuilt instead of submodule-built from tag 3.4 — explicitly disallowed.
- AI-sounding README/docs with banned words — a stated penalty.
- One giant commit — fails the SDLC portion of Stage 1.
- `docker build` slower than 10 minutes from a fresh clone (often caused by over-stuffing toolchains for the bonus).

---

## 12. The 30-second pitch of the strategy

Make the **language registry the spine** of the service and keep it 100% data-driven — that single decision wins the 25% plug-and-play score, gifts the 10% extra-languages bonus, and makes the watched demo-day add trivial. Pair it with a **bounded, queued concurrency model with a read-only base rootfs** so latency stays flat under sustained load (20%). Treat **API conformance and all 7 security fixes as mechanical checklists** (15% + 15% + bonus), keep the repo and commit history honestly clean (10%), and don't leave the **5% health endpoints** on the floor. Build the full published spec now; let the stages be checkpoints, not rewrites.

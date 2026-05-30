# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

goboxd is a sandboxed code execution service for a hackathon (SEEK/Paradox). It runs arbitrary user code in isolated nsjail containers and judges output against expected test cases, returning structured pass/fail results per test.

The core insight driving all architectural decisions: the language registry must be 100% data-driven (YAML config + install scripts, zero Go changes to add a language). This wins the 25% plug-and-play score and the 10% extra-languages bonus simultaneously.

## Commands

```bash
# Build
make build

# Run (dev)
make run
docker-compose up

# Test
make test                     # unit tests
make integration              # end-to-end tests (requires Docker)
make load                     # load test (hey/k6/vegeta against /run)

# Lint
make lint                     # go vet + staticcheck (or golangci-lint)

# Docker
docker build -t goboxd .
docker run -p 8080:8080 goboxd

# Single test
go test ./internal/runner/... -run TestStatusMapping
go test ./internal/flags/... -run TestFlagAllowlist
```

## Architecture

```
cmd/goboxd/main.go        flags, config load, server wiring, graceful shutdown
internal/config/          YAML load + validate, placeholder parsing, defaults merge
internal/registry/        language registry: lookup, smoke probes, /info + /readyz data
internal/api/             HTTP handlers, request decode, validation, error shaping
internal/runner/          execution engine: build → run tests → map status
internal/sandbox/         nsjail wrapper: argv construction, exec, resource readout
internal/limits/          limit merge (defaults ⊕ request override), clamping
internal/flags/           per-language flag allow-list matching
internal/jail/            jail dir lifecycle: unique create, cleanup, startup sweep
internal/status/          status mapping + comparison (incl. whitespace rule)
internal/obs/             structured logging, stats counters
```

**Key separation:** `runner` is pure orchestration (unit-testable with a fake sandbox). `sandbox` is the only package that knows nsjail exists. Never let language-specific branches leak into either — push everything into YAML config.

## Language Registry

Languages are defined in YAML with placeholder templating (`{{source}}`, `{{artifact}}`, `{{flags}}`, `{{workdir}}`). The engine resolves placeholders; it never special-cases a language id. "Has a `build` block" = compiled; absent = interpreted. That's the only branch.

```yaml
languages:
  - id: cpp
    name: C++
    source_filename: solution.cpp
    artifact: solution
    build:
      cmd: /usr/bin/g++
      args: ["{{flags}}", "-o", "{{artifact}}", "{{source}}"]
      limits: { wall_time_s: 3, memory_kb: 1048576, max_processes: 100 }
      flag_allowlist: ["-O0","-O1","-O2","-std=*"]
    run:
      cmd: ./{{artifact}}
      limits: { wall_time_s: 3, memory_kb: 524288, max_processes: 64 }
    smoke: { cmd: /usr/bin/g++, args: ["--version"] }
```

Install scripts live in `scripts/lang_install/<id>.sh` and are invoked from the Dockerfile. Adding a language = YAML block + install script + `docker build`. No Go changes.

## API Contract Rules

These are scoring-critical — get them exact:

- `POST /run` returns `200` even when user code crashes. `5xx` = server failure only (nsjail missing, disk full, sandbox setup error). Never `5xx` for user-code outcomes.
- Top-level status = `accepted` only if `build.status == ok` AND every test `accepted`; otherwise = the **first non-accepted test status in order**.
- If build fails → top-level `build_failed`, every `tests[].status = not_executed`.
- `output_whitespace_mismatch` is distinct from `wrong_output`: compare exact first; if unequal, normalize whitespace; if then equal → `output_whitespace_mismatch`, else `wrong_output`.
- `time_exceeded` / `memory_exceeded` / `runtime_error` must be distinguished: read cgroup v2 `memory.events` for OOM, nsjail time-limit kills → `time_exceeded`, non-zero exit otherwise → `runtime_error`.
- `400` for: bad JSON, unknown language, oversize body, malformed filename, disallowed flag. Shape: `{"error":{"code","message"}}`.
- Always echo `build.duration_ms`, per-test `duration_ms` and `memory_peak_kb` — even when zero.

## Security Holes (all 7 must be closed)

1. Path traversal via filename — validate `^[A-Za-z0-9._-]+$`, assert `filepath.Base(name) == name`
2. Shell invocation — never `sh -c`, always `exec.Command` with explicit argv
3. Compiler-flag injection — per-language allow-list; reject anything not matched
4. Request size limits — `http.MaxBytesReader` + `rlimit_fsize` in nsjail
5. UID collisions — atomic counter + PID + `crypto/rand` suffix for jail dir names
6. Unbounded child output — `io.LimitReader` with hard cap + truncation marker
7. Stale jail dirs — `defer` cleanup at per-request scope + startup sweep of orphans

Document each fix in `docs/security.md` with `file:line` links — that's the judging deliverable.

## Health Endpoints

- `/healthz` — liveness only, no deps, `200 {"status":"ok"}`
- `/readyz` — runs nsjail check + every language smoke probe; `200` if all green, `503` with per-language breakdown if not. Generated from registry.
- `/info` — always `200`. Includes build info (via `-ldflags`), nsjail version, per-language versions + limits, stats (in-flight, totals, `disk_free_bytes_jail_dir`).

## Performance Design

Per-request cost is dominated by filesystem + namespace setup. Build a **read-only base rootfs once at startup** (toolchains, `/usr`, `/lib`); per request, create a small writable workdir that nsjail bind-mounts over tmpfs. This collapses per-request setup to "mkdir + write one file."

Concurrency: bounded global limit via buffered-channel semaphore (`GOBOXD_MAX_CONCURRENCY`, default `runtime.NumCPU()`). Requests queue on slot acquisition (bound by request context), never fail with `503` due to load unless genuinely OOM. Use `automaxprocs` in-container.

## nsjail

Must be built from source at image-build time, pinned to tag `3.4`, added as a git submodule at `external/nsjail`. Do not apt-install or bundle a prebuilt binary — the spec explicitly requires submodule-built from tag 3.4.

## Docs Required

`docs/api.md`, `docs/languages.md`, `docs/security.md`, `docs/benchmarks.md`, `docs/architecture.md`

Benchmarks must show p50/p95/p99 at 1/10/50/100 concurrent clients from a clean `docker run`, naming the hardware. Structured per-request logs (JSON, one line per request) are an explicit bonus criterion.

## README Rules

No AI-sounding filler: no "elegant", "robust", "seamlessly", "leverage", no emoji, no marketing. Write like a tired engineer documenting for a teammate. The spec explicitly penalizes AI-sounding prose.

## Commit Discipline

Branch off `master` as `team/<name>`. Small conventional commits. No single "implement everything" commit — Stage 1 is explicitly scored on commit history as an SDLC signal.

<!-- headroom:learn:start -->
## Headroom Learned Patterns
*Auto-generated by `headroom learn` on 2026-05-29 — do not edit manually*

### Git / GitHub
*~3,500 tokens/session saved*
- Working module path is `github.com/thesouldev/goboxd` (hackathon starter repo). Do NOT init with `ambitious-scrap` or any other path — causes merge-conflict hell when syncing.
- `/security-review` skill uses `git diff origin/HEAD...` which crashes when no remote tracking branch exists. Skip or manually diff instead.
- `ambitious-scrap/goboxd` is a public fork of `thesouldev/goboxd`; GitHub blocks making it private.

### File Editing Rules
*~2,500 tokens/session saved*
- Always Read a file before Write or Edit — the tool rejects writes to unread files with `File has not been read yet`. `cat` via Bash does NOT satisfy this; only the Read tool counts.
- Files confirmed to trigger this repeatedly: `configs/languages.yaml`, `internal/config/config.go`, `internal/status/status_test.go`, `Makefile`, `go.mod`.

### Environment
*~1,500 tokens/session saved*
- Docker is **not installed** on the host Mac. The container runtime is **Colima** (`colima start --cpus 4 --memory 6 --disk 30` before any `docker` command). Docker CLI is present once Colima is running.
- `hey` benchmark tool is installed to `.tools/` (not global PATH): use `GOBIN=$(pwd)/.tools go install github.com/rakyll/hey@latest` and invoke as `.tools/hey`.

### Commands
*~800 tokens/session saved*
- `grep` exits 1 when no matches found — always append `|| true` when no-match is an acceptable outcome, e.g. `grep -rn 'we' docs/ || true`.
- nsjail submodule needs recursive init for the kafel sub-dependency: `git submodule update --init --recursive`. Docker build fails without it.

<!-- headroom:learn:end -->

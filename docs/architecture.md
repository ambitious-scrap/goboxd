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
internal/limits/    request-level limit override merging with clamping
internal/status/    output comparison and top-level status computation
internal/obs/       structured JSON logging and atomic stats counters
```

## Request lifecycle

```
POST /run
  → validate (language known, source size, filename, flags)
  → acquire concurrency slot (buffered channel semaphore)
  → runner.Execute
      → jail.Create        (atomic counter + PID + crypto/rand suffix)
      → defer jail.Cleanup
      → write source file
      → [if compiled] sandbox.Run(build cmd)  → build_failed if exit != 0
      → for each test: sandbox.Run(run cmd, stdin)
          → map sandbox.Result → test status
      → status.TopLevel
  → release slot
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
- A unique read-write workdir bind-mounted at `/workdir`
- Wall time limit (`--time_limit`)
- Address space limit (`--rlimit_as`)
- Process count limit (`--max_pids`)
- File size limit (`--rlimit_fsize`)
- A single CPU (`--max_cpus 1`)

stdout and stderr are captured through `io.LimitReader` with a hard byte cap; excess output is truncated with a `...[truncated]` marker.

OOM kills are detected via cgroup v2 `memory.events`. Timeouts are inferred from wall time vs the configured limit. All other non-zero exits map to `runtime_error`.

## Concurrency model

A buffered channel of size `MaxConcurrency` (default `runtime.NumCPU()`) acts as a semaphore. Requests block on slot acquisition, bound by the request context. This means the service queues under load rather than returning errors. The concurrency limit is the only global lock on the hot path; jail dir naming is lock-free (atomic counter).

## Performance

Per-request overhead is dominated by nsjail's namespace and filesystem setup. The jail directory is a minimal writable tmpfs workdir; the language toolchain lives in the image and is shared read-only across all requests. This keeps per-request setup to one `mkdir` + one file write.

## Status computation

Output comparison is a pure function:
1. Exact match → `accepted`
2. Whitespace-normalized match → `output_whitespace_mismatch`
3. Otherwise → `wrong_output`

Top-level status is the first non-`accepted` test status in order, or `build_failed` if the build step failed. All test statuses are `not_executed` when build fails.

## Health and observability

- `/healthz` — liveness only, no dependencies
- `/readyz` — executes smoke probes for every language at startup; caches results; returns 503 if any language is degraded
- `/info` — build metadata, language versions, atomic request stats, jail dir disk usage

One structured JSON log line is emitted per request.

# Specification Conformance

This document maps each requirement of the API specification (`docs/api.md`) to the code that
implements it, with `file:line` references. It exists as a single, current source of truth so that
already-fixed items are not re-flagged by stale audits.

**The spec (`docs/api.md`) is authoritative.** Where this table says "match", the implementation has
been verified against the spec on the current tree.

Last verified: 2026-06-09 (commit `114be18`).

## Request contract

| Spec requirement (`docs/api.md`) | Implementation | Status |
|---|---|---|
| Nested `build` / `run` objects, each with `limits` + `flags` (api.md:13-30) | `runRequest` with `Build *stepOptions`, `Run *stepOptions`; `stepOptions{Limits *limitsInput, Flags []string}` — `internal/api/handler.go:96-120` | match |
| Limit override is partial replace; present field replaces default, absent keeps default (api.md:48-49) | `limits.Merge` — only non-nil, positive fields override — `internal/limits/limits.go:18-33` | match |
| `source_filename` required for some languages (api.md:36) | resolved per language; validated — `internal/api/handler.go:203-224` | match |
| `artifact_filename` required when language uses request-supplied artifact (api.md:36-37; languages.md:37-39) | `ArtifactFilenameStrategy == "from_request"` path requires it, else `400` — `internal/api/handler.go:211-224`; config `from_request` for java — `configs/languages.yaml` | match (code and docs agree) |

## Response contract

| Spec requirement | Implementation | Status |
|---|---|---|
| Response `{status, build?, tests[]}` (api.md:53-72) | `runResponse{Status, Build *buildInfo, Tests []testOut}` — `internal/api/handler.go:138-142` | match |
| `build` = `{status, duration_ms, stdout, stderr}` | `buildInfo` — `internal/api/handler.go:144-149` | match |
| per-test = `{status, stdout, stderr, duration_ms, memory_peak_kb}` | `testOut` — `internal/api/handler.go:151-157` | match |
| Error shape `{error:{code,message}}` (api.md:105) | `writeError` — `internal/api/handler.go:513-519` | match |
| Status values + top-level = first non-accepted (api.md:76-92) | constants + `status.TopLevel` — `internal/status/status.go:7-23,43-53` | match |

## Language registry

| Requirement | Implementation | Status |
|---|---|---|
| `All()` returns languages in deterministic (id-sorted) order | `sort.Strings(ids)` then build slice — `internal/registry/registry.go:44-54` | match |

## Sandbox / limits

| Doc claim | Implementation | Status |
|---|---|---|
| `--rlimit_fsize` = 100 MiB (security.md:31, architecture.md:58) | `"--rlimit_fsize", "100"` — `internal/sandbox/sandbox.go:176` | match |
| cgroup controllers enabled best-effort per-controller (architecture.md:60) | `tryEnableControllers` — `internal/sandbox/cgroup.go:138-158`; limits applied only if available — `internal/sandbox/cgroup.go:79-92` | match |
| Per-language wall/memory limits (languages.md:25-33) | `configs/languages.yaml` values equal the documented table | match |

## Caching (run results never cached)

| Requirement | Implementation | Status |
|---|---|---|
| Only build output reused; run phase always live (languages.md:106) | cache gates compile only; run executes per test — `internal/runner/runner.go` | match |
| Cache key must not serve stale binary across toolchain/flag changes | key = langID + toolchainVersion + sha256(source) + buildFlags + artifactFilename — `internal/artifactcache/artifactcache.go:44-57` | match |

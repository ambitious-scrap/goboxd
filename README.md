# goboxd

A sandboxed code execution service. Accepts source code and test cases via HTTP, runs them in isolated nsjail containers, and returns per-test pass/fail results.

## Quick start

```bash
docker compose up
curl http://localhost:8080/healthz
```

## Run a submission

```bash
curl -X POST http://localhost:8080/run \
  -H "Content-Type: application/json" \
  -d '{
    "language": "py3",
    "source": "print(input())",
    "tests": [
      {"stdin": "hello", "expected_stdout": "hello\n"}
    ]
  }'
```

Response:

```json
{
  "status": "accepted",
  "tests": [
    {"status": "accepted", "stdout": "hello\n", "duration_ms": 42, "memory_peak_kb": 8192}
  ]
}
```

## Supported languages

| ID         | Language          |
|------------|-------------------|
| py3        | Python 3          |
| c          | C                 |
| cpp        | C++               |
| java       | Java              |
| bash       | Bash              |
| javascript | JavaScript (Node) |
| verilog    | Verilog           |

Run `/readyz` for live status of each language toolchain.

## API

`POST /run` — execute a submission  
`GET /healthz` — liveness check  
`GET /readyz` — readiness check with per-language toolchain status  
`GET /info` — build info, language versions, server stats  

Full request/response schema: [docs/api.md](docs/api.md)

## Adding a language

1. Add a YAML block to `configs/languages.yaml` (reference the shared seccomp
   policy with `seccomp_policy: *deny_seccomp`)
2. Add an install script at `scripts/lang_install/<id>.sh`
3. `docker build`
4. Check `/readyz` — the language appears automatically

No Go changes required.

## Development

```bash
make build        # compile binary
make run          # build + run locally (needs nsjail)
make test         # unit tests
make lint         # go vet + staticcheck
make docker-build # build Docker image
make load         # run load test against localhost:8080
```

Go 1.22+. nsjail is built from source as a git submodule (`external/nsjail`, tag 3.4). The Docker build handles this; running locally requires nsjail on PATH.

## Architecture

[docs/architecture.md](docs/architecture.md)

## Security

Code runs under nsjail (user/mount/pid/net namespaces, dropped capabilities,
`no_new_privs`), cgroup v2 resource caps, and a **seccomp-bpf deny-list enforced by
default** that blocks the kernel sandbox-escape surface (`ptrace`, `bpf`, `mount`,
module loading, `kexec`, `process_vm_*`, …) while leaving JIT/threaded runtimes intact.
Details and the full policy: [docs/security.md](docs/security.md)

## Observability

`goboxd` exposes Prometheus metrics on a separate admin port (`:9090` by default; set
`-metrics-port=-1` to disable), kept off the public API. `docker compose up` brings up the full stack:

- **goboxd** — API on `:8080`, metrics on `:9090`
- **Prometheus** — scrapes `goboxd:9090` every 5s; UI on [localhost:9091](http://localhost:9091)
- **Grafana** — dashboard auto-provisioned at [localhost:3000](http://localhost:3000) (anonymous admin)

The bundled dashboard (`deploy/grafana/dashboards/goboxd.json`) charts run throughput by verdict,
build/run latency p95, queue depth & in-flight, 503 reject rate, cache hit ratio, and admission wait
p95 by lane (light vs heavy — see the fast-lane reservation in [docs/architecture.md](docs/architecture.md)).
Scrape config and provisioning live under `deploy/`.

## Demo UI (not bundled)

An optional Monaco-editor page lives in [`demo/`](demo/) for quick manual exploration.
It is **deliberately not bundled into the service** and is not part of the deployable
artifact: `goboxd` is a headless judge whose only trust boundary is the API + sandbox.
A web UI adds zero security (an attacker hits the API with `curl`, never the page) and
would only enlarge the attack surface, so the page is a standalone static file that
talks to the public API like any other client. It is served separately and requires the
demo-only, env-gated CORS allowance (`GOBOXD_DEMO_CORS_ORIGIN`, off by default, never
`*`, never production). See [`demo/README.md`](demo/README.md).

## Design decisions

Short rationale for the choices a reviewer is most likely to question. Fuller analysis lives
in [docs/improvements.md](docs/improvements.md) (Part C) and [docs/architecture.md](docs/architecture.md).

- **`chi` over a framework.** Three read-only endpoints and one POST don't justify a heavier
  framework; `chi` adds request-id middleware and a panic-recovery handler on top of
  `net/http` without pulling in a runtime.
- **nsjail built from source, pinned.** nsjail is a git submodule checked out at tag 3.4 and
  compiled in the image, rather than trusting a host/distro package. The sandbox boundary is
  reproducible and version-known instead of "whatever nsjail the host happens to ship."
- **Artifact cache, not result cache.** We cache the compiled binary keyed by
  `(lang, toolchain version, sha256(source), build flags)` and **always re-run in a fresh
  jail**. Identical source ⇒ identical binary (safe to reuse); caching *run verdicts* is not
  safe — a nondeterministic program would return a stale result. Verdicts stay live.
- **Backpressure, not limit clamping.** Under overload we shed at the door with
  `503 + Retry-After`; we never silently shrink a run's `wall_time_s`/`memory_kb`. A judge's
  verdict must be a pure function of `(source, tests, limits)` — load-dependent verdicts (pass
  off-peak, `time_exceeded` at peak) are unacceptable, and the spec forbids clamping.

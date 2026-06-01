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

| ID    | Language   |
|-------|------------|
| py3   | Python 3   |
| cpp   | C++        |

Run `/readyz` for live status of each language toolchain.

## API

`POST /run` — execute a submission  
`GET /healthz` — liveness check  
`GET /readyz` — readiness check with per-language toolchain status  
`GET /info` — build info, language versions, server stats  

Full request/response schema: [docs/api.md](docs/api.md)

## Adding a language

1. Add a YAML block to `configs/languages.yaml`
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

[docs/security.md](docs/security.md)

## Framework

`net/http` + `chi` for routing. Three read-only endpoints and one POST don't justify a heavier framework; chi adds request-id middleware and a panic recovery handler without pulling in a runtime.

# Development Guide

This document describes the development workflow, commands, and testing setup for `nsjail-server`. All commands assume Docker and Docker Compose v2 are installed and you're running them from the repository root.

## Development Modes

The server runs the same way in development as in production — inside its container.

### Foreground

```bash
make run
```

The server starts in the foreground on `http://localhost:8000`, with the source tree mounted into `/app` so edits to Python files take effect on container restart.

### Detached

```bash
docker compose up -d web
```

Useful when you want the server running in the background while you iterate on integration tests or curl requests in the same terminal.

## Available Make Commands

### Environment & Build

| Command                          | Description                                                          |
| -------------------------------- | -------------------------------------------------------------------- |
| `make build`                     | Build Docker images for `web` and `integration-tests` services       |
| `make build-no-cache`            | Build images from scratch, ignoring the layer cache                  |
| `make uv-lock`                   | Resolve and update `uv.lock` to current `pyproject.toml` constraints |
| `make generate-proto`            | Regenerate Python protobuf bindings from `proto/*.proto`             |
| `make shell`                     | Open an interactive bash shell inside the `web` container            |

### Application

| Command          | Description                                                     |
| ---------------- | --------------------------------------------------------------- |
| `make run`       | Start the server on `http://localhost:8000`                     |
| `make down`      | Stop the running stack and remove containers, networks, volumes |

### Testing

| Command                              | Description                              |
| ------------------------------------ | ---------------------------------------- |
| `make run-tests`                     | Run unit tests with `pytest`             |
| `make run-integration`               | Run every integration testcase           |
| `make run-integration test=<path>`   | Run one testcase or one language subtree |

### Code Quality

| Command         | Description                                            |
| --------------- | ------------------------------------------------------ |
| `make format`   | Auto-format Python source with `ruff format`           |
| `make lint`     | Lint and auto-fix with `ruff check --fix`              |
| `make check`    | Read-only format and lint checks (used in CI)          |

## Testing

### Unit Tests

```bash
make run-tests
```

Unit tests live under `src/tests/unit/` and exercise individual functions without bringing up `nsjail`. Add new tests there when fixing a bug or adding a helper.

### Integration Tests

Integration tests send a real `request.txt` to a running server, capture the response, and compare it against `reply.txt`. They are the source of truth for the server's wire behaviour.

```bash
make run-integration                       # Run every testcase
make run-integration test=java/hello_world # Run a single testcase
make run-integration test=python3          # Run every Python 3 testcase
```

Testcases are organised by language:

```text
src/tests/testcases/
  bash/
  c/
  cpp/
  java/
  javascript/
  python/
  python3/
  verilog/
  zip/
```

### Adding a Testcase

Create a directory under `src/tests/testcases/<language>/<test_name>/` with two files:

- `request.txt` — Protobuf text-format request body (the value of the JSON `message` field).
- `reply.txt` — The expected protobuf text-format response.

Run the new test in isolation:

```bash
make run-integration test=<language>/<test_name>
```

⚠️ **Note:** Field order in protobuf text format does not matter for equality, but extra/missing fields do. Look at an existing testcase under the same language for the canonical shape.

### Load Testing

Performance tests use Locust to simulate concurrent users against a running server:

```bash
./src/tests/loadtest/run_loadtest.sh <test_type> [host] [language] [duration_seconds]
```

**Test types:**

- `100rps` — High throughput (10 users, 60s)
- `burst` — Spike load (50 users, 30s)
- `sustained` — Stability test (20 users, 5 min)
- `rampup` — Find the breaking point (up to 100 users, 10 min)
- `custom` — Configurable via env vars (`USERS`, `SPAWN_RATE`, `RUN_TIME`)
- `all` — Run the complete suite back-to-back

**Examples:**

```bash
./src/tests/loadtest/run_loadtest.sh help                  # Show usage
./src/tests/loadtest/run_loadtest.sh burst                 # Quick local burst
./src/tests/loadtest/run_loadtest.sh sustained <host> java # Sustained Java load
BURST_USERS=100 ./src/tests/loadtest/run_loadtest.sh burst # Override user count
```

Reports are written to `output/` as an HTML dashboard plus the raw CSV data Locust produces.

## Modifying the Protobuf Schema

If you change any `.proto` file under `src/proto/`, regenerate the Python bindings before running tests:

```bash
make generate-proto
```

The generated `*_pb2.py` files are committed alongside the schema so the project remains buildable without `protoc` on the host.

## Code Quality Workflow

Before opening a pull request:

```bash
make check
```

This runs `ruff format --check` and `ruff check` in read-only mode. Fix anything it reports with:

```bash
make format
make lint
```

`make lint` will auto-fix issues where possible; anything it leaves behind needs a manual edit.

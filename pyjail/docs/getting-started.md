# Getting Started Guide

This document walks you through running `nsjail-server` on your local machine. The server wraps Google's `nsjail` to execute untrusted code against test cases, returning per-test verdicts over HTTP.

## Prerequisites

Everything runs inside Docker, so you don't need Python or `nsjail` on the host. You only need:

- [Docker](https://docs.docker.com/get-docker/) (Engine 24+) with Docker Compose v2.
- A Linux host or a Linux VM. macOS and Windows users should run via Docker Desktop with Linux containers; nsjail uses Linux namespaces and won't run natively on Darwin or Windows.
- ~2 GB of free disk for the image (it bundles compilers and runtimes for every supported language).
- The repository cloned locally:

  ```bash
  git clone <REFERENCE_REPO_URL>
  cd nsjail-server
  ```

**Verify Docker installation:**

```bash
docker --version
docker compose version
```

## Build the image

The first build takes a few minutes — it compiles `nsjail` from source and installs C, C++, Java, Python 3, Bash, Node.js, and Verilog toolchains.

```bash
make build
```

Subsequent builds use the layer cache and finish in seconds for code-only changes.

## Run the server

```bash
make run
```

The server listens on `http://localhost:8000`. A successful boot prints gunicorn worker output and a readiness line. The container runs in the foreground; stop it with `Ctrl+C`, or run `make down` from another terminal to clean up.

**Health check:**

```bash
curl http://localhost:8000/
# => "OK"
```

## Send your first request

The server speaks protobuf in text format, wrapped in a JSON envelope. Save the following as `hello.json`:

```json
{
  "message": "code { full_code: \"print('Hello from Python 3!')\" } language: \"py3\" testcases { input: \"\" output: \"Hello from Python 3!\\n\" }"
}
```

Send it:

```bash
curl -s -X POST http://localhost:8000/ \
  -H 'Content-Type: application/json' \
  --data @hello.json
```

Expected response (truncated):

```text
{"message": "overall_status: OK\ncompilation_result {\n  status: OK\n}\ntest_case_results {\n  status: OK\n  actual_output: \"Hello from Python 3!\\n\"\n  ...\n}"}
```

If you get `OK` back, the server, `nsjail`, and the Python 3 toolchain are all wired up correctly.

## Run the integration tests

The repository ships with sample request/reply pairs for every supported language under `src/tests/testcases/`. Run them all:

```bash
make run-integration
```

Run a single language or testcase:

```bash
make run-integration test=java/hello_world
make run-integration test=python3
```

Each `request.txt` is sent to the running server; the response is compared field-by-field against `reply.txt`. These are the canonical examples of how the server behaves end-to-end — read them when in doubt about wire format.

## Open a shell inside the container

For debugging:

```bash
make shell
```

This drops you into the container with the source tree mounted at `/app`. From here you can re-run gunicorn, inspect `/var/lib`, run `nsjail` directly, or smoke-test a language toolchain.

## Next steps

For development workflows, available `make` targets, and load-testing:

```text
docs/development.md
```

For an end-to-end view of how a request flows through the server and `nsjail`:

```text
docs/architecture.md
```

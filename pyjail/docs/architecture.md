# Architecture

This document describes how `nsjail-server` is put together: the moving parts, the lifecycle of a single request, and the configuration model that drives per-language behaviour.

## High-Level Picture

The server is a thin Flask + gunicorn HTTP service that fans each request out to one or more `nsjail` subprocesses. The Python layer never executes user code directly — it is responsible only for parsing the request, laying out a per-job sandbox directory, building `nsjail` argument lists from configuration, and aggregating the results.

```text
                ┌───────────────────────────────────────────────┐
   HTTP POST ─▶ │  Flask (server.py)                            │
   /            │   └── ProgrammingServer (POST handler)        │
                │         └── CodeManager / EvaluationManager   │
                │               ├── initialize_environment()    │
                │               ├── compile_program()           │
                │               ├── run_program() per testcase  │
                │               └── cleanup()                   │
                └────────────────────────┬──────────────────────┘
                                         │ subprocess.Popen
                                         ▼
                         ┌────────────────────────────────┐
                         │  nsjail (Linux namespaces)     │
                         │   ├── user / pid / mount ns    │
                         │   ├── rlimit_as / cpu / nofile │
                         │   ├── seccomp policy           │
                         │   └── exec compiler / runtime  │
                         └────────────────────────────────┘
```

## Components

### `nsjps_flask/`

The HTTP layer. Three small modules:

- `server.py` — defines the single `ProgrammingServer` Flask-RESTful resource at `POST /`. Parses the JSON envelope, decodes the protobuf-text-format `CodeRequest`, dispatches to the correct manager, and returns the protobuf-text-format `CodeReply`.
- `wsgi.py` — WSGI entry point loaded by gunicorn.
- `gunicorn.py` — gunicorn configuration: worker count derived from CPU, `sync` workers, request timeouts, access logs.

`server.py` also exposes a liveness endpoint at `GET /` (returns the literal string `"OK"`) and a debug dump of the public language settings at `GET /public_settings/`.

### `code_evaluator/`

The orchestration layer.

- `code_manager.py` — `CodeManager` is the base class. `ThreadingCodeManager` runs each testcase in its own Python thread (the default). `EvaluationScriptCodeManager` is used when the request includes an `evaluation_script` and runs that script with a relaxed sandbox.
- `code_runner.py` — `CodeRunner` builds the `nsjail` argument list for one compile invocation and one run invocation, then calls `subprocess.Popen` with the user's stdin file. `EvaluationScriptCodeRunner` overrides this to inject extra capabilities (CHOWN/SETUID/SETGID/FOWNER/DAC_OVERRIDE) and disable the user namespace, which is required when the evaluation script needs to bring up `pg_virtualenv`.

### `proto/`

The wire schema:

- `request.proto` — `CodeRequest` (full code, language, optional compile/run options, list of testcases, optional evaluation script) and `CodeReply` (overall status, compilation result, per-testcase results).
- `common.proto` — `ResourceLimits` (time, memory, processes).
- `default_settings.proto` — `PublicSettings` (per-language compile/run command, defaults) and `PrivateSettings` (the nsjail argument template).

Bindings are generated with `make generate-proto` and committed to the repo so the project builds without `protoc` on the host.

### `config/`

- `public_settings.conf` — Per-language definition: filename, optional binary filename, compile command and args, runtime command and args, default resource limits. Read at server startup.
- `private_settings.conf` — The skeleton `nsjail` invocation. Defines the language-independent flags (mount binds, working directory, rlimits, time limits, logging) using template placeholders that are filled in per request.

## Request Lifecycle

The server receives one HTTP request per code submission. For a request without an evaluation script (the standard test-case flow):

1. **Parse.** The JSON body's `message` field is decoded as ASCII (non-ASCII bytes are dropped) and parsed into a `CodeRequest` protobuf message.

2. **Allocate a sandbox directory.** A 5-digit random UID in `[30000, 60000)` is generated using the OS's CSPRNG. The job directory is `~/nsjps_<uid>/`. If that directory already exists (a rare collision), a new UID is drawn. After three failures the request is aborted.

3. **Lay out the filesystem.** The directory structure for a request with `N` testcases looks like:

    ```text
    ~/nsjps_<uid>/
    ├── proc/                       # bind-mounted into the sandbox
    ├── <source_filename>           # user's source code
    ├── log                         # nsjail log path
    ├── test_0/
    │   └── input                   # testcase 0 stdin
    ├── test_1/
    │   └── input
    └── …
    ```

4. **Compile (if required).** The compile command from `public_settings.conf` is templated with the per-request resource limits and any `compilation_options.extra_args`, then wrapped in the `nsjail` invocation from `private_settings.conf`. The compile runs once per request, with `stdin` closed and `stdout`/`stderr` captured.

5. **Run testcases.** Each testcase is run in its own Python thread by `ThreadingCodeManager`. Each thread:
    - Templates the runtime command and resource limits.
    - Wraps it in the same nsjail skeleton.
    - Opens `test_<i>/input` and feeds it to the child as stdin.
    - Captures `stdout` and `stderr`.
    - Compares `stdout` against the testcase's `expected_output` to assign a status (`OK`, `WRONG_ANSWER`, `PRESENTATION_ERROR`, etc.).

6. **Aggregate.** The threads are joined. Per-testcase statuses become `test_case_results`, and the overall status is the first non-`OK` testcase, or `OK` if every testcase passed.

7. **Clean up.** The job directory is removed regardless of outcome.

## Status Determination

`CodeRunner.parse_error` inspects the captured stderr to assign a status:

- `run time >= time limit` in stderr → `TIME_LIMIT_EXCEEDED`
- `terminated with signal` in stderr → `RUNTIME_ERROR` (a child killed by SIGSEGV/SIGKILL etc.)
- `exited with status: 0` and stdout mismatched → `WRONG_ANSWER`
- Any other failure → `RUNTIME_ERROR`
- Stdout matches expected exactly → `OK`
- Stdout matches after stripping leading/trailing whitespace → `PRESENTATION_ERROR`
- Compile step produced any stderr → `COMPILATION_ERROR`

There is no separate `MEMORY_LIMIT_EXCEEDED` detection today — over-memory programs are reported as `RUNTIME_ERROR` (see the `parse_error` TODO).

## Configuration Templating

Both `public_settings.conf` and `private_settings.conf` use Mustache-like double-brace placeholders that are substituted at request time:

| Placeholder                  | Source                                                |
| ---------------------------- | ----------------------------------------------------- |
| `{{ ROOT_DIRECTORY }}`       | The per-request sandbox directory                     |
| `{{ USER_ID }}`, `{{ GROUP_ID }}` | The random UID for this request                  |
| `{{ TIME_LIMIT }}`           | Effective time limit (request override or default)    |
| `{{ MEMORY_LIMIT }}`         | Effective memory limit                                |
| `{{ PROCESS_LIMIT }}`        | Effective process limit                               |
| `{{ LOG_PATH }}`             | Path to the nsjail log file inside the sandbox        |
| `{{ FILENAME }}`             | The language's source filename                        |
| `{{ BINARY_FILENAME }}`      | The compiled artifact (for compiled languages)        |
| `{{ EXTRA_ARGS }}`           | Joined `extra_args` from the request                  |
| `{{ LANGUAGE_INDEPENDENT_ARGS }}` | Flags shared across all languages                |
| `{{ LANGUAGE_DEPENDENT_ARGS }}`   | The compile or run command for this language     |
| `{{ HOME }}`                 | The container user's `$HOME`, used to locate `nsjail` |

Per-request resource limits in `compilation_options` and `runtime_options` are merged on top of the language defaults using `MergeFrom` (request fields override defaults, missing fields fall back).

## Evaluation-Script Mode

When `CodeRequest.evaluation_script` is set, `EvaluationScriptCodeManager` takes over:

- It writes the script to `~/nsjps_<uid>/evaluator.script`.
- The runtime command is rewritten to `<evaluation_script_lang> evaluator.script <source_filename>`.
- `EvaluationScriptCodeRunner` injects `--disable_clone_newuser` plus capabilities `CAP_CHOWN`, `CAP_FOWNER`, `CAP_SETUID`, `CAP_SETGID`, `CAP_DAC_OVERRIDE` into the nsjail args, before the `--` separator. This is required so `pg_virtualenv` can spin up an ephemeral Postgres for SQL evaluation.
- Test case comparison is skipped; the script's stdout is returned verbatim as `evaluation_result_json`.

This path is used by SQL/HTML/CSS-style flows that need a richer environment than a single `stdin → stdout` test case.

## Concurrency Model

Concurrency exists at two levels:

- **Across requests:** gunicorn forks `WEB_CONCURRENCY` worker processes (default derived from CPU count, see `nsjps_flask/gunicorn.py`). Each worker handles one request at a time.
- **Within a request:** testcases run in Python threads. Because each `nsjail` invocation is a separate OS process, the GIL does not serialise them — the threads spend most of their time blocked on `subprocess.communicate`.

There is no shared mutable state across requests beyond the global `PUBLIC_SETTINGS`/`PRIVATE_SETTINGS` objects, which are loaded once at boot.

## Languages

The server supports the languages declared in `public_settings.conf`. At time of writing:

| Language ID  | Toolchain                          | Compiled |
| ------------ | ---------------------------------- | -------- |
| `c`          | `/usr/bin/gcc`                     | yes      |
| `cpp`        | `/usr/bin/g++`                     | yes      |
| `java`       | OpenJDK 17 (`javac` + `java`)      | yes      |
| `py`         | Python 2.7                         | no       |
| `py3`        | `/usr/bin/python3`                 | no       |
| `bash`       | `/bin/bash`                        | no       |
| `javascript` | Node.js (`/usr/bin/node`)          | no       |
| `verilog`    | Icarus Verilog (`iverilog` + `vvp`)| yes      |
| `haskell`    | GHC via `stack`                    | yes      |
| `zip`        | (no runtime — evaluation-script-only) | n/a   |

Adding a language is a configuration-only change in the simple case: add an entry to `public_settings.conf` and an install script under `scripts/lang_install/` that ensures the toolchain is present in the image.

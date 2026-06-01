# Supported Languages

Languages are defined in `configs/languages.yaml`. Adding a language requires a YAML block and an install script in `scripts/lang_install/<id>.sh`. No Go changes.

## Currently supported

| ID | Name | Interpreter / Compiler | Source filename |
|----|------|------------------------|-----------------|
| `py3` | Python 3 | `/usr/bin/python3` | `solution.py` |
| `c` | C | `/usr/bin/gcc` | `solution.c` |
| `cpp` | C++ | `/usr/bin/g++` | `solution.cpp` |
| `java` | Java | `/usr/bin/javac` + `/usr/bin/java` | from request (must match public class name) |
| `bash` | Bash | `/bin/bash` | `solution.sh` |
| `javascript` | JavaScript (Node.js) | `/usr/bin/node` | `solution.js` |
| `verilog` | Verilog | `/usr/bin/iverilog` | `solution.v` |

## Resource limits

Limits are per-language in the YAML config. Request-level overrides are clamped to the configured maximums.

Values below are the defaults from `configs/languages.yaml`. A request may override
`wall_time_s`, `memory_kb`, and `max_processes` per step via the `build`/`run` objects;
an override replaces the default for that field (missing fields fall back to the default).

| Language | Build wall time | Build memory | Run wall time | Run memory |
|----------|----------------|--------------|---------------|------------|
| py3 | — | — | 9s | 100 MiB |
| c | 10s | 1 GiB | 5s | 256 MiB |
| cpp | 10s | 1 GiB | 5s | 256 MiB |
| java | 15s | 512 MiB | 10s | 512 MiB |
| bash | — | — | 9s | 100 MiB |
| javascript | — | — | 9s | 256 MiB |
| verilog | 10s | 100 MiB | 9s | 100 MiB |

## Java notes

Java requires the source file to be named after the public class. Requests for `java` must include **both** `source_filename` and `artifact_filename` in the request body (e.g., `"source_filename": "HelloWorld.java"`, `"artifact_filename": "HelloWorld"`). Both use `..._strategy: from_request` in the config. The API returns `400 missing_field` if either is absent for a `java` submission.

`artifact_filename` is the class name passed to `java` at run time (the `.java`/`.class` base). The caller supplies it explicitly rather than the server deriving it.

## Adding a language

1. Add a YAML block to `configs/languages.yaml`:
   ```yaml
   - id: rust
     name: Rust
     source_filename: solution.rs
     artifact: solution
     build:
       cmd: /usr/local/bin/rustc
       args: ["-o", "{{artifact}}", "{{source}}"]
       limits: { wall_time_s: 30, memory_kb: 524288, max_processes: 100 }
       flag_allowlist: ["-O", "--edition=*"]
     run:
       cmd: ./{{artifact}}
       limits: { wall_time_s: 5, memory_kb: 131072, max_processes: 64 }
     smoke: { cmd: /usr/local/bin/rustc, args: ["--version"] }
   ```

2. Add `scripts/lang_install/rust.sh`:
   ```bash
   #!/usr/bin/env bash
   set -euo pipefail
   curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal
   rustc --version
   ```

3. `docker build`. No Go changes.

## Smoke probes

Each language has a smoke probe (`smoke.cmd` + `smoke.args`) that runs at startup via `/readyz`. If the probe fails (non-zero exit), that language is reported as degraded in `/readyz` and the response status is `503`. The service continues to serve other languages.

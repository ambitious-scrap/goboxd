# API Reference

Base URL: `http://localhost:8080`

---

## POST /run

Execute a submission against one or more test cases.

### Request

```json
{
  "language": "cpp",
  "source": "#include <iostream>\nint main(){std::cout<<\"hi\\n\";}",
  "source_filename": "solution.cpp",
  "build": {
    "limits": {"wall_time_s": 5, "memory_kb": 1048576, "max_processes": 100},
    "flags": ["-O2"]
  },
  "run": {
    "limits": {"wall_time_s": 3, "memory_kb": 262144, "max_processes": 64},
    "flags": []
  },
  "tests": [
    {"stdin": "hello", "expected_stdout": "hi\n"}
  ]
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `language` | string | yes | Must match an id from `/info` |
| `source` | string | yes | UTF-8, ≤ `max_source_bytes` (default 256 KiB) |
| `source_filename` | string | conditional | Required only for languages that take it from the request (e.g. Java). Single path component. |
| `artifact_filename` | string | conditional | Same, for languages whose artifact name comes from the request (e.g. Java). |
| `build` | object | no | Per-build-step overrides. Ignored for interpreted languages. |
| `build.limits` | object | no | Partial override of `{wall_time_s, memory_kb, max_processes}`; missing fields fall back to language defaults. |
| `build.flags` | string[] | no | Compiler flags; validated against the language's build allowlist. |
| `run` | object | no | Per-run-step overrides. |
| `run.limits` | object | no | Partial override of `{wall_time_s, memory_kb, max_processes}`. |
| `run.flags` | string[] | no | Run-time flags; validated against the language's run allowlist (empty by default). |
| `tests` | object[] | yes | At least one; at most `max_tests`. |
| `tests[].stdin` | string | no | Passed to the program's stdin |
| `tests[].expected_stdout` | string | yes | Compared against stdout |

Limit overrides are a **partial replace**: each present field replaces the language default
for that step; absent fields keep the default. There is no server-imposed ceiling.

### Response — 200

```json
{
  "status": "accepted",
  "build": {
    "status": "ok",
    "duration_ms": 1240,
    "stdout": "",
    "stderr": ""
  },
  "tests": [
    {
      "status": "accepted",
      "stdout": "hello\n",
      "stderr": "",
      "duration_ms": 38,
      "memory_peak_kb": 8192
    }
  ]
}
```

`build` is present only for compiled languages. `build.status` is one of `ok`, `failed`, or `internal_error`.

### Top-level status values

| Value | Meaning |
|---|---|
| `accepted` | Build ok (if applicable) and every test accepted |
| `build_failed` | Compilation failed; all tests are `not_executed` |
| `wrong_output` | First test whose stdout didn't match |
| `output_whitespace_mismatch` | First test that matches after whitespace normalization |
| `time_exceeded` | First test that hit the wall time limit |
| `memory_exceeded` | First test that was OOM-killed |
| `runtime_error` | First test with a non-zero exit code (not OOM, not timeout) |

Top-level status is always the **first** non-accepted test status in order.

### Per-test status values

Same set as above, plus `not_executed` (only when build failed).

### Error responses

All errors: `{"error": {"code": "...", "message": "..."}}` 

| HTTP | Code | Cause |
|---|---|---|
| 400 | `invalid_json` | Malformed request body |
| 400 | `missing_field` | `language` or `tests` absent |
| 400 | `unknown_language` | Language id not registered |
| 400 | `source_too_large` | Source exceeds 256 KiB |
| 400 | `invalid_filename` | Source/artifact filename failed validation |
| 400 | `invalid_flag` | Build or run flag not in the per-step allowlist |
| 400 | `too_many_tests` | More than `max_tests` test cases supplied |
| 500 | `internal_error` | Server-side failure (nsjail missing, disk full, etc.) |

**User code crashing is never a 5xx.** A crash returns `200` with `status: runtime_error`.

---

## GET /healthz

Liveness check. No dependencies checked.

```json
{"status": "ok"}
```

Always `200`.

---

## GET /readyz

Readiness check. Probes nsjail and runs each language's smoke probe at startup; returns cached results.

```json
{
  "status": "ok",
  "nsjail": {"ok": true, "version": "nsjail version 3.4"},
  "languages": {
    "py3":  {"ok": true,  "version": "Python 3.10.12"},
    "cpp":  {"ok": true,  "version": "g++ (Ubuntu) 11.4.0"}
  }
}
```

`200` only if nsjail is OK **and** every language passed its smoke probe. Otherwise `503` with `"status": "degraded"` (failed languages carry an `error` field instead of `version`).

---

## GET /info

Build metadata, language list, and server stats. Always `200`.

```json
{
  "build_info": {
    "version": "v1.0.0",
    "commit": "abc1234",
    "go_version": "go1.22.3"
  },
  "nsjail": {
    "path": "/usr/local/bin/nsjail",
    "version": "nsjail version 3.4"
  },
  "languages": [
    {
      "id": "py3",
      "name": "Python 3",
      "version": "Python 3.10.12",
      "default_run_limits": {
        "wall_time_s": 9,
        "memory_kb": 102400,
        "max_processes": 100
      }
    }
  ],
  "limits": {
    "max_source_bytes": 262144,
    "max_tests": 100,
    "max_concurrent_jobs": 8
  },
  "stats": {
    "jobs_total": 1042,
    "in_flight_jobs": 3,
    "jobs_failed_internal": 0,
    "last_internal_error_at": null,
    "disk_free_bytes_jail_dir": 10737418240,
    "uptime_s": 3600
  }
}
```

`stats.last_internal_error_at` is `null` until the first server-side error, then an RFC 3339 timestamp.

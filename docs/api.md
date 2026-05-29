# API Reference

Base URL: `http://localhost:8080`

---

## POST /run

Execute a submission against one or more test cases.

### Request

```json
{
  "language": "py3",
  "source": "print(input())",
  "flags": [],
  "tests": [
    {"stdin": "hello", "expected_output": "hello\n"},
    {"stdin": "world", "expected_output": "world\n"}
  ]
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `language` | string | yes | Must match an id from `/info` |
| `source` | string | yes | UTF-8, ≤ 256 KiB |
| `flags` | string[] | no | Compiler flags; validated against per-language allowlist |
| `tests` | object[] | yes | At least one required |
| `tests[].stdin` | string | no | Passed to the program's stdin |
| `tests[].expected_output` | string | yes | Compared against stdout |

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

`build` is present only for compiled languages.

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
| 400 | `invalid_filename` | Language source filename failed validation |
| 400 | `invalid_flag` | Compiler flag not in allowlist |
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

Readiness check. Runs smoke probes for each language at startup; returns cached results.

```json
{
  "status": "ok",
  "languages": {
    "py3":  {"ok": true,  "version": "Python 3.10.12"},
    "cpp":  {"ok": true,  "version": "g++ (Ubuntu) 11.4.0"}
  }
}
```

`200` if all languages are ready. `503` with `"status": "degraded"` if any language failed its smoke probe.

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
    "path": "/usr/local/bin/nsjail"
  },
  "languages": [
    {
      "id": "py3",
      "name": "Python 3",
      "version": "Python 3.10.12",
      "default_run_limits": {
        "wall_time_s": 5,
        "memory_kb": 262144,
        "max_processes": 64
      }
    }
  ],
  "stats": {
    "total_requests": 1042,
    "in_flight": 3,
    "total_errors": 0,
    "disk_free_bytes_jail": 10737418240,
    "uptime_s": 3600
  }
}
```

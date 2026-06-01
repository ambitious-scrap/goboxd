# Security

## Threat model

The service executes arbitrary user-submitted code. The attacker controls the source code, stdin, compiler flags, and (within limits) resource requests. The server controls file naming, command invocation, resource limits, and output handling. The goal is to prevent any request from affecting other requests, the host filesystem, or the host network.

## Closed vulnerabilities

### 1. Path traversal via filename

**Location:** `internal/api/validate.go:validateFilename`, `internal/jail/jail.go:Create`

Filenames are validated against `^[A-Za-z0-9._-]+$` and must pass `filepath.Base(name) == name`. This rejects `../etc/passwd`, `/etc/passwd`, `foo/bar`, and names starting with `.`. The validated name is never passed through shell expansion — only to `filepath.Join` with the jail-local workdir.

### 2. Shell injection via command construction

**Location:** `internal/sandbox/sandbox.go:Run`, `internal/jail/jail.go`

All subprocesses are launched via `exec.Command` with an explicit argv slice. No `sh -c`, no string concatenation of user input into a command string. `os.MkdirAll` and `os.RemoveAll` handle directory operations without a shell.

### 3. Compiler flag injection

**Location:** `internal/flags/flags.go:Validate`

Each requested flag is matched against a per-language allowlist. Matching supports exact strings and `filepath.Match` patterns (e.g. `-std=*`). The following prefixes are unconditionally rejected regardless of the allowlist: `-fplugin=`, `--specs=`, `-Wl,`, `-x`, `-B`. Response file arguments (`@`) are also rejected. Unknown flags return a `400` before any subprocess is spawned.

### 4. Request size limits

**Location:** `internal/api/handler.go:run` (body), `internal/sandbox/sandbox.go` (`--rlimit_fsize`)

`http.MaxBytesReader` caps the request body at `MaxBodyBytes` (default 256 KiB). nsjail's `--rlimit_fsize` caps file writes inside the sandbox at 100 MiB (`internal/sandbox/sandbox.go`, `--rlimit_fsize 100`). Both layers are required: the HTTP cap prevents large payloads from reaching the runner; the nsjail cap prevents a submitted program from filling the host disk through its output files.

### 5. Jail directory name collisions under concurrent load

**Location:** `internal/jail/jail.go:Create`

Each jail directory name is `{atomic_counter}_{pid}_{16_hex_chars}`. The atomic counter guarantees uniqueness within a process, the PID distinguishes restarts that reuse the same tmpfs, and the crypto/rand suffix prevents enumeration. `os.Mkdir` (not `MkdirAll`) is used for the final component so a collision returns an error rather than silently reusing an existing directory.

### 6. Unbounded subprocess output

**Location:** `internal/sandbox/sandbox.go:limitedWriter`

stdout and stderr are captured through a `limitedWriter` that stops writing after `OutputCap` bytes (default 64 KiB). Excess bytes are discarded and a `...[truncated]` marker is appended to stdout. Without this, a program writing `/dev/zero` to stdout would exhaust host memory before the time limit fired.

### 7. Stale jail directories from crashed runs

**Location:** `internal/jail/jail.go:SweepOrphans`, `cmd/goboxd/main.go`

`jail.SweepOrphans` runs once at server startup and removes subdirectories of the jail base older than 10 minutes. Per-request cleanup is registered as a `defer` immediately after `jail.Create` succeeds, so it fires on every exit path including panics caught by the recovery middleware. Together these ensure `/tmp/goboxd` stays bounded across restarts and sustained load.

## What nsjail provides

nsjail enforces:
- Network namespace isolation (no outbound connections from user code)
- PID namespace (processes cannot see or signal host processes)
- Mount namespace (read-only view of the host filesystem except the workdir)
- Wall time and address space rlimits
- Max process count

goboxd's application-level controls above are defense-in-depth; nsjail is the primary isolation boundary.

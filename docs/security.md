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

**Location:** `internal/api/handler.go:run` (body + source), `internal/sandbox/sandbox.go` (`--rlimit_fsize`)

`http.MaxBytesReader` caps the whole request body at `MaxBodyBytes` (default 4 MiB) and returns `request_too_large`; the `source` field is separately capped at `MaxSourceBytes` (default 256 KiB) and returns `source_too_large`. The two limits are distinct so an oversize source is reported precisely instead of being masked by the body-reader cap. nsjail's `--rlimit_fsize` caps file writes inside the sandbox at 100 MiB (`internal/sandbox/sandbox.go`, `--rlimit_fsize 100`). All layers are required: the HTTP caps prevent large payloads from reaching the runner; the nsjail cap prevents a submitted program from filling the host disk through its output files.

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
- Max process count (`--rlimit_nproc`), reinforced by the cgroup `pids.max` cap below

goboxd's application-level controls above are defense-in-depth; nsjail is the primary isolation boundary.

## cgroup v2 resource controls

**Location:** `internal/sandbox/cgroup.go`

Each run gets a dedicated cgroup v2 directory with, where the controller is delegated on the host:
- `memory.max` (+ `memory.swap.max=0`) — hard RSS cap from `memory_kb`; OOM kills are clean and swap can't be used to dodge the limit.
- `pids.max` — absolute, hierarchical process-count cap from `max_processes`. This is the real fork-bomb guard: `pids.current` is counted across the whole subtree, whereas `--rlimit_nproc` is per-UID and therefore shared by every concurrent run under the sandbox UID.
- `cpu.max` — optional CPU-bandwidth cap from the per-language `cpu_max_percent` (off by default). A sub-core quota inflates wall-clock time, so enable it only when grading on CPU-time.

The `memory`, `cpu`, and `pids` controllers are delegated independently at startup; a host that cannot delegate `cpu`/`pids` still gets memory accounting, and the missing caps are skipped silently.

## 8. seccomp-bpf syscall filtering

**Location:** `internal/sandbox/sandbox.go:buildNsjailArgs`, `internal/config` (`server.seccomp_mode`, `language.seccomp_policy`)

nsjail loads a kafel seccomp-bpf program per run via `--seccomp_string`, restricting the syscalls user code may make. This is layered **on top of** namespace + capability isolation: namespaces stop you from *seeing* host resources; seccomp stops you from *reaching the kernel surface* used to break out of them. `server.seccomp_mode` selects the behaviour:

- **off** — no filter (byte-for-byte the un-filtered path).
- **audit** — the policy is loaded together with `--seccomp_log`, so denied syscalls are logged rather than only killed. Use to observe a workload's real syscall set.
- **enforce** *(default)* — the policy is applied as written; a denied syscall delivers `SIGSYS` and terminates the process (surfaces as `runtime_error`).

**Policy: deny-list with `DEFAULT ALLOW`.** Rather than an allow-list (`DEFAULT KILL` + per-language enumeration of every syscall a JVM/V8/CPython needs — brittle across runtimes and arch, and the documented way to break JIT), we `KILL` the kernel sandbox-escape surface and allow the rest:

```
ptrace, mount, pivot_root, chroot, setns, unshare,
keyctl, add_key, request_key, bpf, perf_event_open,
init_module, finit_module, delete_module,
kexec_load, reboot, swapon, swapoff,
process_vm_readv, process_vm_writev
```

This is the same shape as Docker's default profile: thread/process creation (`clone`, and `clone3` by glibc fallback), `mmap`/`mprotect(PROT_EXEC)`, `futex`, file and signal I/O all remain available, so all seven languages — including the JVM, Node/V8 and CPython, which spawn threads at startup — run unmodified, while `ptrace`, module loading, `bpf`, `kexec`, `process_vm_*` and mount/namespace manipulation are hard-blocked. The policy is defined once (a YAML anchor, `&deny_seccomp`) and shared by every language. Verified end-to-end: the differential conformance suite (`tests/conformance`) passes under enforce across all languages, and a submission that calls `ptrace` is killed (`runtime_error`) rather than succeeding.

> Names not present in this build's kafel (`umount2`, `kexec_file_load`) are intentionally omitted — kafel fails the whole policy compilation on an unknown identifier, which silently disables the filter. A kafel rule list must **not** have a trailing comma after the final `}` for the same reason.

## 9. Prometheus metrics on a separate admin port

**Location:** `internal/metrics`, `cmd/goboxd/main.go` (`server.metrics_port`)

Operational telemetry is exposed as a Prometheus `/metrics` endpoint on a **dedicated admin port** (`server.metrics_port`, default `9090`), bound by a separate `http.Server` and never mounted on the public API router. This keeps internal detail — in-flight count, per-language verdict distribution, queue-wait latency, Go runtime/process stats — off the surface a submitter can reach. Set `metrics_port` to `0` (or pass `--metrics-port -1`) to disable it entirely.

Label cardinality is bounded on purpose: series are labelled only by `language` (the fixed configured set) and `verdict` (the fixed status constants). Source hashes, request ids, and filenames are never used as labels, since unbounded label values would explode the time-series count and OOM the scrape target.

Exposed series: `goboxd_runs_total{language,verdict}`, `goboxd_run_duration_seconds{language,phase=build|run}` (histogram), `goboxd_queue_wait_seconds` (histogram), `goboxd_inflight` (gauge), `goboxd_requests_total`, `goboxd_internal_errors_total`, `goboxd_queue_depth` (gauge), `goboxd_rejected_total`, `goboxd_cache_hits_total{language}`, `goboxd_cache_misses_total{language}`, `goboxd_build_wait_seconds` (histogram), plus the standard `go_*` / `process_*` collectors.

## 10. Load shedding never mutates verdicts

**Location:** `internal/api/handler.go`

Under saturation `/run` sheds load at the door — `503 server_busy` + `Retry-After` — rather than admitting the request and degrading it. This is deliberate: a judge must never return a load-dependent verdict. The same submission must grade identically whether the server is idle or flooded, so admission control is kept strictly separate from per-run limits. We never lower `wall_time_s` or any other limit under load (the rejected "load-adaptive clamping" design); shedding only changes *whether* a request runs, never *how* it is graded.

## 11. Artifact cache

**Location:** `internal/artifactcache`, `internal/runner/runner.go`

Compiled artifacts are cached content-addressed under `server.cache_dir` (a host-writable directory) to skip redundant recompiles. Security-relevant properties:

- **Verdict-neutral.** Only the build output is reused; the run phase always executes live in a fresh jail per test, so caching cannot change a grade. Run results are never cached.
- **Toolchain-versioned key.** The key folds in the language's smoke-probe toolchain version, so a compiler/runtime upgrade can never serve a binary built by the old toolchain. An unknown (empty) version skips the cache rather than risking a stale hit.
- **Exec from a copy.** On a hit, cached files are copied into the request's own jail; the canonical cached file is never handed to the sandbox, and the per-run jail remains the only writable surface nsjail sees.
- **Bounded and best-effort.** A count cap evicts the oldest entry on insert and a startup TTL sweep reclaims stale entries, so the cache dir can't grow without bound. Any IO error degrades to a normal build — the cache can never fail or block a run. Only successful builds are stored.

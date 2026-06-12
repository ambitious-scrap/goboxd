# goboxd — Design & Rationale (the "why" book)

*A from-the-ground-up explanation of what goboxd is, every feature it has, the quirks
that surprise people, why each design decision was made, and which tempting alternatives
were deliberately rejected.*

This document assumes you are a competent engineer but **not** an expert in Linux
sandboxing, kernel security, or scheduler design. Every domain-specific term is explained
the first time it appears. If you already know the basics, the rationale and
"alternatives rejected" boxes are the parts worth your time — they are the institutional
memory that the code itself cannot record.

Companion docs:
- `README.md` — 60-second quick start.
- `PERSONAL_README.md` — feature-by-feature walkthrough with ELI5 + glossary per section.
- `docs/architecture.md`, `docs/api.md`, `docs/security.md`, `docs/benchmarks.md`,
  `docs/conformance.md`, `docs/improvements.md`, `docs/roadmap.md` — focused references.

This file is the **teaching + decisions** layer that ties them together.

---

## 0. The one sentence, and the one law

**One sentence:** goboxd is a single Go program that accepts source code + test cases over
HTTP, runs the code inside a locked-down Linux sandbox, compares its output to the expected
answers, and returns a per-test grade.

**The one law (the "holy grail"):**

> A verdict must be a *pure function* of `(source, tests, limits)`.
> The same submission must grade **identically** whether the server is idle or on fire.

A "pure function" here means: given the same inputs, you always get the same output, with no
hidden dependence on the outside world. The outside world that could leak in is **server
load** — how busy the machine is. Almost every interesting decision in this project is
downstream of refusing to let load change a grade. Keep this law in mind; it is the reason
several "obvious" optimizations were rejected.

**Why this matters in plain terms:** this is the engine an *online judge* (think
competitive-programming sites, automated graders, take-home test platforms) would sit on.
If a student's correct solution failed only because 200 other students submitted at the same
moment, the judge is broken — even though no code crashed. Fairness *is* the product.

---

## 1. The vocabulary you need (read once, refer back)

| Term | Plain meaning |
|---|---|
| **Sandbox / isolation** | Running untrusted code in a confined box so it can't touch the host, the network, or other jobs. |
| **Verdict** | The graded outcome of one test: `accepted`, `wrong_output`, `time_exceeded`, etc. |
| **Compiled vs interpreted language** | Compiled (C, C++, Java, Verilog) is first *built* into an artifact, then run. Interpreted (Python, Bash, JavaScript) runs the source text directly. |
| **Artifact** | The output of a build step — `a.out`, `*.class`, a Verilog `vvp` image. |
| **Toolchain** | The compiler/interpreter and friends for a language (e.g. `g++`, `python3`). |
| **Syscall (system call)** | The only way a program asks the Linux kernel to do anything privileged — open a file, start a process, send a packet. The kernel is the real security boundary; controlling syscalls controls the program. |
| **Namespace** | A Linux kernel feature that gives a process its *own private view* of one resource — its own filesystem mounts, its own process list, its own (empty) network. |
| **cgroup v2** | "Control group" — a Linux mechanism that puts hard ceilings on a group of processes' memory, CPU, and process count, and *accounts* their usage. |
| **seccomp-bpf** | "Secure computing" — a kernel filter that decides, syscall by syscall, what a process is allowed to call. |
| **rlimit** | An older per-process resource limit (max file size, max processes, max address space). Coarser than cgroups; used here as a backstop. |
| **chroot** | "Change root" — makes a directory *look like* the whole filesystem `/` to a process, so it can't see anything above it. |
| **Semaphore** | A counter that caps how many things may happen at once. Acquire to start, release when done; if the counter is at zero you wait. |
| **Content-addressed** | Identified by a hash of the *content itself*, so identical content always maps to the same key. |
| **Verdict-neutral** | A feature that can never change a grade, by construction. |

You don't need to memorize these — they're re-explained in context below.

---

## 2. The big picture: one stateless binary

goboxd is **one Go binary**, and it is **stateless** — each request is fully
self-contained. There is no database, no session, no shared mutable grading state between
requests. Two reasons this was chosen:

1. **It makes the one law easy to keep.** If a request can't see any other request's state,
   it physically cannot be influenced by one.
2. **It makes the thing trivially horizontally scalable later.** Stateless boxes can be
   cloned behind a load balancer with zero coordination. (The async/job-bus version of this
   is on the roadmap, not built — see §13.)

The request flows through a small set of packages, each with one job:

```
cmd/goboxd/         start-up: read config, wire everything, handle Ctrl-C cleanly
internal/config/    the YAML schema + defaults + validation
internal/registry/  the language table; turns {{placeholders}} into real commands
internal/api/       HTTP handlers, request validation, admission control (the "door")
internal/runner/    orchestration: build → run each test → grade
internal/sandbox/   the actual nsjail invocation: namespaces, cgroups, seccomp, output capture
internal/jail/      per-run scratch directory: create, clean up, sweep crash leftovers
internal/flags/     per-language compiler-flag allow-listing
internal/limits/    merging request limit-overrides onto language defaults
internal/status/    output comparison + final verdict computation
internal/artifactcache/  the compiled-output cache (C-2)
internal/metrics/   Prometheus counters (on a separate admin port)
internal/obs/       structured logs + in-process counters for /info
```

**Why split this finely?** Each file is independently auditable. For a security-sensitive
service, "I can read the entire isolation mechanism in one 200-line file" is worth more than
"clever code reuse." This is a deliberate bias toward *legibility over DRY*.

---

## 3. The HTTP API, and the quirks that trip people up

Three public endpoints plus a private one.

- `POST /run` — the whole point: run code, get a verdict.
- `GET /healthz` — "am I alive?" Always `200`, checks nothing. Used by orchestrators to
  know whether to restart the process.
- `GET /readyz` — "am I able to *serve*?" Runs each language's smoke probe at startup and
  returns `503 degraded` if any toolchain is broken. The split between healthz and readyz is
  a standard Kubernetes pattern: liveness restarts you, readiness pulls you out of the load
  balancer without killing you.
- `GET /info` — build metadata, language versions, live counters.
- `GET /metrics` — Prometheus data, **on a different port** (see §10).

### The request shape, and why it's nested

```json
{
  "language": "cpp",
  "source": "...",
  "source_filename": "solution.cpp",
  "artifact_filename": "a.out",
  "build": { "limits": {...}, "flags": ["-O2"] },
  "run":   { "limits": {...}, "flags": [] },
  "tests": [ {"stdin": "...", "expected_stdout": "..."} ]
}
```

`build` and `run` are **separate objects** because the two phases have genuinely different
needs. A compile may legitimately need 30 seconds and a gigabyte; the resulting program
should run in a few seconds and a few hundred megabytes. Flattening them into one limit set
would either over-grant the run phase or strangle the build phase.

### Quirk 1: a crash in *your* code is never a `5xx`

This surprises people. If the submitted program segfaults, the HTTP response is **`200 OK`**
with `status: runtime_error`. A `5xx` is reserved for **goboxd's** failures (nsjail missing,
disk full). Rationale: from the judge's perspective, "your code crashed" is a *successful
grading* — the system did its job and produced a verdict. Conflating it with a server fault
would make monitoring and retries impossible to reason about.

### Quirk 2: limit overrides are a *partial replace with no ceiling*

`run.limits` may contain any subset of `{wall_time_s, memory_kb, max_processes}`. Present
fields replace the language default; absent fields keep it. There is **no server-imposed
maximum** — a request can ask for more time/memory than the default.

Why no ceiling? Because the deployment owner sets the language defaults, and the request is
trusted to be *configuration from the same operator*, not arbitrary public input, for the
limit fields. (Public-internet hardening — per-key quotas, hard ceilings — is a roadmap
item, §13.) The important invariant is the *next* quirk.

### Quirk 3: limits are never silently lowered under load

This is the one law in API form. The server will **never** quietly reduce your `wall_time_s`
because it's busy. The rejected design that would have done so ("load-adaptive clamping") is
discussed in §11 — it's the single most important "alternative not pursued" in the project.

### Quirk 4: `source_filename` / `artifact_filename` are conditional

Most languages don't need them — the server picks a filename. But some (Java) require the
file to be named after the public class, so the *request* must supply the name. The schema
marks these "conditional": required only for languages whose config says
`strategy: from_request`. The handler enforces this per language rather than globally.

### The verdict ladder

Top-level status is the **first non-`accepted` test status, in order** (or `build_failed`
if compilation failed, in which case every test is `not_executed`):

`accepted` · `build_failed` · `wrong_output` · `output_whitespace_mismatch` ·
`time_exceeded` · `memory_exceeded` · `runtime_error`

"First in order" matters: it makes the headline verdict deterministic and matches how a
human grader reads down a list and stops at the first failure.

---

## 4. Languages are data, not code

Every language lives in `configs/languages.yaml`. There is **no per-language branch in the
Go source**. A language block declares:

- the source filename (or `from_request`),
- an optional `build` step (command, args, limits, flag-allowlist),
- a `run` step,
- a `smoke` probe (a tiny known-good program proving the toolchain works),
- an optional `seccomp_policy`.

At runtime the engine substitutes placeholders — `{{source}}`, `{{artifact}}`,
`{{workdir}}`, `{{flags}}` — into the command templates. "Compiled" simply *means* "the YAML
has a `build` block"; "interpreted" means it doesn't. There is no other distinction in code.

Configured set: **py3, c, cpp, java, bash, javascript, verilog** (7).

**Adding a language** = add a YAML block + an install script under
`scripts/lang_install/<id>.sh` + rebuild the image. No Go recompile.

> **Decision: data-driven registry.**
> **Why:** new languages are a config edit, reviewable in isolation, and the core engine
> never grows a `switch lang { ... }` that rots. It also means the *same* code path is
> exercised by all languages, so a bug can't hide in one language's special case.
> **Alternative rejected — a Go interface per language (`type Language interface{...}`)
> with one implementation each.** That's the "idiomatic OO" answer. Rejected because it
> turns "add a language" into "write, compile, and ship Go," puts language quirks in
> imperative code that's harder to audit, and tempts each implementation to diverge. YAML
> + placeholders keeps the matrix honest.

> **Known quirk (documented in the audit):** installation is *not yet fully* data-driven —
> the Dockerfile still references per-language install scripts rather than deriving them
> purely from the YAML. The runtime registry is fully data-driven; the *build-time install*
> is the remaining seam. Tracked in the roadmap.

---

## 5. The sandbox — the heart of the thing

This is where untrusted code actually runs. Everything else is plumbing around this.

The tool is **nsjail** (Google's process-isolation launcher), built from a **pinned source
submodule** at tag **3.4** (`external/nsjail`). Every build step and every test run is a
fresh `nsjail` invocation.

> **Decision: nsjail built from a pinned source submodule, not an apt package.**
> **Why:** the isolation tool is the security boundary; we want a *reproducible, known*
> version, not whatever the base image's package repo happens to ship this month. A
> `make verify-nsjail` target asserts the pin so it can't silently drift.
> **Alternative rejected — `apt-get install nsjail` / a distro package.** Unpinned,
> version-drifts across rebuilds, and a security boundary you don't control the version of
> isn't one.
> **Alternative rejected (for now) — a heavier sandbox like gVisor or a Firecracker
> microVM.** These are stronger (gVisor reimplements the syscall surface in userspace; a
> microVM gives you a real separate kernel). They're on the roadmap as a "stronger tier."
> They were not used as the baseline because they add large operational weight (a second
> kernel, more memory per run, slower cold starts) for a workload where the namespaces +
> seccomp + cgroups stack already closes the realistic escape surface. The trade is
> startup latency and simplicity vs. defense depth; for a judge serving many short runs,
> per-run latency matters a lot.

### Layer 1 — namespaces + chroot (you can't *see* the host)

Each run gets its own:
- **mount namespace** with a `chroot` into the jail workdir: the program sees that directory
  as `/` and nothing above it. Host toolchain directories are bind-mounted **read-only** so
  the compiler/interpreter is available but unwritable.
- **PID namespace**: it can't see or signal host processes.
- **network namespace** with no interfaces: **no network at all**. User code cannot phone
  home, exfiltrate, or fetch anything. This is all-deny by default, not a blocklist.
- **user namespace**: it runs as an unprivileged user that maps to nobody-useful on the host.

### Layer 2 — cgroup v2 (you can't *exhaust* the host)

A dedicated cgroup is created per run, written, and torn down:

- **`memory.max`** (+ `memory.swap.max = 0`) — a hard memory ceiling. Exceed it → the kernel
  **OOM-kills** the run → goboxd reports `memory_exceeded`. Swap is set to zero so a program
  can't dodge the RSS cap by spilling to swap. After the run, `memory.peak` is read back into
  the response's `memory_peak_kb`.
- **`pids.max`** — an *absolute, whole-subtree* cap on process count, set from
  `max_processes`. **This is the real fork-bomb guard.** (A fork bomb is a program that
  recursively spawns processes until the machine dies.)
- **`cpu.max`** — an *optional* CPU-bandwidth cap, per language (`cpu_max_percent`, where
  100 = one core). **Off by default.**

> **Quirk: why does `pids.max` exist when nsjail already passes `--rlimit_nproc`?**
> Because `rlimit_nproc` is **per-UID**, and all concurrent runs share one sandbox UID. So
> 20 simultaneous runs *share* one process budget under `rlimit_nproc` — one greedy run
> could starve the others, and the limit is fuzzy. `pids.max` is counted per-cgroup, i.e.
> per-run, and is hierarchical (covers every descendant). It's the deterministic guard;
> `rlimit_nproc` is the cheap backstop. Keeping both is defense in depth.

> **Quirk: why is `cpu.max` off by default?**
> Because a sub-one-core CPU quota *inflates wall-clock time* — the program is throttled, so
> the same code takes longer in real seconds and could trip `time_exceeded`. Since grading
> is on **wall time**, a CPU cap would make verdicts depend on a server-side knob, which
> brushes against the one law. So it exists for operators who grade on CPU-time, but ships
> disabled. This is a documented trade-off, not an oversight.

> **Quirk: controllers are delegated independently at startup.**
> cgroup v2 requires each controller (memory/cpu/pids) to be "delegated" into goboxd's
> cgroup before it can be used. They're enabled *independently*, so a host that can't
> delegate `cpu`/`pids` still gets `memory` accounting. Missing controllers are skipped
> silently rather than failing the whole service. `cgroups_enabled` in `/info` tells you
> whether per-run cgroup memory accounting is live; if not, the sandbox falls back to the
> `rlimit_as` address-space cap (limits still enforced, but OOM detection and `memory_peak_kb`
> aren't available — peaks read 0).

### Layer 3 — seccomp-bpf (you can't *reach the kernel's escape hatches*)

Namespaces stop you from *seeing* host resources. seccomp stops you from *calling the
syscalls* used to break out of namespaces in the first place. It's the layer that assumes
the others might have a hole.

`server.seccomp_mode` has three settings:
- **`off`** — no filter (byte-for-byte the old, pre-seccomp behavior).
- **`audit`** — load the policy *and* `--seccomp_log`, so denied syscalls are logged but not
  killed. Use this to *discover* what syscalls a real workload makes before enforcing.
- **`enforce`** — apply the policy; a denied syscall delivers `SIGSYS` and kills the process
  (surfaces as `runtime_error`).

**The shipped `configs/languages.yaml` sets `enforce`.** (The *code fallback*, if the key is
entirely absent from a config, is `off` — a no-regression default. Don't confuse the two: the
product ships enforcing.)

> **The central seccomp decision: a deny-list with `DEFAULT ALLOW`, not an allow-list.**
> There are two ways to write a syscall filter:
> - **Allow-list (`DEFAULT KILL`):** enumerate *every* syscall the program is allowed to
>   make, kill everything else. Maximally strict.
> - **Deny-list (`DEFAULT ALLOW`):** allow everything *except* a named set of dangerous
>   syscalls.
>
> goboxd uses a **deny-list**, defined once as a shared YAML anchor (`&deny_seccomp`) aliased
> by every language. It `KILL`s the kernel sandbox-escape surface:
> ```
> ptrace, mount, pivot_root, chroot, setns, unshare, keyctl, add_key, request_key,
> bpf, perf_event_open, init_module, finit_module, delete_module,
> kexec_load, reboot, swapon, swapoff, process_vm_readv, process_vm_writev
> ```
> **Why deny-list and not the "more secure" allow-list?** Because a correct allow-list for a
> JVM, V8 (Node), and CPython is *enormous and brittle*. These runtimes spawn threads at
> startup, JIT-compile (which needs `mmap`/`mprotect` with exec permissions), and use
> `futex`, signals, and a long tail of syscalls that vary by libc version and CPU
> architecture. An allow-list is the **documented way to break a JIT** — miss one syscall
> and the language silently fails to start. The deny-list blocks the *small, stable,
> well-understood* set of escape syscalls and leaves the rest, so all seven languages run
> unmodified. This is the same shape as Docker's own default profile. The trade is "slightly
> larger theoretical attack surface" for "actually works across runtimes and arch without
> constant maintenance" — the right trade for a multi-language judge.

> **Quirk learned the hard way (two kafel footguns):** kafel is nsjail's policy language.
> (1) An **unknown identifier** — e.g. `umount2` or `kexec_file_load`, which aren't in this
> build's kafel — fails the *entire* policy compilation, which **silently disables the
> filter**. (2) A **trailing comma** after the final rule block does the same. Both mean a
> typo doesn't error loudly; it quietly leaves you unprotected. This is why there's a CI gate
> that boots the image under enforce and asserts a `ptrace` submission is actually killed —
> the only way to be sure the filter is live, not silently void.

> **Decision: ship seccomp in `audit` first, then flip to `enforce`.** Enforcing a wrong
> policy bricks a language. The audit mode existed specifically so the real syscall set of
> each language's smoke run could be observed before turning the kill switch on. The flip to
> enforce-by-default happened only after the differential conformance suite (next section's
> cousin) passed under enforce across all languages.

### Layer 4 — output capping (you can't *flood* the host)

stdout and stderr are captured through a custom `limitedWriter` with a hard byte cap
(`output_cap_bytes`, default 64 KiB). Past the cap, bytes are discarded and a
`...[truncated]` marker is appended. Without this, a program doing `cat /dev/zero` would
exhaust host memory *before* the wall-time limit fired — the time limit alone is not enough.

There's also `--rlimit_fsize 100` (100 MiB): a cap on how large a *file* the program can
write inside the jail, so it can't fill the host disk through output files (distinct from the
stdout cap, which is about the captured streams).

### How outcomes are detected

- **OOM kill** → read from cgroup v2 `memory.events` → `memory_exceeded`.
- **Timeout** → nsjail's wall-time kill (`--time_limit`), backed up by a Go `context`
  deadline as belt-and-suspenders → `time_exceeded`.
- **Any other non-zero exit** → `runtime_error`.

---

## 6. Grading — a deliberately boring pure function

Output comparison (`internal/status`) is intentionally simple and **pure**:

1. Exact byte match → `accepted`.
2. Equal after trimming **leading/trailing** whitespace from the **whole** output →
   `output_whitespace_mismatch`. (Internal whitespace differences are **not** normalized.)
3. Otherwise → `wrong_output`.

> **Quirk: why only trim the ends, not normalize internal whitespace?**
> Because that is exactly what the **reference implementation** this judge must match does.
> goboxd is graded *against* an existing reference (see §7); matching its whitespace
> semantics — even the slightly surprising "ends-only" rule — is the whole job. Being
> "smarter" here would mean being *wrong* relative to the spec.

Top-level status = first non-accepted test, or `build_failed` (→ all tests `not_executed`).

---

## 7. How we know it's correct — testing & conformance

Three layers, in increasing breadth:

1. **Unit + property tests** across the packages. *Property tests* assert that an invariant
   holds over *many generated inputs*, not a few hand-picked examples.
2. **Fuzz tests** (`go test -fuzz`) on the user-input surface: the placeholder resolver and
   flag expander, the verdict classifier, and — importantly — the **nsjail argv builder**.
   The argv-builder fuzz target asserts that **user-supplied tokens can never cross the `--`
   separator into nsjail's own flag region** (a flag-injection guard: if user input could
   become an nsjail flag, it could turn *off* isolation).
3. **Differential conformance** (`tests/conformance`, build tag `integration`): the strongest
   test. It drives the *live* service with the **reference implementation's own recorded
   fixtures** (`pyjail/.../testcases/<lang>/<case>/{request,reply}.txt`, in protobuf-text
   format) and asserts our verdict matches the reference's, across the reference languages.
   It runs **under seccomp enforce**, so the security policy is part of what's conformance-tested.

> **Quirk worth bragging about: one case where we are *more correct than the reference*.**
> `java/error_runtime` (a divide-by-zero) is recorded by the reference as `OK`. goboxd
> correctly returns `runtime_error`. The conformance suite pins this as a *known
> reference-bug correction* — i.e., the test explicitly says "we intentionally differ here
> because the reference is wrong." This is the difference between "passes the spec's tests"
> and "understands the spec."

> **Quirk: the fixtures aren't in the repo.** `pyjail/` is kept local and gitignored, so the
> conformance suite **skips** when fixtures aren't present rather than failing. This keeps
> someone else's reference material out of our history while still letting us run it locally
> and in CI.

**CI** (`.github/workflows/ci.yml`) runs `gofmt` + `vet` + `build` + `go test ./...`, plus a
`smoke` job that builds the Docker image, boots it `--privileged --cgroupns=host`, hits
`/readyz`, runs every language to `accepted` **under seccomp enforce**, and asserts a
`ptrace` submission is killed. That last assertion is the regression gate that catches the
"silently disabled filter" footgun from §5.

---

## 8. Concurrency & admission — the C-1 scheduler

This is where the one law does the most work. The naive design is "one semaphore of size
`NumCPU`; requests block until a slot frees." That has two failure modes under a flood, and
the C-1 work fixes both *without* ever touching a verdict.

### Problem A: unbounded queue → memory blowup

With a bare blocking semaphore, 10,000 simultaneous requests park 10,000 goroutines waiting
for a slot. Memory grows without bound; the box falls over.

**Fix — bounded admission ("shed at the door").** Before waiting for a run slot, each `/run`
counts itself in-system (an atomic `waiting` counter + a `goboxd_queue_depth` gauge). If the
in-system count exceeds `MaxConcurrency + MaxQueue` (default `MaxQueue = 2 × MaxConcurrency`),
the request is **rejected immediately** with `503 server_busy` + `Retry-After: 2`, instead of
queueing. Memory stays bounded under any flood.

> **Why `503 + Retry-After` and not "just queue them"?** Because a bounded queue with an
> honest "come back in 2 seconds" is a *better* client experience than an unbounded queue
> that eventually times out everyone, and it keeps the server alive. Crucially, **shedding
> changes only *whether* a request runs, never *how* it's graded** — so it obeys the one law.

### Problem B: heavy compiles starve light interpreted runs

Two sub-problems, two more lanes.

**Build lane.** Compilation is the CPU-heavy phase. A flood of `g++ -O2` jobs can peg every
core. So a **second semaphore** (`buildSem`, size `MaxBuildConcurrency`, default
`max(1, MaxConcurrency/2)`) caps *concurrent compiles* below the run-slot count. The build
token is held **only during the compile**, never across the run phase.

**Fast-lane reservation (C-3).** Even with the build lane, light interpreted jobs (no build
step) still competed with heavy compiled jobs for the same *run* slots — a burst of slow
`java`/`cpp` runs could **head-of-line-block** a quick `py3` run (i.e. the fast job is stuck
behind slow jobs in the same line). Fix: heavy jobs (`lang.Build != nil`) must first take a
token from a `heavy` semaphore of size `MaxConcurrency − FastLaneReserved` *before* grabbing a
run slot; light jobs skip it. This guarantees `FastLaneReserved` run slots (default
`max(1, MaxConcurrency/4)`) can **never** be held by heavy jobs, so light requests always have
headroom.

> **The deadlock-avoidance reasoning (this is the subtle part).** Three semaphores means
> three locks, and multiple locks invite deadlock. The invariant that prevents it is **strict
> lock ordering plus subset relationships:**
> - The **run-slot** semaphore is always acquired *before* the **build token**. Build-token
>   holders are a strict *subset* of run-slot holders — you can't be compiling without
>   occupying a run slot. So there's no cycle.
> - **Heavy** jobs acquire `heavy` *then* the run slot; **light** jobs take the run slot
>   only and never touch `heavy`. Since light never holds `heavy`, the two orders can't form
>   a cycle either.
> The `heavy` lane is clamped to `≥ 1` so an over-large `FastLaneReserved` can't zero it.
> Setting `FastLaneReserved = 0` disables the reservation; `MaxBuildConcurrency ≤ 0` or
> `≥ MaxConcurrency` disables the build lane.

> **Alternative rejected — a priority queue with "shortest job first" + aging.** The
> tempting scheduler-theory answer: estimate each job's cost (`wall_time × (1 + mem_GB)`),
> run cheap jobs first, and "age" long-waiting jobs upward so they don't starve. **Rejected**
> for three reasons: (1) you don't *know* a job's cost up front — you'd estimate it, and a
> wrong estimate mis-schedules; (2) a min-heap with re-heapify on every aging tick adds lock
> contention right on the hot admission path; (3) it's far more complex to prove correct. The
> two-lane semaphore approach (a poor man's multi-level feedback queue) gets ~90% of the
> fairness benefit with code you can fully reason about. Simplicity on the security/fairness
> hot path beats theoretical optimality.

**Benchmarked payoff:** under `c=100` overload on a 4-CPU box, admitted requests hold a flat
~32 ms p95 while the overflow gets clean `503`s — versus the pre-C-1 behavior where everyone
queued and p95 climbed to ~368 ms. (`docs/benchmarks.md`.)

Every wait is exposed as a metric (`goboxd_queue_wait_seconds{lane}`,
`goboxd_build_wait_seconds`) so the scheduler's behavior is observable, not guessed at.

---

## 9. The artifact cache — C-2 (speed without cheating)

Contestants resubmit the *same* code constantly (tweak a test, retry). Recompiling identical
source is pure waste. The C-2 cache (`internal/artifactcache`) skips it — **without ever
touching a verdict.**

The defining property is **verdict-neutrality by construction:** the cache stores **only the
compiled artifacts** (every file in the jail workdir *except the source* — `a.out`, all
`*.class` including inner classes, Verilog `vvp` images). It **never** stores a run result.
The run phase *always* executes live, in a fresh jail, once per test. So the cache can make
things faster but can *never* change a grade.

- **Key:** `sha256(langID, toolchainVersion, sha256(source), buildFlags, artifactFilename)`.
  The **toolchain version** (from the language's smoke probe) is folded in, so **a compiler
  upgrade invalidates the cache** — a stale binary built by the old compiler can never be
  served. An unknown (empty) toolchain version **skips the cache entirely** rather than risk
  a wrong hit.
- **Hit:** copy cached files into the fresh jail (mode bits preserved, so `a.out` stays
  executable), replay the stored build stdout/stderr/duration, skip the compile. Run live.
- **Single-flight:** an in-process keyed semaphore spans `get → build → put`, so N identical
  *concurrent* submissions compile **exactly once** and the rest wait on the result. The wait
  is **context-cancellable** — a client that disconnects mid-wait unblocks immediately
  instead of parking until the in-flight compile finishes.
- **Eviction:** a count cap (`cache_max_entries`, default 512) evicts the oldest entry by
  mtime on insert; a startup TTL sweep clears stale entries. Commit + eviction are serialized
  under a cache-wide lock so two `Put`s on different keys can't race each other's
  rename/`RemoveAll`.
- **Graceful degradation:** **every disk/IO error degrades to a cache miss** (just build
  normally). The cache can *never* fail or block a run. Only *successful* builds are stored.

> **Decision: cache the *artifact*, not the *result*.** The fast version would be to cache
> the whole verdict for `(source, tests, limits)` and skip execution entirely on a repeat.
> **Rejected as the default** because it's a load-/history-dependent shortcut that's one bug
> away from serving a stale grade, and it's much harder to *prove* correct. Caching only the
> binary keeps the cache on the safe side of the one law: the grade always comes from a real,
> fresh execution. (A result cache keyed on `(lang, sha(source), sha(tests),
> toolchain_version)` is a *possible future* optimization, listed on the roadmap, precisely
> because it's verdict-affecting and needs careful design.)

> **Quirk: why cancellable single-flight matters.** The first version had a non-cancellable
> wait — a disconnecting client would still block until the compile it was coalesced with
> finished. Two best-effort limitations were logged (non-cancellable wait; a cross-key
> eviction race) and have since been **fixed and covered by a `-race` test**. This is an
> example of the project tracking its own known-imperfect spots in `docs/improvements.md`
> rather than pretending they didn't exist.

**Benchmarked payoff:** identical C++ resubmissions ran ~**36× faster** (build 143 ms →
replayed; p50 104 ms → 2.8 ms), with 657 hits / 1 miss across a sweep and **identical
verdicts** throughout.

---

## 10. Observability

### Metrics on a *separate* admin port

Prometheus metrics (`/metrics`) are served on a **dedicated admin port** (`metrics_port`,
default `9090`), bound by its own `http.Server`, **never mounted on the public API router**.

> **Why a separate port?** So a *submitter* (who can reach `:8080`) cannot scrape internal
> operational detail — in-flight count, per-language verdict mix, queue latencies. Operational
> telemetry is for operators, not contestants. Set `metrics_port ≤ 0` to disable it entirely.

> **Quirk: label cardinality is bounded on purpose.** Series are labelled only by `language`
> (a fixed set) and `verdict` (fixed constants). **Never** by source-hash, request-id, or
> filename. *Cardinality* = the number of distinct label combinations; unbounded label values
> (like a hash) would create a near-infinite number of time series and OOM the metrics
> backend. This is a classic Prometheus footgun, avoided deliberately.

Exposed series include `goboxd_runs_total{language,verdict}`,
`goboxd_run_duration_seconds{language,phase}`, `goboxd_queue_wait_seconds{lane}`,
`goboxd_inflight`, `goboxd_queue_depth`, `goboxd_rejected_total`,
`goboxd_cache_hits_total{language}` / `_misses_total`, `goboxd_build_wait_seconds`, plus
standard Go runtime/process collectors.

`docker compose up` brings up the full stack: goboxd + Prometheus (scrapes `goboxd:9090`) +
Grafana (auto-provisioned dashboard charting throughput, p95 latency, queue/in-flight, 503
rate, cache hit ratio, and admission wait by lane).

### Logs & `/info`

Structured JSON logs (`log/slog`) carry a request-id (chi middleware) through each run —
one log line per request. In-process atomic counters surface in `/info.stats`
(jobs total/in-flight/failed, last internal error, disk-free, uptime).

> **Decision: `chi` router, not a heavy framework and not bare `net/http`.** The endpoints
> are simple. `chi` is a thin layer over `net/http` that adds exactly two things we want —
> request-id and panic-recovery middleware — without pulling a runtime or a large dependency
> tree. Bare `net/http` would mean re-implementing those; a full framework (Gin/Echo/Fiber)
> would be weight we don't need for five endpoints.

---

## 11. The decisions that were *rejected* (the most important section)

These are the forks in the road. Recording *why not* is the point of this document.

### 11.1 Load-adaptive limit clamping — **REJECTED (would break the one law)**

The idea: under high load, lower the allowed `wall_time_s` and report it in a `warnings`
field, so the server can push more jobs through.

**Why rejected:** it makes the verdict a function of *server load*. The exact same submission
would pass on a quiet server and `time_exceeded` on a busy one. That is precisely the thing a
judge must never do. The *correct* response to overload is to **shed whole requests**
(`503 + Retry-After`, §8) — change *whether* a job runs, never *how* it's graded. This single
decision is why admission control and per-run limits are kept rigorously separate everywhere
in the code.

### 11.2 Allow-list seccomp — **REJECTED (brittle, breaks JITs)**

Covered in §5. The "more secure on paper" allow-list is the documented way to break a JVM/V8/
CPython across libc and arch versions. The deny-list blocks the stable escape surface and
keeps every language working. Strictness that doesn't ship enforced is worse than a slightly
broader filter that does.

### 11.3 Result cache as the default — **REJECTED (verdict-affecting)**

Covered in §9. Caching whole grades is faster but lives on the dangerous side of the one law.
The artifact cache gets most of the speed while keeping every grade a real execution.

### 11.4 Priority/SJF scheduler with aging — **REJECTED (complex, needs cost it can't know)**

Covered in §8. Two-lane semaphores beat a min-heap-with-aging on the hot path because they're
provably correct and don't need a job-cost oracle.

### 11.5 gVisor / Firecracker as the *baseline* sandbox — **DEFERRED, not rejected**

Covered in §5. Stronger isolation, but heavy per-run cost (a second kernel, slower starts)
for a many-short-runs workload whose escape surface is already closed by namespaces +
seccomp + cgroups. Kept on the roadmap as an opt-in stronger tier.

### 11.6 Bundling the demo web UI into the service — **REJECTED (pure attack surface)**

`demo/index.html` (a Monaco-editor page) is deliberately **not** part of the deployable
artifact. goboxd is a *headless* judge; its only trust boundary is the API + sandbox. A real
submitter (or attacker) hits the API with `curl` and never loads the page, so **any
client-side check is bypassed trivially** — a web UI enforces nothing. Bundling it would only
add attack surface (XSS, CDN dependencies, a second port) for zero security gain. So the demo
is a standalone static file that calls the public API like any other client. Because browsers
block cross-origin `fetch`, the demo needs a CORS allowance — `demoCORSOrigin()` (env
`GOBOXD_DEMO_CORS_ORIGIN` > config `demo_cors_origin`), mounted **only when non-empty, exact
origin only, never `*`, off by default**. CORS is a browser convenience, not a security
control (`curl` ignores it); all server text is rendered via `textContent` to prevent XSS.

---

## 12. Configuration reference & operational notes

Server block (`configs/languages.yaml`), with the defaults and *why* they're shaped that way:

| Key | Default | Purpose / rationale |
|---|---|---|
| `port` | 8080 | public API |
| `metrics_port` | 9090 | admin metrics; `≤0` disables (keeps telemetry off the public surface) |
| `max_concurrency` | `runtime.NumCPU()` | run slots; auto-sizes to the box |
| `max_queue` | `2 × max_concurrency` | extra waiters before `503` (C-1) |
| `fast_lane_reserved` | `max(1, max_concurrency/4)` | run slots reserved for light jobs (C-3); `0` disables |
| `max_build_concurrency` | `max(1, max_concurrency/2)` | build-lane cap (C-1); `0` or `≥ max_concurrency` disables |
| `cache_enabled` | `true` | artifact cache (C-2) |
| `cache_dir` | `/tmp/goboxd-cache` | cache storage |
| `cache_max_entries` | 512 | count cap, oldest-by-mtime evicted |
| `max_body_bytes` | 4 MiB | whole-request envelope cap |
| `max_source_bytes` | 256 KiB | `source` field cap (distinct so an oversize source is reported precisely) |
| `output_cap_bytes` | 64 KiB | stdout/stderr capture cap |
| `max_tests` | 100 | test cases per request |
| `jail_base` | `/tmp/goboxd` | jail workdir root |
| `nsjail_path` | `/usr/local/bin/nsjail` | the pinned nsjail binary |
| `seccomp_mode` | **shipped: `enforce`** (code fallback if key absent: `off`) | `off` / `audit` / `enforce` |

Per-language: `cpu_max_percent` (server-side CPU cap, off by default), `seccomp_policy`
(kafel program, shared `&deny_seccomp` anchor), build/run `limits` + `flag_allowlist`.

Code defaults for limits (`internal/config/load.go`): run = 5 s / 256 MiB / 64 procs;
build = 30 s / 1 GiB / 100 procs.

**Build/run/deploy:** `make build|run|test|lint|verify-nsjail|docker-build|docker-run|load`.
Go 1.22+. `go.uber.org/automaxprocs` sets `GOMAXPROCS` from the cgroup CPU quota so Go's
scheduler doesn't oversubscribe when the container is capped below the host's core count.
Docker run needs `--privileged` (nsjail needs namespace/cgroup access). Graceful shutdown:
SIGINT/SIGTERM → `http.Server.Shutdown` drains in-flight jobs for both the API and metrics
servers before exit.

**Jail lifecycle quirk:** each run's jail dir is named `{atomic_counter}_{pid}_{16_hex}` —
the counter guarantees in-process uniqueness, the PID distinguishes restarts reusing the same
tmpfs, and the crypto/rand suffix prevents enumeration. `os.Mkdir` (not `MkdirAll`) is used
for the final component so a name collision *errors* rather than silently reusing a directory.
Cleanup is a `defer` registered right after create (fires on every path, including a panic
caught by recovery middleware), and a startup `SweepOrphans` removes jail dirs older than 10
minutes left by crashed runs — so `/tmp/goboxd` stays bounded across restarts and sustained
load.

---

## 13. What's intentionally *not* built yet (roadmap honesty)

So you know which gaps are deliberate, not forgotten (full list in `docs/roadmap.md` /
`docs/improvements.md`):

- **Async API** (submit → job-id → poll/SSE) and a **persistent job bus** (NATS/Redis) with a
  stateless worker fleet — the horizontal-scale story. The current server is stateless
  *specifically* to make this a later addition, not a rewrite.
- **Stronger sandbox tier** (gVisor / Firecracker) as an opt-in.
- **Warm jail pools** per language to cut cold-start.
- **Result cache** keyed on `(lang, sha(source), sha(tests), toolchain_version)` — verdict-
  affecting, so deferred until carefully designed.
- **Public-internet hardening:** API-key / mTLS auth, per-key rate limiting and quotas, hard
  limit ceilings. Today's "no ceiling on limit overrides" assumes a trusted operator.
- **Supply-chain:** reproducible builds + SBOM, image signing (cosign) + SLSA attestation,
  per-language checksums.
- **Richer grading:** multi-file projects, interactive/grader tests, partial-credit.
- **Fully data-driven install** (derive language install from the YAML rather than the
  Dockerfile referencing scripts).

---

## 14. The mental model to walk away with

1. **One law:** verdict = `f(source, tests, limits)`, load-independent. Everything else
   bends to this.
2. **Four sandbox layers**, each assuming the next might fail: namespaces (can't see) →
   cgroups (can't exhaust) → seccomp (can't reach kernel escapes) → output caps (can't
   flood).
3. **Speed and fairness features are verdict-neutral by construction:** the cache reuses only
   binaries and always re-runs; the scheduler only sheds/orders, never re-grades.
4. **Legibility over cleverness:** data-driven languages, finely-split packages, deny-list
   seccomp, two-lane semaphores — each chosen because you can *prove it correct by reading
   it*, which on a security boundary is worth more than elegance.
5. **The project tracks its own imperfections** (known-reference-bug corrections, fixed
   best-effort limitations, the install-not-yet-data-driven seam) instead of hiding them.

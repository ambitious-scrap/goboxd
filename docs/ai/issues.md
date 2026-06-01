# Issues Log

Blockers worth recording: what failed, how it was fixed, what was learned.

---

## Issue 1: `{{flags}}` joining into a single argv element

**Date:** 2025-05-28

**What failed:**
Compiler flag expansion. The YAML config for C++ has:
```yaml
args: ["{{flags}}", "-o", "{{artifact}}", "{{source}}"]
```

The `Resolve()` function replaced `{{flags}}` with a space-joined string of all flags: `"-O2 -std=c++17"` as one element. When this reached nsjail, it passed that single string to `g++`, which treated it as a filename, not flags. Compilation silently failed or produced the wrong error.

Didn't catch this during initial implementation because the placeholder tests used single-token replacements. Only discovered when writing the end-to-end test with flags.

**How it was fixed:**
Two-pass approach. `Resolve()` now skips `{{flags}}` entirely (leaves the marker). A second function `ExpandFlags()` rebuilds the args slice: when it encounters a `{{flags}}` element, it appends each flag string individually. Both functions are in `internal/registry/placeholders.go`.

Added a unit test specifically for the multi-flag expansion case.

**What was learned:**
"Works for the simple case" is not the same as "works." The single-flag case (`["-O2"]`) passed through correctly both before and after the fix — the bug only showed up with multiple flags. Write tests for the multi-value case upfront when a field is documented as `[]string`.

---

## Issue 2: Merge conflict with hackathon starter repo

**Date:** 2025-05-27

**What failed:**
The hackathon provides a starter repo at `github.com/thesouldev/goboxd`. The goal was to fork it and work on the fork. Discovered that GitHub won't make a public fork of a public repo private — fork visibility is fixed by the parent's visibility.

Tried rebasing onto the starter repo's `master` branch. The starter repo has its own scaffold with `module github.com/thesouldev/goboxd` in `go.mod`. My code uses `module github.com/ambitious-scrap/goboxd`. Rebase failed with conflicts across every Go file because every import path was different.

Attempted `git rebase --onto` and various merge strategies. The histories are unrelated (different initial commits) so there's no common ancestor to base a clean merge on.

**How it was fixed:**
Abandoned the fork approach entirely. Work locally in the development repo, then at submission time: update all import paths from `github.com/ambitious-scrap/goboxd` to `github.com/thesouldev/goboxd`, update `go.mod`, and push directly to the starter repo as a one-time push at the end.

The module path change is a global find-and-replace in import statements, which is clean to do once at the end rather than trying to keep two module paths in sync.

**What was learned:**
Check GitHub's fork visibility constraints before forking. For hackathons with public starter repos and competition concerns, "fork and work on fork" isn't viable if you want private work. Local-first + push-at-end is a better strategy.

---

## Issue 3: `expected_stdout` vs `expected_output` field name

**Date:** 2025-05-28

**What failed:**
The API handler was decoding the JSON field `expected_output` from test cases. Test submissions with the correct field name `expected_stdout` (from the spec) were silently ignored — Go's JSON decoder doesn't error on unknown fields, so `expected_stdout` in the request just got dropped, and `expected_output` stayed as zero string.

This meant every test case had empty expected output, so any non-empty stdout was `wrong_output`. Took a while to track down because the runner tests passed (they pass `Expected` directly to the struct, bypassing JSON decoding).

**How it was fixed:**
Found the spec page and verified: the field is `expected_stdout`. Updated `testCaseInput` struct in `internal/api/handler.go`:
```go
type testCaseInput struct {
    Stdin          string `json:"stdin"`
    ExpectedOutput string `json:"expected_stdout"`
}
```

Also fixed the same wrong field name in `docs/api.md` and the `make load` target.

**What was learned:**
When the struct field name and JSON tag name diverge (`ExpectedOutput` / `expected_stdout`), it's easy to lose track of which is which. For spec-critical field names, write an integration test that sends raw JSON and checks the decoded struct — not just a unit test that creates the struct in Go.

---

## Issue 4: `memory_exceeded` never fired — wrong memory enforcement and host-global OOM read

**Date:** 2025-05-29

**What failed:**
Two coupled bugs meant the spec's `memory_exceeded` status was effectively unreachable and `memory_peak_kb` was meaningless:

1. Memory was capped only with nsjail's `--rlimit_as` (an address-space rlimit). When user code exceeds it, `malloc`/`mmap` returns NULL and the program crashes with an ordinary non-zero exit — which the runner maps to `runtime_error`, never `memory_exceeded`. An rlimit is not a cgroup memory limit and does not produce an OOM-kill event.

2. The OOM check read `/sys/fs/cgroup/memory.events` — the **host root** cgroup. Its `oom_kill` counter is host-wide and monotonic, so one OOM anywhere on the machine would make every subsequent request falsely report `memory_exceeded`. `memory.peak` at the root was likewise host-wide, so `memory_peak_kb` per test was garbage.

**How it was fixed:**
Per-request cgroup v2. New file `internal/sandbox/cgroup.go`:
- For each run, create a dedicated cgroup `/sys/fs/cgroup/goboxd/<jail-id>`, enable the memory controller in the parent subtree, and write `memory.max` (+ `memory.swap.max = 0` so the cap counts real memory).
- nsjail is pointed at that cgroup with `--use_cgroupv2 --cgroupv2_mount <path> --cgroup_mem_max <bytes>`, so the sandboxed process runs inside it.
- After the run, read **that cgroup's** `memory.events` (`oom_kill` is recursive in cgroup v2, so it captures the child cgroup nsjail creates) and `memory.peak`. Then remove the cgroup.
- A startup sweep (`SweepOrphanCgroups`) removes leftover cgroups from a crashed previous process, mirroring the jail-dir orphan sweep.
- `--rlimit_as` is kept as a second layer.

All cgroup operations are best-effort: if the host doesn't expose a writable cgroup v2 tree, accounting is skipped (`cg.ok == false`) and the rlimit still enforces the cap — no crash, just no OOM/peak reporting.

**What was learned:**
"Read `memory.events`" was the right instinct but the path is the whole problem — reading a shared/global cgroup gives readings that belong to other work. Correct per-request accounting requires owning the cgroup: create it, set the limit on it, run inside it, read it, delete it. cgroup v2's recursive `oom_kill` accounting is what makes reading the parent (whose path we know) sufficient, instead of chasing nsjail's child-cgroup name.

---

## Issue 5: Docker image build — three failures in a row

**Date:** 2025-05-29

**What failed:**
First clean `docker build` on the Colima Linux VM (arm64) failed three times, each at a different stage:

1. **Pinned apt versions in the nsjail-builder stage.** The Dockerfile pinned exact patch versions (`git=1:2.34.1-1ubuntu1.11`, etc.). On arm64 the archive had a different patch revision, so apt reported "Version ... was not found" and the stage failed with code 100.

2. **nsjail's nested `kafel` submodule was empty.** nsjail vendors kafel as its own git submodule. The host had `external/nsjail` checked out but not recursively, so `kafel/` was an empty directory. nsjail's `make` runs a `kafel_init` target that calls `git submodule update --init` — which fatals with "not a git repository" because `COPY` doesn't bring `.git` into the build context.

3. **Go toolchain version mismatch.** `go.mod` declares `go 1.26.3` (the local toolchain), but the builder image was `golang:1.22-bookworm`. With `GOTOOLCHAIN=local` the image refused: "go.mod requires go >= 1.26.3 (running go 1.22.12)".

**How it was fixed:**
1. Unpinned the build-tool versions in the nsjail-builder stage — they're the base image's own packages and exact pins break across architectures and over time; reproducibility comes from the `FROM` tag, not from pinning every build tool.
2. Ran `git submodule update --init --recursive` on the host to populate `kafel/`. nsjail's `kafel_init` is guarded by `ifeq ("$(wildcard kafel/Makefile)","")` — once `kafel/Makefile` exists in the build context, the git call is skipped entirely. No Dockerfile change needed.
3. Bumped the builder image to `golang:1.26-bookworm` to match `go.mod`.

**What was learned:**
- Exact apt version pins for build-only tools are a reproducibility liability, not an asset — they're the first thing to break on a different arch.
- `COPY` of a submodule directory does not include `.git`, so any build step that shells out to git inside the context will fail. Prefer build steps guarded by file existence (like nsjail's `wildcard` check) and populate submodules recursively on the host.
- Keep the Dockerfile's Go builder image version in lockstep with `go.mod`'s `go` directive; a local toolchain bump silently breaks the image build until they match.

---

## Issue 6: Java and Verilog configured with wrong run commands (found via /readyz on a real image)

**Date:** 2025-05-29

**What failed:**
First end-to-end `docker run` + `/readyz` on the built image surfaced two language-config bugs that unit tests couldn't catch, because they're about real binary paths and runtime semantics, not Go logic:

1. **Java pinned an architecture-specific JVM path.** The config used `/usr/lib/jvm/java-17-openjdk-amd64/bin/java(c)`. On the arm64 build VM the directory is `java-17-openjdk-arm64`, so the smoke probe failed with "no such file or directory" and `/readyz` reported java degraded. The path would also silently rot if the JDK minor version changed.

2. **Verilog ran the compiler output directly.** The config compiled with `iverilog` and then tried to execute the artifact as `./a.out`. But `iverilog` emits a vvp bytecode program, not a native executable — it has to be run by the `vvp` runtime. `./a.out` would never have worked.

**How it was fixed:**
1. Switched java to the architecture-independent alternatives symlinks `/usr/bin/javac` and `/usr/bin/java`, which `openjdk-17-jdk-headless` installs regardless of arch. Verified both exist in the container before changing the config.
2. Changed the verilog run command to `/usr/bin/vvp` with the artifact as its argument, and made the build step explicit with `-o {{artifact}}` instead of relying on iverilog's default `a.out`.

**What was learned:**
Smoke probes and a real `/readyz` against a built image catch a whole class of bugs that the fake-sandbox unit tests are blind to by design — wrong binary paths, arch assumptions, and "the compiler output isn't directly runnable" semantics. The data-driven registry makes these config-only fixes (no Go change), which is the point, but it also means the config needs its own end-to-end validation per language. Worth adding a per-language "compile + run a trivial program" integration test, not just a `--version` smoke probe.

---

## Issue 7: Three execution bugs that only surfaced when running real code in the built image

**Date:** 2025-05-29

**What failed:**
After the image built and `/readyz` was green, the first real `POST /run` per language showed only 3 of 7 working (py3, bash, verilog). The unit tests all passed — every one of these is invisible to the fake sandbox.

1. **The run/build `cmd` was never placeholder-resolved.** `internal/runner/runner.go` ran `registry.Resolve` over the *args* but passed `Language.Run.Cmd` and `Language.Build.Cmd` through raw. For C and C++ the run command is `./{{artifact}}`, so nsjail was handed the literal string `./{{artifact}}` to exec — no such file, instant non-zero exit, empty output, which maps to `runtime_error`. Verilog hid the bug: its run command is the absolute `/usr/bin/vvp` and the artifact lives in its *args* (`["{{artifact}}"]`), which *were* resolved — so verilog worked while C/C++ didn't, with no obvious common cause.

2. **The cgroup memory controller was never actually enabled, so the rlimit fallback ran for everything — and that fallback breaks VM runtimes.** `enableMemoryController` wrote `+memory` to the cgroup-namespace root's `cgroup.subtree_control`, but in a container that root holds the service's own processes, and cgroup v2 refuses to enable a controller in a cgroup with internal processes. The write failed silently (`_ =`), so `cg.ok` was false on every request and the sandbox fell back to `--rlimit_as`. An `rlimit_as` set to the RSS budget caps *virtual* address space, which the JVM and V8 reserve far more of than they make resident — javac died (`build_failed`) and Node died with `Fatal process OOM in CodeRange setup: allocate virtual memory`. It also meant `memory_peak_kb` was always 0 and `memory_exceeded` could never fire.

3. **Build/run memory limits were set to values no real toolchain can run in.** The C/C++ build limit was 10 MB and the run limit 1 MB. With the rlimit fallback (and later with the cgroup actually enforcing), `cc1`/`cc1plus` were killed mid-compile — `gcc: internal compiler error: Segmentation fault signal terminated program cc1`. The numbers had never been checked against an actual compiler's footprint.

**How it was fixed:**
1. Added `registry.ResolveOne(string, vars)` and resolved `Run.Cmd`/`Build.Cmd` through it in the runner, so `./{{artifact}}` becomes `./a.out`.
2. `enableMemoryController` now detects the failed enable and evacuates the root cgroup's processes into a leaf cgroup (`/sys/fs/cgroup/_svc`) before retrying `+memory` — satisfying the "no internal processes" rule — then verifies via `cgroup.controllers`. With the cgroup working, `--rlimit_as` is dropped whenever `cg.ok` (cgroup `memory.max` is the correct RSS-based cap); rlimit stays only as the no-cgroup fallback.
3. Raised the limits to realistic values: C/C++ build 1 GiB / run 256 MiB, Java 512 MiB, Node 256 MiB.

After all three: 7/7 languages accepted, `memory_peak_kb` non-zero, and the `memory_exceeded` / `time_exceeded` / `runtime_error` / `wrong_output` / `output_whitespace_mismatch` / `build_failed` distinctions all verified end-to-end against the running image.

**What was learned:**
The fake-sandbox unit tests prove the orchestration logic; they prove nothing about whether code actually executes. Three different layers (placeholder resolution, cgroup delegation, resource limits) were each individually "done" and unit-green while the service couldn't run a C program. The common thread: every one of these is about the boundary between the Go process and the real kernel/toolchain, which is exactly what a fake mocks away. A per-language "build + run a trivial program and check stdout" integration test against the built image would have caught all three at once, and is worth more than any number of additional fake-sandbox cases. Also: silent `_ =` on a privileged filesystem write hid the cgroup failure for a long time — a one-line "memory accounting unavailable" log when `cg.ok` stays false would have pointed straight at it.

---

## Issue 8: `limitedWriter` short-write could turn oversize output into an HTTP 500

**Date:** 2025-05-31

**What failed:**
While adding unit tests for `internal/sandbox`, `TestLimitedWriter` exposed a latent bug in the output-cap writer. On the single `Write` call that first crossed the cap (`len(p) > remaining > 0`), `limitedWriter.Write` wrote only the remaining bytes and returned that shorter count with a nil error. os/exec copies a child's stdout into this writer via `io.Copy`; `io.Copy` treats `n < len(p)` with no error as `io.ErrShortWrite` and stops. That error propagates out of `cmd.Run`, and since it is not an `*exec.ExitError`, `sandbox.Run` returns it as an execution error — which the API maps to a 500. So a program that simply printed more than the output cap could produce a server error instead of a truncated 200.

It was latent because the default cap is 65536 bytes, an exact multiple of `io.Copy`'s 32 KiB internal buffer: writes land on 32768-byte boundaries, `remaining` hits exactly 0, and the next write takes the `remaining <= 0` branch (which already returned `len(p)`). The straddling branch is only reached with a non-aligned cap or partial chunks, so the live container with the default cap never tripped it.

**How it was fixed:**
`limitedWriter.Write` now always reports `len(p)` back to the caller. On the straddling write it writes `p[:remaining]`, sets `truncated`, zeroes `remaining`, and returns `len(p), nil`; the already-exhausted branch already did this. The overflow is swallowed and recorded as truncation rather than surfaced as a short write. Fix in `internal/sandbox/sandbox.go`; covered by the over-cap case in `TestLimitedWriter`. Verified end-to-end against the running image: `print("A"*200000)` returns a 200 with stdout capped at 65536 bytes plus the truncation marker.

**What was learned:**
An `io.Writer` adapter used as a subprocess sink has an implicit contract beyond "cap the bytes": it must not return a short write, because the standard-library copy loop above it treats that as a hard error. The bug had been shipped and was invisible in every test and every live call that used the default cap — only a unit test that drove the writer directly with a non-aligned boundary caught it. The general rule: when wrapping `io.Writer`, return `len(p)` unless you are deliberately signalling failure with a non-nil error.

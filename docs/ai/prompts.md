# AI Prompt Log

This file logs meaningful AI interactions during development. Autocomplete and one-liner lookups not included.

---

## 2025-05-26 — Initial architecture design

**Context:** Starting from scratch. Had the spec PDF and the pyjail reference but no Go code yet. Needed to figure out package layout before writing anything.

**Prompt (paraphrased):**
> I'm building a sandboxed code execution service in Go for a hackathon. It needs to run user code in nsjail, support multiple languages, and return structured pass/fail results per test case. How should I split the packages to keep sandbox details isolated from business logic? The scoring criteria specifically rewards a data-driven language registry — adding a language should require zero Go changes.

**Response summary:**
Suggested separating `sandbox` (nsjail process management only), `runner` (orchestration — knows about test cases and build steps, doesn't know nsjail), `registry` (language config lookup and placeholder resolution), and an `api` layer on top. Recommended the runner depend on a `SandboxRunner` interface rather than the concrete sandbox type — this turned out to be exactly right for unit testing. Also flagged that placeholder expansion in argv slices needs care if flags can be multi-valued.

**Used:** Package structure almost exactly as suggested. The interface idea was new to me and saved a lot of pain during testing. Placeholder expansion advice led to the `ExpandFlags` function.

**Discarded:** It suggested `internal/executor` as a name; I used `internal/runner` because the spec uses "execution engine" language and runner felt closer.

---

## 2025-05-26 — YAML config schema with optional nested structs

**Context:** Designing the `config.Language` struct. Build step is optional (interpreted vs compiled). Wasn't sure how to handle nil-able nested structs with yaml.v3.

**Prompt (paraphrased):**
> In Go with gopkg.in/yaml.v3, how do I make a nested struct field optional — present for compiled languages, absent for interpreted — so I can use `lang.Build != nil` as the "is compiled" check?

**Response summary:**
Use a pointer to the nested struct (`Build *BuildStep`). yaml.v3 leaves it nil if the field is absent in YAML. Also mentioned that `omitempty` on the yaml tag prevents writing empty structs back out if you ever marshal, which is good practice even for read-only configs.

**Used:** `Build *BuildStep` with `yaml:"build,omitempty"`. The `lang.Build != nil` check in runner is the only branch that matters.

**Discarded:** Suggested adding a `Type string` enum ("interpreted" | "compiled") as an explicit discriminant. Didn't use it — the pointer check is enough and an extra field would just be a lie waiting to get out of sync with reality.

---

## 2025-05-27 — Capturing stdout and stderr concurrently from a subprocess

**Context:** Writing the nsjail wrapper. nsjail writes its own log to stderr, and the user process also writes to stderr. Need to capture stdout for comparison and stderr for diagnostics, without deadlocking.

**Prompt (paraphrased):**
> In Go, I'm running a subprocess that writes to both stdout and stderr. I need to capture both streams without deadlocking if one pipe fills up. What's the right pattern?

**Response summary:**
Start goroutines for each pipe reader before calling `cmd.Wait()`. Pointed out that reading from a pipe in the same goroutine that calls `cmd.Wait()` can deadlock if the subprocess fills the pipe buffer. Also mentioned `io.LimitReader` for capping output before it hits memory limits.

**Used:** Goroutines per stream, `io.LimitReader` with a `limitedWriter` that tracks truncation. The `...[truncated]` marker on stdout was my addition — the AI just mentioned "you might want to flag truncation to callers."

**Discarded:** It suggested using `bytes.Buffer` wrapped in a mutex. Used a custom `limitedWriter` struct instead because I wanted to track the truncation flag separately from the byte count.

---

## 2025-05-27 — Detecting OOM kills from nsjail (cgroup v2)

**Context:** The spec requires distinguishing `memory_exceeded` from `runtime_error`. nsjail exits non-zero for both. Needed a way to tell them apart.

**Prompt (paraphrased):**
> nsjail kills a process when it hits the memory limit. From Go, how do I detect that an OOM kill happened vs a regular non-zero exit? The host is running cgroup v2.

**Response summary:**
Read `/proc/<pid>/oom_score_adj` was one suggestion (not reliable for OOM vs limit kill distinction). Better: check `memory.events` file in the process's cgroup — the `oom_kill` counter increments on OOM. For nsjail specifically, the cgroup path can be found under `/sys/fs/cgroup`. Also mentioned that nsjail log output sometimes includes "OOM" but parsing log text is fragile.

**Used initially (and later found wrong):** The `memory.events` approach, but reading the host-global `/sys/fs/cgroup/memory.events`. This was a mistake — see the 2025-05-29 entry below and `issues.md` Issue 4. A host-global counter reports OOMs from other work, and `--rlimit_as` (what was actually capping memory) doesn't produce OOM events at all. Reworked into per-request cgroups.

**Discarded:** The `oom_score_adj` suggestion. Log parsing for OOM string. Both too brittle.

---

## 2025-05-27 — Unique jail directory naming

**Context:** Multiple concurrent requests each need their own isolated workdir. Need names that don't collide between requests, across goroutines, and between process restarts.

**Prompt:**
> I need unique directory names for concurrent sandbox workdirs. UUIDs, or something else? The process might restart, so I need to avoid collisions with directories left by previous runs.

**Response summary:**
UUID v4 is fine but heavier than necessary. Suggested `atomic_counter + PID + rand suffix` — the counter prevents same-process collisions, PID prevents cross-restart collisions (old PID directories get swept), random suffix is a safety net. Also mentioned using `os.Mkdir` (not `MkdirAll`) for the final path component so you get an error if it already exists rather than silently succeeding.

**Used:** Exactly this: `fmt.Sprintf("%d_%d_%s", atomic.AddInt64(&counter, 1), os.Getpid(), randHex(8))`. `os.Mkdir` not `MkdirAll`. The startup `SweepOrphans` function removes directories from previous runs.

**Discarded:** UUID. Not wrong, just heavier than needed.

---

## 2025-05-28 — Whitespace normalization for output comparison

**Context:** The spec defines three distinct outcomes: `accepted`, `output_whitespace_mismatch`, and `wrong_output`. The middle case is when outputs differ only in whitespace. Needed to define "whitespace normalization" precisely enough to pass the spec's test cases.

**Prompt (paraphrased):**
> The spec has a status called output_whitespace_mismatch — the output matches after whitespace normalization but not exactly. What's a reasonable definition of whitespace normalization for program output? I want to handle trailing spaces, CRLF vs LF, trailing newlines, and multiple blank lines.

**Response summary:**
Split on lines, trim trailing whitespace from each line, rejoin with `\n`, trim leading/trailing blank lines from the whole string. This handles trailing spaces, CRLF, and extra newlines at end. More aggressive normalization (collapsing internal whitespace) risks false positives on programs where spacing is meaningful.

**Used:** `strings.Fields`-based normalization was rejected in favor of line-by-line `strings.TrimRight`. The line-level approach is more conservative and matches what the reference implementation appears to do.

**Discarded:** Normalizing internal whitespace (collapsing multiple spaces). Too aggressive — "hello   world" and "hello world" should probably be `wrong_output`, not `whitespace_mismatch`.

---

## 2025-05-28 — Compiler flag `{{flags}}` expansion bug

**Context:** Flags like `-std=c++17` need to appear as separate argv elements. My initial implementation joined them into a single space-separated string, so nsjail received `"-std=c++17 -O2"` as one element — which the compiler rejected.

**Prompt:**
> My placeholder replacement joins `{{flags}}` into a single string like `"-O2 -std=c++17"`, but this needs to be separate argv elements. The Resolve function works on `[]string` but I can't just replace one element with multiple elements in a range loop. How do I handle this?

**Response summary:**
Two-pass approach: first pass resolves all placeholders except `{{flags}}` (leave the marker in place), second pass expands `{{flags}}` by rebuilding the slice — when an element is `{{flags}}`, append all flag strings individually; otherwise append the element as-is. This is cleaner than trying to do it in one pass.

**Used:** Exactly this. `Resolve()` skips `{{flags}}` elements. `ExpandFlags()` does the expansion. Both pure functions that are easy to test in isolation.

**Discarded:** The single-pass approach with index manipulation. Too error-prone when the same args slice is reused.

---

## 2025-05-28 — nsjail sandboxing approach

**Context:** I had been building the nsjail wrapper using `--bindmount` to mount the workdir into a fixed path. Then I looked at the pyjail reference implementation more carefully and it uses `--chroot {workdir}` with bind mounts for system directories. Not sure which is correct for this use case.

**Prompt (paraphrased):**
> I'm writing a nsjail wrapper. I had been using `--bindmount workdir:/workdir` with `--cwd /workdir`, but the reference Python implementation uses `--chroot {workdir}` with `-B /bin -B /usr -B /lib -B /lib64 -B /dev -B /etc -B /tmp`. Which approach should I use and why?

**Response summary:**
The chroot approach makes the workdir the root of the jail's filesystem, so paths inside the sandbox are simpler (`/solution.py` not `/workdir/solution.py`). The bind mount approach requires an absolute inner path that callers need to know about. For a code runner where the sandbox is ephemeral per-request, chroot is cleaner. The bind mounts for `/bin`, `/usr`, etc. are needed because the chroot gives the process an empty root — you have to bring in the toolchain and standard libraries explicitly.

**Used:** Switched to chroot + system bind mounts. This matches pyjail and makes the YAML config paths simpler.

**Discarded:** The `--bindmount workdir:/workdir` approach. It would have worked but was inconsistent with the reference and required callers to construct inner paths.

---

## 2025-05-29 — Java filename strategy

**Context:** Java requires the source filename to match the public class name. Other languages use a fixed filename (`solution.py`, `solution.cpp`). Needed a way to handle this without a Java-specific branch in Go code.

**Prompt (paraphrased):**
> Java requires the source file to be named after the public class. How do I handle this in a data-driven language registry without adding a language-specific branch to the runner or handler?

**Response summary:**
Add a `source_filename_strategy` field to the language config. When set to `from_request`, the API layer takes the filename from the request body instead of from the language config. The runner receives the resolved filename as part of `RunRequest`, not as a raw strategy string — the handler resolves strategy → filename before calling runner. This keeps the strategy logic in one place (handler/validation) and the runner remains filename-agnostic.

**Used:** `SourceFilenameStrategy: "from_request"` in config. Handler resolves to the actual filename before building `RunRequest`. Runner uses `req.SourceFilename` (override) or `lang.SourceFilename` (default).

**Discarded:** Putting the strategy resolution in the runner. Would have pulled request-parsing logic into execution logic.

---

## 2025-05-29 — Multi-stage Dockerfile with nsjail from source

**Context:** Spec requires nsjail 3.4 built from source via git submodule. Never written a Dockerfile that compiles a C++ project as a build stage.

**Prompt (paraphrased):**
> I need a multi-stage Dockerfile where stage 1 builds nsjail from source (it's a C++ project with a Makefile), and stage 2 is the Go app runtime. What build dependencies does nsjail need and what's the right way to copy just the binary across stages?

**Response summary:**
nsjail needs `bison`, `flex`, `libprotobuf-dev`, `protobuf-compiler`, `libnl-3-dev`, `libnl-route-3-dev`, `pkg-config`. The binary at `nsjail` after `make` can be stripped and copied to `/usr/local/bin/nsjail` in the runtime stage. Also noted that the runtime stage needs `libprotobuf` (not just `-dev`) because nsjail is dynamically linked against it.

**Used:** Build deps list directly from response. The `strip` step on the binary and `libprotobuf23` in runtime were both from this interaction.

**Discarded:** Static linking suggestion — `make STATIC=1` — which would remove the runtime libprotobuf dependency. Tried it; nsjail's Makefile doesn't support it cleanly on Ubuntu 22.04. Kept dynamic linking.

---

## 2025-05-29 — Why memory_exceeded never fired, and the cgroup rework

**Context:** Reviewing the sandbox for spec conformance. Suspected `memory_exceeded` was never being produced — memory-heavy programs were coming back as `runtime_error`.

**Prompt (paraphrased):**
> My nsjail sandbox caps memory with --rlimit_as and detects OOM by reading /sys/fs/cgroup/memory.events. Memory-heavy programs return runtime_error, not memory_exceeded, and OOM detection seems flaky. What's wrong and how do I get correct per-request memory accounting under cgroup v2?

**Response summary:**
Two separate problems. (1) `--rlimit_as` is an address-space rlimit, not a cgroup limit — exceeding it makes allocation fail and the program crash normally, with no OOM-kill event, so it reads as a runtime error. (2) `/sys/fs/cgroup/memory.events` is the host root cgroup; its `oom_kill` is host-wide and monotonic, so it can't attribute a kill to one request. The fix is a dedicated cgroup per run: create it, set `memory.max`, run the process inside it via nsjail's `--use_cgroupv2`/`--cgroupv2_mount`/`--cgroup_mem_max`, then read that cgroup's own `memory.events` and `memory.peak`. It noted cgroup v2's `oom_kill` counter is recursive, so reading the parent cgroup (whose path you control) captures nsjail's child cgroup without needing to know the child's name.

**Used:** The whole approach — `internal/sandbox/cgroup.go`. Per-request cgroup under `/sys/fs/cgroup/goboxd/<jail-id>`, `memory.max` + `memory.swap.max=0`, recursive `oom_kill` read from the parent, `memory.peak` for `memory_peak_kb`, startup orphan sweep. Kept `--rlimit_as` as a second layer.

**Discarded:** Trying to locate nsjail's auto-created child cgroup by name — unnecessary once I realized parent-level accounting is recursive. Also kept everything best-effort: if the host has no writable cgroup v2 tree, accounting degrades silently rather than failing the request.

---

## 2025-05-29 — GOMAXPROCS in a CPU-limited container

**Context:** Preparing the concurrency benchmarks. Wanted to be sure the Go scheduler wasn't oversubscribing threads relative to the container's CPU quota.

**Prompt (paraphrased):**
> In a container with a CPU quota smaller than the host core count, Go sets GOMAXPROCS to the host count by default. Best way to align it with the cgroup quota for a latency-sensitive service?

**Response summary:**
`go.uber.org/automaxprocs` — a blank import that reads the cgroup CPU quota at startup and sets GOMAXPROCS accordingly, handling both cgroup v1 and v2 and fractional quotas. The hand-rolled alternative (parse `/sys/fs/cgroup/cpu.max`) works but has to handle the `max` sentinel and rounding itself.

**Used:** The library, blank-imported in `cmd/goboxd/main.go`. Chose it over hand-rolling because it covers cgroup v1/v2 and fractional quotas that a quick parser would get wrong, and the benchmark numbers are the thing it directly affects.

**Discarded:** The manual `/sys/fs/cgroup/cpu.max` parser — fewer dependencies, but more edge cases to get right for no real benefit here.

---

## 2025-05-31 — Verification pass: prove it or pull it

**Context:** Code was committed and tests were green, but I'd started to lose track of which claims in the repo were actually backed by a run and which I was just trusting. Wanted a sweep that closed every gap and, specifically, caught anything that was asserted but never measured.

**Prompt (paraphrased):**
> Go through the project and fill every gap and risk. Most importantly, make sure nothing is faked — if a number or a status is claimed somewhere, it has to come from a real run, not an assumption.

**Response summary:**
Treated the benchmark doc as the prime suspect and confirmed it: the committed numbers couldn't be tied to any current image, and the only image on disk predated the cgroup changes. Rebuilt from source, brought the container up, gated on `/readyz` showing all seven languages green, then ran `scripts/bench.sh` and replaced the table with the measured figures (which were ~3x faster on throughput than the stale ones). Separately, added unit tests for the four packages that had none — `api`, `sandbox`, `jail`, `limits` — and that surfaced a real `limitedWriter` short-write bug that would turn oversize output into a 500 on any non-default cap. Verified the memory/timeout/build-failed status paths live against the running image rather than only through the fake sandbox.

**Used:** All of it. New tests under `internal/{api,sandbox,jail,limits}`, the `limitedWriter` fix, the regenerated benchmark table and corrected prose. Committed as three scoped commits (fix, tests, docs).

**Discarded:** Nothing substantive. Considered rebuilding the image again after the `limitedWriter` fix so the deployable artifact carried it, but the benchmark numbers don't exercise truncation, so I left the doc citing the exact image it was measured on rather than pretend a newer build produced the same figures.

---

## 2025-05-31 — Is a short write actually a problem here?

**Context:** Writing `TestLimitedWriter`, the over-cap case failed because the writer returned the partial byte count. Wanted to know whether that was a test-expectation problem or a real bug before "fixing" either one.

**Prompt (paraphrased):**
> My output-cap io.Writer returns n < len(p) when a write crosses the cap. Is that a problem given it's used as a subprocess's cmd.Stdout, and if so why did it never show up in live runs?

**Response summary:**
It's a real bug. os/exec copies the child's stdout with `io.Copy`, which treats `n < len(p)` and a nil error as `io.ErrShortWrite` and aborts — that error isn't an `*exec.ExitError`, so it propagates as an execution failure and becomes a 500 instead of a truncated 200. It never fired in practice because the default 64 KiB cap is an exact multiple of `io.Copy`'s 32 KiB buffer, so writes land on the boundary and the straddling branch is never taken; a non-aligned cap or partial chunks would trip it. Fix is to always return `len(p)` and record truncation internally.

**Used:** Changed `limitedWriter.Write` to write up to the cap, set `truncated`, and report `len(p)`. Kept the failing test's expectation (it was asserting the correct behavior) and confirmed end-to-end that a 200 KB print returns a capped 200.

**Discarded:** The instinct to just relax the test to match the buggy return value — which would have documented the bug as intended behavior.

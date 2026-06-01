# Plan Evolution

Tracks how the design changed from initial sketch to current implementation, and why.

---

## Original plan (Day 1, May 26)

Going in, the mental model was: serve HTTP, write source file to disk, call nsjail, read output, compare. Maybe 500 lines of Go. Two languages (py3, cpp) to satisfy the spec minimum.

The language registry was going to be a simple map from string to a struct with a handful of hardcoded fields. Something like:

```go
var languages = map[string]Lang{
    "py3": {Run: []string{"/usr/bin/python3", "solution.py"}},
    "cpp": {Build: []string{"g++", "-o", "solution", "solution.cpp"}, Run: []string{"./solution"}},
}
```

Straightforward, no abstraction needed.

---

## Pivot 1: YAML-driven registry (May 26)

Read the scoring criteria properly. "Plug-and-play" is 25% of the total score, and the way to maximize it is for adding a language to require zero Go changes — just config + install script. That made the hardcoded map a bad idea.

Switched to YAML config with placeholder templating. The language definition in YAML describes what commands to run and what the placeholders mean (`{{source}}`, `{{artifact}}`, `{{flags}}`). The Go engine resolves placeholders but never branches on language ID.

This also unlocked the "extra languages" bonus (10%) almost for free: the only cost per language is a YAML block and a shell install script. Went from planning 2 languages to shipping 7 (py3, c, cpp, java, bash, javascript, verilog) with the same Go codebase.

---

## Pivot 2: nsjail args — bind mount vs chroot (May 28)

Initial nsjail wrapper used `--bindmount workdir:/workdir --cwd /workdir`. This worked in isolation but was inconsistent with the pyjail reference implementation. More importantly, the inner path `/workdir/solution.py` had to be threaded through the YAML config somehow — the placeholder resolution would need to know the inner mount point.

The pyjail approach is `--chroot {workdir}` + bind mounts for `/bin /usr /lib /lib64 /dev /etc /tmp`. The workdir becomes the root of the sandbox, so paths are just `/solution.py`. The YAML config doesn't need to know about the mount layout.

Switched to chroot. Rewrote `buildNsjailArgs` in `internal/sandbox/sandbox.go`. The config paths in languages.yaml got simpler as a result.

---

## Pivot 3: SandboxRunner interface (May 27)

Started writing the runner with a direct dependency on `*sandbox.Sandbox`. Then realized integration tests would need nsjail installed everywhere the tests run. That's not great for CI and makes test feedback slow.

Added a `SandboxRunner` interface to `internal/runner/runner.go`:

```go
type SandboxRunner interface {
    Run(ctx context.Context, cfg sandbox.RunConfig) (*sandbox.Result, error)
}
```

The concrete `*sandbox.NsjailRunner` implements it for production. Tests use `fakeSandbox` which replays scripted results in order. This turned out to be the right call — the e2e tests in `tests/runner_e2e_test.go` cover all status paths without touching the filesystem or spawning any processes.

---

## Pivot 4: Status constant naming (May 28)

Originally had a single `BuildFailed = "build_failed"` constant used for both `build.status` and the top-level response status. Then read the spec more carefully: `build.status` when compilation fails is `"failed"`, not `"build_failed"`. The top-level `status` field when build fails is `"build_failed"`.

Two different constants:
- `BuildFailed = "failed"` — used in `build.status`
- `TopBuildFailed = "build_failed"` — used in top-level `status`

This looks like a footgun in the spec but it's intentional — the build object has its own status vocabulary separate from the top-level run status vocabulary.

---

## What didn't change

The request/response shape (200 even on user code crash, 400 for bad input, 5xx only for server failure) was locked in from reading the spec on day 1 and never revisited. Same with the seven security requirements — those were identified early and implemented incrementally but never structurally changed.

The concurrency model (buffered channel semaphore) was in the plan from day 1 and didn't need adjustment.

---

## Verification pass (May 31): trust nothing that wasn't run

This wasn't a design change so much as a reckoning with the gap between "implemented" and "verified." Three things moved:

**Benchmarks went from asserted to measured.** The numbers in `docs/benchmarks.md` had drifted away from any image that still existed. Rebuilt from current source and re-ran `scripts/bench.sh`; the real throughput plateau (~310 req/s) and tail latency (319 ms p50 at concurrency 100) replaced numbers that were off by roughly 3x. The analysis framing held up — it was only the absolute figures that were stale. The doc now names the exact image it was measured against so this can't quietly rot again.

**Test coverage filled in the packages the fake sandbox can't reach.** `api`, `sandbox`, `jail`, and `limits` had no unit tests; the fake-sandbox e2e suite covered orchestration but nothing below it. Adding direct tests immediately paid for itself by surfacing the `limitedWriter` short-write bug, which had shipped invisibly because the default cap aligns with `io.Copy`'s buffer size.

**`limits.Merge` was confirmed dead.** The per-request limit override is constructed in the handler and then discarded — there's no request field feeding it yet. Left the code in place (it's the obvious extension point) but it's now unit-tested and the discard is commented so the next person doesn't assume overrides work.

## What still hasn't changed

The package layout, the request/response contract, the seven security controls, and the concurrency model are all exactly as they were after the first week. The verification pass didn't move any of them — it just proved they do what the docs say.

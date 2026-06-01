# Postmortem

Written after Stage 1 submission. One page on surprises, where AI cost time rather than saving it, and what I'd do differently.

*Finalized at Stage 1 submission (PR #47 against `thesouldev/goboxd:master`).*

---

## What went well

**The YAML registry decision paid off immediately.** Once `placeholders.go` worked, adding C, Java, bash, JavaScript, and Verilog was maybe 90 minutes of work total — mostly writing install scripts and verifying the YAML syntax. The runner never knew. Would have taken significantly longer with a hardcoded approach.

**The SandboxRunner interface.** Writing 10 status-path tests without touching nsjail was the right call. Found the `{{flags}}` expansion bug, the status constant mismatch, and the `expected_stdout` field bug all through tests that ran in under a second. None of those would have been easy to catch through integration testing alone.

**Conventional commits.** Kept commits small and scoped. The git log actually tells a story — you can read it and see the design evolve.

---

## What surprised me

**The spec's `build.status` vs top-level `status` are different vocabularies.** `build.status` = `"failed"` (not `"build_failed"`). Top-level `status` = `"build_failed"`. One constant for two different things would have broken the JSON response. Caught it in tests but only because the constants were named carefully — if I'd used the string literal everywhere, it would have been invisible.

**nsjail's dynamic linking against libprotobuf.** Assumed `make` would produce a self-contained binary. It doesn't on Ubuntu 22.04. Had to add `libprotobuf23` to the runtime image. The multi-stage Dockerfile looked correct before this was discovered.

**Java's source filename constraint is not in the spec prominently.** It's implied — Java requires the public class name to match the source filename. Found this while writing the YAML config and had to add `source_filename_strategy: from_request` to the config schema and add handling in the API layer. Not a lot of code, but it was an unplanned addition that touched three files.

---

## Where AI cost time

**Overcomplicated the cgroup OOM detection path.** The AI response on cgroup v2 OOM detection was technically correct but oriented toward "the proper production solution" — deterministic cgroup path from nsjail config, reading `memory.events`, etc. Spent an hour trying to implement that before stepping back and using stderr parsing, which is good enough for this use case. Should have started simpler and moved to the harder solution only if the simple one didn't work.

**First Dockerfile attempt was wrong.** Generated a Dockerfile that tried to `apt-get install nsjail` in the runtime stage. Spec says build from source. Had to rewrite. Not a lot of time but avoidable if the initial prompt had been more specific about the constraints.

---

## What I'd do differently

**Write the JSON integration test for field names earlier.** The `expected_stdout` / `expected_output` confusion could have been caught on Day 1 if there was an integration test that sent a real JSON request body. Instead, unit tests used Go struct literals which bypass the JSON tag.

**Check nsjail's build process locally before writing the Dockerfile.** Build nsjail once locally, note what it links against, then write the Dockerfile. Going to the Dockerfile first meant a round-trip of "build, discover missing lib, add to Dockerfile, rebuild."

**Start with 5 languages, not 7.** Verilog was easy to add but required `iverilog` which added image size. Not sure it will be tested heavily. Could have added it in Stage 2.

---

## The verification pass that should have happened sooner (May 31)

Went back through everything with one question: which of these claims have I actually proven, and which am I just trusting? That turned up more than I'd like to admit.

The benchmark numbers in `docs/benchmarks.md` were the worst of it. The table read like measured data but I couldn't tell you which image produced it, and the only image still on disk predated the cgroup work. So the numbers were describing a build that no longer existed. Rebuilt from the current source, ran `scripts/bench.sh` against a clean `docker run`, and the real figures weren't even close — ~310 req/s plateau and 319 ms p50 at concurrency 100, versus the ~105 req/s and ~906 ms the doc claimed. The shape of the analysis was right (semaphore at NumCPU, latency scaling linearly past the core count) but the absolute numbers were fiction. Rewrote the table and the prose to match what the box actually does; the hardware section was already accurate so I left it. Lesson I keep relearning: a benchmark you can't regenerate on demand is a screenshot, not a measurement.

The other thing the pass exposed was how much I'd left untested. `api`, `sandbox`, `jail`, and `limits` had no tests at all — I'd been leaning entirely on the fake-sandbox e2e suite. Wrote unit tests for all four. `limits.Merge` turned out to be dead code (the per-request override is built and then thrown away in the handler), so at minimum it's now tested and the discard is documented for whoever wires it up later.

And the tests immediately earned their keep: the `limitedWriter` cap bug (Issue 8) fell out of writing `TestLimitedWriter`, not out of any live request. It was invisible in production because the default 64 KiB cap happens to align with `io.Copy`'s 32 KiB buffer — exactly the kind of bug that survives every real call and waits for a config change to detonate. Fixed it and confirmed oversize output truncates to a 200 against the running image.

Everything else I checked live instead of assuming: `/readyz` green for all seven languages, `memory_peak_kb` coming back non-zero from the real cgroup (OOM landing exactly on the 100 MiB cap), timeouts hitting the wall limit, and `build_failed` returning a 200 with the tests marked `not_executed`. None of that is new code — but "I'm pretty sure it works" and "I watched it work" are different sentences, and the spec is scored on the second one.

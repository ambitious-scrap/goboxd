# Load Testing & Benchmark Comparison

This document provides a side-by-side performance and benchmark comparison of the `goboxd` service across all development stages under the standard memory-heavy `MemoryHog.java` workload.

## System Configuration & Constraints

To ensure a fair evaluation, all benchmarks were run under the same resource-constrained sandbox environment:
* **CPU Limit:** 2 vCPU (`--cpus=2`)
* **Memory Limit:** 2 GB RAM (`--memory=2g`)
* **Container Runtime:** Docker (via Colima VM), run with `--privileged --cgroupns=host`
* **Workload:** `MemoryHog.java` (allocates 150 MB, touches every page to commit to RSS, holds RSS for 1 second)
* **Client Timeout:** 10 seconds (any request taking longer counts as a failure)
* **Load Generator:** `vegeta` (30-second steps at rising offered rates)

---

## Side-by-Side Comparison

The table below shows the performance metrics (successful execution rate and latency) across Stage 1, Stage 2, and Stage 3 under rising offered load:

| Offered Rate | Metric | Stage 1 (Basic Sandbox) | Stage 2 (Bounded Scheduler & Cache) | Stage 3 (Memory Tokens & Tuned JVM) |
|---|---|---|---|---|
| **5 RPS** | Success Count | 66 / 150 (44%) | 90 / 150 (60%) | **150 / 150 (100%)** |
| | Error Rate | 56.0% | 40.0% | **0.0%** |
| | Success p50 Latency | 10000.11 ms (Timeout) | 3526.00 ms | **1080.08 ms** |
| | Success p95 Latency | 10001.17 ms (Timeout) | 4379.40 ms | **1157.56 ms** |
| **10 RPS** | Success Count | 34 / 300 (11%) | 94 / 300 (31%) | **300 / 300 (100%)** |
| | Error Rate | 89.0% | 69.0% | **0.0%** |
| | Success p50 Latency | 10000.17 ms (Timeout) | 1.64 ms (503 shed) | **1481.03 ms** |
| | Success p95 Latency | 10001.21 ms (Timeout) | 4318.60 ms | **1989.31 ms** |
| **25 RPS** | Success Count | 23 / 750 (3.1%) | 90 / 750 (12%) | **316 / 750 (42.2%)** |
| | Error Rate | 96.9% | 88.0% | **57.9%** (Safe 503 shedding) |
| | Success p50 Latency | 10000.19 ms (Timeout) | 1.70 ms (503 shed) | **3520.69 ms** |
| | Success p95 Latency | 10001.20 ms (Timeout) | 4374.51 ms | **4315.90 ms** |
| **50 RPS** | Success Count | 25 / 1500 (1.7%) | 91 / 1500 (6%) | **324 / 1500 (21.6%)** |
| | Error Rate | 98.3% | 94.0% | **78.4%** (Safe 503 shedding) |
| | Success p50 Latency | 10000.20 ms (Timeout) | 1.55 ms (503 shed) | **3534.65 ms** |
| | Success p95 Latency | 10001.29 ms (Timeout) | 3961.29 ms | **3981.74 ms** |
| **Headline Metrics** | **Breaking Point** | **< 5 RPS** | **5 RPS** | **10 RPS** |
| | **Sustained Capacity** | **~0.6 RPS** | **~3.0 RPS** | **10.81 RPS** |
| | **Peak Goodput (HTTP 200)**| **0.62 RPS** | **3.04 RPS** | **10.81 RPS** (18x vs Stage 1, 3.6x vs Stage 2) |

---

## Architectural Evolution & Analysis

### Stage 1: Basic Sandboxed Execution (The Unbounded Baseline)
* **How it worked:** The server processed incoming requests concurrently, limited only by a simple channel semaphore capped at `runtime.NumCPU()`.
* **Why it failed under load:** Without compile caching, every request paid a 300ms CPU-heavy `javac` compilation tax. Without queue limits, requests queued indefinitely. Under burst, the queue length quickly exceeded the 10-second client timeout, triggering a cascading timeout failure where the server spent CPU cycles compiling and executing jobs that the client had already abandoned.
* **Degradation:** Severe. The system became unresponsive, CPU usage pegged at 100%, and the average success rate collapsed to under 2% under a 50 RPS load.

### Stage 2: Bounded Admission Scheduler & Artifact Caching (Traffic Control)
* **How it worked:** Introduced **Artifact Caching** (skipping compilation for identical code) and **Bounded Queue Throttling** (shedding excess load with `503 Service Unavailable` once active + queued requests exceeded `MaxConcurrency + MaxQueue`).
* **Why it improved:** For identical payloads, caching eliminated the compilation tax entirely, freeing CPU cores. For excess load, instant 503 shedding (taking ~1.5ms) prevented queue build-up and connection starvation.
* **Limitations:** The admission controller set `MaxConcurrency` strictly to `GOMAXPROCS` (4 in dev, 2 in production). For memory-bound tasks like `MemoryHog` (which does little CPU work but holds ~180MB RAM for 1s), this was highly restrictive. Heavy compiled runs were restricted to just 1 active slot, resulting in a throughput plateau at ~3.0 RPS while the CPU remained mostly idle.

### Stage 3: Memory Tokens & Tuned JVM (Optimal Concurrency)
* **How it worked:** 
  1. **Decoupled Concurrency:** Decoupled the server concurrency limit (`max_concurrency: 12`) from the CPU core count.
  2. **Memory Token Scheduler:** Gated memory-heavy jobs via a dynamic memory token budget (detecting Colima's 6 GB VM memory capacity via cgroup v1/v2 fallback paths) and allocating token costs based on actual RSS peak memory limits (`scheduler_cost_kb: 204.8 MB` for Java).
  3. **JVM Runtime Tuning:** Tuned Java executions inside the sandbox with `-XX:+UseSerialGC -XX:ActiveProcessorCount=1 -XX:MaxRAMPercentage=70` to eliminate parallel garbage collection threads and keep runtime memory compact.
* **Why it succeeded:** Gating on memory tokens instead of raw process counts allowed the scheduler to pack up to 11 concurrent Java runs safely within the VM memory budget. Goodput scaled linearly up to **10.81 RPS** (100% success at 10 RPS) with zero memory/OOM crashes and zero timeouts.
* **Degradation:** Graceful. Under extreme load (25-50 RPS), excess requests were cleanly rejected with `503 Service Unavailable`, while admitted requests continued executing successfully at low, bounded latency.

---

## How to Reproduce

All raw data, CSV results, plots, and orchestrator scripts are organized under their respective directories:
* Stage 1 Baseline (Original): [`docs/loadtest/baseline_original/`](file:///Users/dinesh/Documents/Projects/Golang/goboxd-stage3/docs/loadtest/baseline_original/)
* Stage 2/3 Baseline (Gemini): [`docs/loadtest/baseline_stage3/`](file:///Users/dinesh/Documents/Projects/Golang/goboxd-stage3/docs/loadtest/baseline_stage3/)
* Phased Optimizations (Stage 3): [`docs/loadtest/phased_optimizations/`](file:///Users/dinesh/Documents/Projects/Golang/goboxd-stage3/docs/loadtest/phased_optimizations/)

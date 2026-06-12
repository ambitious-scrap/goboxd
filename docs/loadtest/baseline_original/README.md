# Load Testing Results - Original goboxd (with Scheduler & Cache)

This document records the results of load testing the original `goboxd` service (featuring the Bounded Admission Scheduler, Build Lane, and Artifact Caching) under the same constraints (2 vCPU, 2 GB RAM).

## System Configuration & Limits
* **CPU Limit:** 2 vCPU (`--cpus=2`)
* **Memory Limit:** 2 GB RAM (`--memory=2g`)
* **Load Test Tool:** `vegeta` (version 12.7.0, installed locally under `.tools/`)
* **Per-Request Timeout:** 10 seconds

## Load Testing Metrics
Below is the summary of the metrics gathered during the stepped concurrency load test (30s duration per step):

| Target RPS | Throughput RPS | Requests | Success (HTTP 200) | Failed | Error Rate (%) | p50 Latency (ms) | p95 Latency (ms) | p99 Latency (ms) | Max Latency (ms) |
|------------|----------------|----------|---------------------|--------|----------------|------------------|------------------|------------------|------------------|
| 5          | 3.02           | 150      | 90                  | 60     | 40.0%          | 3526.00          | 4379.40          | 4397.27          | 4409.27          |
| 10         | 3.14           | 300      | 94                  | 206    | 68.7%          | 1.64             | 4318.60          | 4412.24          | 4422.62          |
| 25         | 3.00           | 750      | 90                  | 660    | 88.0%          | 1.70             | 4374.51          | 4634.49          | 4735.32          |
| 50         | 3.04           | 1500     | 91                  | 1409   | 93.9%          | 1.55             | 3961.29          | 4414.18          | 4655.55          |

## Breaking Point
* **Breaking Point:** **5 RPS** (similar to stage3, but with significantly improved metrics and throughput).

---

## Performance Comparison: stage3 vs. Original Codebase

| Metric | stage3 Branch (No Scheduler/Cache) | Original Branch (With Scheduler/Cache) | Difference / Improvement |
|--- |--- |--- |--- |
| **Breaking Point** | 5 RPS | 5 RPS | Same (due to the 1s sleep cap) |
| **Success Count (5 RPS)** | 66 / 150 | 90 / 150 | **+36.3% more successful runs** |
| **Error Rate (5 RPS)** | 56.0% | 40.0% | **16% absolute reduction in errors** |
| **p50 Latency (5 RPS)** | 10000.11 ms (Timeout) | 3526.00 ms | **Latency cut by 64.7%** |
| **p50 Latency (10 RPS)** | 10000.17 ms (Timeout) | 1.64 ms | **Throttles excess load instantly** |
| **Sustained Throughput** | ~0.6 RPS (saturates & stalls) | ~3.0 RPS (maximum capacity) | **5x higher sustained throughput** |

---

## Architectural Performance Analysis

The original codebase performs vastly better due to two key integrated features:

1. **Artifact Caching (C-2):**
   * Since all load-test requests execute the same `MemoryHog.java` source, the compiler hashes match.
   * The server hits the compile cache, skipping `javac` compilation entirely and saving ~300ms of CPU-heavy workload per request.
   * This leaves the CPU completely free to focus on executing `nsjail` sandboxes rather than repeatedly building Java files.

2. **Bounded Queue Throttling (C-1) & 503 Load Shedding:**
   * In **stage3**, there is no queue limit. The server accepts all requests, piling them up in the concurrency channel. Under overload, this queue blows past the 10-second client timeout, causing massive timeout cascades (10s latencies).
   * In the **original codebase**, the admission controller tracks in-flight runs using `waiting`. When it exceeds the safe queue threshold (`MaxConcurrency + MaxQueue`), the server instantly rejects excess requests with a **`503 Service Unavailable`** response and a **`Retry-After: 2`** header.
   * Because rejected requests fail immediately (taking ~1.5 ms), they do not hog connections or memory, keeping the server responsive and keeping p50 latency low.
   * This load-shedding enables the server to maintain a maximum execution rate of **~3.0 RPS** continuously without OOM-crashing or hanging.

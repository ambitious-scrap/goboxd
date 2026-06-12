#!/usr/bin/env python3
"""goboxd load test — goodput + success-only latency.

Unlike the earlier harness, this reports the metrics that actually matter for a
saturating workload:

  * goodput_rps      — successful (HTTP 200) executions per second. The real
                       throughput; flat goodput across rising offered RPS means
                       the server is at its ceiling.
  * success_p* _ms   — latency percentiles over ONLY the 200 responses. The
                       all-response p50 is misleading here because at high offered
                       RPS the median response is a fast 503 reject, which drags
                       p50 down to ~1ms and hides how long real runs take.

The all-response columns are kept too, so the distortion is visible side by side.

Usage:  python3 load-test.py [tag]
        tag defaults to "run"; outputs land in results-<tag>.csv and
        goodput-<tag>.png / latency-<tag>.png in this directory.
"""

import os
import sys
import json
import csv
import subprocess
import time
import matplotlib.pyplot as plt

TAG = sys.argv[1] if len(sys.argv) > 1 else "run"

BASE_URL = "http://localhost:8080"
RUN_URL = f"{BASE_URL}/run"
DIR = os.path.dirname(os.path.abspath(__file__))
VEGETA_BIN = os.path.join(DIR, "../../.tools/vegeta")
TARGETS_FILE = os.path.join(DIR, "targets.txt")
REQUEST_JSON = os.path.join(DIR, "run-request.json")
RESULTS_CSV = os.path.join(DIR, f"results-{TAG}.csv")

if not os.path.exists(VEGETA_BIN):
    print(f"Error: {VEGETA_BIN} not found. Running go install...")
    subprocess.run(
        ["go", "install", "github.com/tsenart/vegeta@latest"],
        env=dict(os.environ, GOBIN=os.path.join(DIR, "../../.tools")),
    )

# MemoryHog payload (Java): allocates 150 MB, touches every page, holds it ~1s.
payload = {
    "language": "java",
    "source_filename": "MemoryHog.java",
    "artifact_filename": "MemoryHog",
    "source": """public class MemoryHog {
    public static void main(String[] args) throws InterruptedException {
        final int megabytes = readSize();
        final int blockSize = 1 << 20; // 1 MB per block
        final byte[][] blocks = new byte[megabytes][];

        long checksum = 0;

        // Allocate and touch every page so the pages are actually committed to RSS.
        for (int i = 0; i < megabytes; i++) {
            byte[] block = new byte[blockSize];
            for (int j = 0; j < blockSize; j += 4096) {
                block[j] = (byte) (i * 31 + j);
                checksum += block[j];
            }
            blocks[i] = block;
        }

        // Light CPU pass so a run is not pure allocation.
        for (byte[] block : blocks) {
            for (int j = 0; j < block.length; j += 512) {
                checksum += block[j];
            }
        }

        // Hold the memory resident for a moment so concurrent runs pile up.
        Thread.sleep(1000);

        // Deterministic output proves the run actually completed.
        System.out.println("MemoryHog OK mb=" + megabytes + " checksum=" + checksum);
    }

    private static int readSize() {
        String env = System.getenv("MEMHOG_MB");
        if (env != null && !env.isEmpty()) {
            try {
                return Integer.parseInt(env.trim());
            } catch (NumberFormatException ignored) {
            }
        }
        return 150;
    }
}
""",
    "tests": [
        {
            "stdin": "",
            # Real newline. The earlier harness used "\\n" (literal backslash-n),
            # so every run mismatched and returned HTTP-200 wrong_output that was
            # still counted as "success" — goodput here counts completions, and at
            # idle this payload must return verdict "accepted" (asserted below).
            "expected_stdout": "MemoryHog OK mb=150 checksum=-101888\n",
        }
    ],
}

with open(REQUEST_JSON, "w") as f:
    json.dump(payload, f, indent=2)

with open(TARGETS_FILE, "w") as f:
    f.write(f"POST {RUN_URL}\n")
    f.write("Content-Type: application/json\n")
    f.write(f"@{REQUEST_JSON}\n")


def percentile(sorted_vals, q):
    """Nearest-rank percentile over a pre-sorted list of numbers (q in [0,100])."""
    if not sorted_vals:
        return 0.0
    k = max(0, min(len(sorted_vals) - 1, int(round((q / 100.0) * (len(sorted_vals) - 1)))))
    return sorted_vals[k]


def success_only_latencies(bin_report):
    """Decode the vegeta .bin and return (latencies_ms_for_200s, count_200)."""
    out = subprocess.run(
        [VEGETA_BIN, "encode", "--to", "json", bin_report],
        capture_output=True, text=True,
    ).stdout
    lat_ms = []
    for line in out.splitlines():
        line = line.strip()
        if not line:
            continue
        rec = json.loads(line)
        if rec.get("code") == 200:
            lat_ms.append(rec["latency"] / 1e6)  # ns -> ms
    lat_ms.sort()
    return lat_ms, len(lat_ms)


rates = [5, 10, 25, 50]
results = []

print(f"Load test (tag={TAG}) -> {RESULTS_CSV}")
print(f"Targeting: {RUN_URL}")

with open(RESULTS_CSV, "w", newline="") as csvfile:
    csv.writer(csvfile).writerow([
        "target_rps", "throughput_rps", "goodput_rps", "duration_s",
        "requests", "success", "failed", "error_pct",
        "all_p50_ms", "all_p95_ms",
        "success_p50_ms", "success_p95_ms", "success_p99_ms", "success_max_ms",
    ])

for rate in rates:
    print(f"\n--- Offered Rate: {rate} RPS for 30s ---")
    bin_report = os.path.join(DIR, f"report-{TAG}-{rate}.bin")
    json_report = os.path.join(DIR, f"report-{TAG}-{rate}.json")

    subprocess.run([
        VEGETA_BIN, "attack",
        f"-rate={rate}/1s", "-duration=30s", "-timeout=10s",
        f"-targets={TARGETS_FILE}", f"-output={bin_report}",
    ])

    with open(json_report, "w") as out:
        subprocess.run([VEGETA_BIN, "report", "-type=json", bin_report], stdout=out)
    with open(json_report, "r") as f:
        data = json.load(f)

    requests = data["requests"]
    success = data["status_codes"].get("200", 0)
    failed = requests - success
    error_pct = (1.0 - data["success"]) * 100
    throughput = data["throughput"]
    duration = data["duration"] / 1e9

    all_p50 = data["latencies"]["50th"] / 1e6
    all_p95 = data["latencies"]["95th"] / 1e6

    succ_lat, succ_n = success_only_latencies(bin_report)
    goodput = succ_n / duration if duration > 0 else 0.0
    s_p50 = percentile(succ_lat, 50)
    s_p95 = percentile(succ_lat, 95)
    s_p99 = percentile(succ_lat, 99)
    s_max = succ_lat[-1] if succ_lat else 0.0

    print(f"Requests: {requests}, Success: {success}, Failed: {failed}, Error: {error_pct:.1f}%")
    print(f"  goodput={goodput:.2f} rps | all p50={all_p50:.1f}ms p95={all_p95:.1f}ms")
    print(f"  success-only p50={s_p50:.1f}ms p95={s_p95:.1f}ms p99={s_p99:.1f}ms max={s_max:.1f}ms")

    row = [rate, throughput, goodput, duration, requests, success, failed, error_pct,
           all_p50, all_p95, s_p50, s_p95, s_p99, s_max]
    results.append(row)
    with open(RESULTS_CSV, "a", newline="") as csvfile:
        csv.writer(csvfile).writerow(row)

    time.sleep(2)

# Plots -------------------------------------------------------------------
rps = [r[0] for r in results]

plt.figure()
plt.plot(rps, [r[2] for r in results], marker="o", color="seagreen", linewidth=2, label="goodput (200s/s)")
plt.plot(rps, [r[1] for r in results], marker="s", color="gray", linewidth=1, linestyle="--", label="throughput (all/s)")
plt.xlabel("Offered RPS")
plt.ylabel("Requests / s")
plt.title(f"Goodput vs Offered Load ({TAG})")
plt.grid(True, linestyle="--", alpha=0.6)
plt.legend()
plt.savefig(os.path.join(DIR, f"goodput-{TAG}.png"), dpi=150, bbox_inches="tight")

plt.figure()
plt.plot(rps, [r[10] for r in results], marker="o", label="success p50", linewidth=2)
plt.plot(rps, [r[11] for r in results], marker="o", label="success p95", linewidth=2)
plt.plot(rps, [r[12] for r in results], marker="o", label="success p99", linewidth=2)
plt.plot(rps, [r[8] for r in results], marker="x", color="gray", linestyle="--", label="all-response p50", linewidth=1)
plt.xlabel("Offered RPS")
plt.ylabel("Latency (ms)")
plt.title(f"Success-only Latency vs Offered Load ({TAG})")
plt.grid(True, linestyle="--", alpha=0.6)
plt.legend()
plt.savefig(os.path.join(DIR, f"latency-{TAG}.png"), dpi=150, bbox_inches="tight")

print(f"\nSaved results-{TAG}.csv, goodput-{TAG}.png, latency-{TAG}.png")

import os
import sys
import json
import csv
import subprocess
import time
import matplotlib.pyplot as plt

# Configuration
BASE_URL = "http://localhost:8080"
RUN_URL = f"{BASE_URL}/run"
DIR = os.path.dirname(os.path.abspath(__file__))
VEGETA_BIN = os.path.join(DIR, "../../.tools/vegeta")
TARGETS_FILE = os.path.join(DIR, "targets.txt")
REQUEST_JSON = os.path.join(DIR, "run-request.json")
RESULTS_CSV = os.path.join(DIR, "results.csv")

# Ensure .tools/vegeta is executable/exists
if not os.path.exists(VEGETA_BIN):
    print(f"Error: {VEGETA_BIN} not found. Running go install...")
    subprocess.run(["go", "install", "github.com/tsenart/vegeta@latest"], env=dict(os.environ, GOBIN=os.path.join(DIR, "../../.tools")))

# MemoryHog Payload (Java)
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
            "expected_stdout": "MemoryHog OK mb=150 checksum=-101888\\n"
        }
    ]
}

# Write request and target files
with open(REQUEST_JSON, "w") as f:
    json.dump(payload, f, indent=2)

with open(TARGETS_FILE, "w") as f:
    f.write(f"POST {RUN_URL}\n")
    f.write("Content-Type: application/json\n")
    f.write(f"@{REQUEST_JSON}\n")

# Load levels
rates = [5, 10, 25, 50, 75, 100, 150, 200, 300, 400]
results = []
breaking_point_rps = None

print("Starting load test sequence against original goboxd...")
print(f"Targeting: {RUN_URL}")
print(f"Results will be written to {RESULTS_CSV}")

# Initialize CSV file
with open(RESULTS_CSV, "w", newline="") as csvfile:
    writer = csv.writer(csvfile)
    writer.writerow([
        "target_rps", "throughput_rps", "duration_s", "requests", "success", "failed",
        "error_pct", "p50_ms", "p95_ms", "p99_ms", "max_ms"
    ])

for rate in rates:
    print(f"\n--- Testing Offered Rate: {rate} RPS for 30s ---")
    bin_report = os.path.join(DIR, f"report-{rate}.bin")
    json_report = os.path.join(DIR, f"report-{rate}.json")
    
    # Run vegeta attack
    attack_cmd = [
        VEGETA_BIN, "attack",
        f"-rate={rate}/1s",
        "-duration=30s",
        "-timeout=10s",
        f"-targets={TARGETS_FILE}",
        f"-output={bin_report}"
    ]
    
    subprocess.run(attack_cmd)
    
    # Generate JSON report
    with open(json_report, "w") as out:
        subprocess.run([VEGETA_BIN, "report", "-type=json", bin_report], stdout=out)
        
    # Parse JSON report
    with open(json_report, "r") as f:
        data = json.load(f)
        
    requests = data["requests"]
    # Success is HTTP status code 200
    success = data["status_codes"].get("200", 0)
    failed = requests - success
    error_pct = (1.0 - data["success"]) * 100
    throughput = data["throughput"]
    duration = data["duration"] / 1e9  # seconds
    
    # Latencies in milliseconds
    p50 = data["latencies"]["50th"] / 1e6
    p95 = data["latencies"]["95th"] / 1e6
    p99 = data["latencies"]["99th"] / 1e6
    max_lat = data["latencies"]["max"] / 1e6
    
    print(f"Requests: {requests}, Success: {success}, Failed: {failed}, Error Rate: {error_pct:.2f}%")
    print(f"Latencies (ms): p50={p50:.2f}, p95={p95:.2f}, p99={p99:.2f}, max={max_lat:.2f}")
    
    # Save results
    row = [rate, throughput, duration, requests, success, failed, error_pct, p50, p95, p99, max_lat]
    results.append(row)
    
    with open(RESULTS_CSV, "a", newline="") as csvfile:
        writer = csv.writer(csvfile)
        writer.writerow(row)
        
    if failed > 0 and breaking_point_rps is None:
        breaking_point_rps = rate
        print(f"*** BREAKING POINT DETECTED AT {rate} RPS! ***")
        
    # If we have passed the breaking point by 3 steps, we can stop early to avoid crashing the machine completely
    if breaking_point_rps is not None and rates.index(rate) >= rates.index(breaking_point_rps) + 3:
        print("Completed 3 steps past the breaking point. Stopping early.")
        break
        
    # Wait a moment between runs for server recovery
    time.sleep(2)

# Generate plots
print("\nGenerating performance plots...")
rps = [r[0] for r in results]

# 1. Error Rate Plot
plt.figure()
plt.plot(rps, [r[6] for r in results], marker="o", color="royalblue", linewidth=2)
plt.xlabel("Offered RPS")
plt.ylabel("Error rate (%)")
plt.title("Breaking Point Analysis - Original Goboxd")
plt.grid(True, linestyle="--", alpha=0.6)
if breaking_point_rps is not None:
    plt.axvline(x=breaking_point_rps, color="darkorange", linestyle="--", label=f"Breaking Point ({breaking_point_rps} RPS)")
    plt.legend()
plt.savefig(os.path.join(DIR, "breaking-point.png"), dpi=150, bbox_inches="tight")

# 2. Latency Plot
plt.figure()
plt.plot(rps, [r[7] for r in results], marker="o", label="p50", linewidth=2)
plt.plot(rps, [r[8] for r in results], marker="o", label="p95", linewidth=2)
plt.plot(rps, [r[9] for r in results], marker="o", label="p99", linewidth=2)
plt.xlabel("Offered RPS")
plt.ylabel("Latency (ms)")
plt.title("RPS vs Response Latency - Original Goboxd")
plt.grid(True, linestyle="--", alpha=0.6)
plt.legend()
plt.savefig(os.path.join(DIR, "latency.png"), dpi=150, bbox_inches="tight")

print("Plots saved successfully under docs/loadtestCld/")
print("Load test complete.")

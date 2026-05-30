#!/usr/bin/env bash
# Benchmark POST /run at increasing concurrency. Reports p50/p95/p99 per level.
#
# Usage:
#   scripts/bench.sh [BASE_URL] [REQUESTS_PER_LEVEL]
#
# Requires `hey` (go install github.com/rakyll/hey@latest) and a running goboxd
# reachable at BASE_URL (default http://localhost:8080). The target must pass
# /readyz before meaningful numbers are produced.
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
N="${2:-500}"
PAYLOAD='{"language":"py3","source":"print(\"hello\")","tests":[{"stdin":"","expected_stdout":"hello\n"}]}'

command -v hey >/dev/null 2>&1 || {
	echo "hey not found: go install github.com/rakyll/hey@latest" >&2
	exit 1
}

echo "Waiting for ${BASE_URL}/readyz ..."
for _ in $(seq 1 60); do
	if curl -fsS "${BASE_URL}/readyz" >/dev/null 2>&1; then
		echo "ready"
		break
	fi
	sleep 1
done

for C in 1 10 50 100; do
	echo
	echo "=== concurrency ${C}, ${N} requests ==="
	hey -n "${N}" -c "${C}" -m POST \
		-H "Content-Type: application/json" \
		-d "${PAYLOAD}" \
		"${BASE_URL}/run" \
		| grep -E "Total:|Requests/sec|50%|95%|99%|Status code|\[2|\[4|\[5"
done

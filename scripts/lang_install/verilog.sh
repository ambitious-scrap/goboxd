#!/usr/bin/env bash
set -euo pipefail
apt-get install -y --no-install-recommends iverilog
# Print the first version line without piping to head (would trip pipefail).
ver="$(iverilog -V 2>&1 || true)"
printf '%s\n' "${ver%%$'\n'*}"

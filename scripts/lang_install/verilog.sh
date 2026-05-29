#!/usr/bin/env bash
set -euo pipefail
apt-get install -y --no-install-recommends iverilog
iverilog -V 2>&1 | head -1

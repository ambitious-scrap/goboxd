#!/usr/bin/env bash
set -euo pipefail
apt-get install -y --no-install-recommends gcc build-essential
gcc --version

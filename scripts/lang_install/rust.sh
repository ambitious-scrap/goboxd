#!/usr/bin/env bash
set -euo pipefail
# gcc provides the cc linker rustc needs to produce native binaries.
apt-get install -y --no-install-recommends rustc gcc
rustc --version

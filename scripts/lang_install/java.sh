#!/usr/bin/env bash
set -euo pipefail
apt-get install -y --no-install-recommends openjdk-17-jdk-headless
java --version

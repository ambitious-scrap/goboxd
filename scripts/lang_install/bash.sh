#!/usr/bin/env bash
set -euo pipefail
# bash is pre-installed; confirm availability and print the first version line
# without piping to head (head closing the pipe early trips pipefail -> SIGPIPE).
ver="$(bash --version)"
printf '%s\n' "${ver%%$'\n'*}"

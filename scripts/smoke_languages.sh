#!/usr/bin/env bash
# Happy-path smoke test for every configured language against a running goboxd.
# Self-contained (no external fixtures): posts one trivial program per language
# and asserts a top-level "accepted" verdict. Doubles as the seccomp-enforce
# regression gate — if the active seccomp policy kills a legitimate runtime, the
# corresponding language flips to runtime_error and this script fails.
#
# Usage:
#   GOBOXD_URL=http://localhost:8080 scripts/smoke_languages.sh
set -uo pipefail

URL="${GOBOXD_URL:-http://localhost:8080}"

# language|source|expected_stdout   (\n in expected is a real newline)
# Each program prints a known string; interpreted and compiled langs both covered.
run_case() {
  local lang="$1" src="$2" expected="$3" extra="${4:-}"
  local payload
  payload=$(jq -cn --arg l "$lang" --arg s "$src" --arg e "$expected" \
    '{language:$l, source:$s, tests:[{stdin:"", expected_stdout:$e}]} '"$extra")
  local status
  status=$(curl -s -X POST "$URL/run" -H 'Content-Type: application/json' \
    -d "$payload" | jq -r '.status // ("HTTP_ERROR:" + (.error.code // "unknown"))')
  if [ "$status" = "accepted" ]; then
    printf '  ok   %-11s accepted\n' "$lang"
    return 0
  fi
  printf '  FAIL %-11s got %s\n' "$lang" "$status"
  return 1
}

echo "smoke: $URL (seccomp = $(curl -s "$URL/info" | jq -r '.languages|length') langs)"

fail=0
run_case py3        'print("hi")'                                              $'hi\n'        || fail=1
run_case c          $'#include <stdio.h>\nint main(){printf("hi\\n");return 0;}' $'hi\n'      || fail=1
run_case cpp        $'#include <iostream>\nint main(){std::cout<<"hi\\n";}'     $'hi\n'        || fail=1
run_case bash       'echo hi'                                                  $'hi\n'        || fail=1
run_case javascript 'console.log("hi")'                                        $'hi\n'        || fail=1
run_case java       'public class Main{public static void main(String[] a){System.out.println("hi");}}' $'hi\n' \
  ' + {source_filename:"Main.java", artifact_filename:"Main"}'                                || fail=1
run_case verilog    $'module test;\ninitial begin\n  $display("hi");\n  $finish;\nend\nendmodule' $'hi\n' || fail=1

if [ "$fail" -ne 0 ]; then
  echo "smoke: FAILED"
  exit 1
fi
echo "smoke: all languages accepted"

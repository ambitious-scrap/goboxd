#!/usr/bin/env bash

# Get the directory where this script is located
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
DEFAULT_HOST="http://localhost:5000"

OUTPUT_DIR="$SCRIPT_DIR/../../../output"
if ! mkdir -p "$OUTPUT_DIR" 2>/dev/null; then
    OUTPUT_DIR="$SCRIPT_DIR/output"
    mkdir -p "$OUTPUT_DIR"
fi

setup() {
    cd "$SCRIPT_DIR"
    export LANGUAGE="${LANGUAGE:-}"
}

print_info() {
    echo -e "\n$1 | Host: $HOST | Duration: ${DURATION}s"
    [[ -n "$LANGUAGE" ]] && echo "Language: $LANGUAGE"
}

run_100rps() {
    local duration="${1:-60}"
    print_info "100 RPS Test (10 users)"

    uv run locust -f locustfile.py \
        --headless \
        --host "$HOST" \
        --users 10 \
        --spawn-rate 2 \
        --run-time ${duration}s \
        --html "$OUTPUT_DIR/report_100rps.html" \
        --csv "$OUTPUT_DIR/report_100rps" \
        ConstantLoadUser
}

run_burst() {
    local duration="${1:-30}"
    local users="${BURST_USERS:-50}"
    print_info "Burst Test ($users users)"

    uv run locust -f locustfile.py \
        --headless \
        --host "$HOST" \
        --users $users \
        --spawn-rate 10 \
        --run-time ${duration}s \
        --html "$OUTPUT_DIR/report_burst.html" \
        --csv "$OUTPUT_DIR/report_burst" \
        BurstLoadUser
}

run_sustained() {
    local duration="${1:-300}"
    local users="${SUSTAINED_USERS:-20}"
    print_info "Sustained Test ($users users, $(($duration / 60))min)"

    uv run locust -f locustfile.py \
        --headless \
        --host "$HOST" \
        --users $users \
        --spawn-rate 1 \
        --run-time ${duration}s \
        --html "$OUTPUT_DIR/report_sustained.html" \
        --csv "$OUTPUT_DIR/report_sustained" \
        SustainedLoadUser
}

run_rampup() {
    local duration="${1:-600}"
    local max_users="${RAMPUP_USERS:-100}"
    print_info "Ramp-up Test (max $max_users users, $(($duration / 60))min)"

    uv run locust -f locustfile.py \
        --headless \
        --host "$HOST" \
        --users $max_users \
        --spawn-rate 1 \
        --run-time ${duration}s \
        --html "$OUTPUT_DIR/report_rampup.html" \
        --csv "$OUTPUT_DIR/report_rampup" \
        ConstantLoadUser
}

run_custom() {
    local users="${CUSTOM_USERS:-10}"
    local spawn_rate="${CUSTOM_SPAWN_RATE:-1}"
    local duration="${1:-60}"
    local user_class="${CUSTOM_USER_CLASS:-ConstantLoadUser}"

    print_info "Custom Test ($users users, spawn=$spawn_rate)"

    uv run locust -f locustfile.py \
        --headless \
        --host "$HOST" \
        --users $users \
        --spawn-rate $spawn_rate \
        --run-time ${duration}s \
        --html "$OUTPUT_DIR/report_custom.html" \
        --csv "$OUTPUT_DIR/report_custom" \
        $user_class
}

run_all() {
    echo "Running all test scenarios..."

    run_100rps 60
    sleep 10

    run_burst 30
    sleep 10

    run_sustained 120
    sleep 10

    run_rampup 300

    echo -e "\nCompleted. Reports in $OUTPUT_DIR"
}

show_usage() {
    cat << EOF
Usage: $0 <test_type> [HOST] [LANGUAGE] [DURATION]

Test Types:
  100rps     - 100 RPS test (60s, 10 users)
  burst      - Burst test (30s, 50 users)
  sustained  - Sustained test (300s, 20 users)
  rampup     - Ramp-up test (600s, max 100 users)
  custom     - Custom test (use env vars)
  all        - Run all tests

Environment Variables:
  BURST_USERS, SUSTAINED_USERS, RAMPUP_USERS
  CUSTOM_USERS, CUSTOM_SPAWN_RATE, CUSTOM_USER_CLASS

Examples:
  $0 100rps
  $0 burst http://localhost:5000 java 60
  BURST_USERS=100 $0 burst
EOF
}

main() {
    TEST_TYPE="${1:-}"
    HOST="${2:-$DEFAULT_HOST}"
    LANGUAGE="${3:-}"
    DURATION="${4:-}"

    [[ -z "$TEST_TYPE" ]] && { echo "Test type required"; show_usage; exit 1; }

    setup

    case "$TEST_TYPE" in
        100rps)         run_100rps "$DURATION" ;;
        burst)          run_burst "$DURATION" ;;
        sustained)      run_sustained "$DURATION" ;;
        rampup|ramp-up) run_rampup "$DURATION" ;;
        custom)         run_custom "$DURATION" ;;
        all)            run_all ;;
        help|--help|-h) show_usage ;;
        *)              echo "Unknown test: $TEST_TYPE"; show_usage; exit 1 ;;
    esac
}

main "$@"
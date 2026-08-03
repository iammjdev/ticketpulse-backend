#!/usr/bin/env bash
# Runs both k6 stress scenarios against a running TicketPulse backend, then renders
# BENCHMARK_RESULTS.md from their JSON output. Usage:
#   ./run-benchmark.sh
#   BASE_URL=http://localhost:8080 PEAK_VUS=2000 ./run-benchmark.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

BASE_URL="${BASE_URL:-http://localhost:8080}"
EVENT_ID="${EVENT_ID:-22222222-2222-2222-2222-222222222222}"
PEAK_VUS="${PEAK_VUS:-10000}"

mkdir -p results

USE_DOCKER=0
if command -v k6 >/dev/null 2>&1; then
  USE_DOCKER=0
elif command -v docker >/dev/null 2>&1; then
  echo "k6 CLI not found locally - falling back to Docker (grafana/k6)."
  echo "Note: --net=host only works on Linux Docker hosts. On Docker Desktop"
  echo "(Mac/Windows) set BASE_URL=http://host.docker.internal:8080 instead."
  USE_DOCKER=1
else
  echo "Neither k6 nor docker is installed. Install k6: https://k6.io/docs/get-started/installation/"
  exit 1
fi

run_k6() {
  if [ "$USE_DOCKER" -eq 1 ]; then
    docker run --rm --net=host -v "$SCRIPT_DIR":/scripts -w /scripts grafana/k6 run "$@"
  else
    k6 run "$@"
  fi
}

echo "==> Checking backend health at ${BASE_URL}/health"
if ! curl -sf "${BASE_URL}/health" >/dev/null; then
  echo "Backend not reachable at ${BASE_URL}. Start it first: go run cmd/api/main.go"
  exit 1
fi

echo "==> Running seat lock concurrency stress test (1,000 VUs, single seat)"
run_k6 -e BASE_URL="$BASE_URL" -e EVENT_ID="$EVENT_ID" seat_lock_stress.js

echo "==> Running queue surge stress test (ramp -> peak ${PEAK_VUS} VUs -> cool down)"
run_k6 -e BASE_URL="$BASE_URL" -e EVENT_ID="$EVENT_ID" -e PEAK_VUS="$PEAK_VUS" queue_surge_stress.js

echo "==> Generating BENCHMARK_RESULTS.md"
node benchmark_summary.js

echo "==> Done. See tests/k6/BENCHMARK_RESULTS.md"

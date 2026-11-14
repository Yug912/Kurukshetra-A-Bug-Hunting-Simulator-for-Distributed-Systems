#!/usr/bin/env bash
# Runs the same seed twice and checks the engine is deterministic:
#   1. faults.json is byte-identical (the fault plan is a pure function of the seed);
#   2. for message IDs present in both traces, >= MIN% got the same decision.
#
# Usage: scripts/engine-determinism.sh [scenario.yaml] [seed] [min-percent]
# Needs Docker and the kuru/fake-node image (make images).
set -euo pipefail
cd "$(dirname "$0")/.."

SCENARIO=${1:-examples/fake-smoke.yaml}
SEED=${2:-1}
MIN=${3:-99}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

go build -o "$TMP/kuru" ./cmd/kuru
for i in a b; do
  echo "== run $i: $SCENARIO seed $SEED"
  "$TMP/kuru" run "$SCENARIO" --seed "$SEED" --runs-dir "$TMP/$i" --quiet || true
done

A=$(dirname "$(find "$TMP/a" -name faults.json | head -1)")
B=$(dirname "$(find "$TMP/b" -name faults.json | head -1)")

if cmp -s "$A/faults.json" "$B/faults.json"; then
  echo "faults.json: identical (100% identical fault plan)"
else
  echo "faults.json: DIFFERENT"
  diff "$A/faults.json" "$B/faults.json" | head -20
  exit 1
fi
go run ./scripts/engine-tracediff -min "$MIN" "$A/trace.jsonl" "$B/trace.jsonl"

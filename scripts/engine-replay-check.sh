#!/usr/bin/env bash
# Runs and replays a scenario for several seeds and checks replay fidelity.
#
# Usage: scripts/engine-replay-check.sh [scenario.yaml] [first-seed] [last-seed] [min-percent]
# Set KEEP_DIR=<dir> to keep the run folders there for inspection.
# Needs Docker and the scenario's image (make images).
set -euo pipefail
cd "$(dirname "$0")/.."

SCENARIO=${1:-examples/fake-smoke.yaml}
FIRST=${2:-1}
LAST=${3:-5}
MIN=${4:-95}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
go build -o "$TMP/kuru" ./cmd/kuru
RUNS=${KEEP_DIR:-$TMP/runs}

fail=0
for seed in $(seq "$FIRST" "$LAST"); do
  line=$("$TMP/kuru" run "$SCENARIO" --seed "$seed" --runs-dir "$RUNS" --quiet || true)
  dir=${line##*dir=}
  dir=${dir%% *}
  out=$("$TMP/kuru" replay "$dir" --quiet || true)
  fid=$(printf '%s\n' "$out" | sed -n 's/^fidelity \([0-9.]*\)%.*/\1/p')
  faults=$(printf '%s\n' "$out" | sed -n 's/.*faults \([0-9]*\/[0-9]*\)$/\1/p')
  verdict=$(printf '%s\n' "$out" | sed -n 's/^verdict .*: \(.*\)$/\1/p')
  ok=$(awk -v f="${fid:-0}" -v m="$MIN" 'BEGIN { print (f >= m) ? "ok" : "LOW" }')
  printf 'seed %-4s fidelity %6s%%  faults %-7s  verdict %-15s %s\n' "$seed" "${fid:-?}" "${faults:-?}" "${verdict:-?}" "$ok"
  [ "$ok" = ok ] || fail=1
done
exit $fail

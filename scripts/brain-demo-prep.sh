#!/usr/bin/env bash
# Prepare everything docs/demo.md needs, ahead of time:
#   - bin/kuru, the web viewer, and the images kuru/toykv, kuru/etcd:buggy, kuru/etcd:fixed
#   - run folders under runs/ (the stage backup if Docker is slow):
#       with Person A's engine:  explore toykv-stale-read, shrink + replay its first FAIL,
#                                a lease-clock FAIL, a lost-write FAIL, real-etcd buggy/fixed,
#                                and toykv-correct over 200 seeds
#       always (no Docker):      the sample run and a shrunk fake-faults run
# Usage: scripts/brain-demo-prep.sh [--no-docker] [--correct-seeds N]
set -euo pipefail
cd "$(dirname "$0")/.."

DOCKER=1
CORRECT_SEEDS=200
while [ $# -gt 0 ]; do
  case "$1" in
    --no-docker) DOCKER=0 ;;
    --correct-seeds) CORRECT_SEEDS="$2"; shift ;;
    *) echo "unknown flag $1" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '\n== %s\n' "$*"; }
RUNS=runs
mkdir -p "$RUNS"

say "build kuru and the viewer"
make build
make web

say "offline runs (no Docker): sample + shrunk fake-faults"
rm -rf "$RUNS/sample" "$RUNS/fake-faults"
mkdir -p "$RUNS/sample"
cp -R testdata/sample-run "$RUNS/sample/42"
./bin/kuru check "$RUNS/sample/42" --checkers linearizable-kv,durability || true
go run ./scripts/brain-fakerun -scenario examples/fake-faults.yaml -seed 1 -runs-dir "$RUNS"
./bin/kuru shrink "$RUNS/fake-faults/1" --runner fake-faults

if [ "$DOCKER" = 0 ]; then
  say "done (offline only): ./bin/kuru serve --open"
  exit 0
fi

say "images"
scripts/brain-build-images.sh

if ! ./bin/kuru help explore >/dev/null 2>&1; then
  say "Person A's engine (kuru run/explore/replay) is not in this build; skipping engine runs"
  echo "Engine-free evidence: go test -tags integration -run 'ToyKVBugFlags|Etcd.*Partition' ./pkg/workload"
  exit 0
fi

# first_fail <scenario>: the first FAIL run folder of a scenario under runs/.
first_fail() {
  for d in "$RUNS/$1"/*/; do
    d="${d%/}"
    case "$d" in *-min) continue ;; esac
    if [ -f "$d/result.json" ] && grep -q '"verdict": "FAIL"' "$d/result.json"; then
      echo "$d"
      return 0
    fi
  done
  return 1
}

say "explore toykv-stale-read (50 seeds, 4 parallel)"
./bin/kuru explore examples/toykv-stale-read.yaml --runs 50 --parallel 4 || true
if FAIL_DIR=$(first_fail toykv-stale-read); then
  echo "first FAIL: $FAIL_DIR"
  say "shrink $FAIL_DIR"
  ./bin/kuru shrink "$FAIL_DIR" --max-runs 60 --parallel 2
  say "replay $FAIL_DIR"
  ./bin/kuru replay "$FAIL_DIR" || true
  echo "$FAIL_DIR" > "$RUNS/.demo-stale-read"
else
  echo "no FAIL found for toykv-stale-read; widen --runs" >&2
fi

for sc in toykv-lease-clock toykv-lost-write real-etcd-buggy real-etcd-fixed; do
  say "explore $sc (20 seeds)"
  ./bin/kuru explore "examples/$sc.yaml" --runs 20 --parallel 4 || true
done

say "toykv-correct over $CORRECT_SEEDS seeds (must all PASS)"
./bin/kuru explore examples/toykv-correct.yaml --runs "$CORRECT_SEEDS" --parallel 4 || true
if first_fail toykv-correct >/dev/null; then
  echo "WARNING: toykv-correct has a FAIL — investigate before the demo" >&2
fi

say "ready: ./bin/kuru serve --open"

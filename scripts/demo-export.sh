#!/usr/bin/env bash
# Bundle run folders for the static live demo (GitHub Pages, no kuru serve):
# runs `kuru explain` on each FAIL run, then copies the runs into web/timeline/demo
# with `kuru serve --export`. Commit web/timeline/demo afterwards.
#
# Usage: scripts/demo-export.sh <scenario>/<run> [<scenario>/<run> ...]
#   e.g. scripts/demo-export.sh toykv-stale-read/3 toykv-stale-read/3-min \
#          toykv-lease-clock/2 real-etcd-buggy/1 toykv-correct/1
# Preview: cd web/timeline && npm run build:demo && npx vite preview
set -euo pipefail
cd "$(dirname "$0")/.."
[ $# -gt 0 ] || { sed -n 2,9p "$0"; exit 2; }
[ -x bin/kuru ] || make build
RUNS="${RUNS_DIR:-runs}"
ids=()
for id in "$@"; do
  dir="$RUNS/$id"
  [ -f "$dir/result.json" ] || { echo "no run at $dir" >&2; exit 1; }
  if grep -q '"verdict": "FAIL"' "$dir/result.json"; then
    ./bin/kuru explain --exact "$dir" >/dev/null   # each run gets its own story (explain.md)
  fi
  ids+=("$id")
done
./bin/kuru --runs-dir "$RUNS" serve --export web/timeline/demo --only "$(IFS=,; echo "${ids[*]}")"
du -sh web/timeline/demo
